package simdjson

import "unsafe"

// noescape prevents reflective hot paths from making every compiled source or
// destination escape. The returned pointer is valid only for synchronous use
// during the current call and must never be retained.
//
// This is the same pointer-hiding idiom used by the Go runtime. It deliberately
// requires vet's unsafeptr check to be disabled for this package.
//
//go:nosplit
func noescape(p unsafe.Pointer) unsafe.Pointer {
	x := uintptr(p)
	return unsafe.Pointer(x ^ 0) //nolint:unsafeptr
}
