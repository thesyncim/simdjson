package simdjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"unsafe"
)

type typedTestRecord struct {
	ID     int         `json:"id"`
	OK     bool        `json:"ok"`
	Name   string      `json:"name"`
	Scores [3]float64  `json:"scores"`
	Number json.Number `json:"number"`
}

func TestTypedDecoderCursorStaysCompact(t *testing.T) {
	if size := unsafe.Sizeof(decoderCursor{}); size > 64 {
		t.Fatalf("typed decoder cursor size = %d bytes, want <= 64", size)
	}
}

type typedTestDocument struct {
	Items []typedTestRecord `json:"items"`
	Count uint16            `json:"count"`
	Next  *typedTestRecord  `json:"next"`
}

type typedEdgePointer *typedEdgeValue

type typedEdgeInt int

type typedEdgeValue struct {
	ID      int              `json:"id"`
	Long    string           `json:"long_field_name"`
	Escaped string           `json:"escaped"`
	Values  []int            `json:"values"`
	Fixed   [3]typedEdgeInt  `json:"fixed"`
	Next    typedEdgePointer `json:"next"`
	Extra   map[string]int   `json:"extra"`
	Meta    any              `json:"meta"`
}

func TestTypedDecoderMatchesStdlib(t *testing.T) {
	src := []byte(`{"items":[{"id":1,"ok":true,"name":"one","scores":[1,2.5,-3e4],"number":1234567890123456},{"id":2,"ok":false,"name":"two","scores":[4,5,6],"number":2}],"count":2,"next":{"id":3,"ok":true,"name":"three","scores":[7,8,9],"number":3},"unknown":{"nested":[1,2,3]}}`)
	var want typedTestDocument
	stdDecoder := json.NewDecoder(bytes.NewReader(src))
	stdDecoder.UseNumber()
	if err := stdDecoder.Decode(&want); err != nil {
		t.Fatal(err)
	}

	decoder, err := CompileDecoder[typedTestDocument](DecoderOptions{ZeroCopy: true})
	if err != nil {
		t.Fatal(err)
	}
	var got typedTestDocument
	if err := decoder.Decode(src, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("typed decoder = %#v, want %#v", got, want)
	}
}

func TestTypedDecoderReuseAndAllocations(t *testing.T) {
	src := []byte(`{"items":[{"id":1,"ok":true,"name":"one","scores":[1,2.5,-3e4],"number":1},{"id":2,"ok":false,"name":"two","scores":[4,5,6],"number":2}],"count":2,"next":null}`)
	decoder, err := CompileDecoder[typedTestDocument](DecoderOptions{ZeroCopy: true, CaseSensitive: true})
	if err != nil {
		t.Fatal(err)
	}
	dst := typedTestDocument{Items: make([]typedTestRecord, 0, 4)}
	if err := decoder.Decode(src, &dst); err != nil {
		t.Fatal(err)
	}
	base := &dst.Items[0]
	allocs := testing.AllocsPerRun(1000, func() {
		if err := decoder.Decode(src, &dst); err != nil {
			panic(err)
		}
	})
	if allocs != 0 {
		t.Fatalf("typed decoder reuse allocs = %v, want 0", allocs)
	}
	if &dst.Items[0] != base {
		t.Fatal("typed decoder did not reuse destination slice")
	}
}

func TestTypedDecoderOptionsAndUnsupportedTypes(t *testing.T) {
	strict, err := CompileDecoder[typedTestRecord](DecoderOptions{DisallowUnknownFields: true})
	if err != nil {
		t.Fatal(err)
	}
	var record typedTestRecord
	if err := strict.Decode([]byte(`{"unknown":1}`), &record); err == nil {
		t.Fatal("typed decoder accepted unknown field")
	}
	if _, err := CompileDecoder[map[int]string](DecoderOptions{}); err == nil {
		t.Fatal("typed decoder accepted non-string map keys")
	}
	if _, err := CompileDecoder[struct{ Value interface{ M() } }](DecoderOptions{}); err == nil {
		t.Fatal("typed decoder accepted non-empty interface field")
	}
}

