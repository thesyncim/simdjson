//go:build goexperiment.simd && arm64

package simdjson

import (
	"math/bits"
	"simd/archsimd"
)

func firstMaskLane(m archsimd.Mask8x16) int {
	x := m.ToInt8x16().ToBits().ReshapeToUint64s()
	lo := x.GetElem(0)
	if lo != 0 {
		return bits.TrailingZeros64(lo) / 8
	}
	hi := x.GetElem(1)
	if hi != 0 {
		return 8 + bits.TrailingZeros64(hi)/8
	}
	return -1
}

func maskHasAnyLane(m archsimd.Mask8x16) bool {
	x := m.ToInt8x16().ToBits().ReshapeToUint64s()
	return x.GetElem(0)|x.GetElem(1) != 0
}
