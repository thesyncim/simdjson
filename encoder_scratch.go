package simdjson

import (
	"reflect"
	"sync"
	"unsafe"
)

type encoderMarshalerScratch struct {
	value reflect.Value
	boxed any
}

type encoderScratch struct {
	marshalers []encoderMarshalerScratch
	// mapEntries, mapKeyArena, and mapIter are reused by encodeMap so
	// sorted map encoding does not allocate per map. Ownership transfers
	// to one encodeMap call at a time; nested maps fall back to fresh
	// allocations.
	mapEntries  []mapEncodeEntry
	mapKeyArena []byte
	mapIter     *reflect.MapIter
	// mapHeader holds the map reference word that the reused mapIter binds to.
	// Living in the pooled (heap) scratch, it gives the iterator a heap-to-heap
	// reference to the current map, so a stack move during iteration cannot
	// dangle the iterator's copy. It travels with mapIter: whichever encodeMap
	// call owns the iterator owns this word, so a nested map — which allocates
	// its own stack-local iterator — never contends for it. See heapBoundMapValue.
	mapHeader unsafe.Pointer
	// valueBacking is a reused addressable slice of a map's (or ",inline"
	// catch-all's) value type. Encoding copies each value into its own slot
	// with SetIterValue so the entries can be reordered without reflect
	// allocating one value box per member, and so a value that recurses into
	// the same type has independent storage. valueBackingElem records the
	// element type currently backed; ownership borrows and restores like
	// mapEntries so nested maps fall back to fresh allocations.
	valueBacking     reflect.Value
	valueBackingElem reflect.Type
}

func (s *encoderScratch) reset() {
	for i := range s.marshalers {
		s.marshalers[i].value.SetZero()
	}
	// Entries hold key strings and reflect values from the encoded maps;
	// drop those references before the scratch returns to the pool.
	clear(s.mapEntries[:cap(s.mapEntries)])
	// mapHeader still points at the last map encoded; clear it so the pool
	// never pins that map alive.
	s.mapHeader = nil
}

func newEncoderScratchPool(types []reflect.Type, hasMap bool) *sync.Pool {
	if len(types) == 0 && !hasMap {
		return nil
	}
	types = append([]reflect.Type(nil), types...)
	return &sync.Pool{New: func() any {
		scratch := &encoderScratch{marshalers: make([]encoderMarshalerScratch, len(types))}
		for i, typ := range types {
			value := reflect.New(typ)
			scratch.marshalers[i] = encoderMarshalerScratch{value: value.Elem(), boxed: value.Interface()}
		}
		return scratch
	}}
}
