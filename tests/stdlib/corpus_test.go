package stdlibcorpus

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"testing"

	"github.com/thesyncim/simdjson"
)

func TestHighLevelCorpus(t *testing.T) {
	for _, name := range Names {
		name := name
		t.Run(name, func(t *testing.T) {
			src, err := Read(name)
			if err != nil {
				t.Fatal(err)
			}
			checkValidation(t, src)
			checkDynamicDecode(t, src)
			checkNumberDecode(t, src)
			checkIndexRoundTrip(t, src)
			checkTypedCorpus(t, name, src)
		})
	}
}

func checkTypedCorpus(t *testing.T, name string, src []byte) {
	t.Helper()
	switch name {
	case "canada_geometry.json.zst":
		checkTyped[canadaRoot](t, src)
	case "citm_catalog.json.zst":
		checkTyped[citmRoot](t, src)
	case "golang_source.json.zst":
		checkTyped[golangRoot](t, src)
	case "string_escaped.json.zst", "string_unicode.json.zst":
		checkTyped[stringRoot](t, src)
	case "synthea_fhir.json.zst":
		checkTyped[syntheaRoot](t, src)
	case "twitter_status.json.zst":
		checkTyped[twitterRoot](t, src)
	default:
		t.Fatalf("stdlib corpus has no concrete model: %s", name)
	}
}

func checkTyped[T any](t *testing.T, src []byte) {
	t.Helper()
	var want T
	if err := json.Unmarshal(src, &want); err != nil {
		t.Fatalf("encoding/json typed decode: %v", err)
	}

	decoder, err := simdjson.CompileDecoder[T](simdjson.DecoderOptions{})
	if err != nil {
		t.Fatalf("simdjson.CompileDecoder: %v", err)
	}
	var got T
	if err := decoder.Decode(src, &got); err != nil {
		t.Fatalf("simdjson typed decode: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("simdjson typed decode result differs from encoding/json")
	}
	zeroCopyDecoder, err := simdjson.CompileDecoder[T](simdjson.DecoderOptions{ZeroCopy: true})
	if err != nil {
		t.Fatalf("simdjson.CompileDecoder zero copy: %v", err)
	}
	var zeroCopy T
	if err := zeroCopyDecoder.Decode(src, &zeroCopy); err != nil {
		t.Fatalf("simdjson typed zero-copy decode: %v", err)
	}
	if !reflect.DeepEqual(zeroCopy, want) {
		t.Fatal("simdjson typed zero-copy decode result differs from encoding/json")
	}

	wantJSON, err := json.Marshal(&want)
	if err != nil {
		t.Fatalf("encoding/json typed encode: %v", err)
	}
	encoder, err := simdjson.CompileEncoder[T](simdjson.EncoderOptions{})
	if err != nil {
		t.Fatalf("simdjson.CompileEncoder: %v", err)
	}
	gotJSON, err := encoder.AppendJSON(nil, &got)
	if err != nil {
		t.Fatalf("simdjson typed encode: %v", err)
	}
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Fatalf("simdjson typed encode differs from encoding/json: got %d bytes, want %d", len(gotJSON), len(wantJSON))
	}
}

func checkValidation(t *testing.T, src []byte) {
	t.Helper()
	if !json.Valid(src) {
		t.Fatal("Go stdlib corpus entry is not valid JSON")
	}
	if !simdjson.Valid(src) {
		t.Fatal("simdjson.Valid rejected valid Go stdlib corpus entry")
	}
	if err := simdjson.Validate(src); err != nil {
		t.Fatalf("simdjson.Validate rejected valid Go stdlib corpus entry: %v", err)
	}
}

func checkDynamicDecode(t *testing.T, src []byte) {
	t.Helper()
	var want any
	if err := json.Unmarshal(src, &want); err != nil {
		t.Fatalf("encoding/json.Unmarshal: %v", err)
	}
	got, err := simdjson.ParseAny(src)
	if err != nil {
		t.Fatalf("simdjson.ParseAny: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("simdjson.ParseAny result differs from encoding/json")
	}
	zeroCopy, err := simdjson.ParseAnyOptions(src, simdjson.AnyOptions{ZeroCopy: true})
	if err != nil {
		t.Fatalf("simdjson.ParseAnyOptions zero copy: %v", err)
	}
	if !reflect.DeepEqual(zeroCopy, want) {
		t.Fatal("simdjson ParseAny zero-copy result differs from encoding/json")
	}

	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("encoding/json.Marshal: %v", err)
	}
	gotJSON, err := simdjson.Marshal(&got)
	if err != nil {
		t.Fatalf("simdjson.Marshal: %v", err)
	}
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Fatal("simdjson.Marshal output differs from encoding/json")
	}
}

func checkNumberDecode(t *testing.T, src []byte) {
	t.Helper()
	stdlibDecoder := json.NewDecoder(bytes.NewReader(src))
	stdlibDecoder.UseNumber()
	var want any
	if err := stdlibDecoder.Decode(&want); err != nil {
		t.Fatalf("encoding/json.Decoder.Decode with UseNumber: %v", err)
	}
	if err := requireEOF(stdlibDecoder); err != nil {
		t.Fatalf("encoding/json.Decoder trailing input: %v", err)
	}

	got, err := simdjson.ParseAnyOptions(src, simdjson.AnyOptions{UseNumber: true})
	if err != nil {
		t.Fatalf("simdjson.ParseAnyOptions with UseNumber: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("simdjson UseNumber result differs from encoding/json")
	}
}

func requireEOF(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if err == io.EOF {
		return nil
	}
	if err == nil {
		return errors.New("unexpected second JSON value")
	}
	return err
}

func checkIndexRoundTrip(t *testing.T, src []byte) {
	t.Helper()
	root, err := simdjson.Parse(src)
	if err != nil {
		t.Fatalf("simdjson.Parse: %v", err)
	}
	got := root.AppendJSON(nil)
	if !json.Valid(got) {
		t.Fatal("Value.AppendJSON produced invalid JSON")
	}

	var wantValue, gotValue any
	if err := json.Unmarshal(src, &wantValue); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatal("Value.AppendJSON changed the decoded value")
	}
}
