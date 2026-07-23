package storeio

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	indexGroupCatalogVersion = uint32(1)

	// IndexGroupCatalogPayloadHeaderSize is the fixed pointer-free catalog
	// prefix. The payload that follows is a packed sequence of independently
	// length-delimited scalar representatives.
	IndexGroupCatalogPayloadHeaderSize = 32
	// IndexGroupCatalogEntryHeaderSize precedes one representative. Entries
	// are padded to eight bytes so scanners read counts and row tokens from
	// naturally aligned locations on every supported architecture.
	IndexGroupCatalogEntryHeaderSize = 32
)

// ErrIndexGroupCatalogCorrupt reports a malformed aggregate-only exact-index
// cover. The ordinary posting tree remains authoritative, but callers must
// surface corruption instead of silently changing execution plans.
var ErrIndexGroupCatalogCorrupt = errors.New("simdjson: corrupt index group catalog")

// IndexGroupCatalogHeader identifies one self-contained clean-generation
// grouping cover. CoveredIndexes has one bit per durable exact-index id.
type IndexGroupCatalogHeader struct {
	StoreID        [16]byte
	Generation     uint64
	LogicalID      uint64
	PageSize       uint32
	CoveredIndexes uint64
	DocumentCount  uint64
}

// IndexGroupCatalogEntry is one scalar group. Value is an exact JSON scalar
// representative interpreted by the owning package. First is the stable
// chunk/slot token of the earliest row in the group.
type IndexGroupCatalogEntry struct {
	IndexID uint32
	Value   []byte
	Count   uint64
	First   uint64
}

// IndexGroupCatalogEntryEncodedSize returns the packed encoded size of entry.
func IndexGroupCatalogEntryEncodedSize(entry IndexGroupCatalogEntry) (int, error) {
	if len(entry.Value) == 0 || entry.Count == 0 ||
		len(entry.Value) > int(^uint32(0)) ||
		len(entry.Value) > int(^uint(0)>>1)-IndexGroupCatalogEntryHeaderSize-7 {
		return 0, fmt.Errorf("%w: index group entry", ErrInvalidWrite)
	}
	return alignIndexGroupCatalog(IndexGroupCatalogEntryHeaderSize + len(entry.Value)), nil
}

// EncodeIndexGroupCatalogPage encodes one bounded categorical cover. The
// caller chooses a power-of-two extent up to its configured maximum; no
// allocation is performed.
func EncodeIndexGroupCatalogPage(
	dst []byte,
	header IndexGroupCatalogHeader,
	entries []IndexGroupCatalogEntry,
	indexHighWater, chunkHighWater, chunkDocuments uint32,
) ([]byte, error) {
	payloadBytes, err := validateIndexGroupCatalogEntries(
		header, entries, indexHighWater, chunkHighWater, chunkDocuments,
	)
	if err != nil {
		return nil, err
	}
	if payloadBytes > int(header.PageSize)-PageHeaderSize-PageTrailerSize {
		return nil, fmt.Errorf("%w: index group catalog extent", ErrInvalidWrite)
	}
	payload, err := InitPage(dst, PageHeader{
		StoreID: header.StoreID, Generation: header.Generation,
		LogicalID: header.LogicalID, PageSize: header.PageSize,
		PayloadLength: uint32(payloadBytes), Kind: PageIndexGroupCatalog,
	})
	if err != nil {
		return nil, err
	}
	binary.LittleEndian.PutUint32(payload[0:4], indexGroupCatalogVersion)
	binary.LittleEndian.PutUint32(payload[4:8], uint32(len(entries)))
	binary.LittleEndian.PutUint64(payload[8:16], header.CoveredIndexes)
	binary.LittleEndian.PutUint64(payload[16:24], header.DocumentCount)
	cursor := IndexGroupCatalogPayloadHeaderSize
	for _, entry := range entries {
		size, _ := IndexGroupCatalogEntryEncodedSize(entry)
		binary.LittleEndian.PutUint32(payload[cursor:cursor+4], entry.IndexID)
		binary.LittleEndian.PutUint32(payload[cursor+4:cursor+8], uint32(len(entry.Value)))
		binary.LittleEndian.PutUint64(payload[cursor+8:cursor+16], entry.Count)
		binary.LittleEndian.PutUint64(payload[cursor+16:cursor+24], entry.First)
		copy(payload[cursor+IndexGroupCatalogEntryHeaderSize:], entry.Value)
		cursor += size
	}
	page := dst[:int(header.PageSize)]
	if _, err := sealInitializedPage(page); err != nil {
		return nil, err
	}
	return page, nil
}

