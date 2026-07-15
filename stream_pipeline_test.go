package simdjson

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"runtime"
	"strings"
	"testing"
	"time"
)

// newPipeReader builds a Reader in pipelined mode with the given batch size,
// the drop-in way callers turn pipelining on: NewReaderSize + SetPipelined.
func newPipeReader(in io.Reader, batchSize int) *Reader {
	r := NewReaderSize(in, batchSize)
	r.SetPipelined(true)
	return r
}

// newPipeReaderMax is newPipeReader with a per-value size bound.
func newPipeReaderMax(in io.Reader, batchSize, maxValue int) *Reader {
	r := NewReaderSize(in, batchSize)
	r.SetMaxValueBytes(maxValue)
	r.SetPipelined(true)
	return r
}

// pipelineValues drains a pipelined Reader and returns cloned value bytes plus
// the terminal error, mirroring streamValues for the sequential Reader so the
// two framings can be compared for exact equivalence.
func pipelineValues(in io.Reader, batchSize int) ([][]byte, error) {
	r := newPipeReader(in, batchSize)
	defer r.Close()
	var vals [][]byte
	for r.Next() {
		vals = append(vals, bytes.Clone(r.Bytes()))
	}
	return vals, r.Err()
}

// sequentialValues drains the ordinary Reader for the same comparison.
func sequentialValues(in io.Reader, bufSize int) ([][]byte, error) {
	r := NewReaderSize(in, bufSize)
	var vals [][]byte
	for r.Next() {
		vals = append(vals, bytes.Clone(r.Bytes()))
	}
	return vals, r.Err()
}

// TestPipelinedReaderBasic checks the value surface on a clean NDJSON stream.
func TestPipelinedReaderBasic(t *testing.T) {
	data := []byte("[1,2,3]\n{\"a\":\"b\"}\ntrue\nnull\n1.5e10\n\"hi\"\n")
	want := [][]byte{
		[]byte("[1,2,3]"),
		[]byte(`{"a":"b"}`),
		[]byte("true"),
		[]byte("null"),
		[]byte("1.5e10"),
		[]byte(`"hi"`),
	}
	r := newPipeReader(bytes.NewReader(data), 512)
	defer r.Close()
	var got [][]byte
	var offs []int64
	for r.Next() {
		got = append(got, bytes.Clone(r.Bytes()))
		offs = append(offs, r.InputOffset())
	}
	if r.Err() != nil {
		t.Fatalf("Err: %v", r.Err())
	}
	if len(got) != len(want) {
		t.Fatalf("got %d values, want %d", len(got), len(want))
	}
	for i := range want {
		if !bytes.Equal(got[i], want[i]) {
			t.Fatalf("value %d = %q, want %q", i, got[i], want[i])
		}
	}
	// InputOffset must point just past each value in the original input.
	for i, off := range offs {
		if data[off-1] != want[i][len(want[i])-1] {
			t.Fatalf("value %d InputOffset %d does not end the value", i, off)
		}
	}
}

// TestPipelinedReaderDecode exercises the decode + cursor surface.
func TestPipelinedReaderDecode(t *testing.T) {
	var data bytes.Buffer
	enc, _ := CompileEncoder[streamRecord](EncoderOptions{})
	w := NewWriter(&data)
	const n = 300
	for i := 0; i < n; i++ {
		v := streamRecordAt(i)
		EncodeTo(w, enc, &v)
		w.Newline()
	}
	w.Close()

	dec, _ := CompileDecoder[streamRecord](DecoderOptions{ZeroCopy: true})
	r := newPipeReader(bytes.NewReader(data.Bytes()), 4096)
	defer r.Close()
	got := 0
	var rec streamRecord
	for DecodeNext(r, dec, &rec) {
		if rec != streamRecordAt(got) {
			t.Fatalf("record %d = %+v, want %+v", got, rec, streamRecordAt(got))
		}
		got++
	}
	if r.Err() != nil {
		t.Fatalf("Err: %v", r.Err())
	}
	if got != n {
		t.Fatalf("decoded %d records, want %d", got, n)
	}
}

