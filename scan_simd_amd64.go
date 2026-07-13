//go:build goexperiment.simd && amd64

package simdjson

import (
	"math/bits"
	"simd/archsimd"
	"unicode/utf8"
	"unsafe"
)

// scanAMD64Level selects the vector width once at startup. Dispatch happens
// through static calls in a switch rather than function values: indirect
// calls would make escape analysis treat every scanned buffer as leaking,
// forcing callers' stack storage onto the heap.
var scanAMD64Level uint8

const (
	scanLevelScalar uint8 = iota
	scanLevelAVX2
	scanLevelAVX512
)

func initStringScanner() {
	scanCPUFeatures = detectX86CPUFeatures()
	switch {
	case archsimd.X86.AVX512():
		scanAMD64Level = scanLevelAVX512
		scanStringSelectedMinBytes = 32
		scanStringProbeMinBytes = 40
		scanStringSpecialBackend = "amd64-avx512"
		scanStringVectorBytes = 64
	case archsimd.X86.AVX2():
		scanAMD64Level = scanLevelAVX2
		scanStringSelectedMinBytes = 32
		scanStringProbeMinBytes = 40
		scanStringSpecialBackend = "amd64-avx2"
		scanStringVectorBytes = 32
	}
}

func scanStringSpecialRuntime(src []byte, i int) int {
	switch scanAMD64Level {
	case scanLevelAVX512:
		return scanStringSpecialAVX512(src, i)
	case scanLevelAVX2:
		return scanStringSpecialAVX2(src, i)
	default:
		return scanStringSpecialScalar(src, i)
	}
}

func scanStringSyntaxRuntime(src []byte, i int) int {
	switch scanAMD64Level {
	case scanLevelAVX512:
		return scanStringSyntaxAVX512(src, i)
	case scanLevelAVX2:
		return scanStringSyntaxAVX2(src, i)
	default:
		return scanStringSyntaxScalar(src, i)
	}
}

func scanEncodedHTMLSpecialRuntime(src []byte, i int) int {
	switch scanAMD64Level {
	case scanLevelAVX512:
		return scanEncodedHTMLSpecialAVX512(src, i)
	case scanLevelAVX2:
		return scanEncodedHTMLSpecialAVX2(src, i)
	default:
		return scanEncodedHTMLSpecialScalar(src, i)
	}
}

func scanEncodedHTMLSyntaxRuntime(src []byte, i int) int {
	switch scanAMD64Level {
	case scanLevelAVX512:
		return scanEncodedHTMLSyntaxAVX512(src, i)
	case scanLevelAVX2:
		return scanEncodedHTMLSyntaxAVX2(src, i)
	default:
		return scanEncodedHTMLSyntaxScalar(src, i)
	}
}