func validateIndexGroupCatalogEntries(
	header IndexGroupCatalogHeader,
	entries []IndexGroupCatalogEntry,
	indexHighWater, chunkHighWater, chunkDocuments uint32,
) (int, error) {
	if header.StoreID == ([16]byte{}) || header.Generation == 0 ||
		header.LogicalID <= StateRootLogicalID || !validPhysicalPageSize(header.PageSize) ||
		header.CoveredIndexes == 0 || header.DocumentCount == 0 ||
		indexHighWater == 0 || indexHighWater > 64 ||
		chunkHighWater == 0 || chunkDocuments == 0 || chunkDocuments > 64 ||
		header.CoveredIndexes&^(uint64(1)<<indexHighWater-1) != 0 ||
		len(entries) == 0 || len(entries) > int(^uint32(0)) {
		return 0, fmt.Errorf("%w: index group catalog header", ErrInvalidWrite)
	}
	size := IndexGroupCatalogPayloadHeaderSize
	var totals [64]uint64
	var seen uint64
	previous := uint32(0)
	for position, entry := range entries {
		if entry.IndexID >= indexHighWater ||
			header.CoveredIndexes&(uint64(1)<<entry.IndexID) == 0 ||
			position != 0 && entry.IndexID < previous {
			return 0, fmt.Errorf("%w: index group entry order", ErrInvalidWrite)
		}
		entrySize, err := IndexGroupCatalogEntryEncodedSize(entry)
		if err != nil || size > int(^uint(0)>>1)-entrySize {
			return 0, fmt.Errorf("%w: index group entry size", ErrInvalidWrite)
		}
		chunk, slot := entry.First>>6, entry.First&63
		if chunk >= uint64(chunkHighWater) || slot >= uint64(chunkDocuments) ||
			totals[entry.IndexID] > ^uint64(0)-entry.Count {
			return 0, fmt.Errorf("%w: index group entry bounds", ErrInvalidWrite)
		}
		totals[entry.IndexID] += entry.Count
		seen |= uint64(1) << entry.IndexID
		previous = entry.IndexID
		size += entrySize
	}
	if seen != header.CoveredIndexes {
		return 0, fmt.Errorf("%w: index group coverage", ErrInvalidWrite)
	}
	for indexID := uint32(0); indexID < indexHighWater; indexID++ {
		if seen&(uint64(1)<<indexID) != 0 && totals[indexID] != header.DocumentCount {
			return 0, fmt.Errorf("%w: index group document count", ErrInvalidWrite)
		}
	}
	return size, nil
}

// IndexGroupCatalogView borrows one admitted page.
type IndexGroupCatalogView struct {
	header  IndexGroupCatalogHeader
	payload []byte
	count   int
}

func (v IndexGroupCatalogView) Header() IndexGroupCatalogHeader { return v.header }
func (v IndexGroupCatalogView) Len() int                        { return v.count }

// Covered reports whether indexID has a complete scalar grouping cover.
func (v IndexGroupCatalogView) Covered(indexID uint32) bool {
	return indexID < 64 && v.header.CoveredIndexes&(uint64(1)<<indexID) != 0
}

// EntryAt returns one borrowed entry.
func (v IndexGroupCatalogView) EntryAt(rank int) (IndexGroupCatalogEntry, bool) {
	if rank < 0 || rank >= v.count {
		return IndexGroupCatalogEntry{}, false
	}
	cursor := IndexGroupCatalogPayloadHeaderSize
	for position := 0; position <= rank; position++ {
		length := int(binary.LittleEndian.Uint32(v.payload[cursor+4 : cursor+8]))
		size := alignIndexGroupCatalog(IndexGroupCatalogEntryHeaderSize + length)
		if position == rank {
			start := cursor + IndexGroupCatalogEntryHeaderSize
			return IndexGroupCatalogEntry{
				IndexID: binary.LittleEndian.Uint32(v.payload[cursor : cursor+4]),
				Value:   v.payload[start : start+length : start+length],
				Count:   binary.LittleEndian.Uint64(v.payload[cursor+8 : cursor+16]),
				First:   binary.LittleEndian.Uint64(v.payload[cursor+16 : cursor+24]),
			}, true
		}
		cursor += size
	}
	return IndexGroupCatalogEntry{}, false
}

