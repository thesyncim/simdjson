package simdjson

// Probes for streaming API edge cases: pathological io.Readers, Writer state
// machine and emitter parity, DecodeNext error handling, and SetMaxValueBytes
// boundary behavior. Each test pins an invariant that once failed; the
// fixtures are the minimized reproductions.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"
)

// scriptedReader plays back a fixed sequence of Read results. Each step
// returns its bytes together with its error, exercising the io.Reader
// allowance of returning data and an error from the same call. When the
// script is exhausted it returns (0, finalErr), io.EOF by default.
type scriptStep struct {
	data []byte
	err  error
}

type scriptedReader struct {
	steps    []scriptStep
	finalErr error
}

func (r *scriptedReader) Read(p []byte) (int, error) {
	if len(r.steps) == 0 {
		if r.finalErr != nil {
			return 0, r.finalErr
		}
		return 0, io.EOF
	}
	step := r.steps[0]
	n := copy(p, step.data)
	if n < len(step.data) {
		r.steps[0].data = step.data[n:]
		return n, nil
	}
	r.steps = r.steps[1:]
	return n, step.err
}

func collectValues(r *Reader) []string {
	var got []string
	for r.Next() {
		got = append(got, string(r.Bytes()))
	}
	return got
}

// --- Attack surface 1: pathological io.Readers ---------------------------

// Values arriving in the same Read call as a non-EOF error must be
// delivered before the error surfaces: the io.Reader contract puts the
// n > 0 bytes first, and encoding/json's Decoder (the baseline below)
// behaves that way.
func TestProbeReaderValueArrivingWithSameReadError(t *testing.T) {
	boom := errors.New("boom")
	payload := []byte(`{"a":1}` + "\n" + `{"a":2}` + "\n")

	// Baseline: encoding/json delivers both values, then reports the error.
	std := json.NewDecoder(&scriptedReader{steps: []scriptStep{{data: payload, err: boom}}, finalErr: boom})
	stdValues := 0
	var stdErr error
	for {
		var v map[string]any
		if err := std.Decode(&v); err != nil {
			stdErr = err
			break
		}
		stdValues++
	}
	if stdValues != 2 || stdErr != boom {
		t.Fatalf("stdlib baseline: %d values, err %v", stdValues, stdErr)
	}

	r := NewReaderSize(&scriptedReader{steps: []scriptStep{{data: payload, err: boom}}, finalErr: boom}, 512)
	got := collectValues(r)
	if len(got) != 2 {
		t.Errorf("values arriving in the same Read as a non-EOF error were dropped: got %d values %q, want 2", len(got), got)
	}
	if !errors.Is(r.Err(), boom) {
		t.Errorf("Err() = %v, want %v", r.Err(), boom)
	}
}

// The same delivery rule holds when the erroring Read completes a value
// split across earlier calls, including io.ErrUnexpectedEOF as the error.
func TestProbeReaderValueArrivingWithLaterReadError(t *testing.T) {
	for _, tail := range []error{errors.New("boom"), io.ErrUnexpectedEOF} {
		r := NewReaderSize(&scriptedReader{steps: []scriptStep{
			{data: []byte(`{"a":1}` + "\n" + `{"a`)},
			{data: []byte(`":2}` + "\n"), err: tail},
		}, finalErr: tail}, 512)
		got := collectValues(r)
		if len(got) != 2 {
			t.Errorf("err=%v: got %d values %q, want 2 (second value completed by the erroring Read)", tail, len(got), got)
		}
		if !errors.Is(r.Err(), tail) {
			t.Errorf("Err() = %v, want %v", r.Err(), tail)
		}
	}
}

