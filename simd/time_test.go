package simd

import (
	"bytes"
	"math/rand/v2"
	"testing"
	"time"
)

var timeDigitsSink [20]byte

func TestAppendTimeMatchesTime(t *testing.T) {
	locations := []*time.Location{
		time.UTC,
		time.FixedZone("west", -5*60*60-30*60),
		time.FixedZone("east", 14*60*60+45*60),
		time.FixedZone("positive sub-minute", 30),
		time.FixedZone("negative sub-minute", -30),
		time.FixedZone("positive boundary", 24*60*60-1),
		time.FixedZone("negative boundary", -24*60*60+1),
	}
	for _, location := range locations {
		for range 100_000 {
			year := rand.IntN(10_000)
			month := time.Month(rand.IntN(12) + 1)
			day := rand.IntN(28) + 1
			hour := rand.IntN(24)
			minute := rand.IntN(60)
			second := rand.IntN(60)
			nanosecond := rand.IntN(1_000_000_000)
			value := time.Date(year, month, day, hour, minute, second, nanosecond, location)

			want := []byte{'"'}
			var err error
			want, err = value.AppendText(want)
			if err != nil {
				t.Fatal(err)
			}
			want = append(want, '"')
			got, err := AppendTime([]byte("prefix"), value)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, append([]byte("prefix"), want...)) {
				t.Fatalf("AppendTime(%v) = %q, want %q", value, got, want)
			}
		}
	}
}

func TestAppendTimeErrorsDoNotChangeDestination(t *testing.T) {
	for _, value := range []time.Time{
		time.Date(-1, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(10_000, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 1, 1, 0, 0, 0, 0, time.FixedZone("invalid", 24*60*60)),
	} {
		dst := []byte("prefix")
		got, err := AppendTime(dst, value)
		if err == nil {
			t.Fatalf("AppendTime(%v) succeeded", value)
		}
		if string(got) != "prefix" {
			t.Fatalf("AppendTime changed destination to %q", got)
		}
	}
}

func BenchmarkAppendTime(b *testing.B) {
	value := time.Date(2026, 7, 13, 14, 37, 52, 123_456_700, time.FixedZone("west", -4*60*60))
	b.Run("simd", func(b *testing.B) {
		buf := make([]byte, 0, 64)
		for b.Loop() {
			buf, _ = AppendTime(buf[:0], value)
		}
	})
	b.Run("time", func(b *testing.B) {
		buf := make([]byte, 0, 64)
		for b.Loop() {
			buf = append(buf[:0], '"')
			buf, _ = value.AppendText(buf)
			buf = append(buf, '"')
		}
	})
}

func BenchmarkStoreDateTimeDigits(b *testing.B) {
	b.Run("selected", func(b *testing.B) {
		var dst [20]byte
		for b.Loop() {
			storeDateTimeParts(&dst, 2026, 7, 13, 14, 37, 52)
		}
		timeDigitsSink = dst
	})
	b.Run("scalar", func(b *testing.B) {
		var dst [20]byte
		for b.Loop() {
			storeDateTimePartsScalar(&dst, 2026, 7, 13, 14, 37, 52)
		}
		timeDigitsSink = dst
	})
}
