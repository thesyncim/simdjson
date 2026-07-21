package simdjson

import (
	"errors"
	"slices"
	"strings"
)

// StoreIndexKind names an online secondary-index family.
type StoreIndexKind uint8

const (
	// StoreIndexPostings builds the DocSet existence/scalar-containment posting
	// layer in every Store chunk. It accelerates query equality, existence, and
	// scalar containment while exact predicate rechecks preserve semantics.
	StoreIndexPostings StoreIndexKind = iota + 1
)

// StoreIndexState is an online index's publication state.
type StoreIndexState uint8

const (
	// StoreIndexBuilding means exact scan fallback is still required for some
	// live chunks.
	StoreIndexBuilding StoreIndexState = iota + 1
	// StoreIndexReady means every live chunk has physical coverage.
	StoreIndexReady
)

// StoreIndexInfo is immutable index metadata published with a Snapshot.
// During Building, covered chunks already carry the index and uncovered
// chunks remain exact through scan fallback. Ready means every current chunk
// was covered; later writes dual-maintain it before publication.
type StoreIndexInfo struct {
	// Name is the caller-assigned logical index name.
	Name string
	// Kind identifies the shared physical index family.
	Kind StoreIndexKind
	// State is Building until CoveredChunks equals TotalChunks.
	State StoreIndexState
	// CoveredChunks is the count eligible for the physical accelerated path.
	CoveredChunks uint32
	// TotalChunks is the number of live chunks in this publication.
	TotalChunks uint32
}

var (
	// ErrStoreIndexExists reports an AddIndex name collision.
	ErrStoreIndexExists = errors.New("simdjson: Store index already exists")
	// ErrStoreIndexNotFound reports an unknown BackfillIndex or DropIndex name.
	ErrStoreIndexNotFound = errors.New("simdjson: Store index not found")
	// ErrStoreIndexKind reports a StoreIndexKind this build does not implement.
	ErrStoreIndexKind = errors.New("simdjson: unsupported Store index kind")
)

type storeIndexBuild struct {
	info     StoreIndexInfo
	coverage storeCoverage
	scan     storeChunkVector
	cursor   uint64
	all      bool
}

type storeIndexReclaim struct{}