// A reader may return (0, nil) any number of times; the Reader must neither
// error spuriously nor lose data. (bufio gives up after 100 with
// io.ErrNoProgress; this Reader retries indefinitely, which also means a
// reader that returns (0, nil) forever would spin — same as io.Copy.)
func TestProbeReaderZeroByteNilReads(t *testing.T) {
	payload := `{"a":1}` + "\n" + `{"a":2}` + "\n"
	var steps []scriptStep
	for i := 0; i < 300; i++ { // more than bufio's 100 tolerance
		steps = append(steps, scriptStep{})
	}
	for i := 0; i < len(payload); i++ {
		steps = append(steps, scriptStep{}, scriptStep{data: []byte{payload[i]}}, scriptStep{})
	}
	r := NewReaderSize(&scriptedReader{steps: steps}, 512)
	got := collectValues(r)
	if r.Err() != nil {
		t.Fatalf("spurious error from (0, nil) reads: %v", r.Err())
	}
	if len(got) != 2 || got[0] != `{"a":1}` || got[1] != `{"a":2}` {
		t.Fatalf("got %q", got)
	}
}

// One byte per Read with io.EOF attached exactly at the value boundary.
func TestProbeReaderOneBytePerReadEOFAtBoundary(t *testing.T) {
	payload := `{"a":1}` + "\n" + "42"
	var steps []scriptStep
	for i := 0; i < len(payload); i++ {
		step := scriptStep{data: []byte{payload[i]}}
		if i == len(payload)-1 {
			step.err = io.EOF
		}
		steps = append(steps, step)
	}
	r := NewReaderSize(&scriptedReader{steps: steps}, 512)
	got := collectValues(r)
	if r.Err() != nil {
		t.Fatalf("unexpected error: %v", r.Err())
	}
	if len(got) != 2 || got[1] != "42" {
		t.Fatalf("got %q", got)
	}
}

type panicAfterReader struct {
	inner    io.Reader
	panicOn  int
	calls    int
	panicked bool
}

func (r *panicAfterReader) Read(p []byte) (int, error) {
	r.calls++
	if r.calls == r.panicOn && !r.panicked {
		r.panicked = true
		panic("probe: reader panic")
	}
	return r.inner.Read(p)
}

// A panicking source must propagate (not be swallowed) and must not corrupt
// reader state observed afterwards.
func TestProbeReaderSourcePanicPropagates(t *testing.T) {
	src := &panicAfterReader{inner: strings.NewReader(`{"a":1}` + "\n" + `{"a":2}` + "\n"), panicOn: 2}
	r := NewReaderSize(src, 512)
	if !r.Next() || string(r.Bytes()) != `{"a":1}` {
		t.Fatalf("first value: %q err=%v", r.Bytes(), r.Err())
	}
	if !r.Next() || string(r.Bytes()) != `{"a":2}` {
		t.Fatalf("second value: %q err=%v", r.Bytes(), r.Err())
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("source panic did not propagate through Next")
			}
		}()
		r.Next() // needs a refill; Read call #2 panics
		t.Fatal("Next returned instead of panicking")
	}()
	// After the panic the reader must still behave consistently: the retry
	// reaches EOF and the stream ends cleanly.
	if r.Next() {
		t.Fatalf("unexpected value after panic: %q", r.Bytes())
	}
	if r.Err() != nil {
		t.Fatalf("unexpected error after panic: %v", r.Err())
	}
}

// A value of exactly SetMaxValueBytes bytes is delivered regardless of
// whether io.EOF arrives attached to its final bytes: only values LONGER
// than the limit stop the stream, independent of framing.
func TestProbeReaderMaxValueExactLimitFramingIndependence(t *testing.T) {
	val := `{"k":"` + strings.Repeat("a", 504) + `"}` // exactly 512 bytes
	if len(val) != 512 {
		t.Fatal("fixture size")
	}

	run := func(steps []scriptStep) ([]string, error) {
		r := NewReaderSize(&scriptedReader{steps: steps}, 512)
		r.SetMaxValueBytes(512)
		got := collectValues(r)
		return got, r.Err()
	}
	// Framing A: data, then a separate (0, io.EOF).
	gotA, errA := run([]scriptStep{{data: []byte(val)}})
	// Framing B: data with io.EOF attached.
	gotB, errB := run([]scriptStep{{data: []byte(val), err: io.EOF}})

	if (errA == nil) != (errB == nil) || len(gotA) != len(gotB) {
		t.Errorf("outcome depends on framing:\n separate EOF: values=%d err=%v\n attached EOF: values=%d err=%v",
			len(gotA), errA, len(gotB), errB)
	}
}

