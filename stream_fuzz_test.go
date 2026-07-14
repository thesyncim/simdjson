package simdjson

import (
	"bytes"
	"io"
	"testing"
)

// tornReader yields the source in deterministic pseudo-random chunks,
// including single bytes and reads that return data together with io.EOF —
// every framing an io.Reader is allowed to produce.
type tornReader struct {
	data  []byte
	state uint64
}

func (r *tornReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	r.state ^= r.state << 13
	r.state ^= r.state >> 7
	r.state ^= r.state << 17
	n := 1 + int(r.state%97)
	if n > len(r.data) {
		n = len(r.data)
	}
	if n > len(p) {
		n = len(p)
	}
	copy(p, r.data[:n])
	r.data = r.data[n:]
	if len(r.data) == 0 && r.state&1 == 0 {
		return n, io.EOF
	}
	return n, nil
}

type streamResult struct {
	values [][]byte
	err    error
	offset int64
}

func collectStream(t *testing.T, in io.Reader) streamResult {
	t.Helper()
	reader := NewReaderSize(in, 64)
	var result streamResult
	for reader.Next() {
		value := reader.Bytes()
		if !strictJSONValid(value) {
			t.Fatalf("Reader produced a value that is not strict JSON: %.120q", value)
		}
		result.values = append(result.values, bytes.Clone(value))
	}
	result.err = reader.Err()
	result.offset = reader.InputOffset()
	return result
}

// FuzzStreamReaderChunkEquivalence feeds the same bytes through the stream
// reader whole and torn into arbitrary chunks: the sequence of values, the
// error status, and the final input offset must not depend on framing.
func FuzzStreamReaderChunkEquivalence(f *testing.F) {
	f.Add([]byte("{\"a\":1}\n[2,3]\ntrue\n"), uint64(1))
	f.Add([]byte(`1 2 3`), uint64(7))
	f.Add([]byte("\"split \\uD834\\uDD1E value\"\"back to back\""), uint64(9))
	f.Add([]byte("[1,2"), uint64(3))
	f.Add([]byte("  \r\n\t "), uint64(5))
	f.Add([]byte("nullnull"), uint64(11))
	f.Add(append(bytes.Repeat([]byte("{\"k\":\"vvvvvvvvvvvvvvvv\"}\n"), 40), "0.25\n"...), uint64(13))
	f.Fuzz(func(t *testing.T, data []byte, seed uint64) {
		if len(data) > 1<<15 {
			t.Skip()
		}
		whole := collectStream(t, bytes.NewReader(data))
		torn := collectStream(t, &tornReader{data: data, state: seed | 1})

		if (whole.err == nil) != (torn.err == nil) {
			t.Fatalf("error status depends on framing: whole %v, torn %v", whole.err, torn.err)
		}
		if whole.err != nil && whole.err.Error() != torn.err.Error() {
			t.Fatalf("error depends on framing: whole %q, torn %q", whole.err, torn.err)
		}
		if len(whole.values) != len(torn.values) {
			t.Fatalf("value count depends on framing: whole %d, torn %d", len(whole.values), len(torn.values))
		}
		for i := range whole.values {
			if !bytes.Equal(whole.values[i], torn.values[i]) {
				t.Fatalf("value %d depends on framing: %.80q vs %.80q", i, whole.values[i], torn.values[i])
			}
		}
		// InputOffset is specified only through the end of the current value,
		// so it is compared just for cleanly ended streams.
		if whole.err == nil && whole.offset != torn.offset {
			t.Fatalf("input offset depends on framing: whole %d, torn %d", whole.offset, torn.offset)
		}
	})
}
