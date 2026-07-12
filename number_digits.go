package simdjson

import (
	"encoding/binary"
	"unsafe"
)

func all16Digits(base unsafe.Pointer) bool {
	const (
		lower = 0x3030303030303030
		upper = 0x4646464646464646
		high  = 0x8080808080808080
	)
	x0 := loadUint64LE(base)
	x1 := loadUint64LE(unsafe.Add(base, 8))
	return ((x0+upper)|(x0-lower))&high == 0 && ((x1+upper)|(x1-lower))&high == 0
}

func all8Digits(base unsafe.Pointer) bool {
	const (
		lower = 0x3030303030303030
		upper = 0x4646464646464646
		high  = 0x8080808080808080
	)
	x := loadUint64LE(base)
	return ((x+upper)|(x-lower))&high == 0
}

// parse8Digits reduces eight ASCII digits in three pairwise multiply stages.
// It is the small-token companion to the architecture SIMD 16-digit parser.
func parse8Digits(base unsafe.Pointer) uint64 {
	x := loadUint64LE(base)
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