func TestTypedDecoderReplacementAndFieldFallbacks(t *testing.T) {
	decoder, err := CompileDecoder[typedEdgeValue](DecoderOptions{ZeroCopy: true})
	if err != nil {
		t.Fatal(err)
	}
	dst := typedEdgeValue{
		ID:      99,
		Long:    "stale",
		Escaped: "stale",
		Values:  make([]int, 3, 8),
		Fixed:   [3]typedEdgeInt{9, 9, 9},
		Next:    typedEdgePointer(&typedEdgeValue{ID: 99}),
	}
	valuesBase := unsafe.SliceData(dst.Values)
	src := []byte(`{"long_field_name":"first","ID":7,"\u0065scaped":"escaped","fixed":[1,2],"id":8}`)
	if err := decoder.Decode(src, &dst); err != nil {
		t.Fatal(err)
	}
	if dst.ID != 8 || dst.Long != "first" || dst.Escaped != "escaped" {
		t.Fatalf("field fallback result = %#v", dst)
	}
	if dst.Fixed != [3]typedEdgeInt{1, 2, 0} {
		t.Fatalf("fixed array = %v, want [1 2 0]", dst.Fixed)
	}
	if dst.Values == nil || len(dst.Values) != 0 || cap(dst.Values) != 8 {
		t.Fatalf("missing slice = %#v, want retained empty capacity", dst.Values)
	}
	if unsafe.SliceData(dst.Values) != valuesBase {
		t.Fatal("missing slice did not retain its backing array")
	}
	if dst.Next != nil {
		t.Fatalf("missing pointer = %#v, want nil", dst.Next)
	}

	if err := decoder.Decode([]byte(`{"values":null}`), &dst); err != nil {
		t.Fatal(err)
	}
	if dst.Values != nil {
		t.Fatalf("null slice = %#v, want nil", dst.Values)
	}
	if err := decoder.Decode([]byte(`{"values":[]}`), &dst); err != nil {
		t.Fatal(err)
	}
	if dst.Values == nil || len(dst.Values) != 0 {
		t.Fatalf("empty slice = %#v, want non-nil empty", dst.Values)
	}
}

func TestTypedDecoderNestedErrorType(t *testing.T) {
	decoder, err := CompileDecoder[typedEdgeValue](DecoderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var dst typedEdgeValue
	err = decoder.Decode([]byte(`{"fixed":[1,1.5]}`), &dst)
	var typedErr *DecodeError
	if !errors.As(err, &typedErr) {
		t.Fatalf("error = %v, want DecodeError", err)
	}
	if typedErr.Type != reflect.TypeFor[typedEdgeInt]() {
		t.Fatalf("error type = %v, want %v", typedErr.Type, reflect.TypeFor[typedEdgeInt]())
	}
}

func TestTypedDecoderSliceGrowthAndNamedPointer(t *testing.T) {
	decoder, err := CompileDecoder[typedEdgeValue](DecoderOptions{ZeroCopy: true, CaseSensitive: true})
	if err != nil {
		t.Fatal(err)
	}
	dst := typedEdgeValue{Values: []int{99}}
	src := []byte(`{"values":[0,1,2,3,4,5,6,7,8,9],"next":{"id":42}}`)
	if err := decoder.Decode(src, &dst); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(dst.Values, []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}) {
		t.Fatalf("grown slice = %v", dst.Values)
	}
	if dst.Next == nil || dst.Next.ID != 42 {
		t.Fatalf("named pointer = %#v", dst.Next)
	}
	if err := decoder.Decode([]byte(`{"next":null}`), &dst); err != nil {
		t.Fatal(err)
	}
	if dst.Next != nil {
		t.Fatalf("null named pointer = %#v, want nil", dst.Next)
	}
}

func TestTypedDecoderDecodeArray(t *testing.T) {
	decoder, err := CompileDecoder[typedEdgeValue](DecoderOptions{ZeroCopy: true, CaseSensitive: true})
	if err != nil {
		t.Fatal(err)
	}
	dst := make([]typedEdgeValue, 0, 4)
	base := unsafe.SliceData(dst[:cap(dst)])
	dst, err = decoder.DecodeArray([]byte(`[{"id":1},{"id":2}]`), dst)
	if err != nil {
		t.Fatal(err)
	}
	if len(dst) != 2 || dst[0].ID != 1 || dst[1].ID != 2 {
		t.Fatalf("decoded array = %#v", dst)
	}
	if unsafe.SliceData(dst) != base {
		t.Fatal("DecodeArray did not reuse destination capacity")
	}
	dst, err = decoder.DecodeArray([]byte(`null`), dst)
	if err != nil {
		t.Fatal(err)
	}
	if dst != nil {
		t.Fatalf("null top-level array = %#v, want nil", dst)
	}
	dst, err = decoder.DecodeArray([]byte(`[]`), dst)
	if err != nil {
		t.Fatal(err)
	}
	if dst == nil || len(dst) != 0 {
		t.Fatalf("empty top-level array = %#v, want non-nil empty", dst)
	}
}

