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
	// valueBacking is the direct fast-path slot for plans with exactly one map
	// element type. Plans with multiple types use valueBackings instead. Every
	// slot is compile-time-assigned and fixed-type. A taken slot is invalid
	// until its owner releases it; recursive use of the same map node therefore
	// falls back to fresh storage without sharing a value box across levels.
	valueBacking  reflect.Value
	valueBackings []reflect.Value
	// dynamicValueBacking is deliberately separate from the statically typed
	// slots. Interface values are compiled independently of the Encoder that
	// reaches them, so their nodes cannot safely carry plan-local slot indexes.
	// This one bounded polymorphic slot preserves reuse without coupling a
	// dynamic plan to another plan's layout or retaining an unbounded type map.
	dynamicValueBacking     reflect.Value
	dynamicValueBackingElem reflect.Type
}

func (s *encoderScratch) reset() {
	for i := range s.marshalers {
		s.marshalers[i].value.SetZero()
	}
	// Entries hold key strings and reflect values from the encoded maps;
	// drop those references before the scratch returns to the pool.
	clear(s.mapEntries[:cap(s.mapEntries)])
}

func newEncoderScratchPool(types []reflect.Type, backingSlots int, hasMap bool) *sync.Pool {
	if len(types) == 0 && backingSlots == 0 && !hasMap {
		return nil
	}
	types = append([]reflect.Type(nil), types...)
	return &sync.Pool{New: func() any {
		scratch := &encoderScratch{marshalers: make([]encoderMarshalerScratch, len(types))}
		if backingSlots > 1 {
			scratch.valueBackings = make([]reflect.Value, backingSlots)
		}
		for i, typ := range types {
			value := reflect.New(typ)
			scratch.marshalers[i] = encoderMarshalerScratch{value: value.Elem(), boxed: value.Interface()}
		}
		return scratch
	}}
}
