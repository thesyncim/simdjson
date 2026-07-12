package simdjson

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"testing"
	"unsafe"
)

var parsedDigitsSink uint64
var benchmarkFloatSink float64

func TestScanTypedFloat64LeadingZerosMatchesStrconv(t *testing.T) {
	for _, text := range []string{
		"0.0006988752666567719",
		"-0.0011574074074074073",
		"0.00012874983906270118",
		"0.028647215558761104",
	} {
		src := []byte(text + ",")
		end, got, exact, ok := scanTypedFloat64(unsafe.Pointer(unsafe.SliceData(src)), len(src), 0)
		if !ok || !exact || end != len(text) {
			t.Fatalf("scanTypedFloat64(%q) = end %d, exact %v, ok %v", text, end, exact, ok)
		}
		want, err := strconv.ParseFloat(text, 64)
		if err != nil {
			t.Fatal(err)
		}
		if math.Float64bits(got) != math.Float64bits(want) {
			t.Fatalf("scanTypedFloat64(%q) = %.17g (%#x), want %.17g (%#x)",
				text, got, math.Float64bits(got), want, math.Float64bits(want))
		}
	}
}

func TestScanTypedFloat64FormattedValues(t *testing.T) {
	state := uint64(0x243f6a8885a308d3)
	for range 50000 {
		state ^= state << 13
		state ^= state >> 7
		state ^= state << 17
		want := math.Float64frombits(state)
		if math.IsNaN(want) || math.IsInf(want, 0) {
			continue
		}
		text := strconv.FormatFloat(want, 'g', -1, 64)
		src := []byte(text + ",")
		end, got, exact, ok := scanTypedFloat64(unsafe.Pointer(unsafe.SliceData(src)), len(src), 0)
		if !ok || end != len(text) {
			t.Fatalf("scanTypedFloat64(%q) = end %d, ok %v", text, end, ok)
		}
		if exact && math.Float64bits(got) != math.Float64bits(want) {
			t.Fatalf("scanTypedFloat64(%q) = %.17g (%#x), want %.17g (%#x)",
				text, got, math.Float64bits(got), want, math.Float64bits(want))
		}
	}
}

func TestParse8Digits(t *testing.T) {
	for value := uint64(0); value < 100000000; value += 7919 {
		text := []byte(fmt.Sprintf("%08d", value))
		base := unsafe.Pointer(unsafe.SliceData(text))
		if !all8Digits(base) {
			t.Fatalf("all8Digits rejected %q", text)
		}
		if got := parse8Digits(base); got != value {
			t.Fatalf("parse8Digits(%q) = %d, want %d", text, got, value)
		}
	}
	text := []byte("00000000")
	for i := range text {
		for value := 0; value < 256; value++ {
			text[i] = byte(value)
			want := value >= '0' && value <= '9'
			if got := all8Digits(unsafe.Pointer(unsafe.SliceData(text))); got != want {
				t.Fatalf("position %d byte %#02x: all8Digits = %v, want %v", i, value, got, want)
			}
		}
		text[i] = '0'
	}
}

func TestAll16DigitsEveryBytePosition(t *testing.T) {
	var digits [16]byte
	for i := range digits {
		digits[i] = '0'
	}
	base := unsafe.Pointer(&digits[0])
	if !all16Digits(base) {
		t.Fatal("all16Digits rejected zero digits")
	}
	for i := range digits {
		for value := 0; value < 256; value++ {
			digits[i] = byte(value)
			want := value >= '0' && value <= '9'
			if got := all16Digits(base); got != want {
				t.Fatalf("position %d byte %#02x: all16Digits = %v, want %v", i, value, got, want)
			}
		}
		digits[i] = '0'
	}
}

func TestParse16Digits(t *testing.T) {
	for _, text := range []string{
		"0000000000000000",
		"0000000000000001",
		"0123456789012345",
		"1000000000000000",
		"9007199254740992",
		"9999999999999999",
	} {
		checkParse16Digits(t, []byte(text))
	}

	state := uint64(0x9e3779b97f4a7c15)
	var digits [16]byte
	for range 10000 {
		for i := range digits {
			state ^= state << 13
			state ^= state >> 7
			state ^= state << 17
			digits[i] = '0' + byte(state%10)
		}
		checkParse16Digits(t, digits[:])
	}
}

func FuzzParse16Digits(f *testing.F) {
	for _, seed := range []string{
		"0000000000000000",
		"0123456789012345",
		"9007199254740992",
		"9999999999999999",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, text string) {
		if len(text) != 16 {
			t.Skip()
		}
		for i := range text {
			if text[i] < '0' || text[i] > '9' {
				t.Skip()
			}
		}
		checkParse16Digits(t, []byte(text))
	})
}

func checkParse16Digits(t testing.TB, digits []byte) {
	t.Helper()
	base := unsafe.Pointer(unsafe.SliceData(digits))
	want, err := strconv.ParseUint(string(digits), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	if got := parse16DigitsScalar(base); got != want {
		t.Fatalf("parse16DigitsScalar(%q) = %d, want %d", digits, got, want)
	}
	if got := parse16Digits(base); got != want {
		t.Fatalf("parse16Digits(%q) = %d, want %d", digits, got, want)
	}
}

func BenchmarkParse16Digits(b *testing.B) {
	digits := []byte("1234567890123456")
	base := unsafe.Pointer(unsafe.SliceData(digits))
	b.Run("selected", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			parsedDigitsSink = parse16Digits(base)
		}
	})
	b.Run("scalar", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			parsedDigitsSink = parse16DigitsScalar(base)
		}
	})
}

func BenchmarkParseFloat64(b *testing.B) {
	for _, text := range []string{
		"2.5",
		"1234567890123456",
		"1234567890123456.25",
		"1.2345678901234567e-120",
	} {
		data := []byte(text)
		b.Run(text+"/SIMDJSON", func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				value, err := ParseFloat64(data)
				if err != nil {
					b.Fatal(err)
				}
				benchmarkFloatSink = value
			}
		})
		b.Run(text+"/strconv", func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				value, err := strconv.ParseFloat(text, 64)
				if err != nil {
					b.Fatal(err)
				}
				benchmarkFloatSink = value
			}
		})
		b.Run(text+"/encoding-json", func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if err := json.Unmarshal(data, &benchmarkFloatSink); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