// TestPipelinedReaderCursor drives the forward cursor over each value.
func TestPipelinedReaderCursor(t *testing.T) {
	data := eventStreamNDJSON(200)
	var want, got walkSums
	{
		r := NewReader(bytes.NewReader(data))
		for r.Next() {
			c := r.Cursor()
			if err := cursorWalkValue(&c, &want); err != nil {
				t.Fatal(err)
			}
		}
		if r.Err() != nil {
			t.Fatal(r.Err())
		}
	}
	r := newPipeReader(bytes.NewReader(data), 8192)
	defer r.Close()
	for r.Next() {
		c := r.Cursor()
		if err := cursorWalkValue(&c, &got); err != nil {
			t.Fatal(err)
		}
	}
	if r.Err() != nil {
		t.Fatal(r.Err())
	}
	if got != want {
		t.Fatalf("pipelined walk digest %+v != sequential %+v", got, want)
	}
}

// TestPipelinedReaderDifferential is the core correctness gate: the pipelined
// Reader must yield the exact same value sequence and terminal error as the
// sequential Reader over the adversarial corpus, whole and torn into small
// chunks, at several batch sizes.
func TestPipelinedReaderDifferential(t *testing.T) {
	batchSizes := []int{512, 1024, 4096, 64 << 10}
	chunks := []int{1, 2, 3, 7, 13, 64, 512}
	for ci, data := range adversarialStreamCorpus() {
		for _, bs := range batchSizes {
			seq, seqErr := sequentialValues(bytes.NewReader(data), 512)
			for _, cs := range chunks {
				pip, pipErr := pipelineValues(&fixedChunkReader{data: append([]byte(nil), data...), chunk: cs}, bs)
				if (seqErr == nil) != (pipErr == nil) {
					t.Fatalf("corpus %d bs=%d chunk=%d: error status differs seq=%v pip=%v", ci, bs, cs, seqErr, pipErr)
				}
				if len(seq) != len(pip) {
					t.Fatalf("corpus %d bs=%d chunk=%d: value count seq=%d pip=%d", ci, bs, cs, len(seq), len(pip))
				}
				for i := range seq {
					if !bytes.Equal(seq[i], pip[i]) {
						t.Fatalf("corpus %d bs=%d chunk=%d value %d differs:\nseq %.80q\npip %.80q", ci, bs, cs, i, seq[i], pip[i])
					}
				}
			}
		}
	}
}

// FuzzPipelinedReaderDifferential is the permanent differential fuzzer: for any
// input and any chunking, the pipelined Reader's value sequence and error
// status must match the sequential Reader's exactly.
func FuzzPipelinedReaderDifferential(f *testing.F) {
	for _, c := range adversarialStreamCorpus() {
		f.Add(c, uint8(1), uint16(512))
		f.Add(c, uint8(5), uint16(1024))
	}
	f.Add([]byte("1 2 3 4 5"), uint8(1), uint16(512))
	f.Add([]byte(`{"a":1}{"b":2}`), uint8(2), uint16(512))
	f.Fuzz(func(t *testing.T, data []byte, chunk uint8, bs uint16) {
		if len(data) == 0 || len(data) > 1<<16 {
			t.Skip()
		}
		cs := 1 + int(chunk%23)
		batch := 512 + int(bs)

		seq, seqErr := sequentialValues(&fixedChunkReader{data: append([]byte(nil), data...), chunk: cs}, 512)
		pip, pipErr := pipelineValues(&fixedChunkReader{data: append([]byte(nil), data...), chunk: cs}, batch)

		if (seqErr == nil) != (pipErr == nil) {
			t.Fatalf("error status differs: seq=%v pip=%v (chunk=%d bs=%d)", seqErr, pipErr, cs, batch)
		}
		if len(seq) != len(pip) {
			t.Fatalf("value count differs: seq=%d pip=%d (chunk=%d bs=%d)", len(seq), len(pip), cs, batch)
		}
		for i := range seq {
			if !bytes.Equal(seq[i], pip[i]) {
				t.Fatalf("value %d differs:\nseq %.80q\npip %.80q", i, seq[i], pip[i])
			}
		}
		// On a clean stream every framed value must be structurally valid JSON.
		if seqErr == nil {
			for i, v := range pip {
				if !json.Valid(v) {
					t.Fatalf("value %d not valid JSON: %.80q", i, v)
				}
			}
		}
	})
}

