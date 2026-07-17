package simdjson

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
)

type readCounter struct {
	in    io.Reader
	reads atomic.Int64
}

func (r *readCounter) Read(p []byte) (int, error) {
	r.reads.Add(1)
	return r.in.Read(p)
}

func TestReaderLifecycleSequences(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		configure func(*testing.T, *Reader)
		wantNext  bool
		wantValue string
		wantErr   bool
		wantReads bool
	}{
		{
			name:  "set-limit-next",
			input: `"123456789"`,
			configure: func(t *testing.T, r *Reader) {
				if err := r.SetMaxValueBytes(4); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: true, wantReads: true,
		},
		{
			name:  "close-next",
			input: "1\n",
			configure: func(t *testing.T, r *Reader) {
				if err := r.Close(); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:  "close-close-next",
			input: "1\n",
			configure: func(t *testing.T, r *Reader) {
				if err := r.Close(); err != nil {
					t.Fatal(err)
				}
				if err := r.Close(); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			src := &readCounter{in: strings.NewReader(test.input)}
			r := NewReaderSize(src, 512)
			test.configure(t, r)
			if got := src.reads.Load(); got != 0 {
				t.Fatalf("configuration performed %d reads before Next", got)
			}
			if got := r.Next(); got != test.wantNext {
				t.Fatalf("Next = %v, want %v (Err = %v)", got, test.wantNext, r.Err())
			}
			if got := string(r.Bytes()); got != test.wantValue {
				t.Fatalf("Bytes = %q, want %q", got, test.wantValue)
			}
			if got := r.Err() != nil; got != test.wantErr {
				t.Fatalf("Err = %v, want error=%v", r.Err(), test.wantErr)
			}
			if got := src.reads.Load() > 0; got != test.wantReads {
				t.Fatalf("source read=%v, want %v", got, test.wantReads)
			}
		})
	}
}

func TestReaderRejectsConfigurationAfterStart(t *testing.T) {
	r := NewReader(strings.NewReader("1 22"))
	if !r.Next() || !bytes.Equal(r.Bytes(), []byte("1")) {
		t.Fatalf("first Next: Bytes=%q Err=%v", r.Bytes(), r.Err())
	}
	if err := r.SetMaxValueBytes(1); !errors.Is(err, ErrReaderStarted) {
		t.Fatalf("SetMaxValueBytes error = %v, want ErrReaderStarted", err)
	}
	if r.maxValue != 0 {
		t.Fatalf("rejected configuration changed maxValue to %d", r.maxValue)
	}
	if !r.Next() || !bytes.Equal(r.Bytes(), []byte("22")) {
		t.Fatalf("second Next: Bytes=%q Err=%v", r.Bytes(), r.Err())
	}
}

func TestReaderCloseClearsCurrentValue(t *testing.T) {
	dec, err := CompileDecoder[int](DecoderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	r := NewReader(strings.NewReader("1 2"))
	if !r.Next() {
		t.Fatalf("first Next: %v", r.Err())
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if r.Bytes() != nil {
		t.Fatalf("Bytes after Close = %q, want nil", r.Bytes())
	}
	if r.Next() {
		t.Fatal("Next succeeded after Close")
	}
	var dst int
	if DecodeNext(r, dec, &dst) {
		t.Fatal("DecodeNext succeeded after Close")
	}
	if err := DecodeFrom(r, dec, &dst); !errors.Is(err, ErrReaderClosed) {
		t.Fatalf("DecodeFrom error = %v, want ErrReaderClosed", err)
	}
}

func TestNewReaderWithOptionsIsLazy(t *testing.T) {
	src := &readCounter{in: strings.NewReader(`"123456789"`)}
	r, err := NewReaderWithOptions(src, ReaderOptions{
		BufferSize:    512,
		MaxValueBytes: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := src.reads.Load(); got != 0 {
		t.Fatalf("constructor performed %d reads", got)
	}
	if r.Next() || r.Err() == nil {
		t.Fatalf("oversized Next = true or nil error: %v", r.Err())
	}
}

func TestReaderConfigurationValidation(t *testing.T) {
	if _, err := NewReaderWithOptions(strings.NewReader("1"), ReaderOptions{BufferSize: -1}); err == nil {
		t.Fatal("negative buffer size accepted")
	}
	if _, err := NewReaderWithOptions(strings.NewReader("1"), ReaderOptions{MaxValueBytes: -1}); err == nil {
		t.Fatal("negative value limit accepted")
	}
	r := NewReader(strings.NewReader("1"))
	if err := r.SetMaxValueBytes(-1); err == nil {
		t.Fatal("negative value limit accepted by setter")
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err := r.SetMaxValueBytes(1); !errors.Is(err, ErrReaderClosed) {
		t.Fatalf("SetMaxValueBytes after Close = %v, want ErrReaderClosed", err)
	}
}
