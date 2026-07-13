//go:build goexperiment.simd && arm64

package simd

import (
	"simd/archsimd"
	"unicode/utf8"
	"unsafe"
)

var utf8LookupFirstHigh = [16]uint8{
	2, 2, 2, 2, 2, 2, 2, 2,
	128, 128, 128, 128, 33, 1, 21, 73,
}

var utf8LookupFirstLow = [16]uint8{
	231, 163, 131, 131, 139, 203, 203, 203,
	203, 203, 203, 203, 203, 219, 203, 203,
}

var utf8LookupSecondHigh = [16]uint8{
	1, 1, 1, 1, 1, 1, 1, 1,
	230, 174, 186, 186, 1, 1, 1, 1,
}

func initStringScanner() {
	scanStringSelectedMinBytes = 96
	scanStringProbeMinBytes = 17
	scanStringSpecialBackend = "arm64-neon"
	scanStringVectorBytes = 16
	scanCPUFeatures = CPUFeatureNEON.mask()
	if archsimd.ARM64.PMULL() {
		scanCPUFeatures |= CPUFeaturePMULL.mask()
	}
}

func scanStringSpecialRuntime(src []byte, i int) int {
	return scanStringSpecialSIMD(src, i)
}

func scanStringSyntaxRuntime(src []byte, i int) int {
	return scanStringSyntaxSIMD(src, i)
}

func scanEncodedHTMLSpecialRuntime(src []byte, i int) int {
	return scanEncodedHTMLSpecialSIMD(src, i)
}

func scanEncodedHTMLSyntaxRuntime(src []byte, i int) int {
	return scanEncodedHTMLSyntaxSIMD(src, i)
}

func validUTF8Runtime(src []byte) bool {
	if len(src) < 16 {
		return utf8.Valid(src)
	}
	base := unsafe.Pointer(unsafe.SliceData(src))
	firstHighTable := archsimd.LoadUint8x16Array(&utf8LookupFirstHigh)
	firstLowTable := archsimd.LoadUint8x16Array(&utf8LookupFirstLow)
	secondHighTable := archsimd.LoadUint8x16Array(&utf8LookupSecondHigh)
	lowNibble := archsimd.BroadcastUint8x16(0x0f)
	e0Minus1 := archsimd.BroadcastUint8x16(0xdf)
	f0Minus1 := archsimd.BroadcastUint8x16(0xef)
	continuationBit := archsimd.BroadcastUint8x16(0x80)
	shiftRight4 := archsimd.BroadcastInt8x16(-4)
	zero := archsimd.BroadcastUint8x16(0)
	previous := zero
	previousHigh := zero
	errors := zero

	i := 0
	for i+16 <= len(src) {
		input := archsimd.LoadUint8x16Array((*[16]uint8)(unsafe.Add(base, i)))
		prev1 := input.ConcatShiftBytesRight(previous, 15)
		prev2 := input.ConcatShiftBytesRight(previous, 14)
		prev3 := input.ConcatShiftBytesRight(previous, 13)
		inputHigh := input.Shift(shiftRight4)
		prev1High := inputHigh.ConcatShiftBytesRight(previousHigh, 15)
		firstHigh := firstHighTable.LookupOrZero(prev1High)
		firstLow := firstLowTable.LookupOrZero(prev1.And(lowNibble))
		secondHigh := secondHighTable.LookupOrZero(inputHigh)
		special := firstHigh.And(firstLow).And(secondHigh)
		mustContinue := prev2.SubSaturated(e0Minus1).
			Or(prev3.SubSaturated(f0Minus1)).Greater(zero)
		mustContinueBits := mustContinue.ToInt8x16().ToBits().And(continuationBit)
		errors = errors.Or(mustContinueBits.Xor(special))
		previous = input
		previousHigh = inputHigh
		i += 16
	}
	if errors.ReduceMax() != 0 {
		return false
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
