package simdjson

import (
	"math/bits"
	"slices"

	"github.com/thesyncim/simdjson/internal/storeio"
)

const fileScanReadAheadLimit = 64

type fileScanPage struct {
	ref   storeio.PageRef
	mask  uint64
	chunk uint32
}

// RangeRaw visits live rows in ascending chunk/slot order. key and value are
// borrowed only for the callback; overflow values reuse one bounded buffer.
// Returning an error stops the scan immediately.
func (s *FileSnapshot) RangeRaw(fn func(key, value []byte) error) error {
	_, err := s.RangeRawBuffer(nil, fn)
	return err
}

// RangeRawBuffer is RangeRaw with caller-owned overflow storage. The returned
// slice preserves any grown capacity for the next scan. Inline-only scans and
// warmed overflow scans allocate nothing when scratch has sufficient capacity.
//
// This method issues document reads serially. Use RangeRawReadAheadBuffer for
// a cold scan whose corpus exceeds the resident page budget.
func (s *FileSnapshot) RangeRawBuffer(scratch []byte, fn func(key, value []byte) error) ([]byte, error) {
	if s == nil || s.store == nil || s.state == nil {
		return scratch, ErrFileStoreClosed
	}
	if fn == nil {
		return scratch, nil
	}
	state := s.state
	err := storeio.WalkChunkTree(s.store.cache, state.chunkRoot, storeio.ChunkTreeBounds{
		FileEnd: state.super.FileEnd, NextLogicalID: state.root.NextLogicalID,
	}, func(chunk uint32, ref storeio.PageRef) error {
		return s.rangeFileDocumentPage(state, chunk, ref, ^uint64(0), &scratch, fn)
	})
	return scratch, err
}

// RangeRawReadAheadBuffer is the bounded cold-scan form of RangeRawBuffer.
// It discovers a small chunk-ordered window, submits its document extents in
// physical order, and still invokes fn in exact chunk/slot order. The window
// is capped by one quarter of ResidentBytes, PrefetchQueue, four requests per
// read worker, and 64 extents. Queue pressure merely shortens read-ahead;
// demand reads remain authoritative and return every validation or I/O error.
//
// Read-ahead is speculative: if fn stops early, at most one bounded window may
// already have been submitted. The method retains no page lease across fn and
// allocates nothing after caller overflow capacity is warm.
func (s *FileSnapshot) RangeRawReadAheadBuffer(scratch []byte, fn func(key, value []byte) error) ([]byte, error) {
	if s == nil || s.store == nil || s.state == nil {
		return scratch, ErrFileStoreClosed
	}
	if fn == nil {
		return scratch, nil
	}
	// Buffered files already receive kernel readahead, and feeding resident
	// hits through the user-space queue costs more than a direct scan. Explicit
	// read-ahead is for O_DIRECT, where each miss otherwise blocks the walker.
	if !s.store.direct || s.store.options.ReadConcurrency == 1 {
		return s.RangeRawBuffer(scratch, fn)
	}
	state := s.state
	var pages [fileScanReadAheadLimit]fileScanPage
	count := 0
	bytes := uint64(0)
	pageLimit, byteLimit := s.fileScanReadAheadWindow()
	flush := func() error {
		if count == 0 {
			return nil
		}
		err := s.rangeFileReadAheadBatch(state, pages[:count], &scratch, fn)
		count = 0
		bytes = 0
		return err
	}
	err := storeio.WalkChunkTree(s.store.cache, state.chunkRoot, storeio.ChunkTreeBounds{
		FileEnd: state.super.FileEnd, NextLogicalID: state.root.NextLogicalID,
	}, func(chunk uint32, ref storeio.PageRef) error {
		length := uint64(ref.Length)
		if count != 0 && (count == pageLimit || bytes+length > byteLimit) {
			if err := flush(); err != nil {
				return err
			}
		}
		pages[count] = fileScanPage{ref: ref, mask: ^uint64(0), chunk: chunk}
		count++
		bytes += length
		return nil
	})
	if err == nil {
		err = flush()
	}
	return scratch, err
}

// RangeMasksRaw visits only the live stable slots named by ordered masks.
// Masks must be strictly increasing by Chunk; zero and dead bits are ignored.
// The callback order is identical to filtering RangeRaw, so query execution
// can push an exact index bound into page reads without changing LIMIT,
// grouping, or stable tie semantics. Inline key/value slices borrow one cache
// lease for the callback. One overflow buffer is reused for the complete call.
func (s *FileSnapshot) RangeMasksRaw(masks []StoreMask, fn func(key, value []byte) error) error {
	_, err := s.RangeMasksRawBuffer(masks, nil, fn)
	return err
}

