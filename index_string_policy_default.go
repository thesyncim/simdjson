//go:build !go1.27 || go1.28 || !goexperiment.simd || !amd64

package simdjson

const (
	indexScalarStrings        = false
	indexScalarStringMaxBytes = 0
)