// SetMaxValueBytes documents "a longer value stops the stream
// with an error", but the limit is only checked when the buffer is full, so
// any value that fits inside the current buffer is delivered no matter how
// far over the limit it is.
func TestProbeReaderMaxValueEnforcedBelowBufferSize(t *testing.T) {
	val := `{"k":"` + strings.Repeat("a", 992) + `"}` // 1000 bytes
	r := NewReaderSize(strings.NewReader(val+"\n"), 4096)
	r.SetMaxValueBytes(100)
	if r.Next() {
		t.Errorf("value of %d bytes delivered despite a %d byte limit", len(val), 100)
	} else if r.Err() == nil || !strings.Contains(r.Err().Error(), "limit") {
		t.Errorf("expected limit error, got %v", r.Err())
	}
}

// Reader configuration freezes when input consumption begins.
func TestProbeReaderMaxValueRejectedAfterStart(t *testing.T) {
	r := NewReaderSize(strings.NewReader(`{"a":1}`+"\n"+`{"a":2}`+"\n"), 512)
	if !r.Next() {
		t.Fatalf("first value: %v", r.Err())
	}
	if err := r.SetMaxValueBytes(1); !errors.Is(err, ErrReaderStarted) {
		t.Fatalf("SetMaxValueBytes = %v, want ErrReaderStarted", err)
	}
	if !r.Next() || string(r.Bytes()) != `{"a":2}` {
		t.Fatalf("second value = %q, Err = %v", r.Bytes(), r.Err())
	}
}

// When the trailing Next (the one that discovers the clean
// end) compacts the buffer, consumed advances while valEnd keeps its stale
// pre-compaction coordinate, so InputOffset can exceed the total number of
// input bytes and varies with buffer geometry.
func TestProbeReaderInputOffsetAfterCleanEnd(t *testing.T) {
	val := `{"k":"` + strings.Repeat("a", 503) + `"}` // 511 bytes
	data := val + "\n"                                // 512 bytes: fills the 512-byte buffer exactly

	offsets := map[int]int64{}
	for _, size := range []int{512, 1024} {
		r := NewReaderSize(strings.NewReader(data), size)
		if !r.Next() {
			t.Fatalf("size %d: %v", size, r.Err())
		}
		if off := r.InputOffset(); off != int64(len(val)) {
			t.Fatalf("size %d: offset after value = %d, want %d", size, off, len(val))
		}
		if r.Next() || r.Err() != nil {
			t.Fatalf("size %d: expected clean end, err=%v", size, r.Err())
		}
		offsets[size] = r.InputOffset()
	}
	for size, off := range offsets {
		if off > int64(len(data)) {
			t.Errorf("buffer %d: InputOffset after clean end = %d, exceeds total input length %d", size, off, len(data))
		}
	}
	if offsets[512] != offsets[1024] {
		t.Errorf("InputOffset after clean end depends on buffer size: %d vs %d", offsets[512], offsets[1024])
	}
}

// --- Attack surface 3: DecodeNext / DecodeFrom ------------------------------

type streamProbeRec struct {
	A int `json:"a"`
}