// IndexGroupCatalogIterator streams entries without retaining an offset
// directory or rescanning earlier variable-width representatives.
type IndexGroupCatalogIterator struct {
	payload   []byte
	cursor    int
	remaining int
}

func (v IndexGroupCatalogView) Iterator() IndexGroupCatalogIterator {
	return IndexGroupCatalogIterator{
		payload: v.payload, cursor: IndexGroupCatalogPayloadHeaderSize,
		remaining: v.count,
	}
}

func (it *IndexGroupCatalogIterator) Next() (IndexGroupCatalogEntry, bool) {
	if it == nil || it.remaining == 0 {
		return IndexGroupCatalogEntry{}, false
	}
	cursor := it.cursor
	length := int(binary.LittleEndian.Uint32(it.payload[cursor+4 : cursor+8]))
	size := alignIndexGroupCatalog(IndexGroupCatalogEntryHeaderSize + length)
	start := cursor + IndexGroupCatalogEntryHeaderSize
	entry := IndexGroupCatalogEntry{
		IndexID: binary.LittleEndian.Uint32(it.payload[cursor : cursor+4]),
		Value:   it.payload[start : start+length : start+length],
		Count:   binary.LittleEndian.Uint64(it.payload[cursor+8 : cursor+16]),
		First:   binary.LittleEndian.Uint64(it.payload[cursor+16 : cursor+24]),
	}
	it.cursor += size
	it.remaining--
	return entry, true
}

// OpenIndexGroupCatalog verifies a complete catalog page.
func OpenIndexGroupCatalog(
	src []byte,
	indexHighWater, chunkHighWater, chunkDocuments uint32,
) (IndexGroupCatalogView, error) {
	pageHeader, payload, err := OpenPage(src)
	if err != nil {
		return IndexGroupCatalogView{}, fmt.Errorf("%w: %w", ErrIndexGroupCatalogCorrupt, err)
	}
	return openIndexGroupCatalogPayload(
		pageHeader, payload, indexHighWater, chunkHighWater, chunkDocuments,
	)
}

// OpenAdmittedIndexGroupCatalog validates a payload after common admission.
func OpenAdmittedIndexGroupCatalog(
	src []byte,
	indexHighWater, chunkHighWater, chunkDocuments uint32,
) (IndexGroupCatalogView, error) {
	pageHeader, ok := decodePageHeader(src)
	if !ok || len(src) != int(pageHeader.PageSize) {
		return IndexGroupCatalogView{}, ErrIndexGroupCatalogCorrupt
	}
	end := PageHeaderSize + int(pageHeader.PayloadLength)
	return openIndexGroupCatalogPayload(
		pageHeader, src[PageHeaderSize:end:end],
		indexHighWater, chunkHighWater, chunkDocuments,
	)
}

