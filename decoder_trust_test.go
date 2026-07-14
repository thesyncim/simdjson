package simdjson

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// trustSinkInner gives the kitchen sink a nested pointer struct so decode
// paths recurse through pointer indirection.
type trustSinkInner struct {
	S string  `json:"s"`
	A any     `json:"a"`
	F float64 `json:"f"`
}

// trustSink routes one document through every retention-relevant decode
// path: plain and escaped strings, dynamic values, maps, slices, raw
// passthrough, base64, string-tagged scalars, unmarshalers, and numbers.
type trustSink struct {
	S  string            `json:"s"`
	A  any               `json:"a"`
	M  map[string]any    `json:"m"`
	MS map[string]string `json:"ms"`
	L  []any             `json:"l"`
	LS []string          `json:"ls"`
	Q  int64             `json:"q,string"`
	R  json.RawMessage   `json:"r"`
	B  []byte            `json:"b"`
	P  *trustSinkInner   `json:"p"`
	T  time.Time         `json:"t"`
	N  float64           `json:"n"`
	NN json.Number       `json:"nn"`
}

var trustDecoders = func() map[string]Decoder[trustSink] {
	decoders := make(map[string]Decoder[trustSink], 2)
	for name, opts := range map[string]DecoderOptions{
		"owned":     {},
		"zero-copy": {ZeroCopy: true},
	} {
		dec, err := CompileDecoder[trustSink](opts)
		if err != nil {
			panic(err)
		}
		decoders[name] = dec
	}
	return decoders
}()

// trustSinkDoc builds a document that exercises every trustSink field with
// escaped content interleaved so the string arena sees retained writers on
// both sides of every dynamic value.
func trustSinkDoc() []byte {
	e := jsonUnicodeEscape
	return []byte(`{` +
		`"s":"lead` + e("0041") + `tail",` +
		`"a":{"k":"any` + e("00e9") + `value","list":["x` + e("0042") + `y",1.5,true]},` +
		`"m":{"mk":"m` + e("004D") + `v","plain":"clean"},` +
		`"ms":{"a` + e("0043") + `b":"c` + e("0044") + `d"},` +
		`"l":["e` + e("0045") + `f",{"g":"h` + e("0046") + `i"}],` +
		`"ls":["j` + e("0047") + `k","plain"],` +
		`"q":"-42",` +
		`"r":{"raw":  [1, "t` + e("0048") + `u"]},` +
		`"b":"aGVsbG8=",` +
		`"p":{"s":"n` + e("0049") + `o","a":"p` + e("004A") + `q","f":2.5},` +
		`"t":"2026-07-14T12:34:56Z",` +
		`"n":-0.125,` +
		`"nn":1234567890123456789012345` +
		`}`)
}

// FuzzDecodeTrust is the corruption-catching differential: any strictly
// valid document decoded into the kitchen sink must produce exactly
// encoding/json's result in both ownership modes, and invalid documents must
// be rejected. Arena overwrites, aliasing slips, and retention bugs all
// surface as value divergence without needing to anticipate their shape.
func FuzzDecodeTrust(f *testing.F) {
	f.Add(trustSinkDoc())
	f.Add([]byte(`{"b":"p` + jsonUnicodeEscape("0042") + `q","a":"x` + jsonUnicodeEscape("0041") + `y","s":"r` + jsonUnicodeEscape("0043") + `s"}`))
	f.Add([]byte(`{"a":[{"m":{"x":"` + jsonUnicodeEscape("2028") + `"}},"` + jsonUnicodeEscape("D834") + jsonUnicodeEscape("DD1E") + `"]}`))
	f.Add([]byte(`{"r":" not raw ","q":"7"}`))
	f.Add([]byte(`{"unknown":{"deep":["skip",{"me":1}]},"s":"kept"}`))
	f.Fuzz(func(t *testing.T, src []byte) {
		if len(src) > 1<<15 {
			t.Skip()
		}
		valid := strictJSONValid(src)
		var want trustSink
		wantErr := json.Unmarshal(src, &want)
		for name, dec := range trustDecoders {
			var got trustSink
			err := dec.Decode(src, &got)
			if !valid {
				if err == nil {
					t.Fatalf("%s accepted input that is not strict JSON (length %d)", name, len(src))
				}
				continue
			}
			if (err == nil) != (wantErr == nil) {
				t.Fatalf("%s error = %v, encoding/json error = %v", name, err, wantErr)
			}
			if err == nil && !reflect.DeepEqual(got, want) {
				t.Fatalf("%s decode differs from encoding/json:\n got: %+v\nwant: %+v", name, got, want)
			}
		}
	})
}

