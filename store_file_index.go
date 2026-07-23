package simdjson

import (
	"math/bits"

	"github.com/thesyncim/simdjson/internal/storeio"
)

// AppendIndexes appends the frozen exact-index catalog visible to this file
// snapshot. FileStore indexes are complete from generation one and therefore
// always report Ready.
func (s *FileSnapshot) AppendIndexes(dst []StoreIndexInfo) []StoreIndexInfo {
	if s == nil || s.store == nil || s.state == nil {
		return dst
	}
	for _, definition := range s.store.options.Indexes {
		info := StoreIndexInfo{
			Name: definition.Name, Kind: StoreIndexExact, State: StoreIndexReady,
			TotalChunks: s.state.root.LiveChunks, CoveredChunks: s.state.root.LiveChunks,
			ColumnCount: uint8(len(definition.Paths)),
		}
		copy(info.Columns[:], definition.Paths)
		dst = append(dst, info)
	}
	return dst
}

// AppendIndexMasks appends exact, collision-rechecked stable-slot masks for a
// frozen FileStore index. Directory and posting pages are read on demand; only
// candidate document pages are parsed for the mandatory equality recheck.
func (s *FileSnapshot) AppendIndexMasks(dst []StoreMask, name string, values ...Index) ([]StoreMask, error) {
	var workspace FileIndexWorkspace
	return s.AppendIndexMasksInto(dst, &workspace, name, values...)
}

// AppendIndexMasksInto is AppendIndexMasks with reusable transient storage.
// Exact tuple hashes remain candidate filters: every named document is parsed
// and every indexed path is compared before its stable-slot bit is returned.
// With sufficient dst and workspace capacity, a warmed cache-hit probe
// allocates nothing.
func (s *FileSnapshot) AppendIndexMasksInto(dst []StoreMask, workspace *FileIndexWorkspace, name string, values ...Index) ([]StoreMask, error) {
	if s == nil || s.store == nil || s.state == nil {
		return dst, ErrFileStoreClosed
	}
	if workspace == nil {
		var local FileIndexWorkspace
		workspace = &local
	}
	probe, err := s.prepareFileIndexProbe(workspace, name, values)
	if err != nil {
		return dst, err
	}
	for _, directoryEntry := range workspace.directory {
		posting, err := s.fileIndexPosting(probe, directoryEntry)
		if err != nil {
			return dst, err
		}
		documentRef, ok, lookupErr := storeio.LookupChunkTree(s.store.cache, probe.state.chunkRoot, posting.Chunk, storeio.ChunkTreeBounds{
			FileEnd: probe.state.super.FileEnd, NextLogicalID: probe.state.root.NextLogicalID,
		})
		if lookupErr != nil {
			return dst, lookupErr
		}
		if !ok {
			return dst, storeio.ErrPostingPageCorrupt
		}
		documentLease, acquireErr := s.store.cache.Acquire(documentRef)
		if acquireErr != nil {
			return dst, acquireErr
		}
		documentPage, openErr := storeio.OpenDocumentPageWithOverflow(
			documentLease.Page(), probe.state.root.ChunkHighWater, probe.state.root.NextLogicalID,
			probe.state.super.FileEnd, probe.state.root.PageSize,
		)
		if openErr != nil {
			documentLease.Release()
			return dst, openErr
		}
		verified := uint64(0)
		for bitsLeft := posting.Bits; bitsLeft != 0; bitsLeft &= bitsLeft - 1 {
			slot := uint8(bits.TrailingZeros64(bitsLeft))
			value, present := documentPage.LookupValue(slot)
			if !present {
				documentLease.Release()
				return dst, storeio.ErrPostingPageCorrupt
			}
			workspace.document = workspace.document[:0]
			workspace.document, err = s.store.appendFileValue(
				workspace.document, probe.state, value,
				storeio.KeyLocation{Chunk: posting.Chunk, Slot: slot},
			)
			if err != nil {
				documentLease.Release()
				return dst, err
			}
			needed, countErr := RequiredIndexEntries(workspace.document)
			if countErr != nil {
				documentLease.Release()
				return dst, countErr
			}
			if cap(workspace.tape) < needed {
				workspace.tape = make([]IndexEntry, needed)
			}
			index, buildErr := BuildIndexOptions(
				workspace.document, workspace.tape[:needed],
				s.store.options.Store.IndexOptions,
			)
			if buildErr != nil {
				documentLease.Release()
				return dst, buildErr
			}
			matches := true
			for column := 0; column < int(probe.exact.n); column++ {
				node, found, pointerErr := index.PointerCompiled(probe.exact.paths[column])
				if pointerErr != nil || !found || !node.Contains(values[column].Root()) || !values[column].Root().Contains(node) {
					matches = false
					break
				}
			}
			if matches {
				verified |= uint64(1) << slot
			}
		}
		documentLease.Release()
		if verified != 0 {
			dst = append(dst, StoreMask{Chunk: posting.Chunk, Bits: verified})
		}
	}
	return dst, nil
}

