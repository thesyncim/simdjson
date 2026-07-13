//go:build goexperiment.simd && arm64

package simd

import "simd/archsimd"

// stage1Movemask64 folds four 16-lane compare masks into one 64-bit mask,
// one bit per byte. Each lane is weighted with a distinct bit within its
// eight-byte half and a three-level pairwise-add tree (UZP1/UZP2 plus ADD,
// the ADDP idiom) accumulates the weights; the lanes are disjoint so
// addition acts as OR.
func stage1Movemask64(m0, m1, m2, m3 archsimd.Mask8x16, weights archsimd.Uint8x16) uint64 {
	b0 := m0.ToInt8x16().ToBits().And(weights)
	b1 := m1.ToInt8x16().ToBits().And(weights)
	b2 := m2.ToInt8x16().ToBits().And(weights)
	b3 := m3.ToInt8x16().ToBits().And(weights)
	s01 := b0.ConcatEven(b1).Add(b0.ConcatOdd(b1))
	s23 := b2.ConcatEven(b3).Add(b2.ConcatOdd(b3))
	s := s01.ConcatEven(s23).Add(s01.ConcatOdd(s23))
	t := s.ConcatEven(s).Add(s.ConcatOdd(s))
	return t.ReshapeToUint64s().GetElem(0)
}