func validUTF8Runtime(src []byte) bool {
	if len(src) < 16 {
		return utf8.Valid(src)
	}
	base := unsafe.Pointer(unsafe.SliceData(src))
	b80 := archsimd.BroadcastUint8x16(0x80)
	b90 := archsimd.BroadcastUint8x16(0x90)
	ba0 := archsimd.BroadcastUint8x16(0xa0)
	bc0 := archsimd.BroadcastUint8x16(0xc0)
	bc2 := archsimd.BroadcastUint8x16(0xc2)
	be0 := archsimd.BroadcastUint8x16(0xe0)
	bed := archsimd.BroadcastUint8x16(0xed)
	bf0 := archsimd.BroadcastUint8x16(0xf0)
	bf4 := archsimd.BroadcastUint8x16(0xf4)
	bf5 := archsimd.BroadcastUint8x16(0xf5)
	zero := archsimd.BroadcastUint8x16(0)
	prevLead := zero
	prevLead34 := zero
	prevLead4 := zero
	prevE0 := zero
	prevED := zero
	prevF0 := zero
	prevF4 := zero

	i := 0
	for i+16 <= len(src) {
		v := archsimd.LoadUint8x16Array((*[16]uint8)(unsafe.Add(base, i)))
		continuation := v.GreaterEqual(b80).And(v.Less(bc0))
		lead2 := v.GreaterEqual(bc2).And(v.Less(be0))
		lead3 := v.GreaterEqual(be0).And(v.Less(bf0))
		lead4 := v.GreaterEqual(bf0).And(v.Less(bf5))
		invalid := v.GreaterEqual(bc0).And(v.Less(bc2)).Or(v.GreaterEqual(bf5))

		lead := lead2.Or(lead3).Or(lead4).ToInt8x16().ToBits()
		lead34 := lead3.Or(lead4).ToInt8x16().ToBits()
		lead4Bytes := lead4.ToInt8x16().ToBits()
		expected := lead.ConcatShiftBytesRight(prevLead, 15).
			Or(lead34.ConcatShiftBytesRight(prevLead34, 14)).
			Or(lead4Bytes.ConcatShiftBytesRight(prevLead4, 13))
		actual := continuation.ToInt8x16().ToBits()
		invalid = invalid.Or(actual.NotEqual(expected))

		afterE0 := v.Equal(be0).ToInt8x16().ToBits().ConcatShiftBytesRight(prevE0, 15).BitsToInt8().ToMask()
		afterED := v.Equal(bed).ToInt8x16().ToBits().ConcatShiftBytesRight(prevED, 15).BitsToInt8().ToMask()
		afterF0 := v.Equal(bf0).ToInt8x16().ToBits().ConcatShiftBytesRight(prevF0, 15).BitsToInt8().ToMask()
		afterF4 := v.Equal(bf4).ToInt8x16().ToBits().ConcatShiftBytesRight(prevF4, 15).BitsToInt8().ToMask()
		invalid = invalid.Or(afterE0.And(v.Less(ba0)).
			Or(afterED.And(v.GreaterEqual(ba0))).
			Or(afterF0.And(v.Less(b90))).
			Or(afterF4.And(v.GreaterEqual(b90))))
		if maskHasAnyLane(invalid) {
			return false
		}

		prevLead = lead
		prevLead34 = lead34
		prevLead4 = lead4Bytes
		prevE0 = v.Equal(be0).ToInt8x16().ToBits()
		prevED = v.Equal(bed).ToInt8x16().ToBits()
		prevF0 = v.Equal(bf0).ToInt8x16().ToBits()
		prevF4 = v.Equal(bf4).ToInt8x16().ToBits()
		i += 16
	}

	tail := i
	for tail > 0 && i-tail < 3 && src[tail-1]&0xc0 == 0x80 {
		tail--
	}
	if tail > 0 {
		tail--
	}
	return utf8.Valid(src[tail:])
}

func scanEncodedHTMLSpecialAVX2(src []byte, i int) int {
	n := len(src)
	quote := archsimd.BroadcastUint8x32('"')
	slash := archsimd.BroadcastUint8x32('\\')
	lt := archsimd.BroadcastUint8x32('<')
	gt := archsimd.BroadcastUint8x32('>')
	amp := archsimd.BroadcastUint8x32('&')
	ctrlOrNonASCII := archsimd.BroadcastInt8x32(0x20)
	base := unsafe.Pointer(unsafe.SliceData(src))
	for i+64 <= n {
		v0 := archsimd.LoadUint8x32Array((*[32]uint8)(unsafe.Add(base, i)))
		v1 := archsimd.LoadUint8x32Array((*[32]uint8)(unsafe.Add(base, i+32)))
		b0 := v0.Equal(quote).Or(v0.Equal(slash)).Or(v0.Equal(lt)).Or(v0.Equal(gt)).Or(v0.Equal(amp)).Or(v0.BitsToInt8().Less(ctrlOrNonASCII)).ToBits()
		b1 := v1.Equal(quote).Or(v1.Equal(slash)).Or(v1.Equal(lt)).Or(v1.Equal(gt)).Or(v1.Equal(amp)).Or(v1.BitsToInt8().Less(ctrlOrNonASCII)).ToBits()
		if b0|b1 != 0 {
			if b0 != 0 {
				return i + bits.TrailingZeros32(b0)
			}
			return i + 32 + bits.TrailingZeros32(b1)
		}
		i += 64
	}
	if i+32 <= n {
		v := archsimd.LoadUint8x32Array((*[32]uint8)(unsafe.Add(base, i)))
		b := v.Equal(quote).Or(v.Equal(slash)).Or(v.Equal(lt)).Or(v.Equal(gt)).Or(v.Equal(amp)).Or(v.BitsToInt8().Less(ctrlOrNonASCII)).ToBits()
		if b != 0 {
			return i + bits.TrailingZeros32(b)
		}
		i += 32
	}
	return scanEncodedHTMLSpecialSIMD(src, i)
}

