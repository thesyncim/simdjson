package simdjson

import (
	"strings"
	"testing"
)

type hookIntegrityValid int

func (v hookIntegrityValid) MarshalSimdJSON(w TrustedAppender) TrustedAppender {
	return w.Int(int64(v))
}

type hookIntegrityInvalid struct{}

func (hookIntegrityInvalid) MarshalSimdJSON(w TrustedAppender) TrustedAppender {
	return w.RawUnchecked(`{"unterminated"`)
}

func TestHookIntegrityValidationIsSpanLocal(t *testing.T) {
	type document struct {
		Head int                `json:"head"`
		Hook hookIntegrityValid `json:"hook"`
		Tail int                `json:"tail"`
	}
	enc, err := CompileEncoder[document](EncoderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	value := document{Head: 1, Hook: 2, Tail: 3}
	got, err := enc.AppendJSON(nil, &value)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"head":1,"hook":2,"tail":3}`; string(got) != want {
		t.Fatalf("AppendJSON = %s, want %s", got, want)
	}
}

func TestHookIntegrityValidationMode(t *testing.T) {
	type document struct {
		Hook hookIntegrityInvalid `json:"hook"`
	}
	enc, err := CompileEncoder[document](EncoderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	value := document{}
	got, err := enc.AppendJSON(nil, &value)
	if validateSimdHookOutput {
		if err == nil || !strings.Contains(err.Error(), "MarshalSimdJSON produced invalid JSON") {
			t.Fatalf("debug validation = %q, %v, want invalid-hook error", got, err)
		}
		return
	}
	if err != nil {
		t.Fatalf("production unchecked hook returned error: %v", err)
	}
	if Valid(got) {
		t.Fatalf("unchecked malformed hook unexpectedly produced valid JSON: %s", got)
	}
}
