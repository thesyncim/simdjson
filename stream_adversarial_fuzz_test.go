package simdjson

import (
	"bytes"
	"encoding/json"
	"io"
	"testing"
)

// adversarialStreamCorpus builds streaming inputs engineered to stress the
// resumable framer at chunk boundaries: dense escape runs, escaped quotes and
// backslashes, huge string bodies, deeply nested containers, and brackets that
// live inside strings so only correct in-string tracking frames them.
func adversarialStreamCorpus() [][]byte {
	bigStr := append(append([]byte{'"'}, bytes.Repeat([]byte("x"), 5000)...), '"')
	escRun := append(append([]byte{'"'}, bytes.Repeat([]byte(`\\`), 1000)...), '"')
	escQuotes := append(append([]byte{'"'}, bytes.Repeat([]byte(`a\"b`), 500)...), '"')
	deep := append(bytes.Repeat([]byte("["), 400), bytes.Repeat([]byte("]"), 400)...)
	deepObj := append(bytes.Repeat([]byte(`{"k":`), 200), append([]byte("0"), bytes.Repeat([]byte("}"), 200)...)...)
	bracketsInStr := []byte(`{"a":"}]}]}]","b":[{"c":"[[[["}]}`)
	unicodeEsc := append(append([]byte{'"'}, bytes.Repeat([]byte(`𝄞`), 100)...), '"')
	trailingBackslashes := append(append([]byte{'"'}, bytes.Repeat([]byte(`\\`), 999)...), '\\', '"', '"')
	corpus := [][]byte{
		bigStr,
		escRun,
		escQuotes,
		deep,
		deepObj,
		bracketsInStr,
		unicodeEsc,
		trailingBackslashes,
		[]byte(`"\\\\\\\\\\\\\\\\\\\\\\\\\\\\\\\\"`),
		[]byte("[1,2,3]\n{\"a\":\"b\\\"c\"}\ntrue\nnull\n1.5e300\n"),
		bytes.Repeat([]byte(`{"k":"v"}`+"\n"), 50),
	}
	// Concatenations of several adversarial values back to back, no separators.
	var joined []byte
	for _, c := range corpus {
		joined = append(joined, c...)
	}
	corpus = append(corpus, joined)
	return corpus
}

// streamValues drains a Reader and returns the cloned value bytes plus the
// terminal error, so two framings can be compared for exact equivalence.
func streamValues(t *testing.T, in io.Reader, bufSize int) ([][]byte, error) {
	t.Helper()
	r := NewReaderSize(in, bufSize)
	var vals [][]byte
	for r.Next() {
		vals = append(vals, bytes.Clone(r.Bytes()))
	}
	return vals, r.Err()
}

// FuzzStreamFramerAdversarial feeds each corpus value (and fuzzer-mutated
// variants) through the reader whole and in fixed one/small-byte chunks with a
// tiny buffer, forcing the resumable framer to resume across the middle of
// escapes, long strings, and deep nesting. The framed value sequence must be
// identical regardless of chunking, and every framed value that the reader
// reports must be accepted by encoding/json when the stream ends cleanly.
func FuzzStreamFramerAdversarial(f *testing.F) {
	for _, c := range adversarialStreamCorpus() {
		f.Add(c, uint8(1))
		f.Add(c, uint8(3))
	}
	f.Fuzz(func(t *testing.T, data []byte, chunk uint8) {
		if len(data) == 0 || len(data) > 1<<16 {
			t.Skip()
		}
		cs := 1 + int(chunk%17)

		whole, wErr := streamValues(t, bytes.NewReader(data), 512)
		torn, tErr := streamValues(t, &fixedChunkReader{data: append([]byte(nil), data...), chunk: cs}, 512)

		if (wErr == nil) != (tErr == nil) {
			t.Fatalf("error status depends on chunking: whole=%v torn=%v", wErr, tErr)
		}
		if wErr != nil && tErr != nil && wErr.Error() != tErr.Error() {
			t.Fatalf("error text depends on chunking: whole=%q torn=%q", wErr, tErr)
		}
		if len(whole) != len(torn) {
			t.Fatalf("value count depends on chunking: whole=%d torn=%d", len(whole), len(torn))
		}
		for i := range whole {
			if !bytes.Equal(whole[i], torn[i]) {
				t.Fatalf("value %d differs by chunking:\nwhole %.100q\ntorn  %.100q", i, whole[i], torn[i])
			}
		}
		// Cross-check framing against the standard library: on a clean stream,
		// every value the framer isolated must be exactly one structurally valid
		// JSON value. json.Valid is the right oracle here — it checks structure
		// without imposing Go numeric range limits, so a syntactically valid but
		// out-of-float64-range number like 1.5e7000 is (correctly) accepted.
		if wErr == nil {
			for i, v := range whole {
				if !json.Valid(v) {
					t.Fatalf("framed value %d not structurally valid JSON: %.80q", i, v)
				}
			}
		}
	})
}
