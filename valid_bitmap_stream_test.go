package simdjson

import (
	"bytes"
	"encoding/json"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	simdkernels "github.com/thesyncim/simdjson/simd"
)

// streamedOracle compares the batched engine against the per-block engine
// and the scalar Validate on one input. Both engines must agree on decided
// and, when decided, on the verdict. On arm64 validBitmap dispatches to the
// batched engine, so this is the consumer-level differential that guards the
// two classification paths against divergence.
func streamedOracle(t *testing.T, src []byte, label string) {
	t.Helper()
	refOK, refDecided := validBitmapPerBlock(src)
	gpOK, gpDecided := validBitmapStreamed(src)
	if gpDecided != refDecided {
		t.Fatalf("%s: decided mismatch: perBlock=%v streamed=%v (len %d)\n%.200q",
			label, refDecided, gpDecided, len(src), src)
	}
	if !refDecided {
		return
	}
	want := Validate(src) == nil
	if refOK != want || gpOK != want {
		t.Fatalf("%s: verdict mismatch: perBlock=%v streamed=%v, Validate=%v (len %d)\n%.200q",
			label, refOK, gpOK, want, len(src), src)
	}
}

func TestValidBitmapStreamedMatchesScalarOnTestSuite(t *testing.T) {
	if !simdkernels.Stage1StreamEnabled() {
		t.Skip("stage-1 stream kernel not built")
	}
	entries, err := os.ReadDir(jsonTestSuiteDir)
	if err != nil {
		t.Skip("JSONTestSuite corpus not present")
	}
	indent := "\n" + strings.Repeat(" ", 10)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(jsonTestSuiteDir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		streamedOracle(t, data, entry.Name())

		var wrapped bytes.Buffer
		wrapped.WriteString("[")
		for range 8 {
			wrapped.WriteString(indent)
			wrapped.Write(data)
			wrapped.WriteString(",")
		}
		wrapped.WriteString(indent)
		wrapped.Write(data)
		wrapped.WriteString("\n]")
		streamedOracle(t, wrapped.Bytes(), "wrapped "+entry.Name())
	}
}

func TestValidBitmapStreamedMutations(t *testing.T) {
	if !simdkernels.Stage1StreamEnabled() {
		t.Skip("stage-1 stream kernel not built")
	}
	doc := buildBitmapTestDocument(t)
	streamedOracle(t, doc, "base document")
	if ok, decided := validBitmapStreamed(doc); !decided || !ok {
		t.Fatalf("base document: ok=%v decided=%v", ok, decided)
	}

	rng := rand.New(rand.NewPCG(11, 17))
	for mutants := 0; mutants < 8000; mutants++ {
		mutated := append([]byte(nil), doc...)
		switch rng.IntN(4) {
		case 0:
			pos := rng.IntN(len(mutated))
			mutated[pos] = byte(rng.IntN(256))
		case 1:
			pos := rng.IntN(len(mutated))
			hostile := []byte(`"\{}[]:,0x eEtfn.+-` + "\x00\x1f\x80\xe2\xff")
			mutated[pos] = hostile[rng.IntN(len(hostile))]
		case 2:
			pos := rng.IntN(len(mutated))
			mutated = append(mutated[:pos], mutated[pos+1:]...)
		case 3:
			mutated = mutated[:rng.IntN(len(mutated))]
		}
		streamedOracle(t, mutated, "mutant")
	}
}

