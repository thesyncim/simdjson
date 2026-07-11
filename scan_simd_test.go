//go:build goexperiment.simd && (arm64 || amd64)

package simdjson

import (
	"fmt"
	"runtime"
	"testing"
)

var scanSink int

func TestSIMDScannerDispatch(t *testing.T) {
	info := CurrentSIMD()
	backend := info.Backend
	var featureNames [len(cpuFeatureNames)]string
	t.Logf("runtime SIMD: backend=%s number=%s vector=%d min=%d features=%v", info.Backend, info.NumberBackend, info.VectorBytes, info.MinBytes, info.Features.AppendNames(featureNames[:0]))
	if runtime.GOARCH == "arm64" && backend != "arm64-neon" {
		t.Fatalf("CurrentSIMD().Backend = %q on arm64, want arm64-neon", backend)
	}
	if runtime.GOARCH == "arm64" && info.NumberBackend != "arm64-neon" {
		t.Fatalf("CurrentSIMD().NumberBackend = %q on arm64, want arm64-neon", info.NumberBackend)
	}
	if backend == "scalar" {
		return
	}
	if info.VectorBytes < 16 || info.MinBytes < 16 {
		t.Fatalf("selected scanner has invalid runtime info: %+v", info)
	}
	if runtime.GOARCH == "arm64" && !info.Features.Has(CPUFeatureNEON) {
		t.Fatalf("arm64 runtime features = %v, want NEON", info.Features)
	}
	if runtime.GOARCH == "amd64" && !info.Features.Has(CPUFeatureAVX2) {
		t.Fatalf("amd64 SIMD backend features = %v, want AVX2", info.Features)
	}
	// Dispatch is a static switch now; verifying the reported backend
	// string is the remaining contract.
	if backend == "scalar" {
		t.Fatalf("SIMD build selected the scalar backend")
	}
}

func TestSIMDStringSyntaxMatchesScalarAllByteValues(t *testing.T) {
	starts := []int{0, 1, 31, 32, 63, 64, 79, 80, 81}
	for b := 0; b <= 0xff; b++ {
		src := longScanCase(160, 80, byte(b))
		for _, start := range starts {
			want := scanStringSyntaxScalar(src, start)
			got := scanStringSyntax(src, start)
			if got != want {
				t.Fatalf("scanStringSyntax(byte=0x%02x start=%d) = %d, want %d", b, start, got, want)
			}
			got = scanStringSyntaxSIMD(src, start)
			if got != want {
				t.Fatalf("scanStringSyntaxSIMD(byte=0x%02x start=%d) = %d, want %d", b, start, got, want)
			}
		}
	}
}

