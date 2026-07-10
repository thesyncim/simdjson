package simdjson

import "unsafe"

// noescape hides p from escape analysis, using the same idiom as the Go
// runtime. It exists for exactly one purpose: handing caller-owned decode
// destinations and encode sources to custom un/marshalers through reflect
// without forcing every Decode destination in the program onto the heap.
//
// Safety requirements, all satisfied by the call sites in this package:
//
//   - The memory behind p must outlive every use of the laundered pointer.
//     Decode and AppendJSON only use it within the call, and the caller's
//     frame keeps the destination alive throughout.
//   - Custom UnmarshalJSON, UnmarshalText, MarshalJSON, and MarshalText
//     implementations must not retain their receiver after returning, which
//     mirrors encoding/json's requirement that implementations not retain
//     the data slice.
//
// go vet's unsafeptr check flags this function by design; the repository's
// vet configuration disables that single check (go vet -unsafeptr=false).
//
//go:nosplit
func noescape(p unsafe.Pointer) unsafe.Pointer {
	x := uintptr(p)
	return unsafe.Pointer(x ^ 0) //nolint:unsafeptr
}
