package simdjson

import (
	"encoding/json"
	"strconv"
	"testing"
	"unsafe"
)

var parsedDigitsSink uint64
var benchmarkFloatSink float64

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