// buildWhitespaceHeavyDoc constructs the deterministic pretty-printed
// benchmark document: nested records with strings, escapes, numbers,
// and literals under the given indent.
func buildWhitespaceHeavyDoc(tb testing.TB, indent string) []byte {
	type entry struct {
		Name    string    `json:"name"`
		Text    string    `json:"text"`
		Value   float64   `json:"value"`
		Count   int64     `json:"count"`
		Flag    bool      `json:"flag"`
		Nothing *int      `json:"nothing"`
		Scores  []float64 `json:"scores"`
		Tags    []string  `json:"tags"`
	}
	rng := rand.New(rand.NewPCG(21, 42))
	texts := []string{
		"plain ascii words in a sentence", "tab\tand\nnewline", `quote " backslash \ slash /`,
		"payload with more text than structure", " leading and trailing ", "<html> & entities >",
	}
	var entries []entry
	for len(entries) < 500 {
		entries = append(entries, entry{
			Name:   texts[rng.IntN(len(texts))],
			Text:   texts[rng.IntN(len(texts))],
			Value:  rng.Float64() * 1e6,
			Count:  rng.Int64(),
			Flag:   rng.IntN(2) == 0,
			Scores: []float64{rng.Float64(), -rng.Float64() * 1e-7, 0, 1e21},
			Tags:   []string{"alpha", "beta", "gamma"},
		})
	}
	doc, err := json.MarshalIndent(entries, "", indent)
	if err != nil {
		tb.Fatal(err)
	}
	if len(doc) < validBitmapMinBytes {
		tb.Fatalf("benchmark document too small: %d", len(doc))
	}
	return doc
}

// benchmarkBitmapEngines runs both engines interleaved-by-count on one
// document, after asserting that every engine commits and agrees.
func benchmarkBitmapEngines(b *testing.B, doc []byte) {
	if !simdkernels.Stage1StreamEnabled() {
		b.Skip("stage-1 stream kernel not built")
	}
	if ok, decided := validBitmapPerBlock(doc); !decided || !ok {
		b.Fatalf("per-block engine: ok=%v decided=%v (len %d)", ok, decided, len(doc))
	}
	if ok, decided := validBitmapStreamed(doc); !decided || !ok {
		b.Fatal("streamed engine did not commit")
	}
	b.Run("perBlock", func(b *testing.B) {
		b.SetBytes(int64(len(doc)))
		for i := 0; i < b.N; i++ {
			if ok, _ := validBitmapPerBlock(doc); !ok {
				b.Fatal("invalid")
			}
		}
	})
	b.Run("batchedGP", func(b *testing.B) {
		b.SetBytes(int64(len(doc)))
		for i := 0; i < b.N; i++ {
			if ok, _ := validBitmapStreamed(doc); !ok {
				b.Fatal("invalid")
			}
		}
	})
}

// buildNestedTwoSpaceDoc builds the borderline document: 2-space indent
// with deeper nesting, denser emits per block, just above the engine's
// commitment ratio.
func buildNestedTwoSpaceDoc(tb testing.TB) []byte {
	type group struct {
		Label   string           `json:"label"`
		Entries []map[string]any `json:"entries"`
	}
	var groups []group
	for len(groups) < 400 {
		groups = append(groups, group{
			Label: "group with a plain label",
			Entries: []map[string]any{
				{"name": "first item name", "value": 12345.25, "flag": true},
				{"name": "second item with text", "note": "quote \" and backslash \\", "count": 7},
			},
		})
	}
	doc, err := json.MarshalIndent(map[string]any{"groups": groups}, "", "  ")
	if err != nil {
		tb.Fatal(err)
	}
	if len(doc) < validBitmapMinBytes {
		tb.Fatalf("benchmark document too small: %d", len(doc))
	}
	return doc
}

// BenchmarkValidBitmapIndent4: deep 4-space indentation, the engine's
// home turf (whitespace dominates, sparse emits).
func BenchmarkValidBitmapIndent4(b *testing.B) {
	benchmarkBitmapEngines(b, buildWhitespaceHeavyDoc(b, "    "))
}

// BenchmarkValidBitmapNested2: 2-space indent, nested containers,
// denser emits, near the engine's commitment boundary.
func BenchmarkValidBitmapNested2(b *testing.B) {
	benchmarkBitmapEngines(b, buildNestedTwoSpaceDoc(b))
}