// TestPipelinedReaderOversizedValue checks worker-local buffer growth: a value
// larger than a batch grows only that batch's buffer.
func TestPipelinedReaderOversizedValue(t *testing.T) {
	big := `"` + strings.Repeat("a", 200_000) + `"`
	data := "1\n" + big + "\n2\n"
	r := newPipeReader(&fixedChunkReader{data: []byte(data), chunk: 1000}, 4096)
	defer r.Close()
	var got []string
	for r.Next() {
		got = append(got, string(r.Bytes()))
	}
	if r.Err() != nil {
		t.Fatalf("Err: %v", r.Err())
	}
	want := []string{"1", big, "2"}
	if len(got) != len(want) {
		t.Fatalf("got %d values, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("value %d mismatch (len %d vs %d)", i, len(got[i]), len(want[i]))
		}
	}
}

// TestPipelinedReaderMaxValue bounds a single value's growth.
func TestPipelinedReaderMaxValue(t *testing.T) {
	big := `"` + strings.Repeat("a", 100_000) + `"`
	data := "1\n" + big + "\n"
	r := newPipeReaderMax(bytes.NewReader([]byte(data)), 4096, 50_000)
	defer r.Close()
	var count int
	for r.Next() {
		count++
	}
	if r.Err() == nil {
		t.Fatal("expected a max-value error, got nil")
	}
	if count != 1 {
		t.Fatalf("delivered %d values before the limit, want 1", count)
	}
	if !strings.Contains(r.Err().Error(), "exceeds") {
		t.Fatalf("error %q does not mention the limit", r.Err())
	}
}

// TestPipelinedReaderInvalid checks that an invalid value stops the stream
// after the valid values that preceded it, with the same offset framing.
func TestPipelinedReaderInvalid(t *testing.T) {
	data := []byte("1\n2\n{bad}\n3\n")
	r := newPipeReader(bytes.NewReader(data), 512)
	defer r.Close()
	var got []string
	for r.Next() {
		got = append(got, string(r.Bytes()))
	}
	if r.Err() == nil {
		t.Fatal("expected an error on {bad}")
	}
	if len(got) != 2 || got[0] != "1" || got[1] != "2" {
		t.Fatalf("delivered %v before the error, want [1 2]", got)
	}
}

// latencyReader wraps an io.Reader and sleeps before each Read, modeling a
// source with non-trivial read latency (a socket, a slow pipe) so the pipeline
// has IO to overlap with decode.
type latencyReader struct {
	r     io.Reader
	delay time.Duration
}

func (l *latencyReader) Read(p []byte) (int, error) {
	time.Sleep(l.delay)
	return l.r.Read(p)
}

// TestPipelinedReaderNoGoroutineLeak proves Close releases the worker even when
// the reader is abandoned mid-stream while the worker is blocked in Read or on
// a channel send. Run under -race to also catch buffer races.
func TestPipelinedReaderNoGoroutineLeak(t *testing.T) {
	data := eventStreamNDJSON(2000)
	settle := func() {
		for i := 0; i < 50; i++ {
			runtime.GC()
			time.Sleep(2 * time.Millisecond)
		}
	}
	settle()
	base := runtime.NumGoroutine()

	for _, tc := range []struct {
		name    string
		consume int // values to read before abandoning
		delay   time.Duration
	}{
		{"drain", -1, 0},
		{"abandon-early", 3, 0},
		{"abandon-mid", 500, 0},
		{"abandon-blocked-in-read", 1, 5 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := io.Reader(bytes.NewReader(data))
			if tc.delay > 0 {
				src = &latencyReader{r: src, delay: tc.delay}
			}
			r := newPipeReader(src, 4096)
			n := 0
			for r.Next() {
				n++
				if tc.consume >= 0 && n >= tc.consume {
					break
				}
			}
			if err := r.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			// Close again: must be idempotent.
			if err := r.Close(); err != nil {
				t.Fatalf("second Close: %v", err)
			}
		})
	}

	settle()
	if leaked := runtime.NumGoroutine() - base; leaked > 0 {
		t.Fatalf("leaked %d goroutines (base %d, now %d)", leaked, base, runtime.NumGoroutine())
	}
}

