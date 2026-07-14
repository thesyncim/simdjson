package simdjson

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

type inlineRaw struct {
	ID    int                        `json:"id"`
	Name  string                     `json:"name"`
	Extra map[string]json.RawMessage `json:",inline"`
}

type inlineAny struct {
	ID    int            `json:"id"`
	Extra map[string]any `json:",inline"`
}

// inlineOnly declares nothing but the catch-all, so every object member flows
// through it. It anchors the lossless round-trip fuzz below.
type inlineOnly struct {
	Extra map[string]json.RawMessage `json:",inline"`
}

// TestInlineCatchAllRoundTrip covers the core contract: unknown root members
// decode into the ",inline" map and re-emit at the object's own level, after
// the declared fields, in sorted order.
func TestInlineCatchAllRoundTrip(t *testing.T) {
	src := []byte(`{"id":1,"name":"x","c":"hi","a":true,"b":[1,2]}`)

	dec, err := CompileDecoder[inlineRaw](DecoderOptions{InlineFields: true})
	if err != nil {
		t.Fatal(err)
	}
	var v inlineRaw
	if err := dec.Decode(src, &v); err != nil {
		t.Fatal(err)
	}
	if v.ID != 1 || v.Name != "x" {
		t.Fatalf("declared fields = %d, %q", v.ID, v.Name)
	}
	want := map[string]json.RawMessage{
		"a": json.RawMessage("true"),
		"b": json.RawMessage("[1,2]"),
		"c": json.RawMessage(`"hi"`),
	}
	if !reflect.DeepEqual(map[string]json.RawMessage(v.Extra), want) {
		t.Fatalf("catch-all = %v, want %v", v.Extra, want)
	}

	enc, err := CompileEncoder[inlineRaw](EncoderOptions{InlineFields: true})
	if err != nil {
		t.Fatal(err)
	}
	out, err := enc.AppendJSON(nil, &v)
	if err != nil {
		t.Fatal(err)
	}
	// Declared fields in struct order, then the catch-all members sorted.
	const wantOut = `{"id":1,"name":"x","a":true,"b":[1,2],"c":"hi"}`
	if string(out) != wantOut {
		t.Fatalf("encode = %s, want %s", out, wantOut)
	}
}

// TestInlineCatchAllAny checks a map[string]any catch-all decodes the dynamic
// shapes and re-emits them.
func TestInlineCatchAllAny(t *testing.T) {
	dec, err := CompileDecoder[inlineAny](DecoderOptions{InlineFields: true})
	if err != nil {
		t.Fatal(err)
	}
	var v inlineAny
	if err := dec.Decode([]byte(`{"id":2,"flag":true,"nums":[1,2.5]}`), &v); err != nil {
		t.Fatal(err)
	}
	if v.ID != 2 {
		t.Fatalf("id = %d", v.ID)
	}
	if v.Extra["flag"] != true || !reflect.DeepEqual(v.Extra["nums"], []any{1.0, 2.5}) {
		t.Fatalf("catch-all = %#v", v.Extra)
	}
}

