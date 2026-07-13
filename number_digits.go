package simdjson

import (
	"encoding/binary"
	"math/bits"
	"unsafe"

	simdkernels "github.com/thesyncim/simdjson/simd"
)

const (
	digitLower = uint64(0x3030303030303030)
	digitUpper = uint64(0x4646464646464646)
	digitHigh  = uint64(0x8080808080808080)
)

func nonDigitMask8(x uint64) uint64 {
	return ((x + digitUpper) | (x - digitLower)) & digitHigh
}

// scanDigitsFast advances over a decimal digit run. Short runs stay scalar;
// sustained runs classify eight bytes per iteration and locate the delimiter
// directly from the first non-digit lane.
func scanDigitsFast(base unsafe.Pointer, n, i int) int {
	if i+4 <= n && isDigit(fastByteAt(base, i+3)) {
		for i+8 <= n {
			invalid := nonDigitMask8(loadUint64LE(unsafe.Add(base, i)))
			if invalid != 0 {
				return i + bits.TrailingZeros64(invalid)/8
			}
			i += 8
		}
	}
	for i < n && isDigit(fastByteAt(base, i)) {
		i++
	}
	return i
}

func all16Digits(base unsafe.Pointer) bool {
	return simdkernels.All16Digits((*[16]byte)(base))
}

func all8Digits(base unsafe.Pointer) bool {
	return simdkernels.All8Digits((*[8]byte)(base))
}

// parse8Digits reduces eight ASCII digits in three pairwise multiply stages.
// It is the small-token companion to the architecture SIMD 16-digit parser.
func parse8Digits(base unsafe.Pointer) uint64 {
	return simdkernels.Parse8Digits((*[8]byte)(base))
}

func parse8DigitsWord(x uint64) uint64 {
	x = (x & 0x0f0f0f0f0f0f0f0f) * 2561 >> 8
	x = (x & 0x00ff00ff00ff00ff) * 6553601 >> 16
	return (x & 0x0000ffff0000ffff) * 42949672960001 >> 32
}

func loadUint64LE(base unsafe.Pointer) uint64 {
	return binary.LittleEndian.Uint64((*[8]byte)(base)[:])
}

func parse16DigitsScalar(base unsafe.Pointer) uint64 {
	var value uint64
	for i := 0; i < 16; i++ {
		value = value*10 + uint64(fastByteAt(base, i)-'0')
	}
	return value
}
