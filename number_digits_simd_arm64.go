//go:build goexperiment.simd && arm64

package simdjson

import (
	"simd/archsimd"
	"unsafe"
)

var (
	digitWeights10ARM    = [...]uint8{10, 1, 10, 1, 10, 1, 10, 1, 10, 1, 10, 1, 10, 1, 10, 1}
	digitWeights100ARM   = [...]uint16{100, 1, 100, 1, 100, 1, 100, 1}
	digitWeights10000ARM = [...]uint32{10000, 1, 10000, 1}
)

func parse16Digits(base unsafe.Pointer) uint64 {
	digits := archsimd.LoadUint8x16Array((*[16]uint8)(base)).Sub(archsimd.BroadcastUint8x16('0'))
	weighted10 := digits.Mul(archsimd.LoadUint8x16Array(&digitWeights10ARM))
	lo := weighted10.ExtendLo8ToUint16()
	hi := weighted10.HiToLo().ExtendLo8ToUint16()
	pairs := lo.ConcatAddPairs(hi)
	weighted100 := pairs.Mul(archsimd.LoadUint16x8Array(&digitWeights100ARM))
	quads := weighted100.ConcatAddPairs(weighted100).ExtendLo4ToUint32()
	weighted10000 := quads.Mul(archsimd.LoadUint32x4Array(&digitWeights10000ARM))
	eights := weighted10000.ConcatAddPairs(weighted10000)
	return uint64(eights.GetElem(0))*100000000 + uint64(eights.GetElem(1))
}

func numberSIMDBackend() string {
	return "arm64-neon"
}
