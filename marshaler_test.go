package simdjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

type textPoint struct{ X, Y int }

func (p textPoint) MarshalText() ([]byte, error) {
	return fmt.Appendf(nil, "%d;%d", p.X, p.Y), nil
}

func (p *textPoint) UnmarshalText(text []byte) error {
	_, err := fmt.Sscanf(string(text), "%d;%d", &p.X, &p.Y)
	return err
}

type rawJSONBox struct{ Raw string }

func (b rawJSONBox) MarshalJSON() ([]byte, error) {
	if b.Raw == "" {
		return []byte(`{}`), nil
	}
	return []byte(b.Raw), nil
}

func (b *rawJSONBox) UnmarshalJSON(data []byte) error {
	b.Raw = string(data)
	return nil
}

type failingMarshaler struct{}

func (failingMarshaler) MarshalJSON() ([]byte, error) { return nil, errors.New("boom") }

type pointerOnlyMarshaler struct {
	Value int `json:"value"`
}

func (*pointerOnlyMarshaler) MarshalJSON() ([]byte, error) {
	return []byte(`"pointer"`), nil
}

type mixedAddressMarshaler struct{}

func (mixedAddressMarshaler) MarshalText() ([]byte, error) {
	return []byte("text"), nil
}

func (*mixedAddressMarshaler) MarshalJSON() ([]byte, error) {
	return []byte(`"pointer"`), nil
}

type marshalerDocument struct {
	When    time.Time            `json:"when"`
	WhenPtr *time.Time           `json:"when_ptr"`
	Point   textPoint            `json:"point"`
	Box     rawJSONBox           `json:"box"`
	Boxes   []rawJSONBox         `json:"boxes"`
	ByKey   map[string]textPoint `json:"by_key"`
}