func TestTypedDecoderSmallDecodeAllocations(t *testing.T) {
	decoder, err := CompileDecoder[struct {
		ID   int    `json:"id"`
		OK   bool   `json:"ok"`
		Name string `json:"name"`
	}](DecoderOptions{ZeroCopy: true, CaseSensitive: true})
	if err != nil {
		t.Fatal(err)
	}
	src := []byte(`{"id":1,"ok":true,"name":"x"}`)
	var dst struct {
		ID   int    `json:"id"`
		OK   bool   `json:"ok"`
		Name string `json:"name"`
	}
	if allocs := testing.AllocsPerRun(1000, func() {
		if err := decoder.Decode(src, &dst); err != nil {
			panic(err)
		}
	}); allocs != 0 {
		t.Fatalf("small typed Decode allocs = %v, want 0", allocs)
	}
}

func FuzzTypedDecoderMatchesStdlib(f *testing.F) {
	for _, src := range [][]byte{
		[]byte(`{}`),
		[]byte(`null`),
		[]byte(`{"id":1,"long_field_name":"x","values":[1,2],"fixed":[3,4,5],"next":{"id":2}}`),
		[]byte(`{"\u0065scaped":"x","ID":7,"id":8}`),
	} {
		f.Add(src)
	}
	decoder, err := CompileDecoder[typedEdgeValue](DecoderOptions{})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, src []byte) {
		if len(src) > 1<<14 || !Valid(src) {
			return
		}
		var got, want typedEdgeValue
		gotErr := decoder.Decode(src, &got)
		wantErr := json.Unmarshal(src, &want)
		if (gotErr == nil) != (wantErr == nil) {
			t.Fatalf("acceptance differs: simdjson=%v stdlib=%v", gotErr, wantErr)
		}
		if gotErr == nil && !reflect.DeepEqual(got, want) {
			t.Fatalf("decoded value differs: simdjson=%#v stdlib=%#v", got, want)
		}
	})
}

func TestDecodeErrorReportsPath(t *testing.T) {
	decoder, err := CompileDecoder[typedTestDocument](DecoderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		src  string
		path string
	}{
		{
			name: "nested array element",
			src:  `{"items":[{"id":1,"ok":true,"name":"a","scores":[1,2,3],"number":1},{"id":2,"ok":true,"name":"b","scores":[1,"x",3],"number":2}],"count":1}`,
			path: "items[1].scores[1]",
		},
		{
			name: "scalar field",
			src:  `{"items":[{"id":"nope"}],"count":1}`,
			path: "items[0].id",
		},
		{
			name: "container mismatch",
			src:  `{"items":[7],"count":1}`,
			path: "items[0]",
		},
		{
			name: "pointer target field",
			src:  `{"count":1,"next":{"id":1,"ok":"broken"}}`,
			path: "next.ok",
		},
		{
			name: "top level",
			src:  `[1]`,
			path: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var dst typedTestDocument
			err := decoder.Decode([]byte(tc.src), &dst)
			var decodeErr *DecodeError
			if !errors.As(err, &decodeErr) {
				t.Fatalf("Decode(%q) error = %v, want *DecodeError", tc.src, err)
			}
			if decodeErr.Path != tc.path {
				t.Fatalf("Decode(%q) path = %q, want %q", tc.src, decodeErr.Path, tc.path)
			}
		})
	}
}

func TestUnmarshalMatchesCompiledDecoder(t *testing.T) {
	src := []byte(`{"items":[{"ID":1,"ok":true,"name":"one","scores":[1,2.5,-3e4],"number":9}],"count":7}`)
	var want typedTestDocument
	if err := json.Unmarshal(src, &want); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		var got typedTestDocument
		if err := Unmarshal(src, &got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Unmarshal = %#v, want %#v", got, want)
		}
	}

	var invalid func()
	err := Unmarshal([]byte(`1`), &invalid)
	var unsupported *UnsupportedTypeError
	if !errors.As(err, &unsupported) {
		t.Fatalf("Unmarshal into func = %v, want *UnsupportedTypeError", err)
	}

	// Owned strings must survive input mutation.
	input := []byte(`{"items":[{"id":1,"ok":true,"name":"keepsake","scores":[1,2,3],"number":1}],"count":1}`)
	var owned typedTestDocument
	if err := Unmarshal(input, &owned); err != nil {
		t.Fatal(err)
	}
	for i := range input {
		input[i] = 'x'
	}
	if owned.Items[0].Name != "keepsake" {
		t.Fatalf("Unmarshal string aliases input: %q", owned.Items[0].Name)
	}
}