// A decode error mid-stream (valid JSON, wrong shape for the decoder) must
// terminate the stream with a positioned error — no silent skip — and leave
// Err sticky for both DecodeNext and Next.
func TestProbeDecodeNextTypeMismatchMidStream(t *testing.T) {
	dec, err := CompileDecoder[streamProbeRec](DecoderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	data := `{"a":1}` + "\n" + `"nope"` + "\n" + `{"a":3}` + "\n"
	r := NewReaderSize(strings.NewReader(data), 512)

	var v streamProbeRec
	if err := DecodeFrom(r, dec, &v); err == nil {
		t.Fatal("DecodeFrom before Next must error")
	}
	if !DecodeNext(r, dec, &v) || v.A != 1 {
		t.Fatalf("first value: %+v err=%v", v, r.Err())
	}
	if off := r.InputOffset(); off != 7 {
		t.Fatalf("offset after first value = %d", off)
	}
	if DecodeNext(r, dec, &v) {
		t.Fatal("mismatched value must not decode")
	}
	if r.Err() == nil || !strings.Contains(r.Err().Error(), "offset 8") {
		t.Fatalf("expected error positioned at offset 8, got %v", r.Err())
	}
	if DecodeNext(r, dec, &v) || r.Next() {
		t.Fatal("stream must be terminally errored, not silently skipping")
	}
	if off := r.InputOffset(); off != 7 {
		// Not asserted as a failure: after an error there is no current
		// value, so the doc makes no promise. Recorded for visibility.
		t.Logf("note: InputOffset after mid-stream decode error = %d (end of last good value is 7)", off)
	}
}

// The positioned error message must survive buffer compaction (r.consumed
// accounting) when the offending value is larger than the remaining buffer.
func TestProbeDecodeNextErrorOffsetAfterCompaction(t *testing.T) {
	dec, err := CompileDecoder[streamProbeRec](DecoderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	bad := `{"a":"` + strings.Repeat("x", 600) + `"}` // string where int expected, forces compaction+growth in a 512 buffer
	data := `{"a":1}` + "\n" + bad + "\n"
	r := NewReaderSize(strings.NewReader(data), 512)
	var v streamProbeRec
	if !DecodeNext(r, dec, &v) || v.A != 1 {
		t.Fatalf("first value: %+v err=%v", v, r.Err())
	}
	if DecodeNext(r, dec, &v) {
		t.Fatal("mismatched value must not decode")
	}
	if r.Err() == nil || !strings.Contains(r.Err().Error(), "offset 8") {
		t.Fatalf("expected error positioned at offset 8 after compaction, got %v", r.Err())
	}
}

// DecodeNext and Next may be alternated freely on one Reader.
func TestProbeAlternatingNextAndDecodeNext(t *testing.T) {
	dec, err := CompileDecoder[streamProbeRec](DecoderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&data, "{\"a\":%d}\n", i)
	}
	r := NewReaderSize(&chunkReader{data: data.Bytes(), chunk: 3}, 512)
	for i := 0; i < 40; i++ {
		if i%2 == 0 {
			var v streamProbeRec
			if !DecodeNext(r, dec, &v) {
				t.Fatalf("row %d: %v", i, r.Err())
			}
			if v.A != i {
				t.Fatalf("row %d: got %+v", i, v)
			}
			if want := fmt.Sprintf(`{"a":%d}`, i); string(r.Bytes()) != want {
				t.Fatalf("row %d: Bytes %q want %q", i, r.Bytes(), want)
			}
		} else {
			if !r.Next() {
				t.Fatalf("row %d: %v", i, r.Err())
			}
			var v streamProbeRec
			if err := DecodeFrom(r, dec, &v); err != nil || v.A != i {
				t.Fatalf("row %d: %+v err=%v", i, v, err)
			}
		}
	}
	if r.Next() || r.Err() != nil {
		t.Fatalf("expected clean end, err=%v", r.Err())
	}
}

// DecodeNext on a value beyond SetMaxValueBytes must stop with the limit
// error rather than growing without bound or spinning.
func TestProbeDecodeNextOverMaxValue(t *testing.T) {
	dec, err := CompileDecoder[streamProbeRec](DecoderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	big := `{"a":` + strings.Repeat("1", 2000) + `}`
	r := NewReaderSize(strings.NewReader(big+"\n"), 512)
	r.SetMaxValueBytes(512)
	var v streamProbeRec
	if DecodeNext(r, dec, &v) {
		t.Fatal("oversized value must not decode")
	}
	if r.Err() == nil || !strings.Contains(r.Err().Error(), "limit") {
		t.Fatalf("expected limit error, got %v", r.Err())
	}
}

// --- Attack surface 2 and 5: Writer state machine and emitters -----------

// Flush moves buffered bytes to the sink without framing values: a second
// top-level value still requires Newline, and the guard error must not
// advertise Flush as an escape hatch.
func TestProbeWriterFlushDoesNotFrameValues(t *testing.T) {
	var out bytes.Buffer
	w := NewWriter(&out)
	if err := w.Int(1); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	err := w.Int(2)
	if err == nil {
		t.Fatal("second top-level value after Flush was accepted; adjacent numbers would merge")
	}
	if strings.Contains(err.Error(), "Flush") {
		t.Fatalf("guard error advertises Flush, which does not frame values: %v", err)
	}
}

// EncodeTo participates in the token layer's top-level framing: mixing a
// token-built scalar with EncodeTo must error rather than merge two numbers
// into one value.
func TestProbeWriterEncodeToBypassesTopLevelGuard(t *testing.T) {
	enc, err := CompileEncoder[int](EncoderOptions{})
	if err != nil {
		t.Skipf("scalar root encoder unavailable: %v", err)
	}
	var out bytes.Buffer
	w := NewWriter(&out)
	if err := w.Int(1); err != nil {
		t.Fatal(err)
	}
	two := 2
	errEnc := EncodeTo(w, enc, &two)
	errTok := w.Int(3) // started was reset by EncodeTo, so this is accepted too
	if err := w.Flush(); err != nil && errEnc == nil && errTok == nil {
		t.Fatal(err)
	}
	if errEnc == nil && errTok == nil {
		r := NewReader(bytes.NewReader(out.Bytes()))
		got := collectValues(r)
		t.Errorf("token value + EncodeTo + token value produced no error and wrote %q, which reads back as %d value(s) %q instead of 3 values 1, 2, 3",
			out.String(), len(got), got)
	}
}

// Non-finite floats must error like Marshal, and the error must be sticky.
func TestProbeWriterNonFiniteFloats(t *testing.T) {
	for _, v := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		var out bytes.Buffer
		w := NewWriter(&out)
		if err := w.Float64(v); err == nil {
			t.Fatalf("Float(%v) must error like Marshal", v)
		}
		if w.Err() == nil {
			t.Fatalf("Float(%v): error not sticky", v)
		}
		if err := w.Int(1); err == nil {
			t.Fatalf("Float(%v): writer usable after error", v)
		}
	}
}

// String must match Marshal byte for byte, including invalid UTF-8
// replacement, control escapes, and U+2028/U+2029.
func TestProbeWriterStringParity(t *testing.T) {
	cases := []string{
		"",
		"\xff",
		"a\xffb\xfe",
		string([]byte{0xed, 0xa0, 0x80}), // lone surrogate bytes
		"\x00\x1f\x7f",
		"héllo wörld",
		" line sep",
		"<script>alert(1)&</script>",
		strings.Repeat("é", 300) + "\"quote\\back",
		"tab\tnl\ncr\rbs\bff\f",
		strings.Repeat("clean ascii ", 100),
	}
	for _, escape := range []bool{true, false} {
		for _, s := range cases {
			var out bytes.Buffer
			w := NewWriter(&out)
			w.SetEscapeHTML(escape)
			if err := w.String(s); err != nil {
				t.Fatalf("escape=%v %q: %v", escape, s, err)
			}
			if err := w.Flush(); err != nil {
				t.Fatal(err)
			}
			var wantBuf bytes.Buffer
			stdenc := json.NewEncoder(&wantBuf)
			stdenc.SetEscapeHTML(escape)
			if err := stdenc.Encode(s); err != nil {
				t.Fatal(err)
			}
			want := strings.TrimSuffix(wantBuf.String(), "\n")
			if out.String() != want {
				t.Errorf("escape=%v input %q:\n got %s\nwant %s", escape, s, out.String(), want)
			}
		}
	}
}

// Integer emitters at the boundaries.
func TestProbeWriterIntegerBoundaries(t *testing.T) {
	ints := []int64{math.MinInt64, math.MinInt64 + 1, -1, 0, 1, math.MaxInt64}
	for _, v := range ints {
		var out bytes.Buffer
		w := NewWriter(&out)
		if err := w.Int(v); err != nil {
			t.Fatal(err)
		}
		w.Flush()
		if want := strconv.FormatInt(v, 10); out.String() != want {
			t.Errorf("Int(%d) = %s, want %s", v, out.String(), want)
		}
	}
	uints := []uint64{0, 1, math.MaxInt64, math.MaxInt64 + 1, math.MaxUint64}
	for _, v := range uints {
		var out bytes.Buffer
		w := NewWriter(&out)
		if err := w.Uint(v); err != nil {
			t.Fatal(err)
		}
		w.Flush()
		if want := strconv.FormatUint(v, 10); out.String() != want {
			t.Errorf("Uint(%d) = %s, want %s", v, out.String(), want)
		}
	}
}

// Float spelling parity with Marshal on boundary values.
func TestProbeWriterFloatParity(t *testing.T) {
	values := []float64{
		math.Copysign(0, -1), 0, 0.1, -0.1, 1e-6, 5e-324, 1e15, 1e15 - 2,
		1e20, 1e21, 1.5e21, math.MaxFloat64, -math.MaxFloat64, 2.2250738585072014e-308,
	}
	for _, v := range values {
		var out bytes.Buffer
		w := NewWriter(&out)
		if err := w.Float64(v); err != nil {
			t.Fatal(err)
		}
		w.Flush()
		want, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if out.String() != string(want) {
			t.Errorf("Float(%v) = %s, want %s", v, out.String(), want)
		}
	}
}

// Time parity with Marshal, including the prefix/date cache paths (repeated
// second, same day, changed zone) and the out-of-range year error.
func TestProbeWriterTimeParity(t *testing.T) {
	base := time.Date(2026, 7, 14, 12, 0, 0, 987654321, time.UTC)
	zone := time.FixedZone("probe", 5*3600+1800)
	times := []time.Time{
		base,
		base,                      // same second: prefix cache path
		base.Add(3 * time.Second), // same day: date cache path
		base.In(zone),             // same absolute second, different zone
		base.Add(26 * time.Hour),  // next day
		time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC),
	}
	var out bytes.Buffer
	w := NewWriter(&out) // one writer so the internal TimeCache carries across values
	for i, tm := range times {
		out.Reset()
		w.buf = w.buf[:0]
		if err := w.Time(tm); err != nil {
			t.Fatalf("time %d (%v): %v", i, tm, err)
		}
		if err := w.Flush(); err != nil {
			t.Fatal(err)
		}
		w.started = false // fresh top-level slot without disturbing the cache
		want, err := json.Marshal(tm)
		if err != nil {
			t.Fatal(err)
		}
		if out.String() != string(want) {
			t.Errorf("time %d (%v): got %s, want %s", i, tm, out.String(), want)
		}
	}

	var out2 bytes.Buffer
	w2 := NewWriter(&out2)
	if err := w2.Time(time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)); err == nil {
		t.Fatal("year 10000 must error like Marshal")
	}
	if err := w2.Int(1); err == nil {
		t.Fatal("time error not sticky")
	}
}

