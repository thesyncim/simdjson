//go:build !goexperiment.simd || (!arm64 && !amd64)

package simd

// Parse16Digits reduces sixteen ASCII decimal digits without validating them.
// Call All16Digits first when the input is not already known to be digits.
func Parse16Digits(digits *[16]byte) uint64 {
	return parse16DigitsScalar(digits)
}

func store16Digits(dst *[16]byte, value uint64) {
	store16DigitsScalar(dst, value)
}

func storeDateTimeParts(dst *[20]byte, year, month, day, hour, minute, second uint32) {
	storeDateTimePartsScalar(dst, year, month, day, hour, minute, second)
}

func parseBackend() string {
	return "scalar"
}

func parseVectorBytes() int {
	return 0
}

func formatBackend() string {
	return "scalar"
}

func formatVectorBytes() int {
	return 0
}
