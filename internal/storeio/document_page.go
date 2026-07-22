package storeio

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math/bits"
)

const (
	DocumentPagePayloadHeaderSize = 32
	DocumentPageRecordSize        = 8
	documentPageVersion           = uint32(1)
	documentPageKnownFlags        = uint8(0)
)

// ErrDocumentPageCorrupt reports a checksum-valid common page whose document
// payload violates the stable-slot or packed-data format.
var ErrDocumentPageCorrupt = errors.New("simdjson: corrupt Store document page")

// DocumentPageHeader describes one immutable logical chunk. Live is the exact
// stable-slot occupancy word; no tombstone or empty-row descriptor is stored.
type DocumentPageHeader struct {
	StoreID    [16]byte
	Generation uint64
	LogicalID  uint64
	PageSize   uint32
	ChunkID    uint32
	Live       uint64
	Flags      uint8
}

// DocumentRecord is a transient row view. EncodeDocumentPage borrows Key and
// JSON only for the call. Views returned by DocumentPageView alias the admitted
// page and are invalid when its backing frame is reused.
type DocumentRecord struct {
	Key  []byte
	JSON []byte
	Slot uint8
}

// DocumentPageView is an admitted, checksum-verified micro-page. One borrowed
// payload slice represents all rows; it does not create a pointer per key or
// document.
type DocumentPageView struct {
	header    DocumentPageHeader
	payload   []byte
	dataStart int
	count     uint8
}

// EncodeDocumentPage writes one complete stable-slot micro-page into caller
// storage. rows must be ordered by increasing Slot and exactly match
// header.Live. Keys may be empty; JSON must be non-empty and already validated
// by the Store mutation path. Input byte slices must not overlap dst because
// deterministic page initialization clears dst before copying. No allocation
// is performed when rows and dst are caller-owned.
func EncodeDocumentPage(dst []byte, header DocumentPageHeader, rows []DocumentRecord, nextLogicalID uint64) ([]byte, error) {
	dataLength, err := validateDocumentPageWrite(header, rows, nextLogicalID)
	if err != nil {
		return nil, err
	}
	count := len(rows)
	payloadLength := DocumentPagePayloadHeaderSize + count*DocumentPageRecordSize + dataLength
	payload, err := InitPage(dst, PageHeader{
		StoreID:       header.StoreID,
		Generation:    header.Generation,
		LogicalID:     header.LogicalID,
		PageSize:      header.PageSize,
		PayloadLength: uint32(payloadLength),
		Kind:          PageDocument,
	})
	if err != nil {
		return nil, err
	}
	binary.LittleEndian.PutUint32(payload[0:4], documentPageVersion)
	binary.LittleEndian.PutUint32(payload[4:8], header.ChunkID)
	binary.LittleEndian.PutUint64(payload[8:16], header.Live)
	binary.LittleEndian.PutUint32(payload[16:20], uint32(dataLength))
	payload[20] = uint8(count)
	payload[21] = header.Flags
	binary.LittleEndian.PutUint16(payload[22:24], DocumentPageRecordSize)

	dataStart := DocumentPagePayloadHeaderSize + count*DocumentPageRecordSize
	dataEnd := 0
	for rank, row := range rows {
		copy(payload[dataStart+dataEnd:], row.Key)
		dataEnd += len(row.Key)
		descriptor := DocumentPagePayloadHeaderSize + rank*DocumentPageRecordSize
		binary.LittleEndian.PutUint32(payload[descriptor:descriptor+4], uint32(dataEnd))
		copy(payload[dataStart+dataEnd:], row.JSON)
		dataEnd += len(row.JSON)
		binary.LittleEndian.PutUint32(payload[descriptor+4:descriptor+8], uint32(dataEnd))
	}
	page := dst[:int(header.PageSize)]
	if _, err := sealInitializedPage(page); err != nil {
		return nil, err
	}
	return page, nil
}

