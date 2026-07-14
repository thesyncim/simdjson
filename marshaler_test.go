package simdjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

var retainedCustomReceiver *retainingCustomReceiver

type retainingCustomReceiver struct {
	Value int
}

func (v *retainingCustomReceiver) MarshalJSON() ([]byte, error) {
	retainedCustomReceiver = v
	encodedValue := v.Value
	v.Value++
	return fmt.Appendf(nil, `{"value":%d}`, encodedValue), nil
}

func (v *retainingCustomReceiver) UnmarshalJSON(data []byte) error {
	retainedCustomReceiver = v
	var wire struct {
		Value int `json:"value"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	v.Value = wire.Value
	return nil
}

//go:noinline
func encodeRetainingCustomReceiver() ([]byte, int, error) {
	value := retainingCustomReceiver{Value: 41}
	encoded, err := Marshal(&value)
	return encoded, value.Value, err
}

//go:noinline
func decodeRetainingCustomReceiver() (int, error) {
	var value retainingCustomReceiver
	err := Unmarshal([]byte(`{"value":42}`), &value)
	return value.Value, err
}

//go:noinline
func encodeRetainingCustomPointerReceiver() ([]byte, int, error) {
	value := retainingCustomReceiver{Value: 45}
	pointer := &value
	encoded, err := Marshal(&pointer)
	return encoded, value.Value, err
}

//go:noinline
func decodeRetainingCustomPointerReceiver() (int, error) {
	value := retainingCustomReceiver{Value: -1}
	pointer := &value
	err := Unmarshal([]byte(`{"value":46}`), &pointer)
	return value.Value, err
}

//go:noinline
func decodeNilRetainingCustomPointerReceiver() (*retainingCustomReceiver, error) {
	var pointer *retainingCustomReceiver
	err := Unmarshal([]byte(`{"value":47}`), &pointer)
	return pointer, err
}

//go:noinline
func growRetentionTestStack(depth int) int {
	var scratch [2048]byte
	scratch[depth%len(scratch)] = byte(depth)
	if depth == 0 {
		return int(scratch[0])
	}
	return int(scratch[depth%len(scratch)]) + growRetentionTestStack(depth-1)
}

func TestCustomMethodReceiversMayBeRetained(t *testing.T) {
	retainedCustomReceiver = nil
	encoded, copiedBack, err := encodeRetainingCustomReceiver()
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"value":41}` || copiedBack != 42 || retainedCustomReceiver == nil {
		t.Fatalf("encoded = %s, retained = %p", encoded, retainedCustomReceiver)
	}
	encodedReceiver := retainedCustomReceiver
	_ = growRetentionTestStack(128)
	runtime.GC()
	if encodedReceiver.Value != 42 {
		t.Fatalf("retained marshal receiver value = %d, want 42", encodedReceiver.Value)
	}
	encodedReceiver.Value = 43

	retainedCustomReceiver = nil
	decodedValue, err := decodeRetainingCustomReceiver()
	if err != nil {
		t.Fatal(err)
	}
	if decodedValue != 42 {
		t.Fatalf("copied-back unmarshal value = %d, want 42", decodedValue)
	}
	decodedReceiver := retainedCustomReceiver
	if decodedReceiver == nil {
		t.Fatal("custom unmarshal receiver was not retained")
	}
	_ = growRetentionTestStack(128)
	runtime.GC()
	if decodedReceiver.Value != 42 {
		t.Fatalf("retained unmarshal receiver value = %d, want 42", decodedReceiver.Value)
	}
	decodedReceiver.Value = 44

	retainedCustomReceiver = nil
	encoded, copiedBack, err = encodeRetainingCustomPointerReceiver()
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"value":45}` || copiedBack != 46 || retainedCustomReceiver == nil {
		t.Fatalf("pointer encoded = %s, copied back = %d, retained = %p", encoded, copiedBack, retainedCustomReceiver)
	}
	_ = growRetentionTestStack(128)
	runtime.GC()
	if retainedCustomReceiver.Value != 46 {
		t.Fatalf("retained pointer marshal receiver value = %d, want 46", retainedCustomReceiver.Value)
	}

	retainedCustomReceiver = nil
	decodedValue, err = decodeRetainingCustomPointerReceiver()
	if err != nil {
		t.Fatal(err)
	}
	if decodedValue != 46 || retainedCustomReceiver == nil {
		t.Fatalf("pointer decoded = %d, retained = %p", decodedValue, retainedCustomReceiver)
	}
	_ = growRetentionTestStack(128)
	runtime.GC()
	if retainedCustomReceiver.Value != 46 {
		t.Fatalf("retained pointer unmarshal receiver value = %d, want 46", retainedCustomReceiver.Value)
	}

	retainedCustomReceiver = nil
	decodedPointer, err := decodeNilRetainingCustomPointerReceiver()
	if err != nil {
		t.Fatal(err)
	}
	if decodedPointer == nil || decodedPointer.Value != 47 || retainedCustomReceiver == nil {
		t.Fatalf("nil pointer decoded = %p, retained = %p", decodedPointer, retainedCustomReceiver)
	}
	if decodedPointer == retainedCustomReceiver {
		t.Fatal("custom method received the destination pointer instead of a safe shadow")
	}
	retainedCustomReceiver.Value = 48
	if decodedPointer.Value != 47 {
		t.Fatalf("post-return receiver mutation changed destination to %d", decodedPointer.Value)
	}
}

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

type staticValueMarshaler int

var staticValueJSON = []byte("7")

func (v staticValueMarshaler) MarshalJSON() ([]byte, error) {
	if v != 7 {
		return nil, errors.New("unexpected static value")
	}
	return staticValueJSON, nil
}

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

func TestCompiledValueMarshalerScratchAllocs(t *testing.T) {
	type document struct {
		Value staticValueMarshaler `json:"value"`
	}
	encoder, err := CompileEncoder[document](EncoderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	value := document{Value: 7}
	buffer := make([]byte, 0, 32)
	allocs := testing.AllocsPerRun(100, func() {
		out, encodeErr := encoder.AppendJSON(buffer[:0], &value)
		if encodeErr != nil || string(out) != `{"value":7}` {
			t.Fatalf("AppendJSON = %s, %v", out, encodeErr)
		}
	})
	if allocs != 0 {
		t.Fatalf("value marshaler allocations = %v, want 0", allocs)
	}
	scratch := encoder.scratch.Get().(*encoderScratch)
	if !scratch.marshalers[0].value.IsZero() {
		t.Fatal("pooled marshaler scratch retained the encoded value")
	}
	encoder.scratch.Put(scratch)

	var wait sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			local := make([]byte, 0, 32)
			for range 100 {
				out, encodeErr := encoder.AppendJSON(local[:0], &value)
				if encodeErr != nil {
					errs <- encodeErr
					return
				}
				if string(out) != `{"value":7}` {
					errs <- fmt.Errorf("unexpected concurrent output %q", out)
					return
				}
			}
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
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
		assertEncodesLikeStdlib(t, &value)
	}

	sources := []string{
		`{"when":"2026-07-10T22:30:00.123456789Z","when_ptr":"2001-02-03T04:05:06Z","point":"7;8","box":{"any":[1,2]},"boxes":["x",17],"by_key":{"k":"1;1"}}`,
		`{"when":null,"when_ptr":null,"point":null,"box":null}`,
		`{"when":"not a time"}`,
		`{"point":"bad"}`,
		`{"point":17}`,
	}
	for _, src := range sources {
		assertDecodesLikeStdlib[marshalerDocument](t, []byte(src))
	}

	// Round trip through both libraries.
	decoder, err := CompileDecoder[marshalerDocument](DecoderOptions{})
	if err != nil {
		t.Fatal(err)
	}
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
