package simdjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
)

// stdlibCompactJSON encodes v like encoding/json with HTML escaping disabled,
// which is the byte format the compiled encoder targets.
func stdlibCompactJSON(t *testing.T, v any) ([]byte, error) {
	t.Helper()
	var buffer bytes.Buffer
	stdEncoder := json.NewEncoder(&buffer)
	stdEncoder.SetEscapeHTML(false)
	if err := stdEncoder.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n")), nil
}

type encodeOmitEmpty struct {
	Bool    bool        `json:"bool,omitempty"`
	Int     int         `json:"int,omitempty"`
	Uint    uint8       `json:"uint,omitempty"`
	Float   float64     `json:"float,omitempty"`
	Text    string      `json:"text,omitempty"`
	Number  json.Number `json:"number,omitempty"`
	Slice   []int       `json:"slice,omitempty"`
	Pointer *int        `json:"pointer,omitempty"`
	Keep    int         `json:"keep"`
}

type encodeEdge struct {
	Dash    int     `json:"-,"`
	Renamed float32 `json:"float 32"`
	Escaped string  `json:"escaped"`
}

func TestEncoderMatchesStdlib(t *testing.T) {
	one := 1
	values := []any{
		&typedTestRecord{ID: 42, OK: true, Name: "plain", Scores: [3]float64{1, 2.5, -3e4}, Number: json.Number("12.5e3")},
		&typedTestRecord{Name: "esc \" \\ \n \r \t \b \f \x01 <&>     héllo"},
		&typedTestRecord{Name: string([]byte{'b', 'a', 'd', 0xFF, 0xFE, 'x'})},
		&typedTestDocument{},
		&typedTestDocument{Items: []typedTestRecord{}, Count: 7},
		&typedTestDocument{Items: []typedTestRecord{{ID: 1}, {ID: 2, Number: json.Number("-0.5")}}, Next: &typedTestRecord{ID: 3}},
		&encodeOmitEmpty{},
		&encodeOmitEmpty{Bool: true, Int: -1, Uint: 2, Float: 0.5, Text: "x", Number: json.Number("9"), Slice: []int{0}, Pointer: &one},
		&encodeOmitEmpty{Float: math.Copysign(0, -1)}, // negative zero is empty for omitempty
		&encodeEdge{Dash: 1, Renamed: 2.5, Escaped: "ok"},
		&typedEdgeValue{ID: 5, Long: "long", Values: []int{1, 2, 3}, Fixed: [3]typedEdgeInt{7, 8, 9}},
	}
	for _, value := range values {
		want, wantErr := stdlibCompactJSON(t, value)
		got, gotErr := marshalAnyForTest(t, value)
		if (gotErr == nil) != (wantErr == nil) {
			t.Fatalf("%#v: acceptance differs: simdjson=%v stdlib=%v", value, gotErr, wantErr)
		}
		if gotErr != nil {
			continue
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("%#v:\nsimdjson %s\nstdlib   %s", value, got, want)
		}
	}
}

// marshalAnyForTest dispatches the concrete pointer types used by the
// differential tests through the generic Marshal entry point.
func marshalAnyForTest(t *testing.T, value any) ([]byte, error) {
	t.Helper()
	switch v := value.(type) {
	case *typedTestRecord:
		return Marshal(v)
	case *typedTestDocument:
		return Marshal(v)
	case *encodeOmitEmpty:
		return Marshal(v)
	case *encodeEdge:
		return Marshal(v)
	case *typedEdgeValue:
		return Marshal(v)
	default:
		t.Fatalf("unsupported test type %T", value)
		return nil, nil
	}
}

func TestEncoderFloatFormats(t *testing.T) {
	floats := []float64{
		0, math.Copysign(0, -1), 1, -1, 0.5, 1e-6, 9.9e-7, 1e20, 1e21, 1.5e22,
		-2.75e-9, 123456789.123456789, math.MaxFloat64, math.SmallestNonzeroFloat64,
		3.14159265358979, 1e6, 2e8,
	}
	type wrapper struct {
		F64 float64 `json:"f64"`
		F32 float32 `json:"f32"`
	}
	for _, f := range floats {
		value := wrapper{F64: f, F32: float32(f)}
		want, wantErr := stdlibCompactJSON(t, &value)
		got, gotErr := Marshal(&value)
		if (gotErr == nil) != (wantErr == nil) {
			t.Fatalf("float %g: acceptance differs: simdjson=%v stdlib=%v", f, gotErr, wantErr)
		}
		if gotErr == nil && !bytes.Equal(got, want) {
			t.Fatalf("float %g: simdjson %s, stdlib %s", f, got, want)
		}
	}
}

