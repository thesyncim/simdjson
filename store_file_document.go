package simdjson

import (
	"bytes"
	"fmt"

	"github.com/thesyncim/simdjson/internal/storeio"
)

// fileDocumentChunk is the read-side union of an ordinary mutable chunk page
// and one logical chunk selected from an immutable compact-generation group.
// Keeping the branch here prevents grouped-storage details from leaking into
// the query, index, TTL, or public Store APIs.
type fileDocumentChunk struct {
	page    storeio.DocumentPageView
	group   storeio.DocumentGroupChunkView
	chunk   uint32
	grouped bool
}

type fileDocumentValue struct {
	value   storeio.DocumentValue
	slot    uint8
	grouped bool
}

type fileDocumentRecord struct {
	key   []byte
	value fileDocumentValue
	slot  uint8
}

func admittedFileDocumentChunk(page []byte, ref storeio.PageRef, chunk uint32) (fileDocumentChunk, error) {
	switch ref.Kind {
	case storeio.PageDocument:
		view := storeio.AdmittedDocumentPage(page)
		if view.Header().ChunkID != chunk {
			return fileDocumentChunk{}, storeio.ErrDocumentPageCorrupt
		}
		return fileDocumentChunk{page: view, chunk: chunk}, nil
	case storeio.PageDocumentGroup:
		group, ok := storeio.AdmittedDocumentGroup(page).Chunk(chunk)
		if !ok {
			return fileDocumentChunk{}, storeio.ErrDocumentGroupCorrupt
		}
		return fileDocumentChunk{group: group, chunk: chunk, grouped: true}, nil
	default:
		return fileDocumentChunk{}, fmt.Errorf("%w: document reference kind", storeio.ErrChunkDirectoryCorrupt)
	}
}

func (v fileDocumentChunk) live() uint64 {
	if v.grouped {
		return v.group.Live()
	}
	return v.page.Header().Live
}

func (v fileDocumentChunk) groupHeader() (storeio.DocumentGroupHeader, bool) {
	if !v.grouped {
		return storeio.DocumentGroupHeader{}, false
	}
	return v.group.GroupHeader(), true
}

func (v fileDocumentChunk) lookup(slot uint8) (fileDocumentRecord, bool) {
	if v.grouped {
		record, ok := v.group.Lookup(slot)
		if !ok {
			return fileDocumentRecord{}, false
		}
		return fileDocumentRecord{
			key: record.Key, slot: slot,
			value: fileDocumentValue{
				value: storeio.DocumentValue{Length: uint64(record.JSONLength)},
				slot:  slot, grouped: true,
			},
		}, true
	}
	record, ok := v.page.Lookup(slot)
	if !ok {
		return fileDocumentRecord{}, false
	}
	value := storeio.DocumentValue{Inline: record.JSON, Length: uint64(len(record.JSON))}
	if record.Overflow != (storeio.PageRef{}) {
		value = storeio.DocumentValue{Overflow: record.Overflow, Length: record.JSONLength}
	}
	return fileDocumentRecord{
		key: record.Key, slot: slot,
		value: fileDocumentValue{value: value, slot: slot},
	}, true
}

func (v fileDocumentChunk) lookupString(slot uint8, key string) (fileDocumentValue, bool) {
	if v.grouped {
		record, ok := v.group.LookupString(slot, key)
		if !ok {
			return fileDocumentValue{}, false
		}
		return fileDocumentValue{
			value: storeio.DocumentValue{Length: uint64(record.JSONLength)},
			slot:  slot, grouped: true,
		}, true
	}
	value, ok := v.page.LookupStringValue(slot, key)
	return fileDocumentValue{value: value, slot: slot}, ok
}

func (v fileDocumentChunk) lookupKey(slot uint8, key []byte) (fileDocumentValue, bool) {
	record, ok := v.lookup(slot)
	return record.value, ok && bytes.Equal(record.key, key)
}

func (v fileDocumentChunk) appendJSON(dst []byte, value fileDocumentValue) ([]byte, bool) {
	if !value.grouped {
		if value.value.Overflow != (storeio.PageRef{}) {
			return dst, false
		}
		return append(dst, value.value.Inline...), true
	}
	return v.group.AppendJSON(dst, value.slot)
}

func (v fileDocumentChunk) float64ColumnCount() int {
	if v.grouped {
		return v.group.Float64ColumnCount()
	}
	return v.page.Float64ColumnCount()
}

func (v fileDocumentChunk) float64Column(column int) (storeio.DocumentFloat64ColumnView, bool) {
	if v.grouped {
		return v.group.Float64Column(column)
	}
	return v.page.Float64Column(column)
}

func (s *FileStore) appendFileDocumentValue(
	dst []byte,
	state *fileStoreState,
	view fileDocumentChunk,
	value fileDocumentValue,
	location storeio.KeyLocation,
) ([]byte, error) {
	if value.grouped || value.value.Overflow == (storeio.PageRef{}) {
		out, ok := view.appendJSON(dst, value)
		if !ok {
			return dst, storeio.ErrDocumentGroupCorrupt
		}
		return out, nil
	}
	return s.appendFileValue(dst, state, value.value, location)
}