func TestSIMDScanMatchesScalar(t *testing.T) {
	cases := [][]byte{
		[]byte(`plain ascii without anything special`),
		[]byte(`quote " here`),
		[]byte(`slash \ here`),
		[]byte("control \x1f here"),
		[]byte("non-ascii \xe3\x81\x93 here"),
		[]byte(`0123456789abcdef"`),
		[]byte(`0123456789abcdef0123456789abcdef\`),
	}
	for _, src := range cases {
		for start := 0; start <= len(src); start++ {
			got := scanStringSpecial(src, start)
			want := scanStringSpecialScalar(src, start)
			if got != want {
				t.Fatalf("scanStringSpecial(%q, %d) = %d, want %d", src, start, got, want)
			}
			got = scanStringSpecialLong(src, start)
			if got != want {
				t.Fatalf("scanStringSpecialLong(%q, %d) = %d, want %d", src, start, got, want)
			}
		}
	}
}

func TestSIMDLongScanMatchesScalar(t *testing.T) {
	specials := []byte{'"', '\\', 0x1f, 0x80}
	positions := []int{0, 1, 15, 16, 17, 63, 64, 65, 127, 128, 129, 255, 256, 511, 512, 513, 700, 1023}
	starts := []int{0, 1, 7, 15, 16, 31, 64, 127, 128, 255, 511, 512}

	for _, pos := range positions {
		for _, special := range specials {
			src := longScanCase(1200, pos, special)
			for _, start := range starts {
				want := scanStringSpecialScalar(src, start)
				got := scanStringSpecialLong(src, start)
				if got != want {
					t.Fatalf("scanStringSpecialLong(pos=%d special=0x%x start=%d) = %d, want %d", pos, special, start, got, want)
				}
				got = scanStringSpecialSIMD(src, start)
				if got != want {
					t.Fatalf("scanStringSpecialSIMD(pos=%d special=0x%x start=%d) = %d, want %d", pos, special, start, got, want)
				}
			}
		}
	}

	src := longScanCase(1200, -1, 0)
	for _, start := range starts {
		want := scanStringSpecialScalar(src, start)
		got := scanStringSpecialLong(src, start)
		if got != want {
			t.Fatalf("scanStringSpecialLong(no special start=%d) = %d, want %d", start, got, want)
		}
		got = scanStringSpecialSIMD(src, start)
		if got != want {
			t.Fatalf("scanStringSpecialSIMD(no special start=%d) = %d, want %d", start, got, want)
		}
	}
}

func TestSIMDScanMatchesScalarAllByteValues(t *testing.T) {
	starts := []int{0, 1, 63, 64, 79, 80, 81}
	for b := 0; b <= 0xff; b++ {
		src := longScanCase(160, 80, byte(b))
		for _, start := range starts {
			want := scanStringSpecialScalar(src, start)
			got := scanStringSpecial(src, start)
			if got != want {
				t.Fatalf("scanStringSpecial(byte=0x%02x start=%d) = %d, want %d", b, start, got, want)
			}
			got = scanStringSpecialSIMD(src, start)
			if got != want {
				t.Fatalf("scanStringSpecialSIMD(byte=0x%02x start=%d) = %d, want %d", b, start, got, want)
			}
		}
	}
}

func FuzzSIMDScannersMatchScalar(f *testing.F) {
	for _, seed := range [][]byte{
		nil,
		[]byte(`plain ascii`),
		[]byte(`0123456789abcdef"tail`),
		[]byte("0123456789abcdef\\tail"),
		[]byte("0123456789abcdef\x1ftail"),
		[]byte("0123456789abcdef\xe2\x82\xa1tail"),
	} {
		f.Add(seed, uint16(0))
	}
	f.Fuzz(func(t *testing.T, src []byte, startSeed uint16) {
		if len(src) > 1<<16 {
			t.Skip("input too large for scanner fuzz")
		}
		start := 0
		if len(src) != 0 {
			start = int(startSeed) % (len(src) + 1)
		}
		wantSpecial := scanStringSpecialScalar(src, start)
		if got := scanStringSpecial(src, start); got != wantSpecial {
			t.Fatalf("dispatched special scan = %d, scalar = %d", got, wantSpecial)
		}
		if got := scanStringSpecialLong(src, start); got != wantSpecial {
			t.Fatalf("long special scan = %d, scalar = %d", got, wantSpecial)
		}
		if got := scanStringSpecialSIMD(src, start); got != wantSpecial {
			t.Fatalf("direct SIMD special scan = %d, scalar = %d", got, wantSpecial)
		}

		wantSyntax := scanStringSyntaxScalar(src, start)
		if got := scanStringSyntax(src, start); got != wantSyntax {
			t.Fatalf("dispatched syntax scan = %d, scalar = %d", got, wantSyntax)
		}
		if got := scanStringSyntaxSIMD(src, start); got != wantSyntax {
			t.Fatalf("direct SIMD syntax scan = %d, scalar = %d", got, wantSyntax)
		}
	})
}

func longScanCase(n, specialAt int, special byte) []byte {
	src := make([]byte, n)
	for i := range src {
		src[i] = 'a'
	}
	if specialAt >= 0 {
		src[specialAt] = special
	}
	return src
}

func BenchmarkStringScannerASCII(b *testing.B) {
	lengths := []int{8, 15, 16, 24, 31, 32, 48, 63, 64, 96, 127, 128, 192, 255, 256, 384, 511, 512, 768, 1024}
	for _, n := range lengths {
		src := longScanCase(n, -1, 0)
		b.Run(fmt.Sprintf("scalar/%d", n), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				scanSink = scanStringSpecialScalar(src, 0)
			}
		})
		b.Run(fmt.Sprintf("dispatch/%d", n), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				scanSink = scanStringSpecial(src, 0)
			}
		})
		b.Run(fmt.Sprintf("runtime/%d", n), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				scanSink = scanStringSpecialRuntime(src, 0)
			}
		})
		b.Run(fmt.Sprintf("direct/%d", n), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				scanSink = scanStringSpecialSIMD(src, 0)
			}
		})
	}
}

func BenchmarkStringScannerQuoteAtEnd(b *testing.B) {
	lengths := []int{16, 32, 64, 128, 256, 512, 1024}
	for _, n := range lengths {
		src := longScanCase(n, n-1, '"')
		b.Run(fmt.Sprintf("scalar/%d", n), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				scanSink = scanStringSpecialScalar(src, 0)
			}
		})
		b.Run(fmt.Sprintf("dispatch/%d", n), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				scanSink = scanStringSpecial(src, 0)
			}
		})
		b.Run(fmt.Sprintf("direct/%d", n), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				scanSink = scanStringSpecialSIMD(src, 0)
			}
		})
	}
}
