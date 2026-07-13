//go:build goexperiment.simd && arm64

package simd

import (
	"simd/archsimd"
	"unsafe"
)

// stage1Weights carries one distinct bit per lane within each eight-byte
// half; the pairwise-add tree in stage1Movemask64 folds four compare
// vectors into one 64-bit mask.
var stage1Weights = [16]uint8{1, 2, 4, 8, 16, 32, 64, 128, 1, 2, 4, 8, 16, 32, 64, 128}

// Stage1Block classifies one full 64-byte block starting at p.
func Stage1Block(p *[64]byte, m *Stage1Masks) {
	base := unsafe.Pointer(p)
	v0 := archsimd.LoadUint8x16Array((*[16]uint8)(base))
	v1 := archsimd.LoadUint8x16Array((*[16]uint8)(unsafe.Add(base, 16)))
	v2 := archsimd.LoadUint8x16Array((*[16]uint8)(unsafe.Add(base, 32)))
	v3 := archsimd.LoadUint8x16Array((*[16]uint8)(unsafe.Add(base, 48)))

	weights := archsimd.LoadUint8x16Array(&stage1Weights)
	quote := archsimd.BroadcastUint8x16('"')
	slash := archsimd.BroadcastUint8x16('\\')
	ctrl := archsimd.BroadcastUint8x16(0x20)
	lowNibble := archsimd.BroadcastUint8x16(0x0f)
	loTable := archsimd.LoadUint8x16Array(&stage1ClassLo)
	hiTable := archsimd.LoadUint8x16Array(&stage1ClassHi)
	wsBits := archsimd.BroadcastUint8x16(stage1WhitespaceBits)
	structBits := archsimd.BroadcastUint8x16(stage1StructuralBits)
	zero := archsimd.BroadcastUint8x16(0)

	c0 := loTable.LookupOrZero(v0.And(lowNibble)).And(hiTable.LookupOrZero(v0.ShiftAllRight(4)))
	c1 := loTable.LookupOrZero(v1.And(lowNibble)).And(hiTable.LookupOrZero(v1.ShiftAllRight(4)))
	c2 := loTable.LookupOrZero(v2.And(lowNibble)).And(hiTable.LookupOrZero(v2.ShiftAllRight(4)))
	c3 := loTable.LookupOrZero(v3.And(lowNibble)).And(hiTable.LookupOrZero(v3.ShiftAllRight(4)))

	m.Whitespace = stage1Movemask64(
		c0.And(wsBits).NotEqual(zero),
		c1.And(wsBits).NotEqual(zero),
		c2.And(wsBits).NotEqual(zero),
		c3.And(wsBits).NotEqual(zero),
		weights,
	)
	m.Structural = stage1Movemask64(
		c0.And(structBits).NotEqual(zero),
		c1.And(structBits).NotEqual(zero),
		c2.And(structBits).NotEqual(zero),
		c3.And(structBits).NotEqual(zero),
		weights,
	)
	m.Quote = stage1Movemask64(v0.Equal(quote), v1.Equal(quote), v2.Equal(quote), v3.Equal(quote), weights)
	m.Backslash = stage1Movemask64(v0.Equal(slash), v1.Equal(slash), v2.Equal(slash), v3.Equal(slash), weights)
	m.Control = stage1Movemask64(v0.Less(ctrl), v1.Less(ctrl), v2.Less(ctrl), v3.Less(ctrl), weights)
	m.NonASCII = v0.Or(v1).Or(v2).Or(v3).ReduceMax() >= 0x80
}

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