func openIndexGroupCatalogPayload(
	pageHeader PageHeader,
	payload []byte,
	indexHighWater, chunkHighWater, chunkDocuments uint32,
) (IndexGroupCatalogView, error) {
	if pageHeader.Kind != PageIndexGroupCatalog || pageHeader.Flags != 0 ||
		len(payload) < IndexGroupCatalogPayloadHeaderSize ||
		binary.LittleEndian.Uint32(payload[0:4]) != indexGroupCatalogVersion ||
		!allZero(payload[24:IndexGroupCatalogPayloadHeaderSize]) ||
		indexHighWater == 0 || indexHighWater > 64 ||
		chunkHighWater == 0 || chunkDocuments == 0 || chunkDocuments > 64 {
		return IndexGroupCatalogView{}, fmt.Errorf("%w: header", ErrIndexGroupCatalogCorrupt)
	}
	count := int(binary.LittleEndian.Uint32(payload[4:8]))
	header := IndexGroupCatalogHeader{
		StoreID: pageHeader.StoreID, Generation: pageHeader.Generation,
		LogicalID: pageHeader.LogicalID, PageSize: pageHeader.PageSize,
		CoveredIndexes: binary.LittleEndian.Uint64(payload[8:16]),
		DocumentCount:  binary.LittleEndian.Uint64(payload[16:24]),
	}
	if count == 0 || count > (len(payload)-IndexGroupCatalogPayloadHeaderSize)/
		IndexGroupCatalogEntryHeaderSize {
		return IndexGroupCatalogView{}, fmt.Errorf("%w: entry count", ErrIndexGroupCatalogCorrupt)
	}
	cursor := IndexGroupCatalogPayloadHeaderSize
	var totals [64]uint64
	var seen uint64
	var previous uint32
	for position := 0; position < count; position++ {
		if len(payload)-cursor < IndexGroupCatalogEntryHeaderSize ||
			!allZero(payload[cursor+24:cursor+IndexGroupCatalogEntryHeaderSize]) {
			return IndexGroupCatalogView{}, fmt.Errorf("%w: entry header", ErrIndexGroupCatalogCorrupt)
		}
		indexID := binary.LittleEndian.Uint32(payload[cursor : cursor+4])
		length := int(binary.LittleEndian.Uint32(payload[cursor+4 : cursor+8]))
		entry := IndexGroupCatalogEntry{
			IndexID: indexID, Count: binary.LittleEndian.Uint64(payload[cursor+8 : cursor+16]),
			First: binary.LittleEndian.Uint64(payload[cursor+16 : cursor+24]),
		}
		size := alignIndexGroupCatalog(IndexGroupCatalogEntryHeaderSize + length)
		if length == 0 || size < IndexGroupCatalogEntryHeaderSize ||
			size > len(payload)-cursor ||
			!allZero(payload[cursor+IndexGroupCatalogEntryHeaderSize+length:cursor+size]) ||
			indexID >= indexHighWater || indexHighWater > 64 ||
			header.CoveredIndexes&(uint64(1)<<indexID) == 0 ||
			position != 0 && indexID < previous ||
			entry.Count == 0 {
			return IndexGroupCatalogView{}, fmt.Errorf("%w: entry bounds", ErrIndexGroupCatalogCorrupt)
		}
		chunk, slot := entry.First>>6, entry.First&63
		if chunk >= uint64(chunkHighWater) || slot >= uint64(chunkDocuments) ||
			totals[indexID] > ^uint64(0)-entry.Count {
			return IndexGroupCatalogView{}, fmt.Errorf("%w: row token", ErrIndexGroupCatalogCorrupt)
		}
		totals[indexID] += entry.Count
		seen |= uint64(1) << indexID
		previous = indexID
		cursor += size
	}
	if cursor != len(payload) || header.CoveredIndexes == 0 ||
		header.DocumentCount == 0 || seen != header.CoveredIndexes ||
		header.CoveredIndexes&^(uint64(1)<<indexHighWater-1) != 0 {
		return IndexGroupCatalogView{}, fmt.Errorf("%w: coverage", ErrIndexGroupCatalogCorrupt)
	}
	for indexID := uint32(0); indexID < indexHighWater; indexID++ {
		if seen&(uint64(1)<<indexID) != 0 && totals[indexID] != header.DocumentCount {
			return IndexGroupCatalogView{}, fmt.Errorf("%w: document count", ErrIndexGroupCatalogCorrupt)
		}
	}
	return IndexGroupCatalogView{header: header, payload: payload, count: count}, nil
}

// AdmittedIndexGroupCatalog reconstructs a catalog already checked by the
// page-cache validator.
func AdmittedIndexGroupCatalog(src []byte) IndexGroupCatalogView {
	pageHeader, _ := decodePageHeader(src)
	end := PageHeaderSize + int(pageHeader.PayloadLength)
	payload := src[PageHeaderSize:end:end]
	return IndexGroupCatalogView{
		header: IndexGroupCatalogHeader{
			StoreID: pageHeader.StoreID, Generation: pageHeader.Generation,
			LogicalID: pageHeader.LogicalID, PageSize: pageHeader.PageSize,
			CoveredIndexes: binary.LittleEndian.Uint64(payload[8:16]),
			DocumentCount:  binary.LittleEndian.Uint64(payload[16:24]),
		},
		payload: payload, count: int(binary.LittleEndian.Uint32(payload[4:8])),
	}
}

func alignIndexGroupCatalog(value int) int {
	return (value + 7) &^ 7
}
