//go:build goexperiment.simd && amd64

package simdjson

import (
	"math/bits"
	"simd/archsimd"
)

func firstMaskLane(m archsimd.Mask8x16) int {
	b := m.ToBits()
	if b == 0 {
		return -1
	}
	return bits.TrailingZeros16(b)
}

func maskHasAnyLane(m archsimd.Mask8x16) bool {
	return m.ToBits() != 0
}
