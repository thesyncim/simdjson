//go:build goexperiment.simd && arm64

package simd

import "simd/archsimd"

var (
	digitWeights10ARM    = [...]uint8{10, 1, 10, 1, 10, 1, 10, 1, 10, 1, 10, 1, 10, 1, 10, 1}
	digitWeights100ARM   = [...]uint16{100, 1, 100, 1, 100, 1, 100, 1}
	digitWeights10000ARM = [...]uint32{10000, 1, 10000, 1}
	digitFormatDiv100ARM = [...]uint32{10486, 10486, 10486, 10486}
	digitFormatMul100ARM = [...]uint32{100, 100, 100, 100}
	digitFormatDiv10ARM  = [...]uint32{103, 103, 103, 103}
	digitFormatMul10ARM  = [...]uint32{10, 10, 10, 10}
	digitShiftRight20ARM = [...]int32{-20, -20, -20, -20}
	digitShiftRight10ARM = [...]int32{-10, -10, -10, -10}
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

func store16Digits(dst *[16]byte, value uint64) {
	hi := value / 100_000_000
	lo := value - hi*100_000_000
	hiTop := (hi * 0xd1b71759) >> 45
	loTop := (lo * 0xd1b71759) >> 45
	chunksArray := [4]uint32{
		uint32(hiTop), uint32(hi - hiTop*10_000),
		uint32(loTop), uint32(lo - loTop*10_000),
	}
	chunks := archsimd.LoadUint32x4Array(&chunksArray)
	div100 := archsimd.LoadUint32x4Array(&digitFormatDiv100ARM)
	mul100 := archsimd.LoadUint32x4Array(&digitFormatMul100ARM)
	hundreds := chunks.Mul(div100).Shift(archsimd.LoadInt32x4Array(&digitShiftRight20ARM))
	below100 := chunks.Sub(hundreds.Mul(mul100))

	div10 := archsimd.LoadUint32x4Array(&digitFormatDiv10ARM)
	mul10 := archsimd.LoadUint32x4Array(&digitFormatMul10ARM)
	shiftRight10 := archsimd.LoadInt32x4Array(&digitShiftRight10ARM)
	thousands := hundreds.Mul(div10).Shift(shiftRight10)
	hundredsDigit := hundreds.Sub(thousands.Mul(mul10))
	tens := below100.Mul(div10).Shift(shiftRight10)
	ones := below100.Sub(tens.Mul(mul10))

	thousandsBytes := thousands.TruncToUint16().TruncToUint8()
	hundredsBytes := hundredsDigit.TruncToUint16().TruncToUint8()
	tensBytes := tens.TruncToUint16().TruncToUint8()
	onesBytes := ones.TruncToUint16().TruncToUint8()
	highPairs := thousandsBytes.InterleaveLo(tensBytes)
	lowPairs := hundredsBytes.InterleaveLo(onesBytes)
	packed := highPairs.InterleaveLo(lowPairs).Add(archsimd.BroadcastUint8x16('0'))
	packed.StoreArray(dst)
}

func parseBackend() string {
	return "arm64-neon"
}

func parseVectorBytes() int {
	return 16
}

func formatBackend() string {
	return "arm64-neon"
}

func formatVectorBytes() int {
	return 16
}
