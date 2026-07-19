//go:build go1.27 && !go1.28 && goexperiment.simd && amd64

package simd

import (
	"encoding/binary"
	"math/bits"
	"simd/archsimd"
	"unicode/utf8"
	"unsafe"
)

// scanAMD64Level selects the vector width once at startup. Dispatch happens
// through static calls in a switch rather than function values: indirect
// calls make escape analysis treat scanned buffers as leaking, which moves
// callers' stack storage onto the heap.
var scanAMD64Level uint8

const (
	scanLevelScalar uint8 = iota
	scanLevelAVX2
)

func selectAMD64ScannerLevel(features CPUFeatures) uint8 {
	// AVX-512 remains an experimental direct kernel until it wins across
	// representative CPU families and short/long input distributions. AVX2 is
	// the demonstrated production width, including on AVX-512-capable CPUs.
	if features.Has(CPUFeatureAVX2) {
		return scanLevelAVX2
	}
	return scanLevelScalar
}

func initStringScanner() {
	// The raw AVX2 entry and staged syntax scanner need 32 remaining bytes; the
	// syntax scanner's 16-byte word probes run on spans of 40 or more. The
	// ordinary special scanner below owns its separate 24-byte prefix policy.
	// Capability checks happen only here; hot calls read the process-constant
	// level below.
	scanCPUFeatures = detectX86CPUFeatures()
	scanAMD64Level = selectAMD64ScannerLevel(scanCPUFeatures)
	if scanAMD64Level == scanLevelAVX2 {
		scanStringSelectedMinBytes = 32
		scanStringProbeMinBytes = 40
		scanStringSpecialBackend = "amd64-avx2"
		scanStringVectorBytes = 32
	}
}

// scanStringSpecial gives the ordinary amd64 scanner a fixed three-word
// prefix before AVX2. JSON strings found while building an index usually end
// in those words even when the remaining document is long. If a 32-55 byte
// span survives the prefix, the final AVX2 block overlaps only bytes already
// proved clean, closing the old 32-39 byte direct-vector gap without losing the
// vector win for late stops.
func scanStringSpecial(src []byte, i int) int {
	remaining := len(src) - i
	if remaining < 24 {
		return scanStringSpecialScalar(src, i)
	}
	window := src[i : i+24]
	if m := stringSpecialMask(binary.LittleEndian.Uint64(window)); m != 0 {
		return i + bits.TrailingZeros64(m)/8
	}
	if m := stringSpecialMask(binary.LittleEndian.Uint64(window[8:])); m != 0 {
		return i + 8 + bits.TrailingZeros64(m)/8
	}
	if m := stringSpecialMask(binary.LittleEndian.Uint64(window[16:])); m != 0 {
		return i + 16 + bits.TrailingZeros64(m)/8
	}
	i += 24
	if remaining < 32 || scanAMD64Level != scanLevelAVX2 {
		return scanStringSpecialScalar(src, i)
	}
	if len(src)-i < 32 {
		i = len(src) - 32
	}
	return scanStringSpecialAVX2(src, i)
}

func scanStringSpecialRuntime(src []byte, i int) int {
	if scanAMD64Level == scanLevelAVX2 {
		return scanStringSpecialAVX2(src, i)
	}
	return scanStringSpecialScalar(src, i)
}

func scanStringSyntaxRuntime(src []byte, i int) int {
	if scanAMD64Level == scanLevelAVX2 {
		return scanStringSyntaxAVX2(src, i)
	}
	return scanStringSyntaxScalar(src, i)
}

func scanEncodedHTMLSpecialRuntime(src []byte, i int) int {
	if scanAMD64Level == scanLevelAVX2 {
		return scanEncodedHTMLSpecialAVX2(src, i)
	}
	return scanEncodedHTMLSpecialScalar(src, i)
}

func scanEncodedHTMLSyntaxRuntime(src []byte, i int) int {
	if scanAMD64Level == scanLevelAVX2 {
		return scanEncodedHTMLSyntaxAVX2(src, i)
	}
	return scanEncodedHTMLSyntaxScalar(src, i)
}

func validUTF8NoLineSeparatorRuntime(src []byte) bool {
	return validUTF8NoLineSeparatorGeneric(src)
}

func validUTF8Runtime(src []byte) bool {
	return utf8.Valid(src)
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
	if i == n {
		return i
	}
	return scanStringSpecialSIMD(src, i)
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
	return scanStringSyntaxSIMD(src, i)
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
