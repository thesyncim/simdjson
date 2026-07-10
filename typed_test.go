package simdjson

import (
	"bytes"
	"encoding/json"
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
