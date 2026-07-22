//go:build go1.27 && !go1.28 && goexperiment.simd && amd64 && !amd64.v3

package bitset

import "simd/archsimd"

// bitsetAVX2Available is selected once at startup. Keeping the dispatch as a
// process-constant branch at the public kernel boundary avoids an indirect
// function call, which would make escape analysis conservatively leak caller
// buffers. GOAMD64=v1/v2 binaries therefore remain safe on pre-AVX2 CPUs while
// preserving allocation-free calls on AVX2 machines.
var bitsetAVX2Available = archsimd.X86.AVX2()

// Accelerated reports whether this process selected the AVX2 word kernels.
func Accelerated() bool { return bitsetAVX2Available }

func andWords(dst, a, b []uint64) {
	if bitsetAVX2Available {
		andWordsAVX2(dst, a, b)
		return
	}
	andWordsScalar(dst, a, b)
}

func and3Words(dst, a, b, c []uint64) {
	if bitsetAVX2Available {
		and3WordsAVX2(dst, a, b, c)
		return
	}
	and3WordsScalar(dst, a, b, c)
}

func orWords(dst, a, b []uint64) {
	if bitsetAVX2Available {
		orWordsAVX2(dst, a, b)
		return
	}
	orWordsScalar(dst, a, b)
}

func andNotWords(dst, a, b []uint64) {
	if bitsetAVX2Available {
		andNotWordsAVX2(dst, a, b)
		return
	}
	andNotWordsScalar(dst, a, b)
}
