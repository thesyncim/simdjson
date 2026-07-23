package storeio

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	Float64CatalogPayloadHeaderSize = 64
	float64CatalogVersionV1         = uint32(1)
	float64CatalogVersion           = uint32(2)
)

// ErrFloat64CatalogCorrupt reports a checksum-valid compact scan catalog with
// malformed ordering, bounds, or typed-extent references.
var ErrFloat64CatalogCorrupt = errors.New("simdjson: corrupt Store float64 scan catalog")

// Float64CatalogHeader identifies one immutable page in the clean-generation
// scan list. Catalogs contain value-only stripe refs and no per-row state.
type Float64CatalogHeader struct {
	StoreID    [16]byte
	Generation uint64
	LogicalID  uint64
	PageSize   uint32
	Next       PageRef
}

// Float64CatalogView borrows one admitted catalog page.
type Float64CatalogView struct {
	header  Float64CatalogHeader
	payload []byte
	count   int
	version uint32
}

// EncodeFloat64Catalog writes one ordered scan-catalog page without
// allocating. refs must follow physical and logical bulk-build order.
func EncodeFloat64Catalog(
	dst []byte,
	header Float64CatalogHeader,
	refs []PageRef,
	fileEnd, nextLogicalID uint64,
	allocationQuantum uint32,
) ([]byte, error) {
	return encodeFloat64Catalog(
		dst, header, refs, fileEnd, nextLogicalID, allocationQuantum,
		float64CatalogVersionV1,
	)
}

// EncodeMutableFloat64Catalog writes a catalog whose logical stripe order is
// stable while replacement extents may have newer generations and arbitrary
// physical offsets. This is the copy-on-write form used after incrementally
// rebuilding one dense stripe.
func EncodeMutableFloat64Catalog(
	dst []byte,
	header Float64CatalogHeader,
	refs []PageRef,
	fileEnd, nextLogicalID uint64,
	allocationQuantum uint32,
) ([]byte, error) {
	return encodeFloat64Catalog(
		dst, header, refs, fileEnd, nextLogicalID, allocationQuantum,
		float64CatalogVersion,
	)
}

func encodeFloat64Catalog(
	dst []byte,
	header Float64CatalogHeader,
	refs []PageRef,
	fileEnd, nextLogicalID uint64,
	allocationQuantum uint32,
	version uint32,
) ([]byte, error) {
	mutable := version == float64CatalogVersion
	if len(refs) == 0 || len(refs) > int(^uint16(0)) ||
		header.StoreID == ([16]byte{}) || header.Generation == 0 ||
		header.LogicalID <= StateRootLogicalID || header.LogicalID >= nextLogicalID ||
		!validPhysicalPageSize(header.PageSize) ||
		!validPhysicalPageSize(allocationQuantum) ||
		header.PageSize < allocationQuantum || header.PageSize%allocationQuantum != 0 ||
		Float64CatalogPayloadHeaderSize+len(refs)*PageRefSize >
			int(header.PageSize)-PageHeaderSize-PageTrailerSize {
		return nil, fmt.Errorf("%w: float64 catalog header", ErrInvalidWrite)
	}
	if err := validateFloat64CatalogNext(
		header, fileEnd, nextLogicalID, allocationQuantum, mutable,
	); err != nil {
		return nil, err
	}
	if err := validateFloat64CatalogRefs(
		refs, header, fileEnd, nextLogicalID, allocationQuantum, mutable,
	); err != nil {
		return nil, err
	}
	payload, err := InitPage(dst, PageHeader{
		StoreID: header.StoreID, Generation: header.Generation, LogicalID: header.LogicalID,
		PageSize: header.PageSize, PayloadLength: uint32(Float64CatalogPayloadHeaderSize + len(refs)*PageRefSize),
		Kind: PageFloat64Catalog,
	})
	if err != nil {
		return nil, err
	}
	binary.LittleEndian.PutUint32(payload[0:4], version)
	binary.LittleEndian.PutUint16(payload[4:6], uint16(len(refs)))
	encodePageRef(payload[8:8+PageRefSize], header.Next)
	for i, ref := range refs {
		start := Float64CatalogPayloadHeaderSize + i*PageRefSize
		encodePageRef(payload[start:start+PageRefSize], ref)
	}
	page := dst[:int(header.PageSize)]
	if _, err := sealInitializedPage(page); err != nil {
		return nil, err
	}
	return page, nil
}

