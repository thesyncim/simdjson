package simdjson

import (
	"runtime"
	"unsafe"

	"github.com/thesyncim/simdjson/internal/storemem"
)

// storeMappedDocRef is the pointer-free description of one document record in
// a validated Store page image. recordOff is relative to that page's DocSet
// source; source and aligned tape offsets are derived from the fixed record
// header instead of being stored twice. A ref never escapes; docAt reconstructs
// ordinary borrowed slices while Store state pins the complete image.
type storeMappedDocRef struct {
	recordOff  uint64
	srcLen     uint32
	entryCount uint32
	start      uint32
	end        uint32
	shapeID    uint32
	kind       uint8
	enriched   bool
	_          [2]byte
}

const storeMappedNoShape = ^uint32(0)

// The layout is an on-host control-plane ABI. Keep it compact and pointer-free:
// one descriptor per row must not become one GC-scanned object per row.
const storeMappedDocRefBytes = 32

var _ [storeMappedDocRefBytes - unsafe.Sizeof(storeMappedDocRef{})]byte
var _ [unsafe.Sizeof(storeMappedDocRef{}) - storeMappedDocRefBytes]byte

// storeMappedDocs owns every row descriptor for one OpenStore call in one
// pointer-free anonymous region. Chunks retain only this owner and a base
// ordinal. No pointer into the region is returned, so finalization cannot
// invalidate RawValue, Index, or Node; those borrow the separately caller-
// owned Store image under its existing explicit lifetime contract.
type storeMappedDocs struct {
	refs  []storeMappedDocRef
	block *storemem.Block
}

func newStoreMappedDocs(count int) (*storeMappedDocs, error) {
	size := int(unsafe.Sizeof(storeMappedDocRef{}))
	if count < 0 || count > maxInt()/size {
		return nil, ErrStorePersistTooLarge
	}
	block, err := storemem.Allocate(count * size)
	if err != nil {
		return nil, err
	}
	var refs []storeMappedDocRef
	if count != 0 {
		refs = unsafe.Slice((*storeMappedDocRef)(unsafe.Pointer(unsafe.SliceData(block.Bytes()))), count)
	}
	m := &storeMappedDocs{refs: refs, block: block}
	runtime.SetFinalizer(m, (*storeMappedDocs).release)
	return m, nil
}

func (m *storeMappedDocs) release() {
	if m == nil || m.block == nil {
		return
	}
	_ = m.block.Close()
	m.block = nil
	m.refs = nil
}

func (m *storeMappedDocs) externalBytes() uint64 {
	if m == nil || m.block == nil || !m.block.OutsideHeap() {
		return 0
	}
	return uint64(m.block.Len())
}

func (s *DocSet) docAt(i int) Index {
	if s.mappedDocs == nil {
		return s.docs[i]
	}
	r := &s.mappedDocs.refs[s.mappedBase+uint64(i)]
	srcOff := r.recordOff + persistRecordHeaderLen
	srcEnd := srcOff + uint64(r.srcLen)
	entriesOff := persistAlign8(srcEnd)
	kind, count := r.kind, r.entryCount
	runtime.KeepAlive(s.mappedDocs)
	src := s.source[srcOff:srcEnd:srcEnd]
	if kind == persistDocNarrow {
		return Index{src: src}
	}
	return Index{src: src, entries: openEntries(s.source, entriesOff, uint64(count))}
}

// rawAt is the point-read form of docAt. It reconstructs only the source view,
// avoiding entry-tape work on Store.GetRaw and compiled key lookups.
func (s *DocSet) rawAt(i int) []byte {
	if s.mappedDocs == nil {
		return s.docs[i].src
	}
	r := &s.mappedDocs.refs[s.mappedBase+uint64(i)]
	start := r.recordOff + persistRecordHeaderLen
	end := start + uint64(r.srcLen)
	runtime.KeepAlive(s.mappedDocs)
	return s.source[start:end:end]
}

func (s *DocSet) narrowAt(i int, ref shapeTapeRef, ordinal int) shapeNarrowValue {
	if s.mappedDocs == nil {
		return s.narrow[int(ref.off)+ordinal]
	}
	r := &s.mappedDocs.refs[s.mappedBase+uint64(i)]
	srcEnd := r.recordOff + persistRecordHeaderLen + uint64(r.srcLen)
	off := persistAlign8(srcEnd) + uint64(ordinal)*uint64(unsafe.Sizeof(shapeNarrowValue{}))
	value := *(*shapeNarrowValue)(unsafe.Pointer(&s.source[off]))
	runtime.KeepAlive(s.mappedDocs)
	return value
}

func (s *DocSet) narrowLen() int {
	if s.mappedDocs != nil {
		return s.mappedNarrow
	}
	return len(s.narrow)
}
