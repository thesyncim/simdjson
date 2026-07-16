//go:build !race && !simdjson_safehooks

package simdjson

import "unsafe"

// This file holds the slab-backed interface construction used by ordinary
// builds. Building with -race or the simdjson_safehooks tag replaces it with
// the plain conversions in any_box_safe.go. The safety contract lives at the
// top of any_box.go; this file supplies the two-word construction and the
// self-test that must pass before it is ever used.

// anyEface is the memory layout of an empty interface value: the runtime
// type word, then the data word. verifyAnyBoxLayout proves the assumption on
// the running toolchain before any value is built through this view.
type anyEface struct {
	typ  unsafe.Pointer
	data unsafe.Pointer
}

// anyEfaceTypeWord returns v's runtime type word. The probe values below are
// ordinary conversions, so the words come from the runtime itself.
func anyEfaceTypeWord(v any) unsafe.Pointer {
	return (*anyEface)(unsafe.Pointer(&v)).typ
}

var (
	anyFloat64Type = anyEfaceTypeWord(float64(0))
	anyStringType  = anyEfaceTypeWord("")
	anyValuesType  = anyEfaceTypeWord([]any(nil))
)

// packAnyEface builds an interface value from a runtime type word and a
// pointer to a heap slot holding the value. Callers guarantee the slot obeys
// the contract in any_box.go: heap-resident, written before this call, and
// never written again.
func packAnyEface(typ, data unsafe.Pointer) any {
	e := anyEface{typ: typ, data: data}
	return *(*any)(unsafe.Pointer(&e))
}

// anyBoxLayoutOK gates the slab boxers: when the self-test fails, every boxer
// takes the ordinary conversion, degrading to correct-and-slower rather than
// risking a malformed interface value.
var anyBoxLayoutOK = verifyAnyBoxLayout()

// verifyAnyBoxLayout proves on this toolchain that a hand-built interface
// value is indistinguishable from an ordinary conversion: same dynamic type,
// same value through a type assertion, and equal under interface comparison
// (which compares pointed-to values, not data words). One probe per boxed
// kind; any deviation disables the slab boxers.
func verifyAnyBoxLayout() bool {
	const probeFloat = 0x1.5bf0a8b145769p+1 // e, an unmistakable bit pattern
	floats := newAnyFloatSlab(1)
	floats = append(floats, probeFloat)
	fv := packAnyEface(anyFloat64Type, unsafe.Pointer(&floats[0]))
	if f, ok := fv.(float64); !ok || f != probeFloat || fv != any(probeFloat) {
		return false
	}

	const probeString = "simdjson any-box layout probe"
	strs := newAnyStringSlab(1)
	strs = append(strs, probeString)
	sv := packAnyEface(anyStringType, unsafe.Pointer(&strs[0]))
	if s, ok := sv.(string); !ok || s != probeString || sv != any(probeString) {
		return false
	}

	values := newAnyValuesSlab(1)
	values = append(values, []any{probeString})
	vv := packAnyEface(anyValuesType, unsafe.Pointer(&values[0]))
	slice, ok := vv.([]any)
	if !ok || len(slice) != 1 || slice[0] != any(probeString) {
		return false
	}
	return true
}

// float boxes f. The slot address escapes into the returned interface, so
// the chunk stays alive as long as any value boxed from it.
func (b *anyBoxer) float(f float64) any {
	if !anyBoxLayoutOK {
		return f
	}
	s := b.floats
	if len(s) == cap(s) {
		s = newAnyFloatSlab(nextAnySlabSize(cap(s), anyFloatSlabChunk))
	}
	s = append(s, f)
	b.floats = s
	return packAnyEface(anyFloat64Type, unsafe.Pointer(&s[len(s)-1]))
}

// str boxes v.
func (b *anyBoxer) str(v string) any {
	if !anyBoxLayoutOK {
		return v
	}
	s := b.strings
	if len(s) == cap(s) {
		s = newAnyStringSlab(nextAnySlabSize(cap(s), anyStringSlabChunk))
	}
	s = append(s, v)
	b.strings = s
	return packAnyEface(anyStringType, unsafe.Pointer(&s[len(s)-1]))
}

// slice boxes v.
func (b *anyBoxer) slice(v []any) any {
	if !anyBoxLayoutOK {
		return v
	}
	s := b.values
	if len(s) == cap(s) {
		s = newAnyValuesSlab(nextAnySlabSize(cap(s), anyValuesSlabChunk))
	}
	s = append(s, v)
	b.values = s
	return packAnyEface(anyValuesType, unsafe.Pointer(&s[len(s)-1]))
}
