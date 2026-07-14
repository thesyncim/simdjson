package simdjson

import (
	"fmt"
	"sync/atomic"
)

// CodecOptions configures both directions of a Codec.
type CodecOptions struct {
	Decoder DecoderOptions
	Encoder EncoderOptions
}

// Codec bundles a compiled encoder and decoder for one type so the
// allocation-free paths are the obvious ones: Append reuses caller buffers,
// Marshal presizes from a per-codec running size hint instead of the global
// per-type cache, and EncodeTo and DecodeFrom plug directly into the
// streaming Writer and Reader. Compile it once and use it concurrently.
type Codec[T any] struct {
	enc  Encoder[T]
	dec  Decoder[T]
	hint *atomic.Uint64
}

// CompileCodec builds both directions for T.
func CompileCodec[T any](opts CodecOptions) (Codec[T], error) {
	dec, err := CompileDecoder[T](opts.Decoder)
	if err != nil {
		return Codec[T]{}, err
	}
	enc, err := CompileEncoder[T](opts.Encoder)
	if err != nil {
		return Codec[T]{}, err
	}
	return Codec[T]{enc: enc, dec: dec, hint: new(atomic.Uint64)}, nil
}

// Encoder returns the underlying compiled encoder.
func (c Codec[T]) Encoder() Encoder[T] { return c.enc }

// Decoder returns the underlying compiled decoder.
func (c Codec[T]) Decoder() Decoder[T] { return c.dec }

// Decode decodes one complete JSON value into dst.
func (c Codec[T]) Decode(src []byte, dst *T) error {
	return c.dec.Decode(src, dst)
}

// DecodeArray decodes a top-level array, reusing dst's capacity.
func (c Codec[T]) DecodeArray(src []byte, dst []T) ([]T, error) {
	return c.dec.DecodeArray(src, dst)
}

// AppendJSON appends src encoded as compact JSON to dst, under the same
// contract as Encoder.AppendJSON.
func (c Codec[T]) AppendJSON(dst []byte, src *T) ([]byte, error) {
	return c.enc.AppendJSON(dst, src)
}

// Marshal encodes src into a new buffer presized by the codec's running
// size hint, so steady-state calls allocate exactly one right-sized result.
func (c Codec[T]) Marshal(src *T) ([]byte, error) {
	if c.hint == nil {
		return nil, fmt.Errorf("simdjson: zero Codec")
	}
	hint := c.hint.Load()
	if hint < 64 {
		hint = 64
	}
	out, err := c.enc.AppendJSON(make([]byte, 0, hint), src)
	if err != nil {
		return nil, err
	}
	if size := uint64(len(out)); size > c.hint.Load() {
		c.hint.Store(size)
	}
	return out, nil
}

// EncodeTo appends one value to a streaming Writer.
func (c Codec[T]) EncodeTo(w *Writer, src *T) error {
	return EncodeTo(w, c.enc, src)
}

// DecodeFrom decodes the Reader's current value, under the same aliasing
// window as DecodeTo.
func (c Codec[T]) DecodeFrom(r *Reader, dst *T) error {
	return DecodeTo(r, c.dec, dst)
}
