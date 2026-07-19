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
	for _, n := range []int{32, 55, 56, 64, 128, 512, 4096} {
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

// BenchmarkScannerStopPosition separates the selected scanner's staged
// dispatch cost from the scalar word scanner at the early stops exercised by
// index construction. Calls stay direct so the benchmark does not introduce
// function-value overhead of its own.
func BenchmarkScannerStopPosition(b *testing.B) {
	cases := []struct {
		name    string
		bytes   int
		stop    int
		special byte
	}{
		{name: "1024B/quote0", bytes: 1024, stop: 0, special: '"'},
		{name: "1024B/backslash2", bytes: 1024, stop: 2, special: '\\'},
		{name: "1024B/nonASCII2", bytes: 1024, stop: 2, special: 0x80},
		{name: "1024B/quote5", bytes: 1024, stop: 5, special: '"'},
		{name: "1024B/quote15", bytes: 1024, stop: 15, special: '"'},
		{name: "1024B/quote16", bytes: 1024, stop: 16, special: '"'},
		{name: "1024B/quote23", bytes: 1024, stop: 23, special: '"'},
		{name: "1024B/quote24", bytes: 1024, stop: 24, special: '"'},
		{name: "32B/quote31", bytes: 32, stop: 31, special: '"'},
		{name: "38B/quote5", bytes: 38, stop: 5, special: '"'},
		{name: "38B/quote31", bytes: 38, stop: 31, special: '"'},
		{name: "38B/quote37", bytes: 38, stop: 37, special: '"'},
		{name: "39B/clean", bytes: 39, stop: -1},
		{name: "39B/quote38", bytes: 39, stop: 38, special: '"'},
		{name: "40B/clean", bytes: 40, stop: -1},
		{name: "40B/quote39", bytes: 40, stop: 39, special: '"'},
		{name: "47B/clean", bytes: 47, stop: -1},
		{name: "47B/quote46", bytes: 47, stop: 46, special: '"'},
		{name: "48B/clean", bytes: 48, stop: -1},
		{name: "48B/quote47", bytes: 48, stop: 47, special: '"'},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			src := backendScanBytes(tc.bytes, tc.stop, tc.special)
			want := tc.stop
			if want < 0 {
				want = tc.bytes
			}
			if got := Unchecked.IndexStringSpecial(src, 0); got != want {
				b.Fatalf("selected stop = %d, want %d", got, want)
			}
			if got := scanStringSpecialScalar(src, 0); got != want {
				b.Fatalf("scalar stop = %d, want %d", got, want)
			}

			b.Run("selected", func(b *testing.B) {
				b.ReportAllocs()
				for range b.N {
					backendScanSink = Unchecked.IndexStringSpecial(src, 0)
				}
			})
			b.Run("scalar", func(b *testing.B) {
				b.ReportAllocs()
				for range b.N {
					backendScanSink = scanStringSpecialScalar(src, 0)
				}
			})
		})
	}
}