// TestConcurrentCompiledCodecs shares one compiled decoder per mode across
// goroutines decoding distinct documents concurrently, so the race detector
// sees any shared mutable state and each goroutine verifies its own results
// against encoding/json.
func TestConcurrentCompiledCodecs(t *testing.T) {
	docs := [][]byte{
		trustSinkDoc(),
		[]byte(`{"s":"z` + jsonUnicodeEscape("005A") + `z","a":["w` + jsonUnicodeEscape("0057") + `w"],"n":1e-3}`),
		[]byte(`{"m":{"k1":"a` + jsonUnicodeEscape("00E9") + `b","k2":"plain"},"q":"123","b":"QUJD"}`),
		[]byte(`{"l":[1,"two",{"three":3}],"r":[null, false],"nn":42}`),
	}
	wants := make([]trustSink, len(docs))
	for i, doc := range docs {
		if err := json.Unmarshal(doc, &wants[i]); err != nil {
			t.Fatal(err)
		}
	}
	for name, dec := range trustDecoders {
		t.Run(name, func(t *testing.T) {
			var group errgroupLite
			for g := 0; g < 8; g++ {
				doc := docs[g%len(docs)]
				want := wants[g%len(docs)]
				group.Go(func() error {
					for range 200 {
						var got trustSink
						if err := dec.Decode(doc, &got); err != nil {
							return err
						}
						if !reflect.DeepEqual(got, want) {
							return errConcurrentDivergence
						}
					}
					return nil
				})
			}
			if err := group.Wait(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

var errConcurrentDivergence = errorString("concurrent decode diverged from encoding/json")

type errorString string

func (e errorString) Error() string { return string(e) }

// errgroupLite avoids a dependency on x/sync for one test.
type errgroupLite struct {
	wg   chan error
	jobs int
}

func (g *errgroupLite) Go(fn func() error) {
	if g.wg == nil {
		g.wg = make(chan error, 64)
	}
	g.jobs++
	go func() { g.wg <- fn() }()
}

func (g *errgroupLite) Wait() error {
	var first error
	for range g.jobs {
		if err := <-g.wg; err != nil && first == nil {
			first = err
		}
	}
	return first
}

// TestBytesArrayFormParity covers the encoding/json quirk the trust fuzz
// found: a byte slice also decodes from a JSON array of integers, with
// element range errors, empties, and null behaving exactly like the stdlib.
func TestBytesArrayFormParity(t *testing.T) {
	for _, src := range []string{
		`{"b":[72,105]}`,
		`{"b":[]}`,
		`{"b":[0,255]}`,
		`{"b":[256]}`,
		`{"b":[-1]}`,
		`{"b":[1.5]}`,
		`{"b":["x"]}`,
		`{"b":null}`,
		`{"b":"aGk="}`,
	} {
		var want trustSink
		wantErr := json.Unmarshal([]byte(src), &want)
		for name, dec := range trustDecoders {
			var got trustSink
			err := dec.Decode([]byte(src), &got)
			if (err == nil) != (wantErr == nil) {
				t.Fatalf("%s %s: error = %v, encoding/json error = %v", name, src, err, wantErr)
			}
			if err == nil && !reflect.DeepEqual(got.B, want.B) {
				t.Fatalf("%s %s: B = %#v, want %#v", name, src, got.B, want.B)
			}
		}
	}
}

// TestOwnedDecodeSurvivesSourceMutation proves the owned contract: after an
// owned-mode decode, destroying the source buffer must not change one byte
// of the result.
func TestOwnedDecodeSurvivesSourceMutation(t *testing.T) {
	original := trustSinkDoc()

	var want trustSink
	if err := json.Unmarshal(original, &want); err != nil {
		t.Fatal(err)
	}

	src := bytes.Clone(original)
	var got trustSink
	if err := trustDecoders["owned"].Decode(src, &got); err != nil {
		t.Fatal(err)
	}
	for i := range src {
		src[i] = 0xAA
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("owned decode changed after source mutation:\n got: %+v\nwant: %+v", got, want)
	}
}

// TestDecoderCallsAreIndependent proves compiled decoders carry no state
// between calls: an earlier result must not change when the same decoder
// processes different documents afterwards.
func TestDecoderCallsAreIndependent(t *testing.T) {
	first := trustSinkDoc()
	second := []byte(`{"s":"z` + jsonUnicodeEscape("005A") + `z","a":"w` + jsonUnicodeEscape("0057") + `w","m":{"q":"v` + jsonUnicodeEscape("0056") + `v"}}`)

	var want trustSink
	if err := json.Unmarshal(first, &want); err != nil {
		t.Fatal(err)
	}
	for name, dec := range trustDecoders {
		var got trustSink
		if err := dec.Decode(bytes.Clone(first), &got); err != nil {
			t.Fatal(err)
		}
		for range 8 {
			var other trustSink
			if err := dec.Decode(bytes.Clone(second), &other); err != nil {
				t.Fatal(err)
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: first result changed after later decodes:\n got: %+v\nwant: %+v", name, got, want)
		}
	}
}
