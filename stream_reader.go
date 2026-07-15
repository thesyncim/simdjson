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
	// readErr is a non-EOF source error, held until the bytes that arrived
	// with or before it have been scanned; io.Reader delivers data and
	// error together and the data comes first.
	readErr error

	consumed int64 // bytes discarded before buf[0], for error offsets
	maxValue int
	eof      bool
	hasValue bool
	err      error
}

// valueFrame resumably locates the end of one JSON value across buffer refills.
// It advances a cursor through newly available bytes only, keeping O(1) state,
// so a value that arrives in K chunks is framed in O(value length) total rather
// than the O(K·length) of re-scanning it from the start on every refill. It
// tracks structure only; the caller validates the framed extent once. framed
// counts value bytes consumed relative to the value start, so buffer compaction
// (which shifts the start) needs no adjustment.
type valueFrame struct {
	mode    uint8 // frameContainer, frameString, frameNumber, frameLiteral
	depth   int   // open { and [ for containers
	inStr   bool  // inside a string within a container
	esc     bool  // previous string byte was an unescaped backslash
	numE    bool  // previous number byte was e or E (a following +/- stays in it)
	litLeft int   // literal bytes still expected
	framed  int   // value bytes consumed so far, from the value start
}

const (
	frameContainer uint8 = iota
	frameString
	frameNumber
	frameLiteral
)

// init classifies the value from its leading byte and consumes it. An
// unrecognized leading byte is framed as a one-byte number so the caller's
// validator rejects it.
func (f *valueFrame) init(c byte) {
	f.framed = 1
	switch {
	case c == '"':
		f.mode = frameString
	case c == '{' || c == '[':
		f.mode = frameContainer
		f.depth = 1
	case c == 't' || c == 'n':
		f.mode = frameLiteral
		f.litLeft = 3
	case c == 'f':
		f.mode = frameLiteral
		f.litLeft = 4
	default:
		f.mode = frameNumber
	}
}

