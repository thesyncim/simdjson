package simdjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
)

// stdlibCompactJSON is plain encoding/json.Marshal: the default byte format
// the compiled encoder targets, HTML escaping included.
func stdlibCompactJSON(t *testing.T, v any) ([]byte, error) {
	t.Helper()
	return json.Marshal(v)
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
		C chan int `json:"c"`
	}
	if _, err := Marshal(&unsupported{}); err == nil {
		t.Fatal("chan field accepted")
	}
}

func TestEncoderAppendJSONReusesBuffer(t *testing.T) {
	encoder, err := CompileEncoder[typedTestRecord](EncoderOptions{})
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

type mapKey string

type mapDocument struct {
	Plain    map[string]int            `json:"plain"`
	Named    map[mapKey]string         `json:"named"`
	Nested   map[string]map[string]int `json:"nested"`
	Structs  map[string]typedTestRecord `json:"structs"`
	Slices   map[string][]int          `json:"slices"`
	Optional map[string]int            `json:"optional,omitempty"`
}

func TestMapsMatchStdlib(t *testing.T) {
	sources := []string{
		`{"plain":{"b":2,"a":1},"named":{"x":"y"},"nested":{"outer":{"inner":3}},"structs":{"r":{"id":1,"ok":true,"name":"n","scores":[1,2,3],"number":4}},"slices":{"s":[1,2,3]}}`,
		`{"plain":{},"named":null,"nested":{"empty":{}},"structs":{},"slices":{"empty":[]}}`,
		`{"plain":{"esc\"aped":1,"uni ✓":2}}`,
		`{"optional":{}}`,
		`{}`,
	}
	decoder, err := CompileDecoder[mapDocument](DecoderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, src := range sources {
		var got, want mapDocument
		gotErr := decoder.Decode([]byte(src), &got)
		wantErr := json.Unmarshal([]byte(src), &want)
		if (gotErr == nil) != (wantErr == nil) {
			t.Fatalf("%s: acceptance differs: simdjson=%v stdlib=%v", src, gotErr, wantErr)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s:\nsimdjson %#v\nstdlib   %#v", src, got, want)
		}

		gotJSON, gotErr := Marshal(&got)
		wantJSON, wantErr := stdlibCompactJSON(t, &want)
		if (gotErr == nil) != (wantErr == nil) {
			t.Fatalf("%s: encode acceptance differs: simdjson=%v stdlib=%v", src, gotErr, wantErr)
		}
		if !bytes.Equal(gotJSON, wantJSON) {
			t.Fatalf("%s:\nsimdjson %s\nstdlib   %s", src, gotJSON, wantJSON)
		}
	}
}

func TestMapDecodeMergesLikeStdlib(t *testing.T) {
	decoder, err := CompileDecoder[map[string]int](DecoderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{"keep": 1, "replace": 2}
	want := map[string]int{"keep": 1, "replace": 2}
	src := []byte(`{"replace":20,"new":30}`)
	if err := decoder.Decode(src, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(src, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("merge mismatch: simdjson %#v, stdlib %#v", got, want)
	}

	// Owned map keys must survive input mutation.
	input := []byte(`{"retained":7}`)
	owned := map[string]int(nil)
	if err := decoder.Decode(input, &owned); err != nil {
		t.Fatal(err)
	}
	for i := range input {
		input[i] = 'x'
	}
	if _, ok := owned["retained"]; !ok {
		t.Fatalf("map key aliases mutated input: %#v", owned)
	}

	// Slice values must not share backing arrays across entries.
	sliceDecoder, err := CompileDecoder[map[string][]int](DecoderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	shared := map[string][]int(nil)
	if err := sliceDecoder.Decode([]byte(`{"a":[1,2,3],"b":[4,5,6]}`), &shared); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(shared["a"], []int{1, 2, 3}) || !reflect.DeepEqual(shared["b"], []int{4, 5, 6}) {
		t.Fatalf("map slice values share storage: %#v", shared)
	}
}

func TestMapErrorsAndPaths(t *testing.T) {
	decoder, err := CompileDecoder[map[string]map[string]int](DecoderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var dst map[string]map[string]int
	decodeErr := decoder.Decode([]byte(`{"outer":{"inner":"nope"}}`), &dst)
	var typed *DecodeError
	if !errors.As(decodeErr, &typed) {
		t.Fatalf("error = %v, want *DecodeError", decodeErr)
	}
	if typed.Path != "outer.inner" {
		t.Fatalf("path = %q, want outer.inner", typed.Path)
	}

	if _, err := CompileDecoder[map[float64]string](DecoderOptions{}); err == nil {
		t.Fatal("float map keys accepted")
	}

	type withNaN struct {
		M map[string]float64 `json:"m"`
	}
	_, encodeErr := Marshal(&withNaN{M: map[string]float64{"bad": math.NaN()}})
	var enc *EncodeError
	if !errors.As(encodeErr, &enc) {
		t.Fatalf("encode error = %v, want *EncodeError", encodeErr)
	}
	if enc.Path != "m.bad" {
		t.Fatalf("encode path = %q, want m.bad", enc.Path)
	}
}

type anyDocument struct {
	Meta   any            `json:"meta"`
	Blob   map[string]any `json:"blob"`
	Items  []any          `json:"items"`
	Option any            `json:"option,omitempty"`
}

func TestAnyFieldsMatchStdlib(t *testing.T) {
	sources := []string{
		`{"meta":{"a":[1,2.5,{"deep":true}],"b":null},"blob":{"s":"x","n":-3e2},"items":[1,"two",false,null,{"k":"v"}]}`,
		`{"meta":null,"blob":{},"items":[]}`,
		`{"meta":"just a string","blob":{"nested":{"more":{"even":[{}]}}},"items":[[[1]]]}`,
		`{"meta":1e15,"blob":{"big":123456789012345678901234567890}}`,
		`{"option":null}`,
	}
	decoder, err := CompileDecoder[anyDocument](DecoderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, src := range sources {
		var got, want anyDocument
		gotErr := decoder.Decode([]byte(src), &got)
		wantErr := json.Unmarshal([]byte(src), &want)
		if (gotErr == nil) != (wantErr == nil) {
			t.Fatalf("%s: acceptance differs: simdjson=%v stdlib=%v", src, gotErr, wantErr)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s:\nsimdjson %#v\nstdlib   %#v", src, got, want)
		}
		if gotErr != nil {
			continue
		}
		gotJSON, gotEncErr := Marshal(&got)
		wantJSON, wantEncErr := stdlibCompactJSON(t, &want)
		if (gotEncErr == nil) != (wantEncErr == nil) {
			t.Fatalf("%s: encode acceptance differs: simdjson=%v stdlib=%v", src, gotEncErr, wantEncErr)
		}
		if !bytes.Equal(gotJSON, wantJSON) {
			t.Fatalf("%s:\nsimdjson %s\nstdlib   %s", src, gotJSON, wantJSON)
		}
	}
}

func TestAnyEncodeConcreteTypes(t *testing.T) {
	type custom struct {
		N int `json:"n"`
	}
	values := []anyDocument{
		{Meta: int(7), Blob: map[string]any{"i32": int32(-5), "u": uint16(9)}, Items: []any{int8(1), float32(2.5)}},
		{Meta: custom{N: 3}, Items: []any{&custom{N: 4}, map[string]int{"z": 1, "a": 2}}},
		{Meta: []string{"x", "y"}, Blob: map[string]any{"deep": []any{map[string]any{"k": json.Number("5.5")}}}},
		{Meta: [2]int{1, 2}},
	}
	for _, value := range values {
		want, wantErr := stdlibCompactJSON(t, &value)
		got, gotErr := Marshal(&value)
		if (gotErr == nil) != (wantErr == nil) {
			t.Fatalf("%#v: acceptance differs: simdjson=%v stdlib=%v", value, gotErr, wantErr)
		}
		if gotErr == nil && !bytes.Equal(got, want) {
			t.Fatalf("%#v:\nsimdjson %s\nstdlib   %s", value, got, want)
		}
	}

	unsupported := anyDocument{Meta: make(chan int)}
	if _, err := Marshal(&unsupported); err == nil {
		t.Fatal("chan inside any accepted")
	}
}

type bytesDocument struct {
	Data   []byte            `json:"data"`
	Named  namedBlob         `json:"named"`
	Map    map[string][]byte `json:"map"`
	Option []byte            `json:"option,omitempty"`
}

type namedBlob []byte

func TestByteSlicesMatchStdlib(t *testing.T) {
	sources := []string{
		`{"data":"aGVsbG8gd29ybGQ=","named":"AQID","map":{"k":"eA=="}}`,
		`{"data":"","named":null}`,
		`{"data":"aGk="}`,
		`{"data":"aGk="}`,
		`{"data":"!!!invalid!!!"}`,
		`{"data":123}`,
	}
	decoder, err := CompileDecoder[bytesDocument](DecoderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, src := range sources {
		var got, want bytesDocument
		gotErr := decoder.Decode([]byte(src), &got)
		wantErr := json.Unmarshal([]byte(src), &want)
		if (gotErr == nil) != (wantErr == nil) {
			t.Fatalf("%s: acceptance differs: simdjson=%v stdlib=%v", src, gotErr, wantErr)
		}
		if gotErr != nil {
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s:\nsimdjson %#v\nstdlib   %#v", src, got, want)
		}
		gotJSON, gotEncErr := Marshal(&got)
		wantJSON, wantEncErr := stdlibCompactJSON(t, &want)
		if gotEncErr != nil || wantEncErr != nil {
			t.Fatalf("%s: encode errors: simdjson=%v stdlib=%v", src, gotEncErr, wantEncErr)
		}
		if !bytes.Equal(gotJSON, wantJSON) {
			t.Fatalf("%s:\nsimdjson %s\nstdlib   %s", src, gotJSON, wantJSON)
		}
	}

	// Byte-slice capacity is reused across decodes.
	reuse := bytesDocument{Data: make([]byte, 0, 64)}
	base := &reuse.Data[:1][0]
	if err := decoder.Decode([]byte(`{"data":"aGVsbG8="}`), &reuse); err != nil {
		t.Fatal(err)
	}
	if string(reuse.Data) != "hello" {
		t.Fatalf("decoded bytes = %q", reuse.Data)
	}
	if &reuse.Data[0] != base {
		t.Fatal("byte slice capacity was not reused")
	}
}

type quotedDocument struct {
	I   int     `json:"i,string"`
	I8  int8    `json:"i8,string"`
	U   uint32  `json:"u,string"`
	F   float64 `json:"f,string"`
	B   bool    `json:"b,string"`
	S   string  `json:"s,string"`
	N   json.Number `json:"n,string"`
	Ptr *int    `json:"ptr,string"` // stdlib ignores the option here
}

func TestStringTagOptionMatchesStdlib(t *testing.T) {
	one := 1
	// Encode side.
	values := []quotedDocument{
		{I: -42, I8: 7, U: 9, F: 2.5, B: true, S: `quo"ted <&>`, N: json.Number("5.5"), Ptr: &one},
		{},
		{F: 1e21, S: ""},
	}
	for _, value := range values {
		want, wantErr := stdlibCompactJSON(t, &value)
		got, gotErr := Marshal(&value)
		if (gotErr == nil) != (wantErr == nil) {
			t.Fatalf("%#v: encode acceptance differs: simdjson=%v stdlib=%v", value, gotErr, wantErr)
		}
		if gotErr == nil && !bytes.Equal(got, want) {
			t.Fatalf("%#v:\nsimdjson %s\nstdlib   %s", value, got, want)
		}
	}

	// Decode side, including malformed corners.
	decoder, err := CompileDecoder[quotedDocument](DecoderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	sources := []string{
		`{"i":"-42","i8":"7","u":"9","f":"2.5","b":"true","s":"\"hi\"","n":"5.5"}`,
		`{"i":null,"s":null}`,
		`{"i":42}`,
		`{"i":"nope"}`,
		`{"i":"42 "}`,
		`{"i8":"300"}`,
		`{"b":"maybe"}`,
		`{"s":"unquoted"}`,
		`{"f":"1e999"}`,
		`{"n":"not-a-number"}`,
		`{"i":""}`,
		`{"ptr":"1"}`,
		`{"ptr":null}`,
		`{"ptr":1}`,
	}
	for _, src := range sources {
		var got, want quotedDocument
		gotErr := decoder.Decode([]byte(src), &got)
		wantErr := json.Unmarshal([]byte(src), &want)
		if (gotErr == nil) != (wantErr == nil) {
			t.Fatalf("%s: acceptance differs: simdjson=%v stdlib=%v", src, gotErr, wantErr)
		}
		if gotErr == nil && !reflect.DeepEqual(got, want) {
			t.Fatalf("%s:\nsimdjson %#v\nstdlib   %#v", src, got, want)
		}
	}

	// Round trip.
	original := quotedDocument{I: 3, F: -0.25, B: true, S: "wrap", N: json.Number("8")}
	encoded, err := Marshal(&original)
	if err != nil {
		t.Fatal(err)
	}
	var decoded quotedDocument
	if err := decoder.Decode(encoded, &decoded); err != nil {
		t.Fatalf("round trip decode of %s: %v", encoded, err)
	}
	if !reflect.DeepEqual(decoded, original) {
		t.Fatalf("round trip mismatch: %#v vs %#v", decoded, original)
	}
}

func TestDisableHTMLEscaping(t *testing.T) {
	type doc struct {
		S string `json:"s"`
	}
	value := doc{S: `<a href="x">&  </a>`}

	var buffer bytes.Buffer
	stdEncoder := json.NewEncoder(&buffer)
	stdEncoder.SetEscapeHTML(false)
	if err := stdEncoder.Encode(&value); err != nil {
		t.Fatal(err)
	}
	want := bytes.TrimSuffix(buffer.Bytes(), []byte("\n"))

	encoder, err := CompileEncoder[doc](EncoderOptions{DisableHTMLEscaping: true})
	if err != nil {
		t.Fatal(err)
	}
	got, err := encoder.AppendJSON(nil, &value)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("no-escape mode:\nsimdjson %s\nstdlib   %s", got, want)
	}
}

type embBase struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type embShadow struct {
	embBase
	Name string `json:"name"` // shadows the embedded name
}

type embPointer struct {
	*embBase
	Extra string `json:"extra"`
}

type embTagged struct {
	embBase `json:"base"` // tagged: nested object, not flattened
}

type embConflictA struct{ Same int }
type embConflictB struct{ Same int }
type embConflict struct {
	embConflictA
	embConflictB // same depth, same name: both dropped
	Z int `json:"z"`
}

type embInt int

type embNonStruct struct {
	embInt // named by its type
	V int  `json:"v"`
}

type embUnexported struct {
	hidden
	Top int `json:"top"`
}

type hidden struct {
	Inner string `json:"inner"`
}

type embDeep struct {
	embMid
	Own int `json:"own"`
}

type embMid struct {
	embBase
	Mid int `json:"mid"`
}

func TestEmbeddedFieldsMatchStdlib(t *testing.T) {
	type roundTrip func(src string) (gotErr, wantErr error, got, want any)
	cases := []struct {
		name string
		run  roundTrip
	}{
		{"value embedding", differential[embMid](t, `{"id":1,"name":"n","mid":2}`)},
		{"shadowing", differential[embShadow](t, `{"id":3,"name":"outer"}`)},
		{"pointer embedding", differential[embPointer](t, `{"id":4,"name":"p","extra":"e"}`)},
		{"pointer embedding absent", differential[embPointer](t, `{"extra":"only"}`)},
		{"tagged anonymous", differential[embTagged](t, `{"base":{"id":5,"name":"tag"}}`)},
		{"same depth conflict", differential[embConflict](t, `{"Same":9,"z":1}`)},
		{"embedded scalar", differential[embNonStruct](t, `{"embInt":7,"v":8}`)},
		{"unexported embedded struct", differential[embUnexported](t, `{"inner":"i","top":9}`)},
		{"deep nesting", differential[embDeep](t, `{"id":1,"name":"d","mid":2,"own":3}`)},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			gotErr, wantErr, got, want := testCase.run("")
			if (gotErr == nil) != (wantErr == nil) {
				t.Fatalf("acceptance differs: simdjson=%v stdlib=%v", gotErr, wantErr)
			}
			if gotErr == nil && !reflect.DeepEqual(got, want) {
				t.Fatalf("decoded differs:\nsimdjson %#v\nstdlib   %#v", got, want)
			}
		})
	}
}

// differential returns a closure decoding src into T with both libraries and
// then re-encoding, comparing bytes.
func differential[T any](t *testing.T, src string) func(string) (error, error, any, any) {
	return func(string) (error, error, any, any) {
		var got, want T
		gotErr := Unmarshal([]byte(src), &got)
		wantErr := json.Unmarshal([]byte(src), &want)
		if gotErr == nil && wantErr == nil {
			gotJSON, gotEncErr := Marshal(&got)
			wantJSON, wantEncErr := stdlibCompactJSON(t, &want)
			if (gotEncErr == nil) != (wantEncErr == nil) {
				t.Fatalf("%s: encode acceptance differs: simdjson=%v stdlib=%v", src, gotEncErr, wantEncErr)
			}
			if gotEncErr == nil && !bytes.Equal(gotJSON, wantJSON) {
				t.Fatalf("%s:\nsimdjson %s\nstdlib   %s", src, gotJSON, wantJSON)
			}
		}
		return gotErr, wantErr, got, want
	}
}

func TestEmbeddedPointerEncodeNil(t *testing.T) {
	value := embPointer{Extra: "only"}
	want, err := stdlibCompactJSON(t, &value)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Marshal(&value)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("nil embedded pointer:\nsimdjson %s\nstdlib   %s", got, want)
	}
}

type textKey struct{ A, B int }

func (k textKey) MarshalText() ([]byte, error) { return fmt.Appendf(nil, "%d-%d", k.A, k.B), nil }
func (k *textKey) UnmarshalText(text []byte) error {
	_, err := fmt.Sscanf(string(text), "%d-%d", &k.A, &k.B)
	return err
}

type stringTextKey string

func (k stringTextKey) MarshalText() ([]byte, error) { return []byte("SHOULD-NOT-BE-USED"), nil }
func (k *stringTextKey) UnmarshalText(text []byte) error {
	*k = stringTextKey("text:" + string(text))
	return nil
}

type mapKeyDocument struct {
	Ints   map[int]string       `json:"ints"`
	Uints  map[uint8]int        `json:"uints"`
	Texts  map[textKey]int      `json:"texts"`
	Asym   map[stringTextKey]int `json:"asym"`
	Named  map[int32]bool       `json:"named"`
}

func TestNonStringMapKeysMatchStdlib(t *testing.T) {
	// Encode: string kinds beat TextMarshaler; ints format base 10; text keys
	// marshal; everything sorts by rendered name.
	value := mapKeyDocument{
		Ints:  map[int]string{-3: "a", 10: "b", 2: "c"},
		Uints: map[uint8]int{255: 1, 0: 2},
		Texts: map[textKey]int{{A: 1, B: 2}: 3, {A: 0, B: 0}: 4},
		Asym:  map[stringTextKey]int{"raw": 5},
		Named: map[int32]bool{-9: true},
	}
	want, wantErr := stdlibCompactJSON(t, &value)
	got, gotErr := Marshal(&value)
	if (gotErr == nil) != (wantErr == nil) {
		t.Fatalf("encode acceptance differs: simdjson=%v stdlib=%v", gotErr, wantErr)
	}
	if gotErr == nil && !bytes.Equal(got, want) {
		t.Fatalf("encode:\nsimdjson %s\nstdlib   %s", got, want)
	}

	decoder, err := CompileDecoder[mapKeyDocument](DecoderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	sources := []string{
		`{"ints":{"-3":"a","10":"b"},"uints":{"255":1},"texts":{"7-8":9},"asym":{"k":1},"named":{"-9":true}}`,
		`{"ints":{"not-a-number":"x"}}`,
		`{"uints":{"-1":1}}`,
		`{"uints":{"256":1}}`,
		`{"texts":{"badkey":1}}`,
		`{"ints":{"1.5":"x"}}`,
	}
	for _, src := range sources {
		var got, want mapKeyDocument
		gotErr := decoder.Decode([]byte(src), &got)
		wantErr := json.Unmarshal([]byte(src), &want)
		if (gotErr == nil) != (wantErr == nil) {
			t.Fatalf("%s: acceptance differs: simdjson=%v stdlib=%v", src, gotErr, wantErr)
		}
		if gotErr == nil && !reflect.DeepEqual(got, want) {
			t.Fatalf("%s:\nsimdjson %#v\nstdlib   %#v", src, got, want)
		}
	}
}

type speaker interface{ Speak() string }

type dog struct {
	Sound string `json:"sound"`
}

func (d *dog) Speak() string { return d.Sound }

type ifaceDocument struct {
	Animal speaker `json:"animal"`
	Blob   any     `json:"blob"`
	Option speaker `json:"option,omitempty"`
}

func TestNonEmptyInterfacesMatchStdlib(t *testing.T) {
	// Encode: concrete dynamic dispatch, nil as null, omitempty.
	values := []ifaceDocument{
		{Animal: &dog{Sound: "woof"}, Blob: map[string]any{"k": 1.5}},
		{},
	}
	for _, value := range values {
		want, wantErr := stdlibCompactJSON(t, &value)
		got, gotErr := Marshal(&value)
		if (gotErr == nil) != (wantErr == nil) {
			t.Fatalf("%#v: encode acceptance differs: simdjson=%v stdlib=%v", value, gotErr, wantErr)
		}
		if gotErr == nil && !bytes.Equal(got, want) {
			t.Fatalf("%#v:\nsimdjson %s\nstdlib   %s", value, got, want)
		}
	}

	// Decode: null clears; a held non-nil pointer is decoded into; anything
	// else fails, all matching encoding/json.
	decoder, err := CompileDecoder[ifaceDocument](DecoderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	sources := []string{
		`{"animal":null}`,
		`{"animal":{"sound":"nope"}}`,
	}
	for _, src := range sources {
		got := ifaceDocument{Animal: &dog{Sound: "old"}}
		want := ifaceDocument{Animal: &dog{Sound: "old"}}
		gotErr := decoder.Decode([]byte(src), &got)
		wantErr := json.Unmarshal([]byte(src), &want)
		if (gotErr == nil) != (wantErr == nil) {
			t.Fatalf("%s: acceptance differs: simdjson=%v stdlib=%v", src, gotErr, wantErr)
		}
		if gotErr == nil && !reflect.DeepEqual(got, want) {
			t.Fatalf("%s:\nsimdjson %#v\nstdlib   %#v", src, got.Animal, want.Animal)
		}
	}

	// Empty interface holding a pointer is decoded into, keeping identity.
	target := &dog{Sound: "before"}
	holder := struct {
		Blob any `json:"blob"`
	}{Blob: target}
	if err := Unmarshal([]byte(`{"blob":{"sound":"after"}}`), &holder); err != nil {
		t.Fatal(err)
	}
	if target.Sound != "after" {
		t.Fatalf("pointer held by interface not decoded into: %#v", target)
	}
	if holder.Blob != any(target) {
		t.Fatalf("interface identity lost: %#v", holder.Blob)
	}

	// Fresh interface without a pointer errors like stdlib.
	var fresh ifaceDocument
	gotErr := decoder.Decode([]byte(`{"animal":{"sound":"x"}}`), &fresh)
	var want ifaceDocument
	wantErr := json.Unmarshal([]byte(`{"animal":{"sound":"x"}}`), &want)
	if (gotErr == nil) != (wantErr == nil) {
		t.Fatalf("fresh non-empty interface: simdjson=%v stdlib=%v", gotErr, wantErr)
	}
}