// TestFieldOrderPermutationsMatchStdlib exercises adaptive expected-field
// matching: every member order, with and without unknown members, duplicate
// keys, and case-folded keys, must decode exactly like encoding/json.
func TestFieldOrderPermutationsMatchStdlib(t *testing.T) {
	members := []string{
		`"id":42`,
		`"ok":true`,
		`"name":"perm"`,
		`"scores":[1,2.5,3]`,
		`"number":7`,
	}
	extras := [][]string{
		nil,
		{`"unknown":{"nested":[1,2]}`},
		{`"id":43`},         // duplicate key, last wins
		{`"NAME":"folded"`}, // case-insensitive fallback, last wins
	}
	decoder, err := CompileDecoder[typedTestRecord](DecoderOptions{})
	if err != nil {
		t.Fatal(err)
	}

	var permute func(rest, current []string)
	permute = func(rest, current []string) {
		if len(rest) == 0 {
			for _, extra := range extras {
				for insert := 0; insert <= len(current); insert += 2 {
					ordered := make([]string, 0, len(current)+len(extra))
					ordered = append(ordered, current[:insert]...)
					ordered = append(ordered, extra...)
					ordered = append(ordered, current[insert:]...)
					src := []byte("{" + strings.Join(ordered, ",") + "}")

					var got, want typedTestRecord
					gotErr := decoder.Decode(src, &got)
					wantErr := json.Unmarshal(src, &want)
					if (gotErr == nil) != (wantErr == nil) {
						t.Fatalf("%s: acceptance differs: simdjson=%v stdlib=%v", src, gotErr, wantErr)
					}
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("%s: simdjson=%#v stdlib=%#v", src, got, want)
					}
				}
			}
			return
		}
		for i := range rest {
			next := append(append([]string{}, rest[:i]...), rest[i+1:]...)
			permute(next, append(current, rest[i]))
		}
	}
	permute(members, nil)
}

func TestCaseFoldedKeysMatchStdlib(t *testing.T) {
	type untagged struct {
		ID     int
		Name   string
		OK     bool
		Id     string // folds with ID: exact matches must win for both
		Corner int    `json:"x_y"`
	}
	decoder, err := CompileDecoder[untagged](DecoderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	sources := []string{
		`{"id":1,"name":"a","ok":true}`,
		`{"ID":2,"NAME":"b","OK":false}`,
		`{"Id":"exact","ID":3}`,
		`{"iD":4,"Name":"c"}`,
		`{"x_y":5,"X_y":6,"x_Y":7}`,
		`{"id":8,"Id":"both"}`,
	}
	for _, src := range sources {
		var got, want untagged
		gotErr := decoder.Decode([]byte(src), &got)
		wantErr := json.Unmarshal([]byte(src), &want)
		if (gotErr == nil) != (wantErr == nil) {
			t.Fatalf("%s: acceptance differs: simdjson=%v stdlib=%v", src, gotErr, wantErr)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: simdjson=%#v stdlib=%#v", src, got, want)
		}
	}
}

// TestDecodeDestinationStaysOnStack guards against escape-analysis
// contamination: passing dst-derived pointers into reflect (as the map path
// once did) forces every Decode destination onto the heap, which shows up
// here as an allocation for the local value.
func TestDecodeDestinationStaysOnStack(t *testing.T) {
	decoder, err := CompileDecoder[typedTestRecord](DecoderOptions{ZeroCopy: true, CaseSensitive: true})
	if err != nil {
		t.Fatal(err)
	}
	src := []byte(`{"id":1,"ok":true,"name":"sim","scores":[1,2,3],"number":4}`)
	var sink int
	allocs := testing.AllocsPerRun(200, func() {
		var value typedTestRecord
		if err := decoder.Decode(src, &value); err != nil {
			panic(err)
		}
		sink += value.ID
	})
	_ = sink
	if allocs != 0 {
		t.Fatalf("Decode into a local destination allocated %v times per run, want 0", allocs)
	}
}
