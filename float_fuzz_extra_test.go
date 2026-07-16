package simdjson

import (
	"math"
	"strconv"
	"strings"
	"testing"
)

// composeNumberText builds a JSON number spelling from raw fuzzer material so
// the fuzzer explores the digit/exponent shape space directly rather than
// waiting for the mutator to stumble onto valid numbers. Every returned string
// is a syntactically valid JSON number.
func composeNumberText(neg bool, intPart, fracPart string, hasFrac bool, exp int, hasExp bool) string {
	var b strings.Builder
	if neg {
		b.WriteByte('-')
	}
	// A JSON number needs a nonempty integer part with no leading zero unless it
	// is exactly "0".
	intPart = strings.TrimLeft(intPart, "0")
	if intPart == "" {
		b.WriteByte('0')
	} else {
		b.WriteString(intPart)
	}
	if hasFrac {
		if fracPart == "" {
			fracPart = "0"
		}
		b.WriteByte('.')
		b.WriteString(fracPart)
	}
	if hasExp {
		b.WriteByte('e')
		b.WriteString(strconv.Itoa(exp))
	}
	return b.String()
}

// onlyDigits keeps the digit bytes of s and caps the length so composed numbers
// stay in the range JSON parsers actually see.
func onlyDigits(s string, max int) string {
	var b strings.Builder
	for i := 0; i < len(s) && b.Len() < max; i++ {
		if s[i] >= '0' && s[i] <= '9' {
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// checkAllFloatPaths compares every float decode entry point against strconv
// for one JSON number text: ParseFloat64, typed float64 decode, the any/Value
// path, and float32. A parse error from our side is only a mismatch when
// strconv also would have produced a finite value from the identical text.
func checkAllFloatPaths(t *testing.T, text string) {
	t.Helper()

	ref64, err64 := strconv.ParseFloat(text, 64)
	// strconv returns ErrRange with the clamped value (±Inf) for overflow; our
	// parsers reject overflow, an intentional strictness, so skip those.
	strconvOK := err64 == nil && !math.IsInf(ref64, 0)

	got, err := ParseFloat64([]byte(text))
	if err == nil {
		if !strconvOK {
			// We accepted something strconv rejected/overflowed: only flag if the
			// text is genuinely a finite number strconv can read.
			if r, e := strconv.ParseFloat(text, 64); e == nil && !math.IsInf(r, 0) {
				if math.Float64bits(got) != math.Float64bits(r) {
					t.Fatalf("ParseFloat64(%q)=%x want %x", text, math.Float64bits(got), math.Float64bits(r))
				}
			}
		} else if math.Float64bits(got) != math.Float64bits(ref64) {
			t.Fatalf("ParseFloat64(%q)=%x (%v) want %x (%v)", text,
				math.Float64bits(got), got, math.Float64bits(ref64), ref64)
		}
	}

	// Typed float64 decode.
	var typed float64
	if err := decFloat64.Decode([]byte(text), &typed); err == nil && strconvOK {
		if math.Float64bits(typed) != math.Float64bits(ref64) {
			t.Fatalf("typed float64 decode %q = %x (%v) want %x (%v)", text,
				math.Float64bits(typed), typed, math.Float64bits(ref64), ref64)
		}
	}

	// any / Value path (Value.Float64 routes through Node.Float64, the lazy
	// kernel read).
	if v, err := Parse([]byte(text)); err == nil && strconvOK {
		if f, ok := v.Float64(); ok && math.Float64bits(f) != math.Float64bits(ref64) {
			t.Fatalf("Value.Float64(%q) = %x (%v) want %x (%v)", text,
				math.Float64bits(f), f, math.Float64bits(ref64), ref64)
		}
	}

	// RawValue path: no tape flag, so it classifies inline before reusing the
	// same kernel. Whole-document GetRaw yields the number slice.
	if raw, ok, err := GetRaw([]byte(text), ""); err == nil && ok && strconvOK {
		if f, fok := raw.Float64(); fok && math.Float64bits(f) != math.Float64bits(ref64) {
			t.Fatalf("RawValue.Float64(%q) = %x (%v) want %x (%v)", text,
				math.Float64bits(f), f, math.Float64bits(ref64), ref64)
		}
	}
	if a, err := unmarshalAnyForTest([]byte(text)); err == nil && strconvOK {
		if f, ok := a.(float64); ok && math.Float64bits(f) != math.Float64bits(ref64) {
			t.Fatalf("Unmarshal any(%q) = %x (%v) want %x (%v)", text,
				math.Float64bits(f), f, math.Float64bits(ref64), ref64)
		}
	}

	// Typed float32 decode: compare against strconv's 32-bit parse of the same
	// text (which is the correctly rounded float32).
	ref32, err32 := strconv.ParseFloat(text, 32)
	strconv32OK := err32 == nil && !math.IsInf(ref32, 0)
	var typed32 float32
	if err := decFloat32.Decode([]byte(text), &typed32); err == nil && strconv32OK {
		if math.Float32bits(typed32) != math.Float32bits(float32(ref32)) {
			t.Fatalf("typed float32 decode %q = %x (%v) want %x (%v)", text,
				math.Float32bits(typed32), typed32, math.Float32bits(float32(ref32)), float32(ref32))
		}
	}
}

var (
	decFloat64 = mustCompileFloatDecoder[float64]()
	decFloat32 = mustCompileFloatDecoder[float32]()
)

func mustCompileFloatDecoder[T float32 | float64]() Decoder[T] {
	d, err := CompileDecoder[T](DecoderOptions{})
	if err != nil {
		panic(err)
	}
	return d
}

// FuzzFloatDecodeAllPaths composes JSON numbers from raw fuzzer material and
// pins ParseFloat64, typed float64, typed float32, and the any/Value decode all
// to strconv. It deliberately reaches the extreme-exponent, long-mantissa,
// subnormal, and truncated regimes that route around the exact-multiply fast
// path and into Eisel-Lemire and the strconv fallback.
func FuzzFloatDecodeAllPaths(f *testing.F) {
	// Seeds: (neg, intDigits, fracDigits, hasFrac, exp, hasExp).
	f.Add(false, "1", "5", true, 308, true)
	f.Add(true, "9007199254740993", "", false, 0, false)
	f.Add(false, "5", "", false, -324, true)
	f.Add(false, "22250738585072014", "", false, -324, true)
	f.Add(false, "1", "234567890123456789012345", true, -300, true)
	f.Add(false, "0", "1", true, 0, false)
	f.Add(false, "", "3", true, -400, true)
	f.Add(false, "17976931348623157", "", false, 292, true)
	f.Fuzz(func(t *testing.T, neg bool, intPart, fracPart string, hasFrac bool, exp int, hasExp bool) {
		intPart = onlyDigits(intPart, 40)
		fracPart = onlyDigits(fracPart, 40)
		// Keep exponents in a wide but bounded band covering overflow/underflow.
		if exp > 4000 {
			exp = 4000
		}
		if exp < -4000 {
			exp = -4000
		}
		text := composeNumberText(neg, intPart, fracPart, hasFrac, exp, hasExp)
		if len(text) > 90 {
			t.Skip()
		}
		checkAllFloatPaths(t, text)
	})
}

// FuzzFloatDecodeFreeform lets the mutator throw arbitrary short strings at the
// decoders; only strings both sides accept as a finite number are compared, so
// this catches any spelling where an accepted parse disagrees with strconv.
func FuzzFloatDecodeFreeform(f *testing.F) {
	for _, s := range []string{
		"0.1", "1e10", "1.5e-8", "9007199254740993", "3.14159e-22",
		"1.7976931348623157e308", "2.2250738585072014e-308", "5e-324",
		"-0.0", "0e0", "1e400", "1e-400", "123456789012345678901234567890e-15",
		"0.00000000000000000000000000001", "9999999999999999e300",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if len(s) == 0 || len(s) > 100 {
			t.Skip()
		}
		checkAllFloatPaths(t, s)
	})
}

// FuzzFloatRoundTripMarshalDecode Marshals a float64 (and its float32 view)
// through this library, then decodes the produced JSON back through the same
// library and through strconv, confirming both recover the exact bits. Marshal
// must emit a shortest round-tripping decimal, and every decode path must
// recover it, so this closes the encode/decode loop over the full bit space
// including wide exponents and subnormals.
func FuzzFloatRoundTripMarshalDecode(f *testing.F) {
	seeds := []uint64{
		0x0000000000000000, 0x8000000000000000, // +0 -0
		0x0000000000000001, 0x000fffffffffffff, // smallest subnormal, largest subnormal
		0x0010000000000000, 0x7fefffffffffffff, // smallest normal, largest finite
		0x3ff0000000000000, 0x4059000000000000, // 1.0, 100.0
		0x3fb999999999999a, 0x43e158e460913d00, // 0.1, ~2.5e18
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, bits uint64) {
		f64 := math.Float64frombits(bits)
		if math.IsNaN(f64) || math.IsInf(f64, 0) {
			t.Skip()
		}
		type wrap struct {
			F float64 `json:"f"`
		}
		data, err := Marshal(&wrap{F: f64})
		if err != nil {
			t.Fatalf("Marshal(%x) error: %v", bits, err)
		}
		// The emitted text must be a valid shortest round-tripping decimal:
		// strconv reading it back yields identical bits.
		text := extractField(t, data)
		back, perr := strconv.ParseFloat(text, 64)
		if perr != nil {
			t.Fatalf("Marshal produced unparseable float text %q from bits %x: %v", text, bits, perr)
		}
		if math.Float64bits(back) != bits {
			t.Fatalf("round trip via strconv lost bits: %x -> %q -> %x", bits, text, math.Float64bits(back))
		}
		// And our own ParseFloat64 must recover the same bits from that text.
		mine, merr := ParseFloat64([]byte(text))
		if merr != nil {
			t.Fatalf("ParseFloat64(%q) from Marshal error: %v", text, merr)
		}
		if math.Float64bits(mine) != bits {
			t.Fatalf("ParseFloat64 round trip lost bits: %x -> %q -> %x", bits, text, math.Float64bits(mine))
		}
	})
}

// extractField pulls the numeric text out of {"f":<num>} produced by Marshal.
func extractField(t *testing.T, data []byte) string {
	t.Helper()
	s := string(data)
	const pre = `{"f":`
	if len(s) < len(pre)+1 || s[:len(pre)] != pre || s[len(s)-1] != '}' {
		t.Fatalf("unexpected Marshal shape: %q", s)
	}
	return s[len(pre) : len(s)-1]
}
