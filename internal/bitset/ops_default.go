//go:build !go1.27 || go1.28 || !goexperiment.simd || (!arm64 && !amd64)

package bitset

// Accelerated reports whether this build selected the SIMD word kernels.
func Accelerated() bool              { return false }
func andWords(dst, a, b []uint64)    { andWordsScalar(dst, a, b) }
func orWords(dst, a, b []uint64)     { orWordsScalar(dst, a, b) }
func andNotWords(dst, a, b []uint64) { andNotWordsScalar(dst, a, b) }