// Close with unclosed containers must error, for both kinds.
func TestProbeWriterCloseUnfinishedValue(t *testing.T) {
	for _, open := range []func(w *Writer) error{(*Writer).BeginObject, (*Writer).BeginArray} {
		var out bytes.Buffer
		w := NewWriter(&out)
		if err := open(w); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err == nil {
			t.Fatal("Close with an unclosed container must error")
		}
		if w.Err() == nil {
			t.Fatal("Close error not sticky")
		}
	}
}

// shortWriteSink violates the io.Writer contract by accepting one byte per
// call with a nil error; the Writer must convert that to io.ErrShortWrite
// rather than dropping the tail.
type shortWriteSink struct{ got []byte }

func (s *shortWriteSink) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	s.got = append(s.got, p[0])
	return 1, nil
}

func TestProbeWriterShortWriteSink(t *testing.T) {
	sink := &shortWriteSink{}
	w := NewWriterSize(sink, 512)
	if err := w.String("hello world"); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("Close = %v, want io.ErrShortWrite", err)
	}
	if w.Err() == nil {
		t.Fatal("short write not sticky")
	}
}

// A sink error must surface at Close even when nothing crossed the flush
// threshold beforehand — no silent loss of a buffered value.
func TestProbeWriterSinkErrorSurfacesAtClose(t *testing.T) {
	w := NewWriter(&failingWriter{after: 0}) // default 32K threshold: no mid-stream flush
	if err := w.String("buffered"); err != nil {
		t.Fatal(err)
	}
	if err := w.Newline(); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err == nil {
		t.Fatal("Close must report the sink error")
	}
}

