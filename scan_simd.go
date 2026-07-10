//go:build goexperiment.simd && (arm64 || amd64)

package simdjson

import (
	"encoding/binary"
	"math/bits"
	"simd/archsimd"
	"unsafe"
)

var (
	scanStringSpecialBackend   = "scalar"
	scanStringSpecialSelected  = scanStringSpecialScalar
	scanStringSyntaxSelected   = scanStringSyntaxScalar
	scanStringSelectedMinBytes = int(^uint(0) >> 1)
	scanStringProbeMinBytes    = int(^uint(0) >> 1)
	scanStringVectorBytes      int
	scanCPUFeatures            CPUFeatures
)

func init() {
	initStringScanner()
}

func simdEnabled() bool {
	return scanStringSpecialBackend != "scalar"
}

func simdBackend() string {
	return scanStringSpecialBackend
}

func simdInfo() SIMDInfo {
	return SIMDInfo{
		Enabled:       simdEnabled(),
		Backend:       scanStringSpecialBackend,
		NumberBackend: numberSIMDBackend(),
		VectorBytes:   scanStringVectorBytes,
		MinBytes:      scanStringSelectedMinBytes,
		Features:      scanCPUFeatures,
	}
}

func scanStringSpecial(src []byte, i int) int {
	remaining := len(src) - i
	if remaining >= scanStringProbeMinBytes {
		if m := stringSpecialMask(binary.LittleEndian.Uint64(src[i:])); m != 0 {
			return i + bits.TrailingZeros64(m)/8
		}
		i += 8
		if m := stringSpecialMask(binary.LittleEndian.Uint64(src[i:])); m != 0 {
			return i + bits.TrailingZeros64(m)/8
		}
		i += 8
	}
	if len(src)-i >= scanStringSelectedMinBytes {
		return scanStringSpecialRuntime(src, i)
	}
	if i+8 <= len(src) {
		if m := stringSpecialMask(binary.LittleEndian.Uint64(src[i:])); m != 0 {
			return i + bits.TrailingZeros64(m)/8
		}
		i += 8
	}
	for i < len(src) {
		c := src[i]
		if c == '"' || c == '\\' || c < 0x20 || c >= 0x80 {
			return i
		}
		i++
	}
	return len(src)
}

func scanStringSpecialLong(src []byte, i int) int {
	return scanStringSpecialRuntime(src, i)
}

func scanStringSyntax(src []byte, i int) int {
	remaining := len(src) - i
	if remaining >= scanStringProbeMinBytes {
		if m := stringSyntaxMask(binary.LittleEndian.Uint64(src[i:])); m != 0 {
			return i + bits.TrailingZeros64(m)/8
		}
		i += 8
		if m := stringSyntaxMask(binary.LittleEndian.Uint64(src[i:])); m != 0 {
			return i + bits.TrailingZeros64(m)/8
		}
		i += 8
	}
	if len(src)-i >= scanStringSelectedMinBytes {
		return scanStringSyntaxRuntime(src, i)
	}
	if i+8 <= len(src) {
		if m := stringSyntaxMask(binary.LittleEndian.Uint64(src[i:])); m != 0 {
			return i + bits.TrailingZeros64(m)/8
		}
		i += 8
	}
	for i < len(src) {
		c := src[i]
		if c == '"' || c == '\\' || c < 0x20 {
			return i
		}
		i++
	}
	return len(src)
}

