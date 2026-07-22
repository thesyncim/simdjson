//go:build goexperiment.simd && amd64

package storeio

import (
	"hash/crc32"
	"simd/archsimd"
	"unsafe"
)

// The folding schedule and constants are generated from the CRC32C polynomial
// using the MIT/zlib-licensed fast-crc32 formulation:
// https://github.com/corsix/fast-crc32
var (
	crc32cInitialAVX512 = [8]uint64{0xffffffff}
	crc32cFold256AVX512 = [8]uint64{
		0xdcb17aa4, 0xb9e02b86,
		0xdcb17aa4, 0xb9e02b86,
		0xdcb17aa4, 0xb9e02b86,
		0xdcb17aa4, 0xb9e02b86,
	}
	crc32cFold64AVX512 = [8]uint64{
		0x740eef02, 0x9e4addf8,
		0x740eef02, 0x9e4addf8,
		0x740eef02, 0x9e4addf8,
		0x740eef02, 0x9e4addf8,
	}
	crc32cFold128AVX512 = [8]uint64{
		0x6992cea2, 0x0d3b6092,
		0x6992cea2, 0x0d3b6092,
		0x6992cea2, 0x0d3b6092,
		0x6992cea2, 0x0d3b6092,
	}
	crc32cReduce512AVX512 = [8]uint64{
		0x1c291d04, 0xddc0152b,
		0x3da6d0cb, 0xba4fc28e,
		0xf20c0dfe, 0x493c7d27,
		0, 0,
	}
	crc32cReduce64QuotientPCLMUL = [2]uint64{0x4869ec38dea713f1, 0}
	crc32cReduce64PolyPCLMUL     = [2]uint64{0x105ec76f1, 0}
)

func pageChecksum(data []byte) uint32 {
	if len(data) >= 256 && archsimd.X86.AVX512() && archsimd.X86.AVX512VPCLMULQDQ() {
		return pageChecksumAVX512(data)
	}
	return crc32.Checksum(data, pageChecksumTable)
}

// pageChecksumAVX512 folds four independent 512-bit streams with VPCLMULQDQ.
// A final 128-bit PCLMUL stage reduces the residue; only a sub-256-byte tail
// uses the standard CRC32C updater. Everything is Go SIMD intrinsics and stack
// values: this package owns no assembly implementation.
func pageChecksumAVX512(data []byte) uint32 {
	base := unsafe.SliceData(data)
	x0 := loadCRC32CBlock(base, 0).
		Xor(archsimd.LoadUint64x8Array(&crc32cInitialAVX512))
	x1 := loadCRC32CBlock(base, 64)
	x2 := loadCRC32CBlock(base, 128)
	x3 := loadCRC32CBlock(base, 192)

	fold := archsimd.LoadUint64x8Array(&crc32cFold256AVX512)
	i := 256
	for ; i+256 <= len(data); i += 256 {
		y0 := x0.CarrylessMultiplyEven(fold)
		x0 = x0.CarrylessMultiplyOdd(fold).
			Xor(y0).
			Xor(loadCRC32CBlock(base, i))
		y1 := x1.CarrylessMultiplyEven(fold)
		x1 = x1.CarrylessMultiplyOdd(fold).
			Xor(y1).
			Xor(loadCRC32CBlock(base, i+64))
		y2 := x2.CarrylessMultiplyEven(fold)
		x2 = x2.CarrylessMultiplyOdd(fold).
			Xor(y2).
			Xor(loadCRC32CBlock(base, i+128))
		y3 := x3.CarrylessMultiplyEven(fold)
		x3 = x3.CarrylessMultiplyOdd(fold).
			Xor(y3).
			Xor(loadCRC32CBlock(base, i+192))
	}

	fold = archsimd.LoadUint64x8Array(&crc32cFold64AVX512)
	y0 := x0.CarrylessMultiplyEven(fold)
	x0 = x0.CarrylessMultiplyOdd(fold).Xor(y0).Xor(x1)
	y2 := x2.CarrylessMultiplyEven(fold)
	x2 = x2.CarrylessMultiplyOdd(fold).Xor(y2).Xor(x3)

	fold = archsimd.LoadUint64x8Array(&crc32cFold128AVX512)
	y0 = x0.CarrylessMultiplyEven(fold)
	x0 = x0.CarrylessMultiplyOdd(fold).Xor(y0).Xor(x2)

	reduce := archsimd.LoadUint64x8Array(&crc32cReduce512AVX512)
	y0 = x0.CarrylessMultiplyEven(reduce).
		Xor(x0.CarrylessMultiplyOdd(reduce))
	residue := y0.GetLo().GetLo().
		Xor(y0.GetLo().GetHi()).
		Xor(y0.GetHi().GetLo()).
		Xor(x0.GetHi().GetHi())
	crc := reduceCRC32C128PCLMUL(residue)
	if i == len(data) {
		return ^crc
	}
	return crc32.Update(^crc, pageChecksumTable, data[i:])
}

// loadCRC32CBlock loads one complete 64-byte window. pageChecksumAVX512 calls
// it only after proving offset+64 <= len(data); x86 permits unaligned loads.
// The returned vector owns no pointer and the helper retains no storage.
func loadCRC32CBlock(base *byte, offset int) archsimd.Uint64x8 {
	return archsimd.LoadUint64x8Array((*[8]uint64)(unsafe.Add(unsafe.Pointer(base), offset)))
}

func reduceCRC32C128PCLMUL(value archsimd.Uint64x2) uint32 {
	crc := reduceCRC32C64PCLMUL(0, value.GetElem(0))
	return reduceCRC32C64PCLMUL(crc, value.GetElem(1))
}

func reduceCRC32C64PCLMUL(crc uint32, value uint64) uint32 {
	var lane archsimd.Uint64x2
	lane = lane.SetElem(0, uint64(crc)^value)
	lane = lane.CarrylessMultiplyEven(archsimd.LoadUint64x2Array(&crc32cReduce64QuotientPCLMUL))
	lane = lane.CarrylessMultiplyEven(archsimd.LoadUint64x2Array(&crc32cReduce64PolyPCLMUL))
	return uint32(lane.GetElem(1))
}
