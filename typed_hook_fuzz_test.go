package simdjson

import (
	stdjson "encoding/json"
	"testing"
)

// FuzzHookMatchesReflection is the hook-vs-reflection differential fuzzer. For
// arbitrary input it decodes the same bytes through the hook type and its plain
// reflection twin and requires: identical acceptance, identical decoded value
// (compared through the projection), and — for accepted input — byte-identical
// re-encoding on the hook path, the reflection path, and encoding/json. Any
// hook body bug (a missed member order, a mishandled null, a scalar edge, a
// duplicate key resolved differently) diverges from the reflection oracle and
// fails. Both case-sensitivity modes are covered.
//
// Run at least 60s:
//
//	GOEXPERIMENT=simd gotip test -run x -fuzz FuzzHookMatchesReflection -fuzztime 60s ./
func FuzzHookMatchesReflection(f *testing.F) {
	for _, doc := range adversarialHookDocs() {
		f.Add([]byte(doc), true)
		f.Add([]byte(doc), false)
	}
	f.Add([]byte(`{}`), true)
	f.Add([]byte(`null`), true)
	f.Add([]byte(`{"ADDRESS":{"STREET":"x"},"ID":9}`), false)
	f.Add([]byte(`{"id":1,"id":2,"id":3}`), true)
	f.Add([]byte(`{"ſcore":2.5}`), false)
	f.Add([]byte(`{"nic\u212Aname":"kelvin-fold"}`), false)

	build := func(cs bool) (Decoder[hookPerson], Decoder[hookPersonPlain]) {
		opts := DecoderOptions{CaseSensitive: cs}
		hd, err := CompileDecoder[hookPerson](opts)
		if err != nil {
			f.Fatal(err)
		}
		pd, err := CompileDecoder[hookPersonPlain](opts)
		if err != nil {
			f.Fatal(err)
		}
		return hd, pd
	}
	hookDecCS, plainDecCS := build(true)
	hookDecCI, plainDecCI := build(false)

	hookEnc, err := CompileEncoder[hookPerson](EncoderOptions{})
	if err != nil {
		f.Fatal(err)
	}
	plainEnc, err := CompileEncoder[hookPersonPlain](EncoderOptions{})
	if err != nil {
		f.Fatal(err)
	}

	f.Fuzz(func(t *testing.T, src []byte, caseSensitive bool) {
		if len(src) > 1<<14 || !Valid(src) {
			return
		}
		hookDec, plainDec := hookDecCS, plainDecCS
		if !caseSensitive {
			hookDec, plainDec = hookDecCI, plainDecCI
		}

		var viaHook hookPerson
		hookErr := hookDec.Decode(src, &viaHook)
		var viaPlain hookPersonPlain
		plainErr := plainDec.Decode(src, &viaPlain)

		if (hookErr == nil) != (plainErr == nil) {
			t.Fatalf("acceptance differs (cs=%v): hook=%v plain=%v\nsrc=%s", caseSensitive, hookErr, plainErr, src)
		}
		if hookErr != nil {
			return
		}
		if !hookPersonEqual(projectHook(viaHook), viaPlain) {
			t.Fatalf("decoded value differs (cs=%v):\n hook=%+v\nplain=%+v\nsrc=%s", caseSensitive, projectHook(viaHook), viaPlain, src)
		}

		// Re-encode the decoded value three ways and require byte equality.
		hookOut, hookEncErr := hookEnc.AppendJSON(nil, &viaHook)
		plainOut, plainEncErr := plainEnc.AppendJSON(nil, &viaPlain)
		if (hookEncErr == nil) != (plainEncErr == nil) {
			t.Fatalf("encode acceptance differs: hook=%v plain=%v", hookEncErr, plainEncErr)
		}
		if hookEncErr != nil {
			return
		}
		if string(hookOut) != string(plainOut) {
			t.Fatalf("hook vs reflection encode differ:\n hook=%s\nplain=%s", hookOut, plainOut)
		}
		stdOut, err := stdjson.Marshal(&viaPlain)
		if err != nil {
			t.Fatalf("stdlib marshal: %v", err)
		}
		if string(hookOut) != string(stdOut) {
			t.Fatalf("hook vs stdlib encode differ:\n hook=%s\n  std=%s", hookOut, stdOut)
		}
	})
}