func TestMarshalersMatchStdlib(t *testing.T) {
	when := time.Date(2026, 7, 10, 22, 30, 0, 123456789, time.UTC)
	values := []marshalerDocument{
		{
			When:    when,
			WhenPtr: &when,
			Point:   textPoint{X: 3, Y: -4},
			Box:     rawJSONBox{Raw: `{"nested":[1,2,3]}`},
			Boxes:   []rawJSONBox{{Raw: `"s"`}, {Raw: `42`}},
			ByKey:   map[string]textPoint{"a": {X: 1, Y: 2}},
		},
		{},
		{Box: rawJSONBox{Raw: `"html <&> and   separators"`}},
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

	sources := []string{
		`{"when":"2026-07-10T22:30:00.123456789Z","when_ptr":"2001-02-03T04:05:06Z","point":"7;8","box":{"any":[1,2]},"boxes":["x",17],"by_key":{"k":"1;1"}}`,
		`{"when":null,"when_ptr":null,"point":null,"box":null}`,
		`{"when":"not a time"}`,
		`{"point":"bad"}`,
		`{"point":17}`,
	}
	decoder, err := CompileDecoder[marshalerDocument](DecoderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, src := range sources {
		var got, want marshalerDocument
		gotErr := decoder.Decode([]byte(src), &got)
		wantErr := json.Unmarshal([]byte(src), &want)
		if (gotErr == nil) != (wantErr == nil) {
			t.Fatalf("%s: acceptance differs: simdjson=%v stdlib=%v", src, gotErr, wantErr)
		}
		if gotErr == nil && !reflect.DeepEqual(got, want) {
			t.Fatalf("%s:\nsimdjson %#v\nstdlib   %#v", src, got, want)
		}
	}

	// Round trip through both libraries.
	original := marshalerDocument{When: when.Truncate(time.Second), Point: textPoint{X: 9, Y: 9}, Box: rawJSONBox{Raw: `[true]`}}
	encoded, err := Marshal(&original)
	if err != nil {
		t.Fatal(err)
	}
	var decoded marshalerDocument
	if err := decoder.Decode(encoded, &decoded); err != nil {
		t.Fatalf("round trip decode of %s: %v", encoded, err)
	}
	if !decoded.When.Equal(original.When) || decoded.Point != original.Point {
		t.Fatalf("round trip mismatch: %#v vs %#v", decoded, original)
	}
}

func TestMarshalerErrorsAndPaths(t *testing.T) {
	type doc struct {
		Items []failingMarshaler `json:"items"`
	}
	_, err := Marshal(&doc{Items: make([]failingMarshaler, 2)})
	var encodeErr *EncodeError
	if !errors.As(err, &encodeErr) {
		t.Fatalf("error = %v, want *EncodeError", err)
	}
	if encodeErr.Path != "items[0]" || !strings.Contains(encodeErr.Reason, "boom") {
		t.Fatalf("path=%q reason=%q", encodeErr.Path, encodeErr.Reason)
	}

	type timeDoc struct {
		Inner struct {
			T time.Time `json:"t"`
		} `json:"inner"`
	}
	var td timeDoc
	decodeErr := Unmarshal([]byte(`{"inner":{"t":"garbage"}}`), &td)
	var typed *DecodeError
	if !errors.As(decodeErr, &typed) {
		t.Fatalf("decode error = %v, want *DecodeError", decodeErr)
	}
	if typed.Path != "inner.t" {
		t.Fatalf("decode path = %q, want inner.t", typed.Path)
	}
}

func TestMarshalerHTMLReescaped(t *testing.T) {
	type doc struct {
		Box rawJSONBox `json:"box"`
	}
	value := doc{Box: rawJSONBox{Raw: `"<&>"`}}
	want, err := stdlibCompactJSON(t, &value)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Marshal(&value)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("html re-escape:\nsimdjson %s\nstdlib   %s", got, want)
	}
}

func TestPointerOnlyMarshalerAddressabilityMatchesStdlib(t *testing.T) {
	values := []any{
		map[string]pointerOnlyMarshaler{"k": {Value: 1}},
		any(pointerOnlyMarshaler{Value: 1}),
	}
	for _, value := range values {
		want, wantErr := json.Marshal(value)
		got, gotErr := Marshal(&value)
		if (gotErr == nil) != (wantErr == nil) {
			t.Fatalf("%T: acceptance differs: simdjson=%v stdlib=%v", value, gotErr, wantErr)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("%T: simdjson=%s stdlib=%s", value, got, want)
		}
	}

	direct := pointerOnlyMarshaler{Value: 1}
	want, wantErr := json.Marshal(&direct)
	got, gotErr := Marshal(&direct)
	if (gotErr == nil) != (wantErr == nil) || !bytes.Equal(got, want) {
		t.Fatalf("addressable root: simdjson=%s/%v stdlib=%s/%v", got, gotErr, want, wantErr)
	}

	slice := []pointerOnlyMarshaler{{Value: 1}}
	want, wantErr = json.Marshal(&slice)
	got, gotErr = Marshal(&slice)
	if (gotErr == nil) != (wantErr == nil) || !bytes.Equal(got, want) {
		t.Fatalf("addressable slice element: simdjson=%s/%v stdlib=%s/%v", got, gotErr, want, wantErr)
	}

	mixed := mixedAddressMarshaler{}
	want, wantErr = json.Marshal(&mixed)
	got, gotErr = Marshal(&mixed)
	if (gotErr == nil) != (wantErr == nil) || !bytes.Equal(got, want) {
		t.Fatalf("mixed addressable methods: simdjson=%s/%v stdlib=%s/%v", got, gotErr, want, wantErr)
	}
	mixedMap := map[string]mixedAddressMarshaler{"k": {}}
	want, wantErr = json.Marshal(mixedMap)
	got, gotErr = Marshal(&mixedMap)
	if (gotErr == nil) != (wantErr == nil) || !bytes.Equal(got, want) {
		t.Fatalf("mixed non-addressable methods: simdjson=%s/%v stdlib=%s/%v", got, gotErr, want, wantErr)
	}
}