// Values whose ends straddle the flush threshold at many alignments, with
// escapes and multi-byte runes near the boundary: output must match
// encoding/json exactly and flushes must never split inside a value.
type recordingSink struct {
	bytes.Buffer
	writes []int
}

func (s *recordingSink) Write(p []byte) (int, error) {
	s.writes = append(s.writes, len(p))
	return s.Buffer.Write(p)
}

func TestProbeWriterFlushBoundaryEscapes(t *testing.T) {
	sink := &recordingSink{}
	w := NewWriterSize(sink, 512)
	var want bytes.Buffer
	stdenc := json.NewEncoder(&want)
	for i := 0; i < 300; i++ {
		s := strings.Repeat("é", i%7) + "\"\\<& " + strings.Repeat("x", i%13) + "\xff"
		if err := w.String(s); err != nil {
			t.Fatal(err)
		}
		if err := w.Newline(); err != nil {
			t.Fatal(err)
		}
		if err := stdenc.Encode(s); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(sink.Bytes(), want.Bytes()) {
		t.Fatalf("output diverges from encoding/json across %d flushes", len(sink.writes))
	}
	// Every value written must also read back intact.
	r := NewReaderSize(bytes.NewReader(sink.Bytes()), 512)
	count := 0
	for r.Next() {
		count++
	}
	if r.Err() != nil || count != 300 {
		t.Fatalf("re-read: %d values, err=%v", count, r.Err())
	}
}

// Writer output (tokens, EncodeTo, Raw mixed) re-read by Reader must yield
// the same values byte for byte.
func TestProbeWriterReaderRoundTripMixed(t *testing.T) {
	enc, err := CompileEncoder[streamProbeRec](EncoderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	w := NewWriterSize(&out, 512)
	var want []string
	for i := 0; i < 50; i++ {
		switch i % 3 {
		case 0:
			v := streamProbeRec{A: i}
			if err := EncodeTo(w, enc, &v); err != nil {
				t.Fatal(err)
			}
			want = append(want, fmt.Sprintf(`{"a":%d}`, i))
		case 1:
			w.BeginObject()
			w.Key("a")
			w.Int(int64(i))
			if err := w.EndObject(); err != nil {
				t.Fatal(err)
			}
			want = append(want, fmt.Sprintf(`{"a":%d}`, i))
		default:
			raw := fmt.Sprintf(`{"a":%d}`, i)
			if err := w.RawUnchecked([]byte(raw)); err != nil {
				t.Fatal(err)
			}
			want = append(want, raw)
		}
		if err := w.Newline(); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r := NewReaderSize(&chunkReader{data: out.Bytes(), chunk: 7}, 512)
	got := collectValues(r)
	if r.Err() != nil {
		t.Fatal(r.Err())
	}
	if len(got) != len(want) {
		t.Fatalf("got %d values, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("value %d: got %q want %q", i, got[i], want[i])
		}
	}
}
