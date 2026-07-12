package simdjson

import "unsafe"

// noescape prevents internal reflective operations from making every compiled
// source or destination escape. The returned pointer is valid only for
// synchronous operations that neither retain a reflect.Value nor expose p to
// user code. Custom marshal and unmarshal dispatch must never expose a pointer
// produced by this helper.
//
// This is the same pointer-hiding idiom used by the Go runtime. It deliberately
// requires vet's unsafeptr check to be disabled for this package.
//
//go:nosplit
func noescape(p unsafe.Pointer) unsafe.Pointer {
	x := uintptr(p)
	return unsafe.Pointer(x ^ 0) //nolint:unsafeptr
}