// scan advances the frame over src[start+framed : n], resuming its state. It
// returns true once the value is structurally complete: the closing bracket or
// quote is consumed, a fixed-length literal is filled, or (for a number) a
// delimiter byte follows. A number that reaches n without a delimiter stays
// incomplete; at end of input the caller treats it as ending at n.
func (f *valueFrame) scan(src []byte, start, n int) bool {
	i := start + f.framed
	switch f.mode {
	case frameString:
		for i < n {
			c := src[i]
			i++
			if f.esc {
				f.esc = false
				continue
			}
			switch c {
			case '\\':
				f.esc = true
			case '"':
				f.framed = i - start
				return true
			}
		}
	case frameNumber:
		for i < n {
			c := src[i]
			switch {
			case c >= '0' && c <= '9', c == '.':
				f.numE = false
			case c == 'e' || c == 'E':
				f.numE = true
			case c == '+' || c == '-':
				if !f.numE {
					f.framed = i - start
					return true
				}
				f.numE = false
			default:
				f.framed = i - start
				return true
			}
			i++
		}
	case frameLiteral:
		for i < n && f.litLeft > 0 {
			i++
			f.litLeft--
		}
		f.framed = i - start
		return f.litLeft == 0
	default: // frameContainer
		for i < n {
			c := src[i]
			i++
			if f.inStr {
				if f.esc {
					f.esc = false
					continue
				}
				switch c {
				case '\\':
					f.esc = true
				case '"':
					f.inStr = false
				}
				continue
			}
			switch c {
			case '"':
				f.inStr = true
			case '{', '[':
				f.depth++
			case '}', ']':
				f.depth--
				if f.depth == 0 {
					f.framed = i - start
					return true
				}
			}
		}
	}
	f.framed = i - start
	return false
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

// DecodeNext advances to the next value and decodes it in one pass, combining
// Next and DecodeTo. The value's extent is located by a resumable structural
// frame, so a value split across reads is scanned once and decoded once, even
// when it spans many refills. It returns false at the end of the stream or on
// error; Err distinguishes the two. After a true result, Bytes and InputOffset
// describe the decoded value.
func DecodeNext[T any](r *Reader, dec Decoder[T], dst *T) bool {
	if r.err != nil {
		return false
	}
	r.hasValue = false
	i := r.pos
	// Skip inter-value whitespace, refilling as needed, to the value start.
	for {
		i = skipSpace(r.buf[:r.end], i)
		if i < r.end {
			break
		}
		r.pos = i
		if !r.fill(&i) {
			if r.err == nil {
				r.err = r.readErr
			}
			return false
		}
	}

	// Frame the value's end resumably, so a value spanning many refills is
	// scanned once instead of re-decoded from the start each time, then decode
	// the fully-buffered extent exactly once. decodedN caches that decode
	// (relative to the value start) while a scalar awaits its confirming byte.
	var fr valueFrame
	fr.init(r.buf[i])
	framed := false
	decodedN := -1
	for {
		if !framed {
			framed = fr.scan(r.buf, i, r.end)
		}
		if decodedN < 0 && (framed || r.eof) {
			n, err := dec.DecodePrefix(r.buf[i:r.end], dst)
			if err != nil {
				// The value is fully buffered (framed) or the input ended
				// mid-value; the error is real. Diagnose the framed extent so
				// the reason does not depend on trailing input.
				if verr := Validate(r.buf[i : i+fr.framed]); verr != nil {
					err = verr
				}
				r.err = fmt.Errorf("simdjson: value at input offset %d: %w", r.consumed+int64(i), err)
				return false
			}
			decodedN = n
		}
		if decodedN >= 0 {
			end := i + decodedN
			if end < r.end || r.eof {
				if r.maxValue > 0 && decodedN > r.maxValue {
					r.err = fmt.Errorf("simdjson: value at input offset %d exceeds the %d byte limit", r.consumed+int64(i), r.maxValue)
					return false
				}
				r.valStart, r.valEnd = i, end
				r.pos = end
				r.hasValue = true
				return true
			}
			// end == r.end && !r.eof: read one more byte to confirm the scalar
			// boundary, without re-decoding the already-decoded value.
		}
		r.pos = i
		if !r.fill(&i) {
			if r.err != nil {
				return false
			}
			// End of input reached; the loop re-evaluates with r.eof set.
		}
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
	// Skip inter-value whitespace, refilling as needed, to the value start.
	for {
		i = skipSpace(r.buf[:r.end], i)
		if i < r.end {
			break
		}
		r.pos = i
		if !r.fill(&i) {
			if r.err == nil {
				r.err = r.readErr
			}
			return false
		}
	}

	// Locate the value's end by resumable framing, so a value spanning many
	// refills is scanned once rather than re-scanned from the start each time.
	// Once the value is fully buffered it is validated exactly once; validLen
	// caches that result (relative to the value start, so buffer compaction
	// needs no adjustment) while a scalar awaits the byte that confirms its end.
	var fr valueFrame
	fr.init(r.buf[i])
	framed := false
	validLen := -1
	for {
		if !framed {
			framed = fr.scan(r.buf, i, r.end)
		}
		if validLen < 0 && (framed || r.eof) {
			window := r.buf[:r.end]
			base := unsafe.Pointer(unsafe.SliceData(window))
			end, ok := validValueFast(window, base, r.end, i, window[i], 0)
			if !ok {
				// The value is fully buffered (framed) or the input ended
				// mid-value; either way it will not become valid. Diagnose the
				// framed extent, not the whole buffer, so the reported reason
				// does not depend on how much trailing input has arrived.
				verr := Validate(r.buf[i : i+fr.framed])
				if verr == nil {
					verr = io.ErrUnexpectedEOF
				}
				if r.readErr != nil {
					verr = r.readErr
				}
				r.err = fmt.Errorf("simdjson: invalid value at input offset %d: %w", r.consumed+int64(i), verr)
				return false
			}
			validLen = end - i
		}
		if validLen >= 0 {
			end := i + validLen
			if end < r.end || r.eof {
				// A number or literal ending exactly at the buffer edge may
				// continue in unread input, so it only counts once a byte
				// follows it or the input ended.
				if r.maxValue > 0 && validLen > r.maxValue {
					r.err = fmt.Errorf("simdjson: value at input offset %d exceeds the %d byte limit", r.consumed+int64(i), r.maxValue)
					return false
				}
				r.valStart, r.valEnd = i, end
				r.pos = end
				r.hasValue = true
				return true
			}
			// end == r.end && !r.eof: read one more byte to confirm the
			// boundary, without re-validating the already-checked value.
		}
		r.pos = i
		if !r.fill(&i) {
			if r.err != nil {
				return false
			}
			// End of input reached; the loop re-evaluates with r.eof set.
		}
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
		if r.maxValue > 0 && r.end-*keep > r.maxValue {
			r.err = fmt.Errorf("simdjson: value at input offset %d exceeds the %d byte limit", r.consumed+int64(*keep), r.maxValue)
			return false
		}
		if *keep > 0 {
			// Drop everything before the candidate value, keeping the
			// last delivered value's offsets pointing at the same input
			// positions.
			n := copy(r.buf, r.buf[*keep:r.end])
			r.consumed += int64(*keep)
			r.end = n
			r.pos -= *keep
			r.valStart -= *keep
			r.valEnd -= *keep
			*keep = 0
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
			r.eof = true
			r.readErr = err
			return n > 0
		case n > 0:
			return true
		}
	}
}
