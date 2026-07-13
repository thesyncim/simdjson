package simd

import (
	"math"
	"strconv"
	"testing"
)

func TestAppendFloat64MatchesJSON(t *testing.T) {
	values := []float64{
		0, math.Copysign(0, -1), 1, -1, 0.1, -0.1,
		1e-7, 1e-6, 1e20, 1e21, math.SmallestNonzeroFloat64,
		math.MaxFloat64, 43.508331000000055, -59.975554999999986,
	}
	state := uint64(0x9e3779b97f4a7c15)
	for range 250_000 {
		state ^= state << 13
		state ^= state >> 7
		state ^= state << 17
		values = append(values, math.Float64frombits(state))
	}
	for _, value := range values {
		got, ok := AppendFloat64(nil, value)
		if math.IsNaN(value) || math.IsInf(value, 0) {
			if ok || got != nil {
				t.Fatalf("AppendFloat64(%v) = %q, %v, want invalid", value, got, ok)
			}
			continue
		}
		want := appendJSONFloat(nil, value, 64)
		if !ok || string(got) != string(want) {
			t.Fatalf("AppendFloat64(%v [%#x]) = %q, %v, want %q", value, math.Float64bits(value), got, ok, want)
		}
	}
}

func TestAppendFloat32MatchesJSON(t *testing.T) {
	values := []float32{
		0, float32(math.Copysign(0, -1)), 1, -1, 0.1, -0.1,
		1e-7, 1e-6, 1e20, 1e21, math.SmallestNonzeroFloat32,
		math.MaxFloat32,
	}
	state := uint32(0x9e3779b9)
	for range 250_000 {
		state ^= state << 13
		state ^= state >> 17
		state ^= state << 5
		values = append(values, math.Float32frombits(state))
	}
	for _, value := range values {
		got, ok := AppendFloat32(nil, value)
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			if ok || got != nil {
				t.Fatalf("AppendFloat32(%v) = %q, %v, want invalid", value, got, ok)
			}
			continue
		}
		want := appendJSONFloat(nil, float64(value), 32)
		if !ok || string(got) != string(want) {
			t.Fatalf("AppendFloat32(%v [%#x]) = %q, %v, want %q", value, math.Float32bits(value), got, ok, want)
		}
	}
}

func TestAppendFloatInvalidPreservesDestination(t *testing.T) {
	for _, value := range []float64{math.NaN(), math.Inf(-1), math.Inf(1)} {
		dst := []byte("prefix")
		got, ok := AppendFloat64(dst, value)
		if ok || string(got) != "prefix" {
			t.Fatalf("AppendFloat64(%v) = %q, %v", value, got, ok)
		}
	}
}

func appendJSONFloat(dst []byte, value float64, bitSize int) []byte {
	format := byte('f')
	if bitSize == 32 {
		abs := float32(math.Abs(value))
		if abs != 0 && (abs < float32(1e-6) || abs >= float32(1e21)) {
			format = 'e'
		}
	} else {
		abs := math.Abs(value)
		if abs != 0 && (abs < 1e-6 || abs >= 1e21) {
			format = 'e'
		}
	}
	dst = strconv.AppendFloat(dst, value, format, -1, bitSize)
	if format == 'e' {
		if n := len(dst); n >= 4 && dst[n-4] == 'e' && dst[n-3] == '-' && dst[n-2] == '0' {
			dst[n-2] = dst[n-1]
			dst = dst[:n-1]
		}
	}
	return dst
}

func BenchmarkAppendFloat64(b *testing.B) {
	values := [...]float64{43.508331000000055, -59.975554999999986, 1.2345678901234567, 1e-100}
	dst := make([]byte, 0, 32)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		dst, _ = AppendFloat64(dst[:0], values[i&3])
	}
	_ = dst
}

func BenchmarkStrconvAppendFloat64(b *testing.B) {
	values := [...]float64{43.508331000000055, -59.975554999999986, 1.2345678901234567, 1e-100}
	dst := make([]byte, 0, 32)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		dst = strconv.AppendFloat(dst[:0], values[i&3], 'g', -1, 64)
	}
	_ = dst
}
