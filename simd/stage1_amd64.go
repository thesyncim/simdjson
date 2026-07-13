//go:build goexperiment.simd && amd64

package simd

import "simd/archsimd"

// stage1Movemask64 folds four 16-lane compare masks into one 64-bit mask,
// one bit per byte. amd64 mask registers convert to bits directly; the
// weights argument exists for the arm64 pairwise-add variant.
func stage1Movemask64(m0, m1, m2, m3 archsimd.Mask8x16, weights archsimd.Uint8x16) uint64 {
	_ = weights
	return uint64(m0.ToBits()) |
		uint64(m1.ToBits())<<16 |
		uint64(m2.ToBits())<<32 |
		uint64(m3.ToBits())<<48
}