// TestPipelinedReaderSteadyStateNoAlloc proves the framing/handoff path
// allocates nothing per value in steady state: a single long-lived reader
// draining a large stream must not allocate on the value-delivery path (Next,
// buffer recycling, extent recording). Any allocation would come from the
// worker re-growing buffers or failing to reuse the free list.
func TestPipelinedReaderSteadyStateNoAlloc(t *testing.T) {
	data := eventStreamNDJSON(4000)
	// Warm the reader past the first two batches so the free list is primed and
	// buffers have reached their steady size, then measure a long drain.
	r := newPipeReader(&repeatReader{data: data, n: 1 << 20}, 64<<10)
	defer r.Close()
	for i := 0; i < 5000 && r.Next(); i++ {
	}
	avg := testing.AllocsPerRun(200000, func() {
		if !r.Next() {
			t.Fatal("stream ended during the alloc probe")
		}
		_ = r.Bytes()
	})
	if avg > 0 {
		t.Fatalf("steady-state Next allocates %.3f objects/value, want 0", avg)
	}
}

// TestPipelinedReaderBufio confirms the pipeline composes with a bufio.Reader
// wrapping the source: the Reader accepts any io.Reader, so a caller who already
// buffers with bufio just hands it in. bufio.Reader is synchronous and cannot
// overlap reads with decode on its own — that is what the worker adds — but it
// coalesces small underlying reads, and the two layers must agree on framing.
func TestPipelinedReaderBufio(t *testing.T) {
	data := eventStreamNDJSON(500)
	seq, seqErr := sequentialValues(bytes.NewReader(data), 4096)

	// A bufio.Reader over a byte-dribbling source: bufio hides the tiny reads,
	// the pipeline frames whole values off bufio's buffer.
	src := bufio.NewReaderSize(&fixedChunkReader{data: append([]byte(nil), data...), chunk: 7}, 1024)
	pip, pipErr := pipelineValues(src, 8192)

	if (seqErr == nil) != (pipErr == nil) {
		t.Fatalf("error status differs: seq=%v pip=%v", seqErr, pipErr)
	}
	if len(seq) != len(pip) {
		t.Fatalf("value count seq=%d pip=%d", len(seq), len(pip))
	}
	for i := range seq {
		if !bytes.Equal(seq[i], pip[i]) {
			t.Fatalf("value %d differs through bufio", i)
		}
	}
}

// TestPipelinedReaderReadError propagates a mid-stream read error after the
// values that arrived before it.
func TestPipelinedReaderReadError(t *testing.T) {
	boom := fmt.Errorf("boom")
	data := []byte("1\n2\n3\n")
	src := &errAfterReader{data: data, err: boom}
	r := newPipeReader(src, 512)
	defer r.Close()
	var got []string
	for r.Next() {
		got = append(got, string(r.Bytes()))
	}
	if r.Err() == nil || !strings.Contains(r.Err().Error(), "boom") {
		t.Fatalf("Err = %v, want boom", r.Err())
	}
	if strings.Join(got, ",") != "1,2,3" {
		t.Fatalf("got %v before the error, want [1 2 3]", got)
	}
}

// errAfterReader returns all data, then the given error (not EOF).
type errAfterReader struct {
	data []byte
	err  error
	done bool
}