// OpenDocumentPage verifies a common page and its complete packed row
// directory once. chunkHighWater and nextLogicalID come from the selecting
// state root and reject pages outside that graph. The returned view borrows src
// and repeated lookups allocate nothing.
func OpenDocumentPage(src []byte, chunkHighWater uint32, nextLogicalID uint64) (DocumentPageView, error) {
	pageHeader, payload, err := OpenPage(src)
	if err != nil {
		return DocumentPageView{}, fmt.Errorf("%w: %w", ErrDocumentPageCorrupt, err)
	}
	if pageHeader.Kind != PageDocument || len(payload) < DocumentPagePayloadHeaderSize ||
		binary.LittleEndian.Uint32(payload[0:4]) != documentPageVersion ||
		binary.LittleEndian.Uint16(payload[22:24]) != DocumentPageRecordSize ||
		!allZero(payload[24:DocumentPagePayloadHeaderSize]) {
		return DocumentPageView{}, fmt.Errorf("%w: header, version, or reserved bytes", ErrDocumentPageCorrupt)
	}
	header := DocumentPageHeader{
		StoreID:    pageHeader.StoreID,
		Generation: pageHeader.Generation,
		LogicalID:  pageHeader.LogicalID,
		PageSize:   pageHeader.PageSize,
		ChunkID:    binary.LittleEndian.Uint32(payload[4:8]),
		Live:       binary.LittleEndian.Uint64(payload[8:16]),
		Flags:      payload[21],
	}
	count := int(payload[20])
	dataLength := uint64(binary.LittleEndian.Uint32(payload[16:20]))
	dataStart := uint64(DocumentPagePayloadHeaderSize + count*DocumentPageRecordSize)
	if err := validateDocumentPageHeader(header, count, chunkHighWater, nextLogicalID); err != nil {
		return DocumentPageView{}, fmt.Errorf("%w: %v", ErrDocumentPageCorrupt, err)
	}
	if dataStart+dataLength != uint64(len(payload)) {
		return DocumentPageView{}, fmt.Errorf("%w: payload length", ErrDocumentPageCorrupt)
	}
	var previousEnd uint32
	for rank := 0; rank < count; rank++ {
		descriptor := DocumentPagePayloadHeaderSize + rank*DocumentPageRecordSize
		keyEnd := binary.LittleEndian.Uint32(payload[descriptor : descriptor+4])
		jsonEnd := binary.LittleEndian.Uint32(payload[descriptor+4 : descriptor+8])
		if keyEnd < previousEnd || jsonEnd <= keyEnd || uint64(jsonEnd) > dataLength {
			return DocumentPageView{}, fmt.Errorf("%w: non-canonical record bounds", ErrDocumentPageCorrupt)
		}
		previousEnd = jsonEnd
	}
	if uint64(previousEnd) != dataLength {
		return DocumentPageView{}, fmt.Errorf("%w: unreferenced packed data", ErrDocumentPageCorrupt)
	}
	return DocumentPageView{
		header:    header,
		payload:   payload,
		dataStart: int(dataStart),
		count:     uint8(count),
	}, nil
}

// Header returns the value-only identity and stable-slot metadata of the view.
func (v DocumentPageView) Header() DocumentPageHeader { return v.header }

// Len returns the number of live rows.
func (v DocumentPageView) Len() int { return int(v.count) }

// Lookup returns the row at stable slot. It uses one bitmap probe, one popcount
// rank, and at most three cumulative-end loads; no scan or allocation occurs.
func (v DocumentPageView) Lookup(slot uint8) (DocumentRecord, bool) {
	if slot >= 64 {
		return DocumentRecord{}, false
	}
	bit := uint64(1) << slot
	if v.header.Live&bit == 0 {
		return DocumentRecord{}, false
	}
	rank := bits.OnesCount64(v.header.Live & (bit - 1))
	return v.recordAt(rank, slot)
}

// LookupJSON returns only the JSON bytes for a stable slot. Point reads use
// this narrower form after a trusted compiled-key hit so they do not construct
// or copy key metadata. The returned slice is capacity-clipped to the document.
func (v DocumentPageView) LookupJSON(slot uint8) ([]byte, bool) {
	if slot >= 64 {
		return nil, false
	}
	bit := uint64(1) << slot
	if v.header.Live&bit == 0 {
		return nil, false
	}
	rank := bits.OnesCount64(v.header.Live & (bit - 1))
	descriptor := DocumentPagePayloadHeaderSize + rank*DocumentPageRecordSize
	keyEnd := binary.LittleEndian.Uint32(v.payload[descriptor : descriptor+4])
	jsonEnd := binary.LittleEndian.Uint32(v.payload[descriptor+4 : descriptor+8])
	start := v.dataStart + int(keyEnd)
	end := v.dataStart + int(jsonEnd)
	if start >= end || end > len(v.payload) {
		return nil, false
	}
	return v.payload[start:end:end], true
}

// LookupKey verifies the complete key at a candidate stable slot and returns
// its JSON. Hash or fingerprint collisions therefore cannot return another
// document. The admitted-page fast path allocates nothing.
func (v DocumentPageView) LookupKey(slot uint8, key []byte) ([]byte, bool) {
	if slot >= 64 {
		return nil, false
	}
	bit := uint64(1) << slot
	if v.header.Live&bit == 0 {
		return nil, false
	}
	rank := bits.OnesCount64(v.header.Live & (bit - 1))
	descriptor := DocumentPagePayloadHeaderSize + rank*DocumentPageRecordSize
	rowStart := uint32(0)
	if rank != 0 {
		rowStart = binary.LittleEndian.Uint32(v.payload[descriptor-4 : descriptor])
	}
	keyEnd := binary.LittleEndian.Uint32(v.payload[descriptor : descriptor+4])
	jsonEnd := binary.LittleEndian.Uint32(v.payload[descriptor+4 : descriptor+8])
	keyStart := v.dataStart + int(rowStart)
	jsonStart := v.dataStart + int(keyEnd)
	end := v.dataStart + int(jsonEnd)
	if keyStart > jsonStart || jsonStart >= end || end > len(v.payload) ||
		!bytes.Equal(v.payload[keyStart:jsonStart], key) {
		return nil, false
	}
	return v.payload[jsonStart:end:end], true
}

