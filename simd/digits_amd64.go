//go:build goexperiment.simd && amd64

package simd

import "simd/archsimd"

var (
	digitWeights10AMD    = [...]int8{10, 1, 10, 1, 10, 1, 10, 1, 10, 1, 10, 1, 10, 1, 10, 1}
	digitWeights100AMD   = [...]int16{100, 1, 100, 1, 100, 1, 100, 1}
	digitWeights10000AMD = [...]int32{10000, 1, 10000, 1}
	useAVXDigitParser    = archsimd.X86.AVX()
)

// Parse16Digits reduces sixteen ASCII decimal digits without validating them.
// Call All16Digits first when the input is not already known to be digits.
func Parse16Digits(digits *[16]byte) uint64 {
	if !useAVXDigitParser {
		return parse16DigitsScalar(digits)
	}
	values := archsimd.LoadUint8x16Array(digits).Sub(archsimd.BroadcastUint8x16('0'))
	pairs := values.DotProductPairsSaturated(archsimd.LoadInt8x16Array(&digitWeights10AMD))
	weighted100 := pairs.Mul(archsimd.LoadInt16x8Array(&digitWeights100AMD))
	quads := weighted100.ConcatAddPairs(weighted100).ExtendLo4ToInt32()
	weighted10000 := quads.Mul(archsimd.LoadInt32x4Array(&digitWeights10000AMD))
	eights := weighted10000.ConcatAddPairs(weighted10000)
	return uint64(eights.GetElem(0))*100000000 + uint64(eights.GetElem(1))
}

func numberBackend() string {
	if useAVXDigitParser {
		return "amd64-avx"
	}
	return "scalar"
}

func numberVectorBytes() int {
	if useAVXDigitParser {
		return 16
	}
	return 0
}