func (e *errAfterReader) Read(p []byte) (int, error) {
	if e.done {
		return 0, e.err
	}
	n := copy(p, e.data)
	e.data = e.data[n:]
	if len(e.data) == 0 {
		e.done = true
	}
	return n, nil
}

// chunkLatencyReader serves data in fixed-size chunks, sleeping before each
// chunk to model a source whose reads carry non-trivial latency (a socket, a
// slow pipe). The chunk cap keeps the per-read delay realistic: a single 64KiB
// Read would otherwise deliver the whole batch in one sleep.
type chunkLatencyReader struct {
	data  []byte
	pos   int
	chunk int
	delay time.Duration
}

func (c *chunkLatencyReader) Read(p []byte) (int, error) {
	if c.pos >= len(c.data) {
		return 0, io.EOF
	}
	if c.delay > 0 {
		time.Sleep(c.delay)
	}
	n := c.chunk
	if n > len(p) {
		n = len(p)
	}
	if c.pos+n > len(c.data) {
		n = len(c.data) - c.pos
	}
	copy(p, c.data[c.pos:c.pos+n])
	c.pos += n
	return n, nil
}

// benchDecodeHeavyNDJSON builds a decode-heavy NDJSON corpus large enough that
// each 64KiB batch carries real decode work to overlap with reads.
func benchDecodeHeavyNDJSON() []byte {
	var buf bytes.Buffer
	enc, _ := CompileEncoder[streamRecord](EncoderOptions{})
	w := NewWriter(&buf)
	for i := 0; i < 8000; i++ {
		v := streamRecordAt(i)
		EncodeTo(w, enc, &v)
		w.Newline()
	}
	w.Close()
	return buf.Bytes()
}

// BenchmarkPipelineDecode compares the sequential DecodeNext against the
// pipelined DecodeNext on decode-heavy NDJSON at a 64KiB batch, both
// with a zero-latency bytes.Reader and with a per-read-latency source. The
// pipeline can only win when there is read latency to overlap with decode; the
// zero-latency case measures its overhead. Interleave the A/B pair with
// -count>=10 and read the ratio, not the absolute time, on a busy machine.
func BenchmarkPipelineDecode(b *testing.B) {
	data := benchDecodeHeavyNDJSON()
	const batch = 64 << 10
	// 4KiB chunks: at batch=64KiB the worker issues ~16 reads per batch, so the
	// per-read latency accumulates to a batch-scale stall the pipeline hides.
	const chunk = 4 << 10

	newSrc := func(delay time.Duration) io.Reader {
		if delay == 0 {
			return bytes.NewReader(data)
		}
		return &chunkLatencyReader{data: data, chunk: chunk, delay: delay}
	}

	dec, _ := CompileDecoder[streamRecord](DecoderOptions{ZeroCopy: true})
	for _, lat := range []struct {
		name  string
		delay time.Duration
	}{
		{"NoLatency", 0},
		{"Latency20us", 20 * time.Microsecond},
	} {
		b.Run(lat.name+"/Sequential", func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			b.ReportAllocs()
			for range b.N {
				r := NewReaderSize(newSrc(lat.delay), batch)
				var rec streamRecord
				n := 0
				for DecodeNext(r, dec, &rec) {
					n++
				}
				if r.Err() != nil {
					b.Fatal(r.Err())
				}
				if n == 0 {
					b.Fatal("no records")
				}
			}
		})

		b.Run(lat.name+"/Pipelined", func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			b.ReportAllocs()
			for range b.N {
				r := newPipeReader(newSrc(lat.delay), batch)
				var rec streamRecord
				n := 0
				for DecodeNext(r, dec, &rec) {
					n++
				}
				if r.Err() != nil {
					r.Close()
					b.Fatal(r.Err())
				}
				r.Close()
				if n == 0 {
					b.Fatal("no records")
				}
			}
		})
	}
}

