//go:build !goexperiment.simd || (!arm64 && !amd64)

package simdjson

func simdEnabled() bool {
	return false
}

func simdBackend() string {
	return "scalar"
}

func scanStringSpecial(src []byte, i int) int {
	return scanStringSpecialScalar(src, i)
}

func scanStringSpecialLong(src []byte, i int) int {
	return scanStringSpecialScalar(src, i)
}

func scanStringSyntax(src []byte, i int) int {
	return scanStringSyntaxScalar(src, i)
}

func simdInfo() SIMDInfo {
	return SIMDInfo{Backend: "scalar", NumberBackend: numberSIMDBackend()}
}
