package simdjson

import "time"

// StoreStats is an O(1), allocation-free operational snapshot. The Chunks field
// counts materialized immutable chunks; ChunkHighWater is the persistent
// vector's address span and may be larger after deletes. Sparse vector
// traversal skips absent branches, so that difference is metadata, not scan or
// compaction debt. ReusableChunks includes both partially filled and empty
// chunk ids.
type StoreStats struct {
	// Generation is the latest atomic publication number.
	Generation uint64
	// Keys is the current document count.
	Keys int
	// Chunks is the number of materialized immutable chunks.
	Chunks uint32
	// ChunkHighWater is the persistent vector's address span.
	ChunkHighWater uint32
	// ChunkDocuments is the configured per-chunk document bound.
	ChunkDocuments int
	// ReusableChunks counts partially filled and empty writer-side ids.
	ReusableChunks int
	// ExpiringKeys is the exact TTL heap-node count.
	ExpiringKeys int
	// Indexes is the number of logical online index definitions.
	Indexes int
	// IndexedChunks counts chunks that physically retain postings.
	IndexedChunks int
	// IndexReclaiming reports detached physical postings still being removed.
	IndexReclaiming bool
}

// Stats returns current writer and publication counters without traversing
// documents or allocating. It briefly takes the writer mutex so TTL and
// reclamation counters describe the same instant as the published state.
func (s *Store) Stats() StoreStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.state.Load()
	if state == nil {
		chunkDocuments := s.Options.ChunkDocuments
		if chunkDocuments == 0 {
			chunkDocuments = storeMaxChunkDocuments
		}
		return StoreStats{ChunkDocuments: chunkDocuments}
	}
	return StoreStats{
		Generation:      state.generation,
		Keys:            state.count,
		Chunks:          state.chunkCount,
		ChunkHighWater:  state.chunks.count,
		ChunkDocuments:  state.options.ChunkDocuments,
		ReusableChunks:  len(s.free.ids),
		ExpiringKeys:    len(s.ttl.heap),
		Indexes:         len(s.indexes),
		IndexedChunks:   len(s.postingChunks.ids),
		IndexReclaiming: s.reclaim != nil,
	}
}

// NextExpiration returns the earliest assigned deadline. Expired keys remain
// visible until ExpireDue or RunExpiry publishes their ordinary delete.
func (s *Store) NextExpiration() (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.ttl.heap) == 0 {
		return time.Time{}, false
	}
	return s.ttl.heap[0].deadline.time(), true
}