// AddIndex atomically publishes an online index definition. Existing chunks
// are backfilled by BackfillIndex; all writes from this call onward build the
// index before their new snapshot is visible. Query consumers can inspect the
// published coverage and use exact scan fallback until Ready.
func (s *Store) AddIndex(name string, kind StoreIndexKind) (StoreIndexInfo, error) {
	if kind != StoreIndexPostings {
		return StoreIndexInfo{}, ErrStoreIndexKind
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.initLocked()
	if err != nil {
		return StoreIndexInfo{}, err
	}
	if s.indexes == nil {
		s.indexes = make(map[string]*storeIndexBuild)
	}
	if _, exists := s.indexes[name]; exists {
		return StoreIndexInfo{}, ErrStoreIndexExists
	}
	name = strings.Clone(name)
	s.reclaim = nil // a new consumer cancels removal of the same physical layer
	b := &storeIndexBuild{info: StoreIndexInfo{
		Name:        name,
		Kind:        kind,
		State:       StoreIndexBuilding,
		TotalChunks: state.chunkCount,
	}, scan: state.chunks}
	// Logical postings indexes share one physical layer. Copying an existing
	// build's coverage is O(coverage words), not a document pass; a Store born
	// with Postings already covers the complete vector. If reclamation was in
	// flight, start conservatively uncovered and let bounded backfill discover
	// the still-indexed chunks without trusting stale logical metadata.
	for _, existing := range s.indexes {
		if existing.info.Kind == kind {
			b.coverage = existing.coverage.clone()
			b.info.CoveredChunks = existing.info.CoveredChunks
			b.info.State = existing.info.State
			b.all = existing.all
			break
		}
	}
	if state.options.Postings {
		b.info.CoveredChunks = b.info.TotalChunks
	}
	b.updateState()
	s.indexes[name] = b
	next := *state
	next.generation++
	next.indexes = s.indexInfosLocked()
	s.state.Store(&next)
	return b.info, nil
}

// BackfillIndex examines and rebuilds at most maxChunks chunks from the
// definition's immutable start snapshot, then publishes changed coverage
// atomically. maxChunks <= 0 means all remaining chunks. The resumable radix
// cursor skips deleted subtrees without scanning integer ids. Writes that
// touched a chunk after AddIndex already marked it covered and need no rebuild.
func (s *Store) BackfillIndex(name string, maxChunks int) (StoreIndexInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.indexes[name]
	if b == nil {
		return StoreIndexInfo{}, ErrStoreIndexNotFound
	}
	state := s.state.Load()
	if b.info.State == StoreIndexReady || state == nil {
		return b.info, nil
	}
	nextChunks := state.chunks
	examined := 0
	changed := false
	for maxChunks <= 0 || examined < maxChunks {
		id, _, ok := b.scan.next(b.cursor)
		if !ok {
			break
		}
		b.cursor = uint64(id) + 1
		examined++
		if b.has(id) {
			continue
		}
		chunk := state.chunks.get(id)
		if chunk == nil {
			continue
		}
		if !chunk.docs.Postings {
			rebuilt, err := cloneStoreChunk(state.options, true, chunk)
			if err != nil {
				return b.info, err
			}
			nextChunks = nextChunks.set(id, rebuilt)
			s.noteChunkPostingsLocked(id, chunk, rebuilt)
		}
		s.markIndexesCoveredLocked(id)
		changed = true
	}
	b.updateState()
	if changed {
		next := *state
		next.generation++
		next.chunks = nextChunks
		next.indexes = s.indexInfosLocked()
		s.state.Store(&next)
	}
	return b.info, nil
}

// DropIndex removes the logical definition from the next snapshot immediately.
// If it was the last postings consumer, future writes omit postings and
// ReclaimIndexes can rebuild old chunks in bounded batches. Old Snapshots keep
// their indexed chunks until ordinary garbage collection releases them.
func (s *Store) DropIndex(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.indexes == nil || s.indexes[name] == nil {
		return ErrStoreIndexNotFound
	}
	delete(s.indexes, name)
	state := s.state.Load()
	if state == nil {
		return nil
	}
	if !s.options.Postings && !s.hasPostingsIndexLocked() {
		if len(s.postingChunks.ids) != 0 {
			s.reclaim = &storeIndexReclaim{}
		}
	}
	next := *state
	next.generation++
	next.indexes = s.indexInfosLocked()
	s.state.Store(&next)
	return nil
}

// ReclaimIndexes removes physically detached postings from at most maxChunks
// chunks, atomically publishing the batch. It returns the number rebuilt and
// whether reclamation is complete. maxChunks <= 0 processes all remaining
// chunks. No live read is blocked and old snapshots retain their old chunks.
func (s *Store) ReclaimIndexes(maxChunks int) (rebuilt int, done bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reclaim == nil || s.options.Postings || s.hasPostingsIndexLocked() {
		return 0, true
	}
	state := s.state.Load()
	if state == nil || len(s.postingChunks.ids) == 0 {
		s.reclaim = nil
		return 0, true
	}
	limit := maxChunks
	if limit <= 0 {
		limit = int(state.chunks.count)
	}
	nextChunks := state.chunks
	for len(s.postingChunks.ids) != 0 && rebuilt < limit {
		id := s.postingChunks.ids[len(s.postingChunks.ids)-1]
		chunk := state.chunks.get(id)
		if chunk == nil || !chunk.docs.Postings {
			s.postingChunks.remove(id)
			continue
		}
		plain, err := cloneStoreChunk(state.options, false, chunk)
		if err != nil {
			panic("simdjson: rebuilding validated Store chunk: " + err.Error())
		}
		nextChunks = nextChunks.set(id, plain)
		s.noteChunkPostingsLocked(id, chunk, plain)
		rebuilt++
	}
	done = len(s.postingChunks.ids) == 0
	if rebuilt != 0 {
		next := *state
		next.generation++
		next.chunks = nextChunks
		s.state.Store(&next)
	}
	if done {
		s.reclaim = nil
	}
	return rebuilt, done
}

func (b *storeIndexBuild) has(id uint32) bool {
	if b.all {
		return true
	}
	return b.coverage.has(id)
}

func (b *storeIndexBuild) mark(id uint32) {
	if b.all {
		b.info.CoveredChunks = b.info.TotalChunks
		return
	}
	if b.coverage.mark(id) {
		b.info.CoveredChunks++
	}
}

func (b *storeIndexBuild) unmark(id uint32) {
	if b.all {
		b.info.CoveredChunks = b.info.TotalChunks
		return
	}
	if b.coverage.unmark(id) {
		b.info.CoveredChunks--
	}
}

// updateState collapses complete coverage to an implicit all-live-chunks
// representation. Readiness is monotonic because every later write builds the
// active physical family before publication. Discarding both the bitmap and
// AddIndex snapshot prevents a completed logical index from pinning historical
// chunks or retaining one bit per chunk indefinitely.
func (b *storeIndexBuild) updateState() {
	if b.info.CoveredChunks == b.info.TotalChunks {
		b.info.State = StoreIndexReady
		b.coverage = storeCoverage{}
		b.all = true
		b.scan = storeChunkVector{}
		b.cursor = 0
		return
	}
	b.info.State = StoreIndexBuilding
}

func (s *Store) hasPostingsIndexLocked() bool {
	for _, b := range s.indexes {
		if b.info.Kind == StoreIndexPostings {
			return true
		}
	}
	return false
}

func (s *Store) markIndexesCoveredLocked(id uint32) {
	for _, b := range s.indexes {
		if b.info.Kind == StoreIndexPostings {
			b.mark(id)
			b.updateState()
		}
	}
}

// noteIndexesForChunkLocked updates logical coverage for a Store write. A
// newly materialized chunk joins every logical index already fully built;
// removing the last document removes that chunk from both total and covered
// counts. Rewrites dual-maintain before publication and therefore mark the
// chunk covered even while background backfill is still running elsewhere.
func (s *Store) noteIndexesForChunkLocked(id uint32, old, next *storeChunk) {
	oldLive, nextLive := old != nil, next != nil
	for _, b := range s.indexes {
		if b.info.Kind != StoreIndexPostings {
			continue
		}
		switch {
		case !oldLive && nextLive:
			b.info.TotalChunks++
			b.mark(id)
		case oldLive && !nextLive:
			b.info.TotalChunks--
			b.unmark(id)
		case nextLive:
			b.mark(id)
		}
		b.updateState()
	}
}

func (s *Store) indexInfosLocked() []StoreIndexInfo {
	if len(s.indexes) == 0 {
		return nil
	}
	out := make([]StoreIndexInfo, 0, len(s.indexes))
	for _, b := range s.indexes {
		out = append(out, b.info)
	}
	slices.SortFunc(out, func(a, b StoreIndexInfo) int {
		if a.Name < b.Name {
			return -1
		}
		if a.Name > b.Name {
			return 1
		}
		return 0
	})
	return out
}

// AppendIndexes appends immutable index metadata to dst.
func (s Snapshot) AppendIndexes(dst []StoreIndexInfo) []StoreIndexInfo {
	if s.state == nil {
		return dst
	}
	return append(dst, s.state.indexes...)
}
