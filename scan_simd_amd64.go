//go:build goexperiment.simd && amd64

package simdjson

import (
	"math/bits"
	"simd/archsimd"
	"unsafe"
)

func initStringScanner() {
	scanCPUFeatures = detectX86CPUFeatures()
	switch {
	case archsimd.X86.AVX512():
		scanStringSpecialSelected = scanStringSpecialAVX512
		scanStringSyntaxSelected = scanStringSyntaxAVX512
		scanStringSelectedMinBytes = 32
		scanStringProbeMinBytes = 40
		scanStringSpecialBackend = "amd64-avx512"
		scanStringVectorBytes = 64
	case archsimd.X86.AVX2():
		scanStringSpecialSelected = scanStringSpecialAVX2
		scanStringSyntaxSelected = scanStringSyntaxAVX2
		scanStringSelectedMinBytes = 32
		scanStringProbeMinBytes = 40
		scanStringSpecialBackend = "amd64-avx2"
		scanStringVectorBytes = 32
	}
}

func scanStringSpecialRuntime(src []byte, i int) int {
	return scanStringSpecialSelected(src, i)
}

func scanStringSyntaxRuntime(src []byte, i int) int {
	return scanStringSyntaxSelected(src, i)
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
