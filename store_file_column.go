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
	if state.root.Float64ScanHead != (storeio.PageRef{}) {
		if err := s.reduceFloat64ScanChain(dst, ordinals[:len(paths)]); err != nil {
			clear(dst)
			return true, err
		}
		return true, nil
	}
	err := storeio.WalkChunkTreeFloat64Runs(s.store.cache, state.chunkRoot, storeio.ChunkTreeBounds{
		FileEnd: state.super.FileEnd, NextLogicalID: state.root.NextLogicalID,
	}, uint32(s.store.options.PageSize), func(
		first, chunks uint32, ref storeio.PageRef, detached bool,
	) error {
		return s.reduceFloat64MappedRun(
			dst, ordinals[:len(paths)], first, chunks, ref, detached,
		)
	})
	if err != nil {
		clear(dst)
		return true, err
	}
	return true, nil
}

// reduceFloat64ScanChain is the untouched compact-generation fast path. The
// state-root catalog and contiguous value-only stripes omit the stable-slot
// tree walk; the first document write clears the head, making the general
// overlay-aware path above authoritative for mixed base and peeled chunks.
func (s *FileSnapshot) reduceFloat64ScanChain(dst []Float64Aggregate, ordinals []uint16) error {
	state := s.state
	catalogRef := state.root.Float64ScanHead
	nextChunk := uint32(0)
	var previousScanRef storeio.PageRef
	for catalogRef != (storeio.PageRef{}) {
		lease, err := s.store.cache.Acquire(catalogRef)
		if err != nil {
			return err
		}
		catalog := storeio.AdmittedFloat64Catalog(lease.Page())
		header := catalog.Header()
		var refs [64]storeio.PageRef
		for first := 0; first < catalog.Len(); first += len(refs) {
			count := min(len(refs), catalog.Len()-first)
			for i := 0; i < count; i++ {
				var ok bool
				refs[i], ok = catalog.RefAt(first + i)
				if !ok {
					lease.Release()
					return storeio.ErrFloat64CatalogCorrupt
				}
			}
			if _, err := s.store.cache.Prefetch(refs[:count]); err != nil {
				lease.Release()
				return err
			}
			for i := 0; i < count; i++ {
				if previousScanRef != (storeio.PageRef{}) &&
					(refs[i].Offset <= previousScanRef.Offset ||
						refs[i].LogicalID <= previousScanRef.LogicalID) {
					lease.Release()
					return fmt.Errorf(
						"%w: float64 catalog global order",
						storeio.ErrFloat64CatalogCorrupt,
					)
				}
				groupLease, acquireErr := s.store.cache.Acquire(refs[i])
				if acquireErr != nil {
					lease.Release()
					return acquireErr
				}
				stripe := storeio.AdmittedFloat64Stripe(groupLease.Page())
				stripeHeader := stripe.Header()
				if stripeHeader.FirstChunk != nextChunk ||
					stripeHeader.ColumnCount != uint16(len(s.store.options.float64Columns)) {
					groupLease.Release()
					lease.Release()
					return fmt.Errorf("%w: float64 stripe coverage", storeio.ErrFloat64StripeCorrupt)
				}
				for column, ordinal := range ordinals {
					values, encoding, found := stripe.ColumnValues(int(ordinal))
					if !found || !dst[column].addPackedFloat64Width(values, encoding.ByteWidth()) {
						groupLease.Release()
						lease.Release()
						return fmt.Errorf("%w: float64 stripe column", storeio.ErrFloat64StripeCorrupt)
					}
				}
				coveredChunks := stripeHeader.ChunkCount
				if uint64(nextChunk)+uint64(coveredChunks) > uint64(^uint32(0)) {
					groupLease.Release()
					lease.Release()
					return fmt.Errorf("%w: float64 scan chunk overflow", storeio.ErrFloat64GroupCorrupt)
				}
				nextChunk += coveredChunks
				previousScanRef = refs[i]
				groupLease.Release()
			}
		}
		next := header.Next
		if next != (storeio.PageRef{}) &&
			(next.Offset <= catalogRef.Offset || next.LogicalID <= catalogRef.LogicalID) {
			lease.Release()
			return fmt.Errorf("%w: float64 catalog link order", storeio.ErrFloat64CatalogCorrupt)
		}
		lease.Release()
		catalogRef = next
	}
	if nextChunk != state.root.ChunkHighWater {
		return fmt.Errorf("%w: incomplete float64 stripe coverage", storeio.ErrFloat64StripeCorrupt)
	}
	return nil
}

func (s *FileSnapshot) reduceFloat64MappedRun(
	dst []Float64Aggregate,
	ordinals []uint16,
	first, chunks uint32,
	ref storeio.PageRef,
	detached bool,
) error {
	if chunks == 0 || ref.Kind == storeio.PageDocument && chunks != 1 {
		return storeio.ErrChunkDirectoryCorrupt
	}
	lease, err := s.store.cache.Acquire(ref)
	if err != nil {
		return err
	}
	if detached {
		group := storeio.AdmittedFloat64Group(lease.Page())
		header := group.Header()
		runEnd := uint64(first) + uint64(chunks)
		groupEnd := uint64(header.FirstChunk) + uint64(header.ChunkCount)
		if header.ColumnCount != uint16(len(s.store.options.float64Columns)) ||
			first < header.FirstChunk || runEnd > groupEnd {
			lease.Release()
			return fmt.Errorf("%w: detached float64 covering catalog", storeio.ErrFloat64GroupCorrupt)
		}
		for i, ordinal := range ordinals {
			values, encoding, found := group.Float64ColumnRangeValues(int(ordinal), first, chunks)
			if !found || !dst[i].addPackedFloat64Width(values, encoding.ByteWidth()) {
				lease.Release()
				return fmt.Errorf("%w: detached float64 packed column", storeio.ErrFloat64GroupCorrupt)
			}
		}
		lease.Release()
		return nil
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
		for i, ordinal := range ordinals {
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
}

func (s *FileSnapshot) float64ColumnOrdinal(path string) int {
	for i, column := range s.store.options.float64Columns {
		if column.spec == path {
			return i
		}
	}
	return -1
}
