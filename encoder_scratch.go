package simdjson

import (
	"reflect"
	"sync"
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
	// inlineBacking is a reused addressable slice of a ",inline" map's value
	// type. Sorted catch-all encoding copies each value into a slot with
	// SetIterValue so the entries can be reordered without reflect allocating
	// one value box per member. inlineElem records the element type currently
	// backed; ownership borrows and restores like mapEntries so nested catch-
	// alls fall back to fresh allocations.
	inlineBacking reflect.Value
	inlineElem    reflect.Type
}

func (s *encoderScratch) reset() {
	for i := range s.marshalers {
		s.marshalers[i].value.SetZero()
	}
	// Entries hold key strings and reflect values from the encoded maps;
	// drop those references before the scratch returns to the pool.
	clear(s.mapEntries[:cap(s.mapEntries)])
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
