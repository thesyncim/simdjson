package simdjson

import (
	"fmt"

	"github.com/thesyncim/simdjson/internal/storeio"
)

// HasFloat64Path reports whether path names a persistent typed covering
// column in this snapshot. Paths use the RFC 6901 spelling from
// [FileStoreOptions.Float64Columns].
func (s *FileSnapshot) HasFloat64Path(path string) bool {
	if s == nil || s.store == nil || s.state == nil ||
		s.state.root.Options&storeio.StateOptionFloat64Columns == 0 {
		return false
	}
	return s.float64ColumnOrdinal(path) >= 0
}

// ReduceFloat64Path reduces one persistent typed covering column without
// reading or parsing JSON values. Missing and non-numeric cells were omitted
// when their document generation was written, so the result has the same
// acceptance semantics as RawValue.Float64. The returned boolean is false
// when path is not configured; corruption and I/O errors are never converted
// to a fallback miss.
//
// A warmed scan allocates nothing. Cold reads remain bounded by the FileStore
// page cache and may evict unrelated document extents.
func (s *FileSnapshot) ReduceFloat64Path(path string) (Float64Aggregate, bool, error) {
	var totals [1]Float64Aggregate
	covered, err := s.ReduceFloat64PathsInto(totals[:], []string{path})
	return totals[0], covered, err
}

// ReduceFloat64PathsInto fuses several persistent typed covering columns into
// one page walk. dst and paths must have equal lengths. The method preflights
// the complete path list before admitting a document page: false therefore
// means no scan occurred and callers may safely choose one coherent fallback.
// Duplicate paths are allowed, though callers should normally deduplicate
// them. A warmed call allocates nothing.
func (s *FileSnapshot) ReduceFloat64PathsInto(dst []Float64Aggregate, paths []string) (bool, error) {
	if s == nil || s.store == nil || s.state == nil {
		return false, ErrFileStoreClosed
	}
	if len(dst) != len(paths) || len(paths) > fileStoreMaxFloat64Columns {
		return false, fmt.Errorf("simdjson: invalid float64 covering reduction buffers")
	}
	if len(paths) == 0 {
		return true, nil
	}
	if s.state.root.DocumentCount > uint64(maxInt()) {
		return false, ErrStoreTooLarge
	}
	if s.state.root.Options&storeio.StateOptionFloat64Columns == 0 {
		return false, nil
	}
	var ordinals [fileStoreMaxFloat64Columns]uint16
	for i, path := range paths {
		column := s.float64ColumnOrdinal(path)
		if column < 0 {
			return false, nil
		}
		ordinals[i] = uint16(column)
	}
	clear(dst)
	state := s.state
	err := storeio.WalkChunkTreeRuns(s.store.cache, state.chunkRoot, storeio.ChunkTreeBounds{
		FileEnd: state.super.FileEnd, NextLogicalID: state.root.NextLogicalID,
	}, func(first, chunks uint32, ref storeio.PageRef) error {
		if chunks == 0 || ref.Kind == storeio.PageDocument && chunks != 1 {
			return storeio.ErrChunkDirectoryCorrupt
		}
		lease, err := s.store.cache.Acquire(ref)
		if err != nil {
			return err
		}
		for chunkOrdinal := uint32(0); chunkOrdinal < chunks; chunkOrdinal++ {
			view, viewErr := admittedFileDocumentChunk(lease.Page(), ref, first+chunkOrdinal)
			if viewErr != nil {
				lease.Release()
				return viewErr
			}
			if view.float64ColumnCount() != len(s.store.options.float64Columns) {
				lease.Release()
				return fmt.Errorf("%w: float64 covering catalog", storeio.ErrDocumentPageCorrupt)
			}
			for i, ordinal := range ordinals[:len(paths)] {
				covered, ok := view.float64Column(int(ordinal))
				if !ok {
					lease.Release()
					return fmt.Errorf("%w: float64 covering ordinal", storeio.ErrDocumentPageCorrupt)
				}
				iterator := covered.Values()
				for {
					value, present := iterator.Next()
					if !present {
						break
					}
					dst[i].add(value)
				}
			}
		}
		lease.Release()
		return nil
	})
	if err != nil {
		clear(dst)
		return true, err
	}
	return true, nil
}

func (s *FileSnapshot) float64ColumnOrdinal(path string) int {
	for i, column := range s.store.options.float64Columns {
		if column.spec == path {
			return i
		}
	}
	return -1
}
