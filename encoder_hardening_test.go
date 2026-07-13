package simdjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"testing"
)

type encoderPackedNames struct {
	First  int64  `json:"abcdefghijklm"`
	Second int64  `json:"nopqrstuvwxy"`
	Third  int64  `json:"qrstuvwxyzabcd"`
	Fourth string `json:"x<y"`
	Last   bool   `json:"z"`
}

func TestEncoderPackedNamesDoNotWritePastResult(t *testing.T) {
	encoder, err := CompileEncoder[encoderPackedNames](EncoderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !encoder.root.encSimple {
		t.Fatal("test type did not compile as a simple struct")
	}
	packed := 0
	for _, field := range encoder.root.encFields {
		if field.encNameLen != 0 {
			packed++
			if int(field.encNameLen) != len(field.encName) || len(field.encName) > 16 {
				t.Fatalf("invalid packed metadata for %q: len=%d encoded=%d", field.encName, field.encNameLen, len(field.encName))
			}
		}
	}
	if packed < 3 {
		t.Fatalf("only %d names used the packed path", packed)
	}
	if last := encoder.root.encFields[len(encoder.root.encFields)-1]; last.encNameLen != 0 {
		t.Fatalf("short tail name %q unexpectedly uses a wide store", last.encName)
	}

	value := encoderPackedNames{First: -1, Second: 2, Third: 3, Fourth: "<&>", Last: true}
	wantJSON, err := json.Marshal(&value)
	if err != nil {
		t.Fatal(err)
	}
	prefix := []byte("pre:")
	for extra := 0; extra <= 32; extra++ {
		resultLen := len(prefix) + len(wantJSON)
		storage := bytes.Repeat([]byte{0xa5}, resultLen+extra+32)
		copy(storage, prefix)
		dst := storage[: len(prefix) : resultLen+extra]
		got, gotErr := encoder.AppendJSON(dst, &value)
		want := append(append([]byte(nil), prefix...), wantJSON...)
		if gotErr != nil || !bytes.Equal(got, want) {
			t.Fatalf("AppendJSON(extra=%d) = %q, %v, want %q", extra, got, gotErr, want)
		}
		for i, b := range storage[len(got):] {
			if b != 0xa5 {
				t.Fatalf("AppendJSON(extra=%d) wrote past result at byte %d", extra, len(got)+i)
			}
		}
	}
}

type encoderPairLeaf struct {
	N int64 `json:"n"`
}

type encoderPairMarshaler string

func (value encoderPairMarshaler) MarshalJSON() ([]byte, error) {
	return json.Marshal(string(value))
}

type encoderPairMatrix struct {
	A00 string               `json:"a00"`
	B00 string               `json:"b00"`
	A01 []int64              `json:"a01"`
	B01 string               `json:"b01"`
	A02 []int64              `json:"a02"`
	B02 encoderPairLeaf      `json:"b02"`
	A03 []int64              `json:"a03"`
	B03 []int64              `json:"b03"`
	A04 encoderPairLeaf      `json:"a04"`
	B04 encoderPairLeaf      `json:"b04"`
	A05 encoderPairMarshaler `json:"a05"`
	B05 encoderPairMarshaler `json:"b05"`
	A06 encoderPairLeaf      `json:"a06"`
	B06 []int64              `json:"b06"`
	A07 string               `json:"a07"`
	B07 []int64              `json:"b07"`
	A08 encoderPairMarshaler `json:"a08"`
	B08 encoderPairLeaf      `json:"b08"`
	A09 encoderPairMarshaler `json:"a09"`
	B09 string               `json:"b09"`
	A10 encoderPairLeaf      `json:"a10"`
	B10 string               `json:"b10"`
	A11 string               `json:"a11"`
	B11 encoderPairLeaf      `json:"b11"`
	A12 float64              `json:"a12"`
	B12 int64                `json:"b12"`
	A13 uint64               `json:"a13"`
	B13 uint64               `json:"b13"`
	A14 string               `json:"a14"`
	B14 float64              `json:"b14"`
	A15 encoderPairLeaf      `json:"a15"`
	B15 int64                `json:"b15"`
	A16 int64                `json:"a16"`
	B16 int64                `json:"b16"`
	A17 int64                `json:"a17"`
	B17 string               `json:"b17"`
	A18 string               `json:"a18"`
	B18 int64                `json:"b18"`
	A19 int64                `json:"a19"`
	B19 []int64              `json:"b19"`
	A20 []int64              `json:"a20"`
	B20 int64                `json:"b20"`
	A21 []int64              `json:"a21"`
	B21 any                  `json:"b21"`
	A22 any                  `json:"a22"`
	B22 []int64              `json:"b22"`
	A23 any                  `json:"a23"`
	B23 any                  `json:"b23"`
	A24 any                  `json:"a24"`
	B24 int64                `json:"b24"`
	A25 map[string]int64     `json:"a25"`
	B25 map[string]int64     `json:"b25"`
}

func TestEncoderPairMatrixMatchesStdlib(t *testing.T) {
	encoder, err := CompileEncoder[encoderPairMatrix](EncoderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	wantOps := []typedEncPairOp{
		typedEncPairStringString,
		typedEncPairSliceString,
		typedEncPairSliceStruct,
		typedEncPairSliceSlice,
		typedEncPairStructStruct,
		typedEncPairMarshalerMarshaler,
		typedEncPairStructSlice,
		typedEncPairStringSlice,
		typedEncPairMarshalerStruct,
		typedEncPairMarshalerString,
		typedEncPairStructString,
		typedEncPairStringStruct,
		typedEncPairFloat64Int64,
		typedEncPairUint64Uint64,
		typedEncPairStringFloat64,
		typedEncPairStructInt64,
		typedEncPairInt64Int64,
		typedEncPairInt64String,
		typedEncPairStringInt64,
		typedEncPairInt64Slice,
		typedEncPairSliceInt64,
		typedEncPairSliceAny,
		typedEncPairAnySlice,
		typedEncPairAnyAny,
		typedEncPairAnyInt64,
		typedEncPairMapMap,
	}
	if !encoder.root.encSimple || len(encoder.root.encFields) != len(wantOps)*2 {
		t.Fatalf("unexpected pair plan: simple=%v fields=%d", encoder.root.encSimple, len(encoder.root.encFields))
	}
	for i, want := range wantOps {
		if got := encoder.root.encFields[i*2].pairOp; got != want {
			t.Fatalf("pair %d opcode = %d, want %d", i, got, want)
		}
	}

	value := encoderPairMatrix{
		A00: "left", B00: "right",
		A01: []int64{1, -2}, B01: "slice-string",
		A02: []int64{}, B02: encoderPairLeaf{N: 2},
		A03: nil, B03: []int64{3},
		A04: encoderPairLeaf{N: 4}, B04: encoderPairLeaf{N: -4},
		A05: "marshal-a", B05: "marshal-b",
		A06: encoderPairLeaf{N: 6}, B06: []int64{6, 7},
		A07: "string-slice", B07: []int64{},
		A08: "marshal-struct", B08: encoderPairLeaf{N: 8},
		A09: "marshal-string", B09: "plain",
		A10: encoderPairLeaf{N: 10}, B10: "struct-string",
		A11: "string-struct", B11: encoderPairLeaf{N: 11},
		A12: -12.5, B12: -12,
		A13: math.MaxUint64, B13: 13,
		A14: "string-float", B14: 14.25,
		A15: encoderPairLeaf{N: 15}, B15: -15,
		A16: math.MinInt64, B16: math.MaxInt64,
		A17: 17, B17: "int-string",
		A18: "string-int", B18: -18,
		A19: 19, B19: []int64{19},
		A20: []int64{20}, B20: -20,
		A21: []int64{21}, B21: "slice-any",
		A22: int64(22), B22: []int64{22},
		A23: true, B23: 23.5,
		A24: "any-int", B24: 24,
		A25: map[string]int64{"z": 25, "a": -25}, B25: map[string]int64{},
	}
	wantJSON, err := json.Marshal(&value)
	if err != nil {
		t.Fatal(err)
	}
	for _, capacity := range []int{0, 1, len(wantJSON) - 1, len(wantJSON), len(wantJSON) + 32} {
		storage := bytes.Repeat([]byte{0xa5}, capacity+32)
		dst := storage[:0:capacity]
		got, gotErr := encoder.AppendJSON(dst, &value)
		if gotErr != nil || !bytes.Equal(got, wantJSON) {
			t.Fatalf("AppendJSON(cap=%d) = %s, %v, want %s", capacity, got, gotErr, wantJSON)
		}
		for i, b := range storage[capacity:] {
			if b != 0xa5 {
				t.Fatalf("AppendJSON(cap=%d) wrote past capacity at byte %d", capacity, capacity+i)
			}
		}
		if capacity >= len(got) {
			for i, b := range storage[len(got):capacity] {
				if b != 0xa5 {
					t.Fatalf("AppendJSON(cap=%d) wrote past result at byte %d", capacity, len(got)+i)
				}
			}
		}
	}
}

type encoderPairFirstFloatError struct {
	First  float64 `json:"first"`
	Second int64   `json:"second"`
}

type encoderPairSecondFloatError struct {
	First  string  `json:"first"`
	Second float64 `json:"second"`
}

type encoderPairAnyError struct {
	First  any `json:"first"`
	Second any `json:"second"`
}

type encoderPairBadMarshaler struct {
	Bad bool
}

func (value encoderPairBadMarshaler) MarshalJSON() ([]byte, error) {
	if value.Bad {
		return nil, errors.New("pair marshaler failed")
	}
	return []byte(`"ok"`), nil
}

type encoderPairMarshalerError struct {
	First  encoderPairBadMarshaler `json:"first"`
	Second encoderPairBadMarshaler `json:"second"`
}

func TestEncoderPairErrorsReportCorrectField(t *testing.T) {
	requireEncoderErrorPath(t, &encoderPairFirstFloatError{First: math.NaN()}, "first")
	requireEncoderErrorPath(t, &encoderPairSecondFloatError{Second: math.Inf(1)}, "second")
	requireEncoderErrorPath(t, &encoderPairAnyError{First: "ok", Second: make(chan int)}, "second")
	requireEncoderErrorPath(t, &encoderPairMarshalerError{Second: encoderPairBadMarshaler{Bad: true}}, "second")
}

func requireEncoderErrorPath[T any](t *testing.T, value *T, want string) {
	t.Helper()
	encoder, err := CompileEncoder[T](EncoderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = encoder.AppendJSON(nil, value)
	var encodeErr *EncodeError
	if !errors.As(err, &encodeErr) {
		t.Fatalf("AppendJSON error = %v, want *EncodeError", err)
	}
	if encodeErr.Path != want {
		t.Fatalf("AppendJSON error path = %q, want %q (%v)", encodeErr.Path, want, err)
	}
}
