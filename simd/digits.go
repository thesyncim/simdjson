package simd

import "encoding/binary"

const (
	digitLower = uint64(0x3030303030303030)
	digitUpper = uint64(0x4646464646464646)
	digitHigh  = uint64(0x8080808080808080)
)

// Backend describes the implementation selected for the exported kernels.
type Backend struct {
	Name        string
	SIMD        bool
	VectorBytes int
}

// Current reports the implementation selected once during package startup.
func Current() Backend {
	name := numberBackend()
	return Backend{Name: name, SIMD: name != "scalar", VectorBytes: numberVectorBytes()}
}

// All8Digits reports whether every byte is an ASCII decimal digit.
func All8Digits(digits *[8]byte) bool {
	return nonDigitMask8(binary.LittleEndian.Uint64(digits[:])) == 0
}

// All16Digits reports whether every byte is an ASCII decimal digit.
func All16Digits(digits *[16]byte) bool {
	return nonDigitMask8(binary.LittleEndian.Uint64(digits[:8])) == 0 &&
		nonDigitMask8(binary.LittleEndian.Uint64(digits[8:])) == 0
}

// Parse8Digits reduces eight ASCII decimal digits without validating them.
// Call All8Digits first when the input is not already known to be digits.
func Parse8Digits(digits *[8]byte) uint64 {
	x := binary.LittleEndian.Uint64(digits[:])
	x = (x & 0x0f0f0f0f0f0f0f0f) * 2561 >> 8
	x = (x & 0x00ff00ff00ff00ff) * 6553601 >> 16
	return (x & 0x0000ffff0000ffff) * 42949672960001 >> 32
}

func nonDigitMask8(x uint64) uint64 {
	return ((x + digitUpper) | (x - digitLower)) & digitHigh
}

func parse16DigitsScalar(digits *[16]byte) uint64 {
	var value uint64
	for _, digit := range digits {
		value = value*10 + uint64(digit-'0')
	}
	return value
}
