package simd

import (
	"bytes"
	"fmt"
	"testing"
	"unicode/utf8"
)

var validUTF8BenchmarkSink bool

func BenchmarkValidUTF8Backend(b *testing.B) {
	mixedUnit := []byte("aé界🙂")
	for _, size := range []int{64, 512, 4096} {
		ascii := bytes.Repeat([]byte{'a'}, size)
		mixed := make([]byte, 0, size)
		for len(mixed)+len(mixedUnit) <= size {
			mixed = append(mixed, mixedUnit...)
		}
		mixed = append(mixed, bytes.Repeat([]byte{'a'}, size-len(mixed))...)
		invalidEarly := bytes.Clone(ascii)
		invalidEarly[0] = 0xff
		invalidLate := bytes.Clone(ascii)
		invalidLate[len(invalidLate)-1] = 0xff

		cases := []struct {
			name string
			src  []byte
		}{
			{name: "ascii", src: ascii},
			{name: "mixed", src: mixed},
			{name: "invalid-early", src: invalidEarly},
			{name: "invalid-late", src: invalidLate},
		}
		for _, tc := range cases {
			b.Run(fmt.Sprintf("%s/%d", tc.name, size), func(b *testing.B) {
				want := utf8.Valid(tc.src)
				if got := validUTF8Fast(tc.src); got != want {
					b.Fatalf("validUTF8Fast = %v, want %v", got, want)
				}
				b.Run("selected", func(b *testing.B) {
					b.SetBytes(int64(len(tc.src)))
					b.ReportAllocs()
					for range b.N {
						validUTF8BenchmarkSink = validUTF8Fast(tc.src)
					}
				})
				b.Run("stdlib", func(b *testing.B) {
					b.SetBytes(int64(len(tc.src)))
					b.ReportAllocs()
					for range b.N {
						validUTF8BenchmarkSink = utf8.Valid(tc.src)
					}
				})
			})
		}
	}
}