// AppendIndexCandidateMasks appends hash-bounded stable-slot candidates
// without reopening documents. It may return false positives and must be
// followed by an exact predicate recheck; it never turns a non-match into a
// public query result by itself. This lane exists for query engines that will
// immediately parse every candidate and avoids reading each document twice.
func (s *FileSnapshot) AppendIndexCandidateMasks(dst []StoreMask, name string, values ...Index) ([]StoreMask, error) {
	var workspace FileIndexWorkspace
	return s.AppendIndexCandidateMasksInto(dst, &workspace, name, values...)
}

// AppendIndexCandidateMasksInto is AppendIndexCandidateMasks with reusable
// directory storage. The returned masks are ordered, non-zero posting
// candidates, not exact answers.
func (s *FileSnapshot) AppendIndexCandidateMasksInto(dst []StoreMask, workspace *FileIndexWorkspace, name string, values ...Index) ([]StoreMask, error) {
	if s == nil || s.store == nil || s.state == nil {
		return dst, ErrFileStoreClosed
	}
	if workspace == nil {
		var local FileIndexWorkspace
		workspace = &local
	}
	probe, err := s.prepareFileIndexProbe(workspace, name, values)
	if err != nil {
		return dst, err
	}
	for _, directoryEntry := range workspace.directory {
		posting, err := s.fileIndexPosting(probe, directoryEntry)
		if err != nil {
			return dst, err
		}
		dst = append(dst, StoreMask{Chunk: posting.Chunk, Bits: posting.Bits})
	}
	return dst, nil
}

type fileIndexProbe struct {
	state   *fileStoreState
	exact   *storeExactIndex
	indexID uint32
	hash    uint64
}

func (s *FileSnapshot) prepareFileIndexProbe(workspace *FileIndexWorkspace, name string, values []Index) (fileIndexProbe, error) {
	indexID := -1
	for i, definition := range s.store.options.Indexes {
		if definition.Name == name {
			indexID = i
			break
		}
	}
	if indexID < 0 {
		return fileIndexProbe{}, ErrStoreIndexNotFound
	}
	exact := s.store.options.indexes[indexID]
	hash, err := fileIndexNeedleHash(exact, values)
	if err != nil {
		return fileIndexProbe{}, err
	}
	state := s.state
	probe := fileIndexProbe{state: state, exact: exact, indexID: uint32(indexID), hash: hash}
	workspace.directory = workspace.directory[:0]
	if state.indexRoot == (storeio.PageRef{}) {
		return probe, nil
	}
	if uint64(state.root.LiveChunks) > uint64(^uint(0)>>1) {
		return fileIndexProbe{}, ErrStoreTooLarge
	}
	workspace.directory, err = storeio.AppendIndexTreeHash(
		s.store.cache, state.indexRoot, probe.indexID, hash,
		storeio.IndexTreeBounds{
			FileEnd: state.super.FileEnd, NextLogicalID: state.root.NextLogicalID,
			IndexHighWater: state.root.IndexCount,
		}, workspace.directory, int(state.root.LiveChunks),
	)
	if err != nil {
		return fileIndexProbe{}, err
	}
	return probe, nil
}

func (s *FileSnapshot) fileIndexPosting(probe fileIndexProbe, directoryEntry storeio.IndexDirectoryEntry) (storeio.PostingEntry, error) {
	postingLease, err := s.store.cache.Acquire(directoryEntry.Posting.Page)
	if err != nil {
		return storeio.PostingEntry{}, err
	}
	postingPage, err := storeio.OpenPostingPage(
		postingLease.Page(), probe.state.root.NextLogicalID, probe.state.root.IndexCount,
	)
	if err != nil {
		postingLease.Release()
		return storeio.PostingEntry{}, err
	}
	segment, ok := postingPage.SegmentAt(int(directoryEntry.Posting.Segment))
	if !ok || postingPage.Header().IndexID != probe.indexID || segment.Header().TupleHash != probe.hash {
		postingLease.Release()
		return storeio.PostingEntry{}, storeio.ErrPostingPageCorrupt
	}
	iterator := segment.Iterator()
	posting, ok := iterator.Next()
	postingLease.Release()
	if !ok || posting.Chunk != directoryEntry.Key.Chunk {
		return storeio.PostingEntry{}, storeio.ErrPostingPageCorrupt
	}
	return posting, nil
}

// AppendIndexMasks acquires a temporary snapshot and returns exact masks. Hot
// callers should retain a FileSnapshot and FileIndexWorkspace instead.
func (s *FileStore) AppendIndexMasks(dst []StoreMask, name string, values ...Index) ([]StoreMask, error) {
	snapshot, err := s.Snapshot()
	if err != nil {
		return dst, err
	}
	defer snapshot.Close()
	return snapshot.AppendIndexMasks(dst, name, values...)
}