func scanEncodedHTMLSpecialAVX512(src []byte, i int) int {
	n := len(src)
	quote := archsimd.BroadcastUint8x64('"')
	slash := archsimd.BroadcastUint8x64('\\')
	lt := archsimd.BroadcastUint8x64('<')
	gt := archsimd.BroadcastUint8x64('>')
	amp := archsimd.BroadcastUint8x64('&')
	ctrlOrNonASCII := archsimd.BroadcastInt8x64(0x20)
	base := unsafe.Pointer(unsafe.SliceData(src))
	for i+128 <= n {
		v0 := archsimd.LoadUint8x64Array((*[64]uint8)(unsafe.Add(base, i)))
		v1 := archsimd.LoadUint8x64Array((*[64]uint8)(unsafe.Add(base, i+64)))
		b0 := v0.Equal(quote).ToBits() | v0.Equal(slash).ToBits() | v0.Equal(lt).ToBits() | v0.Equal(gt).ToBits() | v0.Equal(amp).ToBits() | v0.BitsToInt8().Less(ctrlOrNonASCII).ToBits()
		b1 := v1.Equal(quote).ToBits() | v1.Equal(slash).ToBits() | v1.Equal(lt).ToBits() | v1.Equal(gt).ToBits() | v1.Equal(amp).ToBits() | v1.BitsToInt8().Less(ctrlOrNonASCII).ToBits()
		if b0|b1 != 0 {
			if b0 != 0 {
				return i + bits.TrailingZeros64(b0)
			}
			return i + 64 + bits.TrailingZeros64(b1)
		}
		i += 128
	}
	if i+64 <= n {
		v := archsimd.LoadUint8x64Array((*[64]uint8)(unsafe.Add(base, i)))
		b := v.Equal(quote).ToBits() | v.Equal(slash).ToBits() | v.Equal(lt).ToBits() | v.Equal(gt).ToBits() | v.Equal(amp).ToBits() | v.BitsToInt8().Less(ctrlOrNonASCII).ToBits()
		if b != 0 {
			return i + bits.TrailingZeros64(b)
		}
		i += 64
	}
	return scanEncodedHTMLSpecialAVX2(src, i)
}

func scanEncodedHTMLSyntaxAVX2(src []byte, i int) int {
	n := len(src)
	quote := archsimd.BroadcastUint8x32('"')
	slash := archsimd.BroadcastUint8x32('\\')
	lt := archsimd.BroadcastUint8x32('<')
	gt := archsimd.BroadcastUint8x32('>')
	amp := archsimd.BroadcastUint8x32('&')
	ctrl := archsimd.BroadcastUint8x32(0x20)
	base := unsafe.Pointer(unsafe.SliceData(src))
	for i+64 <= n {
		v0 := archsimd.LoadUint8x32Array((*[32]uint8)(unsafe.Add(base, i)))
		v1 := archsimd.LoadUint8x32Array((*[32]uint8)(unsafe.Add(base, i+32)))
		b0 := v0.Equal(quote).Or(v0.Equal(slash)).Or(v0.Equal(lt)).Or(v0.Equal(gt)).Or(v0.Equal(amp)).Or(v0.Less(ctrl)).ToBits()
		b1 := v1.Equal(quote).Or(v1.Equal(slash)).Or(v1.Equal(lt)).Or(v1.Equal(gt)).Or(v1.Equal(amp)).Or(v1.Less(ctrl)).ToBits()
		if b0|b1 != 0 {
			if b0 != 0 {
				return i + bits.TrailingZeros32(b0)
			}
			return i + 32 + bits.TrailingZeros32(b1)
		}
		i += 64
	}
	if i+32 <= n {
		v := archsimd.LoadUint8x32Array((*[32]uint8)(unsafe.Add(base, i)))
		b := v.Equal(quote).Or(v.Equal(slash)).Or(v.Equal(lt)).Or(v.Equal(gt)).Or(v.Equal(amp)).Or(v.Less(ctrl)).ToBits()
		if b != 0 {
			return i + bits.TrailingZeros32(b)
		}
		i += 32
	}
	return scanEncodedHTMLSyntaxSIMD(src, i)
}

