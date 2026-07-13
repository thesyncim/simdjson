package simd

import (
	"fmt"
	"strconv"
	"testing"
)

var parsedDigitsSink uint64

func TestDigitKernels(t *testing.T) {
	state := uint64(0x9e3779b97f4a7c15)
	for range 100000 {
		state ^= state << 13
		state ^= state >> 7
		state ^= state << 17

		var text16 [16]byte
		copy(text16[:], strconv.FormatUint(1_000_000_000_000_000+state%9_000_000_000_000_000, 10))
		if !All16Digits(&text16) {
			t.Fatalf("All16Digits rejected %q", text16)
		}
		want16, _ := strconv.ParseUint(string(text16[:]), 10, 64)
		if got := Parse16Digits(&text16); got != want16 {
			t.Fatalf("Parse16Digits(%q) = %d, want %d", text16, got, want16)
		}

		var text8 [8]byte
		copy(text8[:], fmt.Sprintf("%08d", state%1e8))
		if !All8Digits(&text8) {
			t.Fatalf("All8Digits rejected %q", text8)
		}
		want8, _ := strconv.ParseUint(string(text8[:]), 10, 64)
		if got := Parse8Digits(&text8); got != want8 {
			t.Fatalf("Parse8Digits(%q) = %d, want %d", text8, got, want8)
		}
	}
}

func TestDigitClassifiersRejectEveryByte(t *testing.T) {
	valid8 := [8]byte{'0', '1', '2', '3', '4', '5', '6', '7'}
	valid16 := [16]byte{'0', '1', '2', '3', '4', '5', '6', '7', '8', '9', '0', '1', '2', '3', '4', '5'}
	for value := range 256 {
		want := '0' <= byte(value) && byte(value) <= '9'
		for i := range valid8 {
			candidate := valid8
			candidate[i] = byte(value)
			if got := All8Digits(&candidate); got != want {
				t.Fatalf("All8Digits position %d byte %#02x = %v, want %v", i, value, got, want)
			}
		}
		for i := range valid16 {
			candidate := valid16
			candidate[i] = byte(value)
			if got := All16Digits(&candidate); got != want {
				t.Fatalf("All16Digits position %d byte %#02x = %v, want %v", i, value, got, want)
			}
		}
	}
}

func BenchmarkParse16Digits(b *testing.B) {
	digits := [16]byte{'1', '2', '3', '4', '5', '6', '7', '8', '9', '0', '1', '2', '3', '4', '5', '6'}
	b.Run("selected", func(b *testing.B) {
		for range b.N {
			parsedDigitsSink = Parse16Digits(&digits)
		}
	})
	b.Run("scalar", func(b *testing.B) {
		for range b.N {
			parsedDigitsSink = parse16DigitsScalar(&digits)
		}
	})
}
