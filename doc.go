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
// pointer identity after the method returns.
//
// # Validation and selection
//
// [Valid] and [Validate] check strict JSON syntax without building any
// representation. [GetRaw] and [ScanRaw] resolve RFC 6901 JSON Pointers to
// raw source slices; [CompilePointer] avoids reparsing the pointer on hot
// paths. [BuildIndex] validates the input once and lays out a navigable
// structural index in caller-provided storage, which [Node] and the
// iterators traverse without allocating.
//
// # Dynamic values and transforms
//
// [Parse] produces an ordered syntax tree of [Value] nodes. [ParseAny]
// produces ordinary maps, slices, strings, float64 values, booleans, and
// nil. [AppendCompact], [AppendIndent], and [AppendCanonicalize] rewrite
// documents into caller-owned buffers.
//
// # Toolchain
//
// The module currently requires a Go 1.27 development toolchain for generic
// methods. Building with GOEXPERIMENT=simd on arm64 or amd64 enables vector
// kernels for string and number scanning through Go's experimental
// simd/archsimd package; other builds use scalar equivalents selected at
// package initialization. [CurrentSIMD] reports the active backend.
package simdjson
