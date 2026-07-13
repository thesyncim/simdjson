//go:build goexperiment.simd && amd64

package simd

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

func maskNot(m archsimd.Mask8x16) archsimd.Mask8x16 {
	ones := archsimd.BroadcastUint8x16(0xff)
	return m.ToInt8x16().ToBits().Xor(ones).BitsToInt8().ToMask()
}
