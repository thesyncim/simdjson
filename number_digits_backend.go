package simdjson

import (
	"unsafe"

	simdkernels "github.com/thesyncim/simdjson/simd"
)

func parse16Digits(base unsafe.Pointer) uint64 {
	return simdkernels.Parse16Digits((*[16]byte)(base))
}