func validateFloat64CatalogNext(
	header Float64CatalogHeader,
	fileEnd, nextLogicalID uint64,
	allocationQuantum uint32,
	mutable bool,
) error {
	if header.Next == (PageRef{}) {
		return nil
	}
	ref := header.Next
	if ref.Kind != PageFloat64Catalog || ref.Flags != 0 || ref.Aux != 0 ||
		(!mutable && ref.Generation != header.Generation) ||
		mutable && (ref.Generation == 0 || ref.Generation > header.Generation) ||
		ref.LogicalID <= header.LogicalID ||
		ref.LogicalID >= nextLogicalID || ref.Offset%uint64(allocationQuantum) != 0 ||
		!validPhysicalPageSize(ref.Length) || ref.Length < allocationQuantum ||
		ref.Length%allocationQuantum != 0 || uint64(ref.Length) > fileEnd ||
		ref.Offset > fileEnd-uint64(ref.Length) {
		return fmt.Errorf("%w: float64 catalog next", ErrInvalidWrite)
	}
	return nil
}

func validateFloat64CatalogRefs(
	refs []PageRef,
	header Float64CatalogHeader,
	fileEnd, nextLogicalID uint64,
	allocationQuantum uint32,
	mutable bool,
) error {
	var previous PageRef
	for _, ref := range refs {
		if !validFloat64CatalogRef(
			ref, previous, header.Generation, fileEnd, nextLogicalID,
			allocationQuantum, mutable,
		) {
			return fmt.Errorf("%w: float64 catalog entry", ErrInvalidWrite)
		}
		previous = ref
	}
	return nil
}

func validFloat64CatalogRef(
	ref, previous PageRef,
	generation, fileEnd, nextLogicalID uint64,
	allocationQuantum uint32,
	mutable bool,
) bool {
	length := uint64(ref.Length)
	return ref.Kind == PageFloat64Stripe &&
		ref.Flags == 0 && ref.Aux == 0 &&
		(!mutable && ref.Generation == generation ||
			mutable &&
				ref.Generation != 0 && ref.Generation <= generation) &&
		ref.LogicalID > StateRootLogicalID && ref.LogicalID < nextLogicalID &&
		validPhysicalPageSize(ref.Length) && ref.Length >= allocationQuantum &&
		ref.Length%allocationQuantum == 0 &&
		ref.Offset%uint64(allocationQuantum) == 0 &&
		length <= fileEnd && ref.Offset <= fileEnd-length &&
		(previous == (PageRef{}) ||
			ref.LogicalID > previous.LogicalID &&
				(mutable || ref.Offset > previous.Offset))
}

// OpenFloat64Catalog verifies a complete catalog page.
func OpenFloat64Catalog(
	src []byte,
	fileEnd, nextLogicalID uint64,
	allocationQuantum uint32,
) (Float64CatalogView, error) {
	pageHeader, payload, err := OpenPage(src)
	if err != nil {
		return Float64CatalogView{}, fmt.Errorf("%w: %w", ErrFloat64CatalogCorrupt, err)
	}
	return openFloat64CatalogPayload(pageHeader, payload, fileEnd, nextLogicalID, allocationQuantum)
}

// OpenAdmittedFloat64Catalog validates a payload after common CRC admission.
func OpenAdmittedFloat64Catalog(
	src []byte,
	fileEnd, nextLogicalID uint64,
	allocationQuantum uint32,
) (Float64CatalogView, error) {
	pageHeader, ok := decodePageHeader(src)
	if !ok || len(src) != int(pageHeader.PageSize) {
		return Float64CatalogView{}, ErrFloat64CatalogCorrupt
	}
	end := PageHeaderSize + int(pageHeader.PayloadLength)
	return openFloat64CatalogPayload(
		pageHeader, src[PageHeaderSize:end:end], fileEnd, nextLogicalID, allocationQuantum,
	)
}

