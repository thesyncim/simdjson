//go:build !goexperiment.simd || (!arm64 && !amd64)

package simdjson

import "unsafe"

func parse16Digits(base unsafe.Pointer) uint64 {
	return parse16DigitsScalar(base)
}

func numberSIMDBackend() string {
	return "scalar"
}
