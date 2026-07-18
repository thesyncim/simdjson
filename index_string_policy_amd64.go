//go:build go1.27 && !go1.28 && goexperiment.simd && amd64

package simdjson

// Small index builds contain enough short strings that direct scalar scanning
// avoids paying the staged SIMD dispatch cost repeatedly. The cutoff is a
// whole-document policy; long documents retain the selected AVX2 scanner.
const (
	indexScalarStrings        = true
	indexScalarStringMaxBytes = 1024
)
