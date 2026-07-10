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
	src := unsafe.Slice((*byte)(base), 16)
	x0 := binary.LittleEndian.Uint64(src)
	x1 := binary.LittleEndian.Uint64(src[8:])
	return ((x0+upper)|(x0-lower))&high == 0 && ((x1+upper)|(x1-lower))&high == 0
}

func parse16DigitsScalar(base unsafe.Pointer) uint64 {
	var value uint64
	for i := 0; i < 16; i++ {
		value = value*10 + uint64(fastByteAt(base, i)-'0')
	}
	return value
}