func scanEncodedHTMLSyntaxAVX512(src []byte, i int) int {
	n := len(src)
	quote := archsimd.BroadcastUint8x64('"')
	slash := archsimd.BroadcastUint8x64('\\')
	lt := archsimd.BroadcastUint8x64('<')
	gt := archsimd.BroadcastUint8x64('>')
	amp := archsimd.BroadcastUint8x64('&')
	ctrl := archsimd.BroadcastUint8x64(0x20)
	base := unsafe.Pointer(unsafe.SliceData(src))
	for i+128 <= n {
		v0 := archsimd.LoadUint8x64Array((*[64]uint8)(unsafe.Add(base, i)))
		v1 := archsimd.LoadUint8x64Array((*[64]uint8)(unsafe.Add(base, i+64)))
		b0 := v0.Equal(quote).ToBits() | v0.Equal(slash).ToBits() | v0.Equal(lt).ToBits() | v0.Equal(gt).ToBits() | v0.Equal(amp).ToBits() | v0.Less(ctrl).ToBits()
		b1 := v1.Equal(quote).ToBits() | v1.Equal(slash).ToBits() | v1.Equal(lt).ToBits() | v1.Equal(gt).ToBits() | v1.Equal(amp).ToBits() | v1.Less(ctrl).ToBits()
		if b0|b1 != 0 {
			if b0 != 0 {
				return i + bits.TrailingZeros64(b0)
			}
			return i + 64 + bits.TrailingZeros64(b1)
		}
		i += 128
	}
	if i+64 <= n {
		v := archsimd.LoadUint8x64Array((*[64]uint8)(unsafe.Add(base, i)))
		b := v.Equal(quote).ToBits() | v.Equal(slash).ToBits() | v.Equal(lt).ToBits() | v.Equal(gt).ToBits() | v.Equal(amp).ToBits() | v.Less(ctrl).ToBits()
		if b != 0 {
			return i + bits.TrailingZeros64(b)
		}
		i += 64
	}
	return scanEncodedHTMLSyntaxAVX2(src, i)
}

func scanStringSpecialAVX2(src []byte, i int) int {
	n := len(src)
	quote := archsimd.BroadcastUint8x32('"')
	slash := archsimd.BroadcastUint8x32('\\')
	ctrlOrNonASCII := archsimd.BroadcastInt8x32(0x20)
	base := unsafe.Pointer(unsafe.SliceData(src))

	for i+64 <= n {
		v0 := archsimd.LoadUint8x32Array((*[32]uint8)(unsafe.Add(base, i)))
		v1 := archsimd.LoadUint8x32Array((*[32]uint8)(unsafe.Add(base, i+32)))
		b0 := v0.Equal(quote).
			Or(v0.Equal(slash)).
			Or(v0.BitsToInt8().Less(ctrlOrNonASCII)).ToBits()
		b1 := v1.Equal(quote).
			Or(v1.Equal(slash)).
			Or(v1.BitsToInt8().Less(ctrlOrNonASCII)).ToBits()
		if b0|b1 != 0 {
			if b0 != 0 {
				return i + bits.TrailingZeros32(b0)
			}
			return i + 32 + bits.TrailingZeros32(b1)
		}
		i += 64
	}
	if i+32 <= n {
		v := archsimd.LoadUint8x32Array((*[32]uint8)(unsafe.Add(base, i)))
		b := v.Equal(quote).
			Or(v.Equal(slash)).
			Or(v.BitsToInt8().Less(ctrlOrNonASCII)).ToBits()
		if b != 0 {
			return i + bits.TrailingZeros32(b)
		}
		i += 32
	}
	return scanStringSpecialScalar(src, i)
}

func scanStringSpecialAVX512(src []byte, i int) int {
	n := len(src)
	quote := archsimd.BroadcastUint8x64('"')
	slash := archsimd.BroadcastUint8x64('\\')
	ctrlOrNonASCII := archsimd.BroadcastInt8x64(0x20)
	base := unsafe.Pointer(unsafe.SliceData(src))

	for i+128 <= n {
		v0 := archsimd.LoadUint8x64Array((*[64]uint8)(unsafe.Add(base, i)))
		v1 := archsimd.LoadUint8x64Array((*[64]uint8)(unsafe.Add(base, i+64)))
		b0 := v0.Equal(quote).ToBits() |
			v0.Equal(slash).ToBits() |
			v0.BitsToInt8().Less(ctrlOrNonASCII).ToBits()
		b1 := v1.Equal(quote).ToBits() |
			v1.Equal(slash).ToBits() |
			v1.BitsToInt8().Less(ctrlOrNonASCII).ToBits()
		if b0|b1 != 0 {
			if b0 != 0 {
				return i + bits.TrailingZeros64(b0)
			}
			return i + 64 + bits.TrailingZeros64(b1)
		}
		i += 128
	}
	if i+64 <= n {
		v := archsimd.LoadUint8x64Array((*[64]uint8)(unsafe.Add(base, i)))
		b := v.Equal(quote).ToBits() |
			v.Equal(slash).ToBits() |
			v.BitsToInt8().Less(ctrlOrNonASCII).ToBits()
		if b != 0 {
			return i + bits.TrailingZeros64(b)
		}
		i += 64
	}
	return scanStringSpecialAVX2(src, i)
}