func TestEncoderErrors(t *testing.T) {
	type inner struct {
		F float64 `json:"f"`
	}
	type outer struct {
		Items []inner `json:"items"`
	}
	badFloat := outer{Items: []inner{{F: 1}, {F: math.NaN()}}}
	_, err := Marshal(&badFloat)
	var encodeErr *EncodeError
	if !errors.As(err, &encodeErr) {
		t.Fatalf("NaN error = %v, want *EncodeError", err)
	}
	if encodeErr.Path != "items[1].f" {
		t.Fatalf("NaN path = %q, want items[1].f", encodeErr.Path)
	}

	type badNumber struct {
		N json.Number `json:"n"`
	}
	if _, err := Marshal(&badNumber{N: json.Number("1e")}); err == nil {
		t.Fatal("invalid json.Number accepted")
	}
	got, err := Marshal(&badNumber{})
	if err != nil || string(got) != `{"n":0}` {
		t.Fatalf("empty json.Number = %s, %v; want {\"n\":0}", got, err)
	}

	type unsupported struct {
		M map[string]int `json:"m"`
	}
	if _, err := Marshal(&unsupported{}); err == nil {
		t.Fatal("map field accepted")
	}
}

func TestEncoderAppendJSONReusesBuffer(t *testing.T) {
	encoder, err := CompileEncoder[typedTestRecord]()
	if err != nil {
		t.Fatal(err)
	}
	value := typedTestRecord{ID: 9, OK: true, Name: "reuse", Scores: [3]float64{1, 2, 3}, Number: json.Number("4")}
	buffer := make([]byte, 0, 256)
	allocs := testing.AllocsPerRun(1000, func() {
		out, err := encoder.AppendJSON(buffer[:0], &value)
		if err != nil {
			panic(err)
		}
		buffer = out[:0]
	})
	if allocs != 0 {
		t.Fatalf("AppendJSON allocations = %v, want 0", allocs)
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	decoder, err := CompileDecoder[typedTestDocument](DecoderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	original := typedTestDocument{
		Items: []typedTestRecord{
			{ID: 1, OK: true, Name: "first   record", Scores: [3]float64{1.5, -2, 3e19}, Number: json.Number("42")},
			{ID: -2, Name: strings.Repeat("wide ascii payload ", 8), Number: json.Number("0")},
		},
		Count: 2,
		Next:  &typedTestRecord{ID: 3, Number: json.Number("-7.25")},
	}
	encoded, err := Marshal(&original)
	if err != nil {
		t.Fatal(err)
	}
	var decoded typedTestDocument
	if err := decoder.Decode(encoded, &decoded); err != nil {
		t.Fatalf("round trip decode of %s: %v", encoded, err)
	}
	if !reflect.DeepEqual(decoded, original) {
		t.Fatalf("round trip mismatch:\noriginal %#v\ndecoded  %#v", original, decoded)
	}
}

func FuzzEncoderMatchesStdlib(f *testing.F) {
	f.Add([]byte(`{"id":1,"ok":true,"name":"x","scores":[1,2.5,-3e4],"number":9}`))
	f.Add([]byte(`{"name":" 😀� <&> \t"}`))
	f.Add([]byte(`{"items":[{"id":1}],"count":2,"next":{"id":3}}`))
	decoder, err := CompileDecoder[typedTestDocument](DecoderOptions{})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, src []byte) {
		if len(src) > 1<<14 || !Valid(src) {
			return
		}
		var value typedTestDocument
		if err := decoder.Decode(src, &value); err != nil {
			return
		}
		got, gotErr := Marshal(&value)
		want, wantErr := stdlibCompactJSON(t, &value)
		if (gotErr == nil) != (wantErr == nil) {
			t.Fatalf("acceptance differs: simdjson=%v stdlib=%v", gotErr, wantErr)
		}
		if gotErr == nil && !bytes.Equal(got, want) {
			t.Fatalf("encoding differs:\nsimdjson %s\nstdlib   %s", got, want)
		}
	})
}

// TestEncoderRandomFloatsMatchStdlib hammers the float fast paths with a
// deterministic mix of exact decimals, integers, and raw bit patterns.
func TestEncoderRandomFloatsMatchStdlib(t *testing.T) {
	type wrapper struct {
		F64 float64 `json:"f64"`
		F32 float32 `json:"f32"`
	}
	state := uint64(0x9E3779B97F4A7C15)
	next := func() uint64 {
		state ^= state << 13
		state ^= state >> 7
		state ^= state << 17
		return state
	}
	for i := 0; i < 200000; i++ {
		var f float64
		switch i % 4 {
		case 0: // small exact decimals
			f = float64(int64(next()%2_000_000)-1_000_000) / 100
		case 1: // integers across the fast-path boundary
			f = float64(int64(next()%(1<<51)) - 1<<50)
		case 2: // arbitrary bit patterns (skip NaN/Inf)
			f = math.Float64frombits(next())
			if math.IsNaN(f) || math.IsInf(f, 0) {
				continue
			}
		default: // tenths near the scaled boundary
			f = float64(int64(next()%20_000_000_000)-10_000_000_000) / 10
		}
		value := wrapper{F64: f, F32: float32(f)}
		if math.IsInf(float64(value.F32), 0) {
			continue
		}
		want, err := stdlibCompactJSON(t, &value)
		if err != nil {
			continue
		}
		got, err := Marshal(&value)
		if err != nil {
			t.Fatalf("float %g (bits %#x): %v", f, math.Float64bits(f), err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("float %g (bits %#x):\nsimdjson %s\nstdlib   %s", f, math.Float64bits(f), got, want)
		}
	}
}