// RangeMasksRawBuffer is RangeMasksRaw with caller-owned overflow storage.
// The returned slice preserves capacity even when iteration stops with an
// error, allowing a retry loop to remain allocation-free.
func (s *FileSnapshot) RangeMasksRawBuffer(masks []StoreMask, scratch []byte, fn func(key, value []byte) error) ([]byte, error) {
	if s == nil || s.store == nil || s.state == nil {
		return scratch, ErrFileStoreClosed
	}
	if fn == nil {
		return scratch, nil
	}
	state := s.state
	bounds := storeio.ChunkTreeBounds{
		FileEnd: state.super.FileEnd, NextLogicalID: state.root.NextLogicalID,
	}
	var previous uint32
	for i, mask := range masks {
		if i != 0 && mask.Chunk <= previous {
			return scratch, ErrStoreMaskOrder
		}
		previous = mask.Chunk
		if mask.Bits == 0 {
			continue
		}
		ref, ok, err := storeio.LookupChunkTree(s.store.cache, state.chunkRoot, mask.Chunk, bounds)
		if err != nil {
			return scratch, err
		}
		if !ok {
			return scratch, ErrStoreMaskChunk
		}
		if err := s.rangeFileDocumentPage(state, mask.Chunk, ref, mask.Bits, &scratch, fn); err != nil {
			return scratch, err
		}
	}
	return scratch, nil
}

func (s *FileSnapshot) fileScanReadAheadWindow() (int, uint64) {
	options := s.store.options
	pageLimit := min(fileScanReadAheadLimit, options.PrefetchQueue, options.ReadConcurrency*4)
	if pageLimit < 1 {
		pageLimit = 1
	}
	byteLimit := uint64(options.ResidentBytes / 4)
	if byteLimit < uint64(options.MaxPageSize) {
		byteLimit = uint64(options.MaxPageSize)
	}
	return pageLimit, byteLimit
}

func (s *FileSnapshot) rangeFileReadAheadBatch(
	state *fileStoreState,
	pages []fileScanPage,
	overflow *[]byte,
	fn func(key, value []byte) error,
) error {
	var refs [fileScanReadAheadLimit]storeio.PageRef
	for i := range pages {
		refs[i] = pages[i].ref
	}
	physical := refs[:len(pages)]
	slices.SortFunc(physical, func(a, b storeio.PageRef) int {
		switch {
		case a.Offset < b.Offset:
			return -1
		case a.Offset > b.Offset:
			return 1
		default:
			return 0
		}
	})
	if _, err := s.store.cache.Prefetch(physical); err != nil {
		return err
	}
	for i := range pages {
		page := pages[i]
		if err := s.rangeFileDocumentPage(state, page.chunk, page.ref, page.mask, overflow, fn); err != nil {
			return err
		}
	}
	return nil
}

func (s *FileSnapshot) rangeFileDocumentPage(
	state *fileStoreState,
	chunk uint32,
	ref storeio.PageRef,
	mask uint64,
	overflow *[]byte,
	fn func(key, value []byte) error,
) error {
	lease, err := s.store.cache.Acquire(ref)
	if err != nil {
		return err
	}
	view, err := storeio.OpenDocumentPageWithOverflow(
		lease.Page(), state.root.ChunkHighWater, state.root.NextLogicalID,
		state.super.FileEnd, state.root.PageSize,
	)
	if err != nil {
		lease.Release()
		return err
	}
	if view.Header().ChunkID != chunk {
		lease.Release()
		return storeio.ErrDocumentPageCorrupt
	}
	for live := view.Header().Live & mask; live != 0; live &= live - 1 {
		slot := uint8(bits.TrailingZeros64(live))
		record, ok := view.Lookup(slot)
		if !ok {
			lease.Release()
			return storeio.ErrDocumentPageCorrupt
		}
		value := record.JSON
		if record.Overflow != (storeio.PageRef{}) {
			*overflow = (*overflow)[:0]
			*overflow, err = s.store.appendFileValue(*overflow, state, storeio.DocumentValue{
				Overflow: record.Overflow, Length: record.JSONLength,
			}, storeio.KeyLocation{Chunk: chunk, Slot: slot})
			if err != nil {
				lease.Release()
				return err
			}
			value = *overflow
		}
		if err := fn(record.Key, value); err != nil {
			lease.Release()
			return err
		}
	}
	lease.Release()
	return nil
}
