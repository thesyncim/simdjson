//go:build goexperiment.simd && arm64

package simd

import "simd/archsimd"

var (
	digitWeights10ARM    = [...]uint8{10, 1, 10, 1, 10, 1, 10, 1, 10, 1, 10, 1, 10, 1, 10, 1}
	digitWeights100ARM   = [...]uint16{100, 1, 100, 1, 100, 1, 100, 1}
	digitWeights10000ARM = [...]uint32{10000, 1, 10000, 1}
)

// Parse16Digits reduces sixteen ASCII decimal digits without validating them.
// Call All16Digits first when the input is not already known to be digits.
func Parse16Digits(digits *[16]byte) uint64 {
	values := archsimd.LoadUint8x16Array(digits).Sub(archsimd.BroadcastUint8x16('0'))
	weighted10 := values.Mul(archsimd.LoadUint8x16Array(&digitWeights10ARM))
	lo := weighted10.ExtendLo8ToUint16()
	hi := weighted10.HiToLo().ExtendLo8ToUint16()
	pairs := lo.ConcatAddPairs(hi)
	weighted100 := pairs.Mul(archsimd.LoadUint16x8Array(&digitWeights100ARM))
	quads := weighted100.ConcatAddPairs(weighted100).ExtendLo4ToUint32()
	weighted10000 := quads.Mul(archsimd.LoadUint32x4Array(&digitWeights10000ARM))
	eights := weighted10000.ConcatAddPairs(weighted10000)
	return uint64(eights.GetElem(0))*100000000 + uint64(eights.GetElem(1))
}

func numberBackend() string {
	return "arm64-neon"
}

func numberVectorBytes() int {
	return 16
}
