//go:build go1.27 && !go1.28 && goexperiment.simd && amd64 && amd64.v3

package bitset

// GOAMD64=v3 and newer binaries require AVX2-capable processors. Direct calls
// remove even the process-constant capability branch from bitmap operations.

// Accelerated reports whether this process selected the AVX2 word kernels.
func Accelerated() bool { return true }

func andWords(dst, a, b []uint64)     { andWordsAVX2(dst, a, b) }
func and3Words(dst, a, b, c []uint64) { and3WordsAVX2(dst, a, b, c) }
func orWords(dst, a, b []uint64)      { orWordsAVX2(dst, a, b) }
func andNotWords(dst, a, b []uint64)  { andNotWordsAVX2(dst, a, b) }
