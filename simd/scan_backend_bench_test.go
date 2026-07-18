package simd

import (
	"fmt"
	"testing"
)

var backendScanSink int

func backendScanBytes(n int, specialAt int, special byte) []byte {
	src := make([]byte, n)
	for i := range src {
		src[i] = 'a'
	}
	if specialAt >= 0 {
		src[specialAt] = special
	}
	return src
}

// BenchmarkScannerBackend exercises the ordinary selected scanner surface in
// both portable and SIMD builds. The backend-comparison workflow runs these
// same rows natively on amd64 and ARM64.
func BenchmarkScannerBackend(b *testing.B) {
	for _, n := range []int{32, 64, 128, 512, 4096} {
		b.Run(fmt.Sprintf("string/ascii/%d", n), func(b *testing.B) {
			src := backendScanBytes(n, -1, 0)
			b.SetBytes(int64(n))
			b.ReportAllocs()
			for range b.N {
				backendScanSink = Unchecked.IndexStringSpecial(src, 0)
			}
		})
		b.Run(fmt.Sprintf("string/quote-end/%d", n), func(b *testing.B) {
			src := backendScanBytes(n, n-1, '"')
			b.SetBytes(int64(n))
			b.ReportAllocs()
			for range b.N {
				backendScanSink = Unchecked.IndexStringSpecial(src, 0)
			}
		})
		b.Run(fmt.Sprintf("html/ascii/%d", n), func(b *testing.B) {
			src := backendScanBytes(n, -1, 0)
			b.SetBytes(int64(n))
			b.ReportAllocs()
			for range b.N {
				backendScanSink = Unchecked.IndexHTMLStringSpecial(src, 0)
			}
		})
	}
}
