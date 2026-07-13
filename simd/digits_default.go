//go:build !goexperiment.simd || (!arm64 && !amd64)

package simd

// Parse16Digits reduces sixteen ASCII decimal digits without validating them.
// Call All16Digits first when the input is not already known to be digits.
func Parse16Digits(digits *[16]byte) uint64 {
	return parse16DigitsScalar(digits)
}

func numberBackend() string {
	return "scalar"
}

func numberVectorBytes() int {
	return 0
}
