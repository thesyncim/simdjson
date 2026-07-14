package simdjson

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

func TestCodecRoundTrip(t *testing.T) {
	codec, err := CompileCodec[streamRecord](CodecOptions{})
	if err != nil {
		t.Fatal(err)
	}
	v := streamRecordAt(7)

	out, err := codec.Marshal(&v)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(v)
	if string(out) != string(want) {
		t.Fatalf("Marshal = %s, want %s", out, want)
	}

	var back streamRecord
	if err := codec.Decode(out, &back); err != nil || back != v {
		t.Fatalf("Decode = %+v, %v", back, err)
	}

	buf := make([]byte, 0, 256)
	appended, err := codec.Append(buf, &v)
	if err != nil || string(appended) != string(want) {
		t.Fatalf("Append = %s, %v", appended, err)
	}

	arr, err := codec.DecodeArray([]byte(`[`+string(want)+`,`+string(want)+`]`), nil)
	if err != nil || len(arr) != 2 || arr[1] != v {
		t.Fatalf("DecodeArray = %+v, %v", arr, err)
	}

	// Streaming both ways through the codec.
	var stream bytes.Buffer
	w := NewWriter(&stream)
	for i := 0; i < 10; i++ {
		row := streamRecordAt(i)
		if err := codec.EncodeTo(w, &row); err != nil {
			t.Fatal(err)
		}
		w.Newline()
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r := NewReader(&stream)
	count := 0
	for r.Next() {
		var row streamRecord
		if err := codec.DecodeFrom(r, &row); err != nil || row != streamRecordAt(count) {
			t.Fatalf("row %d: %+v, %v", count, row, err)
		}
		count++
	}
	if r.Err() != nil || count != 10 {
		t.Fatalf("count=%d err=%v", count, r.Err())
	}
}

func TestCodecMarshalHintAndConcurrency(t *testing.T) {
	codec, err := CompileCodec[streamRecord](CodecOptions{})
	if err != nil {
		t.Fatal(err)
	}
	v := streamRecordAt(3)
	first, err := codec.Marshal(&v)
	if err != nil {
		t.Fatal(err)
	}
	second, err := codec.Marshal(&v)
	if err != nil {
		t.Fatal(err)
	}
	if cap(second) < len(first) {
		t.Fatalf("hint not applied: cap=%d want >= %d", cap(second), len(first))
	}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			row := streamRecordAt(g)
			for i := 0; i < 500; i++ {
				out, err := codec.Marshal(&row)
				if err != nil {
					t.Error(err)
					return
				}
				var back streamRecord
				if err := codec.Decode(out, &back); err != nil || back != row {
					t.Errorf("g=%d: %+v %v", g, back, err)
					return
				}
			}
		}(g)
	}
	wg.Wait()
}

func TestCodecZeroValue(t *testing.T) {
	var codec Codec[streamRecord]
	var v streamRecord
	if _, err := codec.Marshal(&v); err == nil {
		t.Fatal("zero codec Marshal must error")
	}
	if err := codec.Decode([]byte(`{}`), &v); err == nil {
		t.Fatal("zero codec Decode must error")
	}
}

func TestDecodePrefixConcatenated(t *testing.T) {
	dec, err := CompileDecoder[streamRecord](DecoderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	one, _ := json.Marshal(streamRecordAt(1))
	two, _ := json.Marshal(streamRecordAt(2))
	src := []byte("  " + string(one) + " \n\t" + string(two) + "trailing-garbage-left-alone")
	var v streamRecord
	n, err := dec.DecodePrefix(src, &v)
	if err != nil || v != streamRecordAt(1) {
		t.Fatalf("first: n=%d err=%v v=%+v", n, err, v)
	}
	rest := src[n:]
	m, err := dec.DecodePrefix(rest, &v)
	if err != nil || v != streamRecordAt(2) {
		t.Fatalf("second: err=%v v=%+v", err, v)
	}
	if !strings.HasPrefix(string(rest[m:]), "trailing-garbage") {
		t.Fatalf("consumed too much: %q", rest[m:])
	}
	// Truncated input must error, not panic.
	if _, err := dec.DecodePrefix(one[:len(one)-3], &v); err == nil {
		t.Fatal("truncated prefix must error")
	}
}

func TestDecodeNextStream(t *testing.T) {
	dec, err := CompileDecoder[streamRecord](DecoderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	const rows = 300
	for _, chunk := range []int{1, 7, 999, 1 << 20} {
		for _, sep := range []string{"\n", " ", ""} {
			data := streamFixture(t, rows, sep)
			r := NewReaderSize(&chunkReader{data: data, chunk: chunk}, 512)
			count := 0
			var got streamRecord
			for DecodeNext(r, dec, &got) {
				if got != streamRecordAt(count) {
					t.Fatalf("chunk=%d sep=%q row %d: %+v", chunk, sep, count, got)
				}
				count++
			}
			if r.Err() != nil || count != rows {
				t.Fatalf("chunk=%d sep=%q: count=%d err=%v", chunk, sep, count, r.Err())
			}
		}
	}
}

func TestDecodeNextErrors(t *testing.T) {
	dec, _ := CompileDecoder[streamRecord](DecoderOptions{})
	t.Run("type error surfaces without draining", func(t *testing.T) {
		// The mistyped value is followed by much more input; the error must
		// surface once the value is complete, not after reading the rest.
		tail := strings.Repeat(`{"id":1}`+"\n", 100_000)
		r := NewReaderSize(strings.NewReader(`{"id":"nope"}`+"\n"+tail), 512)
		var got streamRecord
		if DecodeNext(r, dec, &got) {
			t.Fatal("mistyped value must not decode")
		}
		if r.Err() == nil || !strings.Contains(r.Err().Error(), "offset 0") {
			t.Fatalf("expected positioned decode error, got %v", r.Err())
		}
	})
	t.Run("truncated", func(t *testing.T) {
		r := NewReader(strings.NewReader(`{"id":1}` + "\n" + `{"id":`))
		var got streamRecord
		if !DecodeNext(r, dec, &got) {
			t.Fatalf("first value: %v", r.Err())
		}
		if DecodeNext(r, dec, &got) {
			t.Fatal("truncated value must not decode")
		}
		if r.Err() == nil {
			t.Fatal("expected truncation error")
		}
	})
	t.Run("clean end", func(t *testing.T) {
		r := NewReader(strings.NewReader(`{"id":1}` + "\n \t"))
		var got streamRecord
		if !DecodeNext(r, dec, &got) || got.ID != 1 {
			t.Fatalf("first value: %v %+v", r.Err(), got)
		}
		if DecodeNext(r, dec, &got) || r.Err() != nil {
			t.Fatalf("clean end expected, err=%v", r.Err())
		}
	})
}

func BenchmarkStreamDecodeNextNDJSON(b *testing.B) {
	var data bytes.Buffer
	{
		enc, _ := CompileEncoder[streamRecord](EncoderOptions{})
		w := NewWriter(&data)
		for i := 0; i < 512; i++ {
			v := streamRecordAt(i)
			EncodeTo(w, enc, &v)
			w.Newline()
		}
		w.Close()
	}
	dec, _ := CompileDecoder[streamRecord](DecoderOptions{ZeroCopy: true})
	b.SetBytes(int64(data.Len()))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		r := NewReader(bytes.NewReader(data.Bytes()))
		var got streamRecord
		for DecodeNext(r, dec, &got) {
		}
		if r.Err() != nil {
			b.Fatal(r.Err())
		}
	}
}