// BenchmarkPipelineDecodeHeavy is BenchmarkPipelineDecode with a heavier
// per-value consumer (decode into map[string]any), so decode time is a larger
// share of the per-batch cost and the overlap with read latency is more
// visible. This is the case the pipeline targets: decode-bound consumption of a
// latency-bound source.
//
// The latency source delivers one batch-sized read per sleep, modeling a socket
// or pipe that hands over a large segment per syscall (rather than dribbling
// tiny chunks, which would make raw IO dominate and leave no decode to overlap).
// With batch-sized reads the per-batch read wait is comparable to the per-batch
// decode, which is where pipelining overlaps the two and wins.
func BenchmarkPipelineDecodeHeavy(b *testing.B) {
	data := benchDecodeHeavyNDJSON()
	const batch = 64 << 10
	newSrc := func(delay time.Duration) io.Reader {
		if delay == 0 {
			return bytes.NewReader(data)
		}
		return &chunkLatencyReader{data: data, chunk: batch, delay: delay}
	}
	dec, _ := CompileDecoder[map[string]any](DecoderOptions{ZeroCopy: true})

	for _, lat := range []struct {
		name  string
		delay time.Duration
	}{
		{"NoLatency", 0},
		{"Latency20us", 20 * time.Microsecond},
		{"Latency100us", 100 * time.Microsecond},
	} {
		b.Run(lat.name+"/Sequential", func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			b.ReportAllocs()
			for range b.N {
				r := NewReaderSize(newSrc(lat.delay), batch)
				var m map[string]any
				n := 0
				for DecodeNext(r, dec, &m) {
					n += len(m)
				}
				if r.Err() != nil {
					b.Fatal(r.Err())
				}
				if n == 0 {
					b.Fatal("no fields")
				}
			}
		})
		b.Run(lat.name+"/Pipelined", func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			b.ReportAllocs()
			for range b.N {
				r := newPipeReader(newSrc(lat.delay), batch)
				var m map[string]any
				n := 0
				for DecodeNext(r, dec, &m) {
					n += len(m)
				}
				if r.Err() != nil {
					r.Close()
					b.Fatal(r.Err())
				}
				r.Close()
				if n == 0 {
					b.Fatal("no fields")
				}
			}
		})
	}
}

// BenchmarkPipelineSteadyAlloc drains one long-lived reader over a large stream
// and reports allocations, isolating per-value/per-batch steady-state cost from
// the one-time goroutine-spawn cost that a fresh reader per iteration charges.
// Steady state must allocate only what decode itself allocates.
func BenchmarkPipelineSteadyAlloc(b *testing.B) {
	data := benchDecodeHeavyNDJSON()
	dec, _ := CompileDecoder[streamRecord](DecoderOptions{ZeroCopy: true})
	b.Run("Sequential", func(b *testing.B) {
		b.ReportAllocs()
		var rec streamRecord
		for range b.N {
			r := NewReaderSize(bytes.NewReader(data), 64<<10)
			for DecodeNext(r, dec, &rec) {
			}
			if r.Err() != nil {
				b.Fatal(r.Err())
			}
		}
	})
	b.Run("Pipelined", func(b *testing.B) {
		b.ReportAllocs()
		var rec streamRecord
		// One reader, drained b.N times' worth of a repeated stream, so the
		// goroutine spawn is amortized and per-value allocation is what remains.
		r := newPipeReader(&repeatReader{data: data, n: b.N}, 64<<10)
		defer r.Close()
		for DecodeNext(r, dec, &rec) {
		}
		if r.Err() != nil {
			b.Fatal(r.Err())
		}
	})
}

// repeatReader serves data back to back n times, one long stream, so a single
// reader amortizes the worker goroutine over many values.
type repeatReader struct {
	data []byte
	n    int
	i    int
	pos  int
}

func (r *repeatReader) Read(p []byte) (int, error) {
	if r.i >= r.n {
		return 0, io.EOF
	}
	if r.pos >= len(r.data) {
		r.pos = 0
		r.i++
		if r.i >= r.n {
			return 0, io.EOF
		}
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}