func scanStringSpecialSIMD(src []byte, i int) int {
	n := len(src)
	quote := archsimd.BroadcastUint8x16('"')
	slash := archsimd.BroadcastUint8x16('\\')
	ctrlOrNonASCII := archsimd.BroadcastInt8x16(0x20)
	base := unsafe.Pointer(unsafe.SliceData(src))

	for i+64 <= n {
		v0 := archsimd.LoadUint8x16Array((*[16]uint8)(unsafe.Add(base, i)))
		v1 := archsimd.LoadUint8x16Array((*[16]uint8)(unsafe.Add(base, i+16)))
		v2 := archsimd.LoadUint8x16Array((*[16]uint8)(unsafe.Add(base, i+32)))
		v3 := archsimd.LoadUint8x16Array((*[16]uint8)(unsafe.Add(base, i+48)))

		m0 := v0.Equal(quote).
			Or(v0.Equal(slash)).
			Or(v0.BitsToInt8().Less(ctrlOrNonASCII))
		m1 := v1.Equal(quote).
			Or(v1.Equal(slash)).
			Or(v1.BitsToInt8().Less(ctrlOrNonASCII))
		m2 := v2.Equal(quote).
			Or(v2.Equal(slash)).
			Or(v2.BitsToInt8().Less(ctrlOrNonASCII))
		m3 := v3.Equal(quote).
			Or(v3.Equal(slash)).
			Or(v3.BitsToInt8().Less(ctrlOrNonASCII))

		if maskHasAnyLane(m0.Or(m1).Or(m2).Or(m3)) {
			if maskHasAnyLane(m0.Or(m1)) {
				if lane := firstMaskLane(m0); lane >= 0 {
					return i + lane
				}
				return i + 16 + firstMaskLane(m1)
			}
			if lane := firstMaskLane(m2); lane >= 0 {
				return i + 32 + lane
			}
			return i + 48 + firstMaskLane(m3)
		}
		i += 64
	}

	for i+16 <= n {
		v := archsimd.LoadUint8x16Array((*[16]uint8)(unsafe.Add(base, i)))
		m := v.Equal(quote).
			Or(v.Equal(slash)).
			Or(v.BitsToInt8().Less(ctrlOrNonASCII))
		if lane := firstMaskLane(m); lane >= 0 {
			return i + lane
		}
		i += 16
	}
	return scanStringSpecialScalar(src, i)
}

func scanStringSyntaxSIMD(src []byte, i int) int {
	n := len(src)
	quote := archsimd.BroadcastUint8x16('"')
	slash := archsimd.BroadcastUint8x16('\\')
	ctrl := archsimd.BroadcastUint8x16(0x20)
	base := unsafe.Pointer(unsafe.SliceData(src))

	for i+64 <= n {
		v0 := archsimd.LoadUint8x16Array((*[16]uint8)(unsafe.Add(base, i)))
		v1 := archsimd.LoadUint8x16Array((*[16]uint8)(unsafe.Add(base, i+16)))
		v2 := archsimd.LoadUint8x16Array((*[16]uint8)(unsafe.Add(base, i+32)))
		v3 := archsimd.LoadUint8x16Array((*[16]uint8)(unsafe.Add(base, i+48)))

		m0 := v0.Equal(quote).Or(v0.Equal(slash)).Or(v0.Less(ctrl))
		m1 := v1.Equal(quote).Or(v1.Equal(slash)).Or(v1.Less(ctrl))
		m2 := v2.Equal(quote).Or(v2.Equal(slash)).Or(v2.Less(ctrl))
		m3 := v3.Equal(quote).Or(v3.Equal(slash)).Or(v3.Less(ctrl))

		if maskHasAnyLane(m0.Or(m1).Or(m2).Or(m3)) {
			if maskHasAnyLane(m0.Or(m1)) {
				if lane := firstMaskLane(m0); lane >= 0 {
					return i + lane
				}
				return i + 16 + firstMaskLane(m1)
			}
			if lane := firstMaskLane(m2); lane >= 0 {
				return i + 32 + lane
			}
			return i + 48 + firstMaskLane(m3)
		}
		i += 64
	}

	for i+16 <= n {
		v := archsimd.LoadUint8x16Array((*[16]uint8)(unsafe.Add(base, i)))
		m := v.Equal(quote).Or(v.Equal(slash)).Or(v.Less(ctrl))
		if lane := firstMaskLane(m); lane >= 0 {
			return i + lane
		}
		i += 16
	}
	return scanStringSyntaxScalar(src, i)
}