// TestInlineEmptyCatchAll checks that no unknown members leaves the map nil and
// emits nothing extra.
func TestInlineEmptyCatchAll(t *testing.T) {
	dec, err := CompileDecoder[inlineRaw](DecoderOptions{InlineFields: true})
	if err != nil {
		t.Fatal(err)
	}
	var v inlineRaw
	if err := dec.Decode([]byte(`{"id":7,"name":"y"}`), &v); err != nil {
		t.Fatal(err)
	}
	if v.Extra != nil {
		t.Fatalf("catch-all allocated with no unknown members: %v", v.Extra)
	}
	enc, err := CompileEncoder[inlineRaw](EncoderOptions{InlineFields: true})
	if err != nil {
		t.Fatal(err)
	}
	out, err := enc.AppendJSON(nil, &v)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"id":7,"name":"y"}` {
		t.Fatalf("encode = %s", out)
	}
}

// TestInlineCatchAllWinsOverDisallow verifies the catch-all consumes members
// that DisallowUnknownFields would otherwise reject.
func TestInlineCatchAllWinsOverDisallow(t *testing.T) {
	dec, err := CompileDecoder[inlineRaw](DecoderOptions{InlineFields: true, DisallowUnknownFields: true})
	if err != nil {
		t.Fatal(err)
	}
	var v inlineRaw
	if err := dec.Decode([]byte(`{"id":1,"surprise":9}`), &v); err != nil {
		t.Fatalf("catch-all did not consume the unknown member: %v", err)
	}
	if string(v.Extra["surprise"]) != "9" {
		t.Fatalf("catch-all = %v", v.Extra)
	}
}

// TestInlineUnsortedOption checks the ordering toggle emits members in map
// order (verified only by re-decoding, since map order is nondeterministic).
func TestInlineUnsortedOption(t *testing.T) {
	enc, err := CompileEncoder[inlineRaw](EncoderOptions{InlineFields: true, UnsortedInlineFields: true})
	if err != nil {
		t.Fatal(err)
	}
	v := inlineRaw{ID: 1, Extra: map[string]json.RawMessage{"z": json.RawMessage("1"), "a": json.RawMessage("2")}}
	out, err := enc.AppendJSON(nil, &v)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := CompileDecoder[inlineRaw](DecoderOptions{InlineFields: true})
	if err != nil {
		t.Fatal(err)
	}
	var back inlineRaw
	if err := dec.Decode(out, &back); err != nil {
		t.Fatalf("re-decode of unsorted output failed: %v (%s)", err, out)
	}
	if string(back.Extra["z"]) != "1" || string(back.Extra["a"]) != "2" {
		t.Fatalf("round trip lost members: %s", out)
	}
}

// TestInlineRejectsNonMap rejects a non-map ",inline" field at compile time,
// but only once the extension is opted in.
func TestInlineRejectsNonMap(t *testing.T) {
	type bad struct {
		X int `json:",inline"`
	}
	if _, err := CompileEncoder[bad](EncoderOptions{InlineFields: true}); err == nil {
		t.Fatal("expected an error for a non-map inline field")
	}
}

// TestInlineOptOutIsInert is the opt-in proof: without InlineFields the tag is
// an ordinary field named by its Go name. Unknown members are not captured and
// the map serializes under its field name, exactly as it would with no tag.
func TestInlineOptOutIsInert(t *testing.T) {
	dec, err := CompileDecoder[inlineRaw](DecoderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var v inlineRaw
	if err := dec.Decode([]byte(`{"id":1,"surprise":9}`), &v); err != nil {
		t.Fatal(err)
	}
	if v.Extra != nil {
		t.Fatalf("catch-all captured a member with the extension off: %v", v.Extra)
	}

	enc, err := CompileEncoder[inlineRaw](EncoderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	v.Extra = map[string]json.RawMessage{"k": json.RawMessage("1")}
	out, err := enc.AppendJSON(nil, &v)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"Extra":{"k":1}`) {
		t.Fatalf("inert map did not serialize under its field name: %s", out)
	}
}

// TestInlineReplaceClearsStale pins the reuse semantics: the default merge
// keeps unknown members from a prior decode, while Replace clears them so the
// map reflects only the current document.
func TestInlineReplaceClearsStale(t *testing.T) {
	merge, err := CompileDecoder[inlineRaw](DecoderOptions{InlineFields: true})
	if err != nil {
		t.Fatal(err)
	}
	replace, err := CompileDecoder[inlineRaw](DecoderOptions{InlineFields: true, Replace: true})
	if err != nil {
		t.Fatal(err)
	}

	var v inlineRaw
	if err := merge.Decode([]byte(`{"id":1,"a":1,"b":2}`), &v); err != nil {
		t.Fatal(err)
	}
	// Merge into the same destination: the new unknown joins the survivors.
	if err := merge.Decode([]byte(`{"id":2,"c":3}`), &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Extra) != 3 || string(v.Extra["a"]) != "1" || string(v.Extra["c"]) != "3" {
		t.Fatalf("merge did not accumulate: %v", v.Extra)
	}

	// Replace into the same destination: only the current unknown remains.
	if err := replace.Decode([]byte(`{"id":3,"d":4}`), &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Extra) != 1 || string(v.Extra["d"]) != "4" {
		t.Fatalf("replace did not clear stale members: %v", v.Extra)
	}
}

