// Package simdjson implements strict, allocation-conscious JSON processing:
// compiled typed decoding, validation, caller-backed structural indexes,
// JSON Pointer selectors, dynamic decoding, and transforms.
//
// # Decoding into structs
//
// [Unmarshal] behaves like encoding/json.Unmarshal and caches one compiled
// decoder per destination type:
//
//	var event Event
//	err := simdjson.Unmarshal(data, &event)
//
// Hot paths should compile the decoder once with [CompileDecoder] and reuse
// it; the returned [Decoder] is immutable and safe for concurrent use.
// [DecoderOptions] selects string ownership (ZeroCopy), unknown-field
// handling, case sensitivity, and merge-versus-replace semantics for
// existing destination state (the default merges like encoding/json). When decoding fails against the Go type,
// the returned [DecodeError] reports the byte offset and the path of the
// offending value (for example "items[3].scores[1]"); the path is
// constructed only when an error unwinds, so successful decodes pay nothing
// for it.
//
// # Encoding structs
//
// [Marshal] produces encoding/json.Marshal-compatible output for supported
// values and caches one compiled encoder per source type:
//
//	data, err := simdjson.Marshal(&event)
//
// Hot paths should compile with [CompileEncoder] and reuse the [Encoder];
// its AppendJSON method appends to a caller-owned buffer, so steady-state
// encoding does not allocate. Unrepresentable values (NaN, infinities,
// malformed json.Number) return an [EncodeError] carrying the same style of
// value path as [DecodeError].
//
// # Custom marshalers
//
// Types implementing json.Marshaler, json.Unmarshaler,
// encoding.TextMarshaler, or encoding.TextUnmarshaler — including time.Time —
// are dispatched through those interfaces like encoding/json. Ordinary plans
// remain stack eligible. Pointer-receiver methods use a heap-backed shadow that
// is copied back before return, so a retained receiver cannot become a stale
// stack pointer. The shadow is a shallow Go copy and does not preserve receiver
// pointer identity after the method returns. Hot types can instead implement
// the simdjson-native [UnmarshalerSimd] and [MarshalerSimd] hooks, whose
// [DecodeCursor] and [Appender] expose the kernels the compiled codecs use.
//
// # Validation and selection
//
// [Valid] and [Validate] check strict JSON syntax without building any
// representation. [GetRaw] and [ScanRaw] resolve RFC 6901 JSON Pointers to
// raw source slices; [CompilePointer] avoids reparsing the pointer on hot
// paths. [BuildIndex] validates the input once and lays out a navigable
// structural index in caller-provided storage, which [Node] and the
// iterators traverse without allocating. [IndexParser] owns and reuses that
// storage when a borrowed, single-owner parser is more convenient.
//
// # Dynamic values and transforms
//
// [Parse] produces an ordered syntax tree of [Value] nodes. [Unmarshal]
// into a *any produces ordinary maps, slices, strings, float64 values,
// booleans, and nil through a dedicated one-pass builder;
// [DecoderOptions].UseNumber selects json.Number for dynamic numbers.
// [AppendCompact], [AppendIndent], and [AppendCanonicalize] rewrite
// documents into caller-owned buffers.
//
// # Streaming
//
// [Writer] streams NDJSON or concatenated top-level values through one
// reused buffer, from compiled encoders via [EncodeTo] or through token
// methods that track container state so malformed output is impossible.
// [Reader] iterates validated top-level values from an io.Reader with a
// rolling buffer; [DecodeFrom] decodes the current value, which aliases the
// buffer only until the next [Reader.Next], and [DecodeNext] fuses
// iteration and typed decoding into a single pass. [CompileCodec] bundles
// a compiled encoder and decoder for one type behind one options struct.
//
// # Toolchain
//
// The module currently requires a Go 1.27 development toolchain for generic
// methods. Building with GOEXPERIMENT=simd on arm64 or amd64 enables vector
// kernels for string scanning, UTF-8 validation, number parsing, and number
// and time formatting through Go's experimental simd/archsimd package.
// Architecture code, portable fallbacks, and runtime reporting live in the
// public github.com/thesyncim/simdjson/simd subpackage.
package simdjson