func scanStringSyntaxAVX2(src []byte, i int) int {
	n := len(src)
	quote := archsimd.BroadcastUint8x32('"')
	slash := archsimd.BroadcastUint8x32('\\')
	ctrl := archsimd.BroadcastUint8x32(0x20)
	base := unsafe.Pointer(unsafe.SliceData(src))

	for i+64 <= n {
		v0 := archsimd.LoadUint8x32Array((*[32]uint8)(unsafe.Add(base, i)))
		v1 := archsimd.LoadUint8x32Array((*[32]uint8)(unsafe.Add(base, i+32)))
		b0 := v0.Equal(quote).Or(v0.Equal(slash)).Or(v0.Less(ctrl)).ToBits()
		b1 := v1.Equal(quote).Or(v1.Equal(slash)).Or(v1.Less(ctrl)).ToBits()
		if b0|b1 != 0 {
			if b0 != 0 {
				return i + bits.TrailingZeros32(b0)
			}
			return i + 32 + bits.TrailingZeros32(b1)
		}
		i += 64
	}
	if i+32 <= n {
		v := archsimd.LoadUint8x32Array((*[32]uint8)(unsafe.Add(base, i)))
		b := v.Equal(quote).Or(v.Equal(slash)).Or(v.Less(ctrl)).ToBits()
		if b != 0 {
			return i + bits.TrailingZeros32(b)
		}
		i += 32
	}
	return scanStringSyntaxScalar(src, i)
}

func scanStringSyntaxAVX512(src []byte, i int) int {
	n := len(src)
	quote := archsimd.BroadcastUint8x64('"')
	slash := archsimd.BroadcastUint8x64('\\')
	ctrl := archsimd.BroadcastUint8x64(0x20)
	base := unsafe.Pointer(unsafe.SliceData(src))

	for i+128 <= n {
		v0 := archsimd.LoadUint8x64Array((*[64]uint8)(unsafe.Add(base, i)))
		v1 := archsimd.LoadUint8x64Array((*[64]uint8)(unsafe.Add(base, i+64)))
		b0 := v0.Equal(quote).ToBits() | v0.Equal(slash).ToBits() | v0.Less(ctrl).ToBits()
		b1 := v1.Equal(quote).ToBits() | v1.Equal(slash).ToBits() | v1.Less(ctrl).ToBits()
		if b0|b1 != 0 {
			if b0 != 0 {
				return i + bits.TrailingZeros64(b0)
			}
			return i + 64 + bits.TrailingZeros64(b1)
		}
		i += 128
	}
	if i+64 <= n {
		v := archsimd.LoadUint8x64Array((*[64]uint8)(unsafe.Add(base, i)))
		b := v.Equal(quote).ToBits() | v.Equal(slash).ToBits() | v.Less(ctrl).ToBits()
		if b != 0 {
			return i + bits.TrailingZeros64(b)
		}
		i += 64
	}
	return scanStringSyntaxAVX2(src, i)
}

func detectX86CPUFeatures() CPUFeatures {
	var features CPUFeatures
	probes := [...]struct {
		feature CPUFeature
		has     bool
	}{
		{CPUFeatureAVX, archsimd.X86.AVX()},
		{CPUFeatureAVX2, archsimd.X86.AVX2()},
		{CPUFeatureAVX512, archsimd.X86.AVX512()},
		{CPUFeatureAVX512BITALG, archsimd.X86.AVX512BITALG()},
		{CPUFeatureAVX512GFNI, archsimd.X86.AVX512GFNI()},
		{CPUFeatureAVX512VAES, archsimd.X86.AVX512VAES()},
		{CPUFeatureAVX512VBMI, archsimd.X86.AVX512VBMI()},
		{CPUFeatureAVX512VBMI2, archsimd.X86.AVX512VBMI2()},
		{CPUFeatureAVX512VNNI, archsimd.X86.AVX512VNNI()},
		{CPUFeatureAVX512VPCLMULQDQ, archsimd.X86.AVX512VPCLMULQDQ()},
		{CPUFeatureAVX512VPOPCNTDQ, archsimd.X86.AVX512VPOPCNTDQ()},
		{CPUFeatureAVXAES, archsimd.X86.AVXAES()},
		{CPUFeatureAVXVNNI, archsimd.X86.AVXVNNI()},
		{CPUFeatureFMA, archsimd.X86.FMA()},
		{CPUFeatureSHA, archsimd.X86.SHA()},
		{CPUFeatureVAES, archsimd.X86.VAES()},
	}
	for _, probe := range probes {
		if probe.has {
			features |= probe.feature.mask()
		}
	}
	return features
}
