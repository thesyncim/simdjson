//go:build goexperiment.simd && arm64

package simdjson

import "simd/archsimd"

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
