package simdjson

import "testing"

type staticMarshaler struct{ V int }

func (m staticMarshaler) MarshalJSON() ([]byte, error) { return []byte(`"static"`), nil }

type dynamicMarshaler struct{ S string }

func (m dynamicMarshaler) MarshalJSON() ([]byte, error) { return []byte(`"dynamic"`), nil }

func TestDynamicMarshalerForeignScratch(t *testing.T) {
	type doc struct {
		A staticMarshaler `json:"a"`
		B any             `json:"b"`
	}
	v := doc{A: staticMarshaler{V: 1}, B: dynamicMarshaler{S: "x"}}
	out, err := Marshal(&v)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"a":"static","b":"dynamic"}` {
		t.Fatalf("got %s", out)
	}
}

type recursiveMap map[string]recursiveMap

func TestEncodeMapScratchReuse(t *testing.T) {
	type doc struct {
		M recursiveMap      `json:"m"`
		N map[string]int    `json:"n"`
		O map[int]string    `json:"o"`
		P any               `json:"p"`
	}
	v := doc{
		M: recursiveMap{"a": recursiveMap{"b": recursiveMap{"c": nil}}, "z": nil},
		N: map[string]int{"x": 1, "y": 2},
		O: map[int]string{-5: "neg", 10: "ten", 3: "three"},
		P: map[string]bool{"k": true},
	}
	enc, err := CompileEncoder[doc](EncoderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"m":{"a":{"b":{"c":null}},"z":null},"n":{"x":1,"y":2},"o":{"-5":"neg","10":"ten","3":"three"},"p":{"k":true}}`
	buf := make([]byte, 0, 256)
	for round := 0; round < 50; round++ {
		out, err := enc.AppendJSON(buf[:0], &v)
		if err != nil {
			t.Fatal(err)
		}
		if string(out) != want {
			t.Fatalf("round %d:\ngot  %s\nwant %s", round, out, want)
		}
	}
}
