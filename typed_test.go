package simdjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
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

func TestDecoderCursorStaysCompact(t *testing.T) {
	if size := unsafe.Sizeof(DecoderCursor{}); size > 64 {
		t.Fatalf("DecoderCursor size = %d bytes, want <= 64", size)
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
}

func TestTypedDecoderMatchesStdlib(t *testing.T) {
	src := []byte(`{"items":[{"id":1,"ok":true,"name":"one","scores":[1,2.5,-3e4],"number":1234567890123456},{"id":2,"ok":false,"name":"two","scores":[4,5,6],"number":2}],"count":2,"next":{"id":3,"ok":true,"name":"three","scores":[7,8,9],"number":3},"unknown":{"nested":[1,2,3]}}`)
	var want typedTestDocument
	stdDecoder := json.NewDecoder(bytes.NewReader(src))
	stdDecoder.UseNumber()
	if err := stdDecoder.Decode(&want); err != nil {
		t.Fatal(err)
	}

	decoder, err := CompileDecoder[typedTestDocument](TypedOptions{ZeroCopy: true})
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
	decoder, err := CompileDecoder[typedTestDocument](TypedOptions{ZeroCopy: true, CaseSensitive: true})
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
	strict, err := CompileDecoder[typedTestRecord](TypedOptions{DisallowUnknownFields: true})
	if err != nil {
		t.Fatal(err)
	}
	var record typedTestRecord
	if err := strict.Decode([]byte(`{"unknown":1}`), &record); err == nil {
		t.Fatal("typed decoder accepted unknown field")
	}
	if _, err := CompileDecoder[map[string]int](TypedOptions{}); err == nil {
		t.Fatal("typed decoder accepted map type")
	}
	if _, err := CompileDecoder[struct{ Value any }](TypedOptions{}); err == nil {
		t.Fatal("typed decoder accepted interface field")
	}
}

func TestTypedDecoderReplacementAndFieldFallbacks(t *testing.T) {
	decoder, err := CompileDecoder[typedEdgeValue](TypedOptions{ZeroCopy: true})
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
	decoder, err := CompileDecoder[typedEdgeValue](TypedOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var dst typedEdgeValue
	err = decoder.Decode([]byte(`{"fixed":[1,1.5]}`), &dst)
	var typedErr *TypedDecodeError
	if !errors.As(err, &typedErr) {
		t.Fatalf("error = %v, want TypedDecodeError", err)
	}
	if typedErr.Type != reflect.TypeFor[typedEdgeInt]() {
		t.Fatalf("error type = %v, want %v", typedErr.Type, reflect.TypeFor[typedEdgeInt]())
	}
}

func TestTypedDecoderSliceGrowthAndNamedPointer(t *testing.T) {
	decoder, err := CompileDecoder[typedEdgeValue](TypedOptions{ZeroCopy: true, CaseSensitive: true})
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
	decoder, err := CompileDecoder[typedEdgeValue](TypedOptions{ZeroCopy: true, CaseSensitive: true})
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
	}](TypedOptions{ZeroCopy: true, CaseSensitive: true})
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
	decoder, err := CompileDecoder[typedEdgeValue](TypedOptions{})
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
