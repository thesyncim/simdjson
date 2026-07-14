package simdjson

import (
	"reflect"
	"testing"
)

// jsonUnicodeEscape builds a \uXXXX escape without a literal backslash-u in
// source, keeping the intent obvious at call sites.
func jsonUnicodeEscape(hex string) string {
	return "\\u" + hex
}

// TestArenaRetainsAnyStrings covers the arena-overwrite regression: an any
// field whose string content was unescaped into the arena must survive later
// escaped strings appending after it. The B field creates the arena, A
// retains arena bytes inside the dynamic value, and C appends next.
func TestArenaRetainsAnyStrings(t *testing.T) {
	type S struct {
		B string         `json:"b"`
		A any            `json:"a"`
		M map[string]any `json:"m"`
		C string         `json:"c"`
	}
	src := []byte(`{"b":"p` + jsonUnicodeEscape("0042") + `q",` +
		`"a":"x` + jsonUnicodeEscape("0041") + `y",` +
		`"m":{"k":"m` + jsonUnicodeEscape("004D") + `n"},` +
		`"c":"r` + jsonUnicodeEscape("0043") + `s"}`)
	for _, opts := range []DecoderOptions{{}, {ZeroCopy: true}} {
		dec, err := CompileDecoder[S](opts)
		if err != nil {
			t.Fatal(err)
		}
		var s S
		if err := dec.Decode(src, &s); err != nil {
			t.Fatal(err)
		}
		want := S{B: "pBq", A: any("xAy"), M: map[string]any{"k": "mMn"}, C: "rCs"}
		if !reflect.DeepEqual(s, want) {
			t.Fatalf("ZeroCopy=%v: got %+v, want %+v", opts.ZeroCopy, s, want)
		}
	}
}