func openFloat64CatalogPayload(
	pageHeader PageHeader,
	payload []byte,
	fileEnd, nextLogicalID uint64,
	allocationQuantum uint32,
) (Float64CatalogView, error) {
	if pageHeader.Kind != PageFloat64Catalog || pageHeader.Flags != 0 ||
		len(payload) < Float64CatalogPayloadHeaderSize ||
		(binary.LittleEndian.Uint32(payload[0:4]) != float64CatalogVersionV1 &&
			binary.LittleEndian.Uint32(payload[0:4]) != float64CatalogVersion) ||
		!allZero(payload[6:8]) || !pageRefReservedZero(payload[8:8+PageRefSize]) ||
		!allZero(payload[40:Float64CatalogPayloadHeaderSize]) {
		return Float64CatalogView{}, fmt.Errorf("%w: header or reserved bytes", ErrFloat64CatalogCorrupt)
	}
	count := int(binary.LittleEndian.Uint16(payload[4:6]))
	if count == 0 || len(payload) != Float64CatalogPayloadHeaderSize+count*PageRefSize {
		return Float64CatalogView{}, fmt.Errorf("%w: count or payload length", ErrFloat64CatalogCorrupt)
	}
	header := Float64CatalogHeader{
		StoreID: pageHeader.StoreID, Generation: pageHeader.Generation,
		LogicalID: pageHeader.LogicalID, PageSize: pageHeader.PageSize,
		Next: decodePageRef(payload[8 : 8+PageRefSize]),
	}
	version := binary.LittleEndian.Uint32(payload[0:4])
	mutable := version == float64CatalogVersion
	if err := validateFloat64CatalogNext(
		header, fileEnd, nextLogicalID, allocationQuantum, mutable,
	); err != nil {
		return Float64CatalogView{}, fmt.Errorf("%w: %v", ErrFloat64CatalogCorrupt, err)
	}
	var previous PageRef
	for i := 0; i < count; i++ {
		start := Float64CatalogPayloadHeaderSize + i*PageRefSize
		if !pageRefReservedZero(payload[start : start+PageRefSize]) {
			return Float64CatalogView{}, fmt.Errorf("%w: entry reserved bytes", ErrFloat64CatalogCorrupt)
		}
		ref := decodePageRef(payload[start : start+PageRefSize])
		if !validFloat64CatalogRef(
			ref, previous, header.Generation, fileEnd, nextLogicalID,
			allocationQuantum, mutable,
		) {
			return Float64CatalogView{}, fmt.Errorf("%w: entry bounds or order", ErrFloat64CatalogCorrupt)
		}
		previous = ref
	}
	return Float64CatalogView{
		header: header, payload: payload, count: count, version: version,
	}, nil
}

// AdmittedFloat64Catalog reconstructs a previously validated catalog.
func AdmittedFloat64Catalog(src []byte) Float64CatalogView {
	pageHeader, _ := decodePageHeader(src)
	end := PageHeaderSize + int(pageHeader.PayloadLength)
	payload := src[PageHeaderSize:end:end]
	return Float64CatalogView{
		header: Float64CatalogHeader{
			StoreID: pageHeader.StoreID, Generation: pageHeader.Generation,
			LogicalID: pageHeader.LogicalID, PageSize: pageHeader.PageSize,
			Next: decodePageRef(payload[8 : 8+PageRefSize]),
		},
		payload: payload, count: int(binary.LittleEndian.Uint16(payload[4:6])),
		version: binary.LittleEndian.Uint32(payload[0:4]),
	}
}

func (v Float64CatalogView) Header() Float64CatalogHeader { return v.header }
func (v Float64CatalogView) Len() int                     { return v.count }
func (v Float64CatalogView) Mutable() bool {
	return v.version == float64CatalogVersion
}

func (v Float64CatalogView) RefAt(index int) (PageRef, bool) {
	start := Float64CatalogPayloadHeaderSize + index*PageRefSize
	if index < 0 || index >= v.count || start+PageRefSize > len(v.payload) {
		return PageRef{}, false
	}
	return decodePageRef(v.payload[start : start+PageRefSize]), true
}