// BenchmarkInlineEncode measures a populated catch-all with a reused encoder:
// the pooled scratch makes it allocation-free after warmup.
func BenchmarkInlineEncode(b *testing.B) {
	enc, err := CompileEncoder[inlineRaw](EncoderOptions{InlineFields: true})
	if err != nil {
		b.Fatal(err)
	}
	v := inlineRaw{ID: 1, Name: "x", Extra: map[string]json.RawMessage{
		"alpha": json.RawMessage("true"),
		"beta":  json.RawMessage("[1,2,3]"),
		"gamma": json.RawMessage(`"hello"`),
		"delta": json.RawMessage("42"),
	}}
	buf := make([]byte, 0, 128)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf, err = enc.AppendJSON(buf[:0], &v)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkInlineDecode measures decoding unknown members into the catch-all:
// one reusable element serves every member, so cost does not scale with the
// number of unknowns beyond the map's own storage.
func BenchmarkInlineDecode(b *testing.B) {
	dec, err := CompileDecoder[inlineRaw](DecoderOptions{InlineFields: true})
	if err != nil {
		b.Fatal(err)
	}
	src := []byte(`{"id":1,"name":"x","alpha":true,"beta":[1,2,3],"gamma":"hello","delta":42}`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var v inlineRaw
		if err := dec.Decode(src, &v); err != nil {
			b.Fatal(err)
		}
	}
}

// FuzzInlineRoundTrip is the powerful invariant: a struct whose only field is
// the catch-all captures every member of any object it decodes, so re-encoding
// must reproduce the same object. Decode compatibility itself is covered by the
// validation corpus; here we assume a decodable input and prove no member,
// value, or key is lost or invented on the way back out.
func FuzzInlineRoundTrip(f *testing.F) {
	for _, seed := range []string{
		`{}`,
		`{"id":1,"name":"x"}`,
		`{"a":true,"b":[1,2],"c":"hi"}`,
		`{"zebra":1,"alpha":2,"mango":3}`,
		`{"nested":{"deep":[{"k":"v"}]},"n":-0.5}`,
		`{"unié":true,"esc\"key":"v"}`,
		`{"a":1,"a":2}`,
		`{"big":123456789012345678,"f":1e30,"z":0.0}`,
	} {
		f.Add([]byte(seed))
	}

	dec, err := CompileDecoder[inlineOnly](DecoderOptions{InlineFields: true})
	if err != nil {
		f.Fatal(err)
	}
	enc, err := CompileEncoder[inlineOnly](EncoderOptions{InlineFields: true})
	if err != nil {
		f.Fatal(err)
	}

	f.Fuzz(func(t *testing.T, src []byte) {
		var want map[string]any
		if err := json.Unmarshal(src, &want); err != nil || want == nil {
			return // not a JSON object (or a top-level null): nothing to prove
		}
		var v inlineOnly
		if err := dec.Decode(src, &v); err != nil {
			return // decode compatibility is the corpus's job, not this test's
		}
		out, err := enc.AppendJSON(nil, &v)
		if err != nil {
			t.Fatalf("encode failed: %v (src=%s)", err, src)
		}
		var got map[string]any
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("re-encoded output is not valid JSON: %v (%s)", err, out)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("round trip changed the object:\n src=%s\n out=%s\n want=%#v\n got=%#v", src, out, want, got)
		}
	})
}