// LookupString verifies the complete string key at a candidate stable slot and
// returns its JSON. The byte-slice/string comparison is allocation-free; this
// is the direct bridge from the Store's ordinary keyed API to a resident page.
func (v DocumentPageView) LookupString(slot uint8, key string) ([]byte, bool) {
	if slot >= 64 {
		return nil, false
	}
	bit := uint64(1) << slot
	if v.header.Live&bit == 0 {
		return nil, false
	}
	rank := bits.OnesCount64(v.header.Live & (bit - 1))
	descriptor := DocumentPagePayloadHeaderSize + rank*DocumentPageRecordSize
	rowStart := uint32(0)
	if rank != 0 {
		rowStart = binary.LittleEndian.Uint32(v.payload[descriptor-4 : descriptor])
	}
	keyEnd := binary.LittleEndian.Uint32(v.payload[descriptor : descriptor+4])
	jsonEnd := binary.LittleEndian.Uint32(v.payload[descriptor+4 : descriptor+8])
	keyStart := v.dataStart + int(rowStart)
	jsonStart := v.dataStart + int(keyEnd)
	end := v.dataStart + int(jsonEnd)
	if keyStart > jsonStart || jsonStart >= end || end > len(v.payload) ||
		string(v.payload[keyStart:jsonStart]) != key {
		return nil, false
	}
	return v.payload[jsonStart:end:end], true
}

// RecordAt returns the packed-rank row and its stable slot.
func (v DocumentPageView) RecordAt(rank int) (DocumentRecord, bool) {
	if rank < 0 || rank >= int(v.count) {
		return DocumentRecord{}, false
	}
	live := v.header.Live
	for range rank {
		live &= live - 1
	}
	return v.recordAt(rank, uint8(bits.TrailingZeros64(live)))
}

func (v DocumentPageView) recordAt(rank int, slot uint8) (DocumentRecord, bool) {
	descriptor := DocumentPagePayloadHeaderSize + rank*DocumentPageRecordSize
	if rank < 0 || rank >= int(v.count) || descriptor+DocumentPageRecordSize > v.dataStart {
		return DocumentRecord{}, false
	}
	start := uint32(0)
	if rank != 0 {
		start = binary.LittleEndian.Uint32(v.payload[descriptor-4 : descriptor])
	}
	keyEnd := binary.LittleEndian.Uint32(v.payload[descriptor : descriptor+4])
	jsonEnd := binary.LittleEndian.Uint32(v.payload[descriptor+4 : descriptor+8])
	keyStart := v.dataStart + int(start)
	jsonStart := v.dataStart + int(keyEnd)
	end := v.dataStart + int(jsonEnd)
	if keyStart > jsonStart || jsonStart >= end || end > len(v.payload) {
		return DocumentRecord{}, false
	}
	return DocumentRecord{
		Key:  v.payload[keyStart:jsonStart:jsonStart],
		JSON: v.payload[jsonStart:end:end],
		Slot: slot,
	}, true
}

func validateDocumentPageWrite(header DocumentPageHeader, rows []DocumentRecord, nextLogicalID uint64) (int, error) {
	if err := validateDocumentPageHeader(header, len(rows), header.ChunkID+1, nextLogicalID); err != nil {
		return 0, err
	}
	live := header.Live
	dataLength := uint64(0)
	for _, row := range rows {
		slot := uint8(bits.TrailingZeros64(live))
		if row.Slot != slot || len(row.JSON) == 0 {
			return 0, fmt.Errorf("%w: rows do not match stable slots", ErrInvalidWrite)
		}
		dataLength += uint64(len(row.Key)) + uint64(len(row.JSON))
		if dataLength > uint64(^uint32(0)) {
			return 0, fmt.Errorf("%w: document data length", ErrInvalidWrite)
		}
		live &= live - 1
	}
	payloadLength := uint64(DocumentPagePayloadHeaderSize+len(rows)*DocumentPageRecordSize) + dataLength
	if payloadLength > uint64(header.PageSize)-PageHeaderSize-PageTrailerSize {
		return 0, fmt.Errorf("%w: document payload does not fit", ErrInvalidWrite)
	}
	return int(dataLength), nil
}

func validateDocumentPageHeader(header DocumentPageHeader, count int, chunkHighWater uint32, nextLogicalID uint64) error {
	if header.StoreID == ([16]byte{}) || header.Generation == 0 ||
		header.LogicalID <= StateRootLogicalID || header.LogicalID >= nextLogicalID ||
		!validPhysicalPageSize(header.PageSize) || header.Flags&^documentPageKnownFlags != 0 {
		return fmt.Errorf("%w: document identity, page size, or flags", ErrInvalidWrite)
	}
	if header.Live == 0 || count != bits.OnesCount64(header.Live) || count > 64 ||
		chunkHighWater == 0 || header.ChunkID >= chunkHighWater {
		return fmt.Errorf("%w: document chunk or live rows", ErrInvalidWrite)
	}
	return nil
}
