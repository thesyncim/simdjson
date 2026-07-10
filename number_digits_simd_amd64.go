//go:build goexperiment.simd && amd64

package simdjson

import (
	"simd/archsimd"
	"unsafe"
)

var (
	digitWeights10AMD    = [...]int8{10, 1, 10, 1, 10, 1, 10, 1, 10, 1, 10, 1, 10, 1, 10, 1}
	digitWeights100AMD   = [...]int16{100, 1, 100, 1, 100, 1, 100, 1}
	digitWeights10000AMD = [...]int32{10000, 1, 10000, 1}
)

var useAVXDigitParser = archsimd.X86.AVX()

func parse16Digits(base unsafe.Pointer) uint64 {
	if !useAVXDigitParser {
		return parse16DigitsScalar(base)
	}
	return parse16DigitsAVX(base)
}

func parse16DigitsAVX(base unsafe.Pointer) uint64 {
	digits := archsimd.LoadUint8x16Array((*[16]uint8)(base)).Sub(archsimd.BroadcastUint8x16('0'))
	pairs := digits.DotProductPairsSaturated(archsimd.LoadInt8x16Array(&digitWeights10AMD))
	weighted100 := pairs.Mul(archsimd.LoadInt16x8Array(&digitWeights100AMD))
	quads := weighted100.ConcatAddPairs(weighted100).ExtendLo4ToInt32()
	weighted10000 := quads.Mul(archsimd.LoadInt32x4Array(&digitWeights10000AMD))
	eights := weighted10000.ConcatAddPairs(weighted10000)
	return uint64(eights.GetElem(0))*100000000 + uint64(eights.GetElem(1))
}

func numberSIMDBackend() string {
	if useAVXDigitParser {
		return "amd64-avx"
	}
	return "scalar"
}
