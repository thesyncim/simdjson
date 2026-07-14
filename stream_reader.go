package simdjson

import (
	"errors"
	"fmt"
	"io"
	"unsafe"
)

// Reader streams top-level JSON values from an io.Reader: NDJSON, other
// whitespace-separated values, and directly concatenated values all work.
// Next advances to the next complete value, validating it in full, so a
// true result guarantees Bytes holds exactly one valid JSON value.
//
// The reader owns one rolling buffer. Values are exposed as aliases into
// it: Bytes, and any zero-copy decode of the current value, are valid only
// until the next call to Next. A value that arrives split across reads
// costs one compacting copy of its partial prefix; everything else is read
// straight into place, and steady-state operation allocates nothing once
// the buffer has grown to the largest value seen (bounded by
// SetMaxValueBytes). A Reader is not safe for concurrent use.
type Reader struct {
	in  io.Reader
	buf []byte

	pos int // scan position within buf
	end int // valid bytes end within buf

	valStart int // current value extent
	valEnd   int

	consumed int64 // bytes discarded before buf[0], for error offsets
	maxValue int
	eof      bool
	hasValue bool
	err      error
}

// defaultReaderSize holds several typical NDJSON records per read.
const defaultReaderSize = 64 << 10

// NewReader returns a Reader with the default buffer size.
func NewReader(in io.Reader) *Reader {
	return NewReaderSize(in, defaultReaderSize)
}

// NewReaderSize is NewReader with an explicit initial buffer size. The
// buffer still grows as needed to hold one complete value.
func NewReaderSize(in io.Reader, size int) *Reader {
	if size < 512 {
		size = 512
	}
	return &Reader{in: in, buf: make([]byte, size)}
}

// SetMaxValueBytes bounds the size of a single value; a longer value stops
// the stream with an error instead of growing the buffer without limit.
// Zero, the default, means no bound.
func (r *Reader) SetMaxValueBytes(n int) {
	r.maxValue = n
}

// Err returns the first error encountered, or nil after a clean end of
// stream.
func (r *Reader) Err() error {
	return r.err
}

// InputOffset returns the number of input bytes consumed through the end of
// the current value.
func (r *Reader) InputOffset() int64 {
	return r.consumed + int64(r.valEnd)
}

// Bytes returns the current value, aliasing the reader's buffer: the slice
// is valid only until the next call to Next.
func (r *Reader) Bytes() []byte {
	if !r.hasValue {
		return nil
	}
	return r.buf[r.valStart:r.valEnd]
}

// DecodeTo decodes the current value through a compiled decoder. Decoders
// compiled with ZeroCopy alias the reader's buffer and follow the Bytes
// validity window; owned decoders copy and are safe to retain.
func DecodeTo[T any](r *Reader, dec Decoder[T], dst *T) error {
	if !r.hasValue {
		if r.err != nil {
			return r.err
		}
		return errors.New("simdjson: DecodeTo without a current value; call Next first")
	}
	return dec.Decode(r.buf[r.valStart:r.valEnd], dst)
}

// DecodeNext advances to the next value and decodes it in one pass,
// combining Next and DecodeTo without the separate boundary scan: the
// decoder itself finds where the value ends. It returns false at the end
// of the stream or on error; Err distinguishes the two.
//
// Retried attempts on values that arrive split across reads re-decode the
// same document prefix into dst, so merge semantics are preserved; custom
// unmarshalers may observe such repeated partial calls. After a true
// result, Bytes and InputOffset describe the decoded value.
func DecodeNext[T any](r *Reader, dec Decoder[T], dst *T) bool {
	if r.err != nil {
		return false
	}
	r.hasValue = false
	i := r.pos
	for {
		i = skipSpace(r.buf[:r.end], i)
		if i == r.end {
			r.pos = i
			if !r.fill(&i) {
				return false
			}
			continue
		}

		n, err := dec.DecodePrefix(r.buf[i:r.end], dst)
		end := i + n
		if err == nil && (end < r.end || r.eof) {
			// A value ending exactly at the window edge may continue in
			// unread input; see Next.
			r.valStart, r.valEnd = i, end
			r.pos = end
			r.hasValue = true
			return true
		}

		// The failure may only mean the value is still arriving. If the
		// window already holds one complete value, the error is real and
		// is reported now; otherwise read more and decode again.
		if err != nil {
			window := r.buf[:r.end]
			base := unsafe.Pointer(unsafe.SliceData(window))
			if valEnd, ok := validValueFast(window, base, r.end, i, window[i], 0); ok && (valEnd < r.end || r.eof) {
				r.err = fmt.Errorf("simdjson: value at input offset %d: %w", r.consumed+int64(i), err)
				return false
			}
		}
		if !r.eof {
			r.pos = i
			if !r.fill(&i) {
				if r.err != nil {
					return false
				}
				continue
			}
			continue
		}

		err = Validate(r.buf[i:r.end])
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		r.err = fmt.Errorf("simdjson: invalid value at input offset %d: %w", r.consumed+int64(i), err)
		return false
	}
}

// Next advances to the next value in the stream. It returns false at the
// end of the stream or on error; Err distinguishes the two.
func (r *Reader) Next() bool {
	if r.err != nil {
		return false
	}
	r.hasValue = false
	i := r.pos
	for {
		// Skip inter-value whitespace, refilling as needed.
		i = skipSpace(r.buf[:r.end], i)
		if i == r.end {
			r.pos = i
			if !r.fill(&i) {
				return false
			}
			continue
		}

		window := r.buf[:r.end]
		base := unsafe.Pointer(unsafe.SliceData(window))
		end, ok := validValueFast(window, base, r.end, i, window[i], 0)
		if ok && (end < r.end || r.eof) {
			// A value ending exactly at the window edge may continue in
			// unread input (numbers and literals are not self-delimiting),
			// so it only counts once a byte follows it or the input ended.
			r.valStart, r.valEnd = i, end
			r.pos = end
			r.hasValue = true
			return true
		}
		if !r.eof {
			r.pos = i
			if !r.fill(&i) {
				if r.err != nil {
					return false
				}
				continue // reached end of input; final validation pass below
			}
			continue
		}

		// The input ended inside this candidate value: report why with the
		// diagnostic validator, positioned against the whole stream.
		err := Validate(r.buf[i:r.end])
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		r.err = fmt.Errorf("simdjson: invalid value at input offset %d: %w", r.consumed+int64(i), err)
		return false
	}
}

// fill reads more input, compacting or growing the buffer so the candidate
// value starting at *keep stays available. It returns false when no new
// bytes will ever arrive; r.err is set only for real read errors.
func (r *Reader) fill(keep *int) bool {
	if r.eof {
		return false
	}
	if r.end == len(r.buf) {
		if *keep > 0 {
			// Drop everything before the candidate value.
			n := copy(r.buf, r.buf[*keep:r.end])
			r.consumed += int64(*keep)
			r.end = n
			r.pos -= *keep
			*keep = 0
		} else if r.maxValue > 0 && len(r.buf) >= r.maxValue {
			r.err = fmt.Errorf("simdjson: value at input offset %d exceeds the %d byte limit", r.consumed, r.maxValue)
			return false
		} else {
			grown := make([]byte, len(r.buf)*2)
			copy(grown, r.buf[:r.end])
			r.buf = grown
		}
	}
	for {
		n, err := r.in.Read(r.buf[r.end:])
		r.end += n
		switch {
		case err == io.EOF:
			r.eof = true
			return n > 0
		case err != nil:
			r.err = err
			return false
		case n > 0:
			return true
		}
	}
}
