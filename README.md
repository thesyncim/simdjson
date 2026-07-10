# simdjson

Fast, strict JSON for Go tip, written entirely in Go.

simdjson combines compiled typed decoders, source-backed selectors, caller-owned
structural indexes, and runtime-selected SIMD. The root module has no third-party
dependencies, assembly, C, `go:linkname`, or runtime map-layout tricks.

> **Toolchain status:** simdjson currently requires a pinned **Go 1.27 development
> toolchain**. Typed decoding uses generic methods, and SIMD builds use the
> experimental `simd/archsimd` package. The scalar build works without
> `GOEXPERIMENT=simd`, but it still requires Go tip today.

## Results

On an Apple M4 Max, `CompileDecoder[T]` parsed the benchmark fixtures in:

| Mode | 31 B object | 4.2 KB / 32 records | 136.6 KB / 1,024 records |
|---|---:|---:|---:|
| **SIMD, source-backed** | **27.8 ns / 0 allocs** | **2.26 us / 2 allocs** | **67.8 us / 2 allocs** |
| Pure Go, source-backed | 28.2 ns / 0 allocs | 2.41 us / 2 allocs | 71.6 us / 2 allocs |
| **SIMD, owned strings** | **43.2 ns / 1 alloc** | **2.52 us / 3 allocs** | **74.3 us / 3 allocs** |

The large source-backed fixture decodes at 2.0 GB/s. Reusing the destination
removes the remaining container allocation in source-backed mode:
`2.15 us / 0 allocs` for 32 records and `66.4 us / 0 allocs` for 1,024
records. Pretty-printed input stays on the fast path: the same large document
indented with two spaces (222 KB) decodes at 1.9 GB/s. So do documents whose
member order does not match the struct (rotated order decodes within 16% of
in-order) and untagged structs matching lowercase keys case-insensitively
(within 2% of exact-case matching).

These are medians of five one-second samples, not claims about every schema.
The [benchmark methodology](#benchmark-methodology) records the exact compiler,
ownership rules, fixtures, competitor versions, and commands.

## Quick Start

Install and pin a Go tip toolchain:

```sh
go install golang.org/dl/gotip@latest
gotip download
gotip get github.com/thesyncim/simdjson
```

Define a model:

```go
package events

type Event struct {
	ID      int       `json:"id"`
	Name    string    `json:"name"`
	Scores  []float64 `json:"scores"`
	Enabled bool      `json:"enabled"`
}
```

`Unmarshal` is a drop-in replacement for `encoding/json.Unmarshal`. It compiles
a decoder for each destination type once, caches it for the process lifetime,
and matches stdlib semantics: owned strings, case-insensitive field fallback,
and merge behavior (absent members leave existing values untouched; null
clears pointers, maps, slices, and interfaces but not scalars). Destinations
reused across decodes usually want `DecoderOptions{Replace: true}`, which
resets everything the document does not mention:

```go
var event Event
if err := simdjson.Unmarshal(payload, &event); err != nil {
	return err
}
```

Hot paths should compile the decoder once and reuse it concurrently; that also
unlocks zero-copy strings and the other options:

```go
decoder, err := simdjson.CompileDecoder[Event](simdjson.DecoderOptions{
	ZeroCopy:      true,
	CaseSensitive: true,
})
if err != nil {
	return err
}

var event Event
if err := decoder.Decode(payload, &event); err != nil {
	return err
}
```

Leave `ZeroCopy` false when decoded strings must own their storage. `DecodeArray`
decodes a top-level array and reuses caller-provided slice capacity.

Encoding mirrors decoding. `Marshal` is byte-identical to
`encoding/json.Marshal` — float formats, `omitempty`, the `string` tag
option, HTML escaping, and escape rules included.
`EncoderOptions.DisableHTMLEscaping` matches `SetEscapeHTML(false)` instead.
A compiled `Encoder` reused with `AppendJSON` encodes with zero allocations:

```go
encoder, err := simdjson.CompileEncoder[Event](simdjson.EncoderOptions{})
if err != nil {
	return err
}
buf, err = encoder.AppendJSON(buf[:0], &event)
```

Encoding the 1,024-record fixture:

| Encoder | Time | Allocations |
|---|---:|---:|
| **simdjson `AppendJSON`, reused buffer** | **72.7 us** | **0** |
| simdjson `Marshal` | 80.5 us | 1 |
| Segment encoding v0.5.4 | 131.6 us | 1 |
| go-json v0.10.6 | 145.6 us | 1 |
| `encoding/json`, Go tip | 248.6 us | 1 |
| jsoniter v1.1.12 | 325.7 us | 2 |

## Decode Errors Carry Paths

When valid JSON cannot be stored in the destination type, the returned
`*DecodeError` reports the byte offset and the path of the offending value:

```go
err := simdjson.Unmarshal(payload, &doc)
var decodeErr *simdjson.DecodeError
if errors.As(err, &decodeErr) {
	fmt.Println(decodeErr.Path) // items[3].scores[1]
}
// simdjson: cannot decode JSON at byte 57 into float64 at items[3].scores[1]: expected number
```

The path is assembled only while an error unwinds, so successful decodes pay
nothing for it.

## Choose An API

| Job | API | Data model |
|---|---|---|
| Drop-in `json.Unmarshal` | `Unmarshal[T]` | Cached compiled decoder per type |
| Fast concrete structs | `CompileDecoder[T]` | Compiled fields and scalar operations |
| Drop-in `json.Marshal` | `Marshal[T]` | Cached compiled encoder per type |
| Zero-allocation encoding | `CompileEncoder[T]`, `AppendJSON` | Caller-owned output buffer |
| Repeated zero-allocation traversal | `BuildIndex`, `Index`, `Node` | Source and caller workspace backed |
| Strict JSON Pointer lookup | `GetRaw`, `CompilePointer` | Validates the complete document |
| Early-exit JSON Pointer scan | `ScanRaw`, `CompilePointer` | Stops after validating the target |
| Ordered syntax tree | `Parse`, `Value` | Owned strings and ordered members |
| Standard maps and slices | `ParseAny` | Normal Go dynamic values |
| Validation only | `Valid`, `Validate` | No result allocation |
| Transforms | `AppendCompact`, `AppendIndent`, `AppendCanonicalize` | Caller-owned destination |

The compiled plan is immutable and safe for concurrent use. Compilation is
excluded from the benchmark timer. The plan uses packed expected-key matching,
exact scalar operations, lazy replacement resets, and specialized fixed-float
arrays.

| Workload | SIMD | Pure Go |
|---|---:|---:|
| 31 B, fresh | **27.77 ns / 0 allocs** | 28.23 ns / 0 allocs |
| 4.2 KB, fresh | **2.260 us / 2 allocs** | 2.410 us / 2 allocs |
| 4.2 KB, reused | **2.152 us / 0 allocs** | 2.200 us / 0 allocs |
| 136.6 KB, fresh | **67.768 us / 2 allocs** | 71.565 us / 2 allocs |
| 136.6 KB, reused | **66.374 us / 0 allocs** | 68.429 us / 0 allocs |

## Zero-Allocation Traversal

`BuildIndex` creates a validated, navigable view in caller-provided storage:

```go
var entries [128]simdjson.IndexEntry
idPointer := simdjson.MustCompilePointer("/items/0/id")

index, err := simdjson.BuildIndex(payload, entries[:])
if err != nil {
	return err
}

id, ok, err := index.PointerCompiled(idPointer)
if err != nil {
	return err
}
if !ok {
	return fmt.Errorf("missing item id")
}
rawID := id.Raw().Bytes()
```

Use `RequiredIndexEntries` when the workspace size is not known. `Index` and
`Node` alias both the input and the entry storage, so both must remain alive and
unchanged while nodes are in use. With sufficient reused storage, valid input
does not allocate.

`ArrayIter`, `ObjectIter`, and their flat fixed-stride variants support
allocation-free iteration. `NextRaw` avoids constructing a node when only the
source range is needed.

## String Ownership

Ownership is explicit because it changes both speed and lifetime:

| Decoder mode | Unescaped strings | Cost |
|---|---|---|
| Default | Alias one private copy of the input | One source-sized allocation |
| `ZeroCopy: true` | Alias caller `src` | No string copy |
| Either mode, escaped string | Decode into owned storage | Allocation only when needed |

In zero-copy mode, keep `src` alive and immutable. In default mode, retaining
one decoded string can retain the private input copy. Source-backed APIs such as
`RawValue`, `Index`, and `Node` always alias their input.

## SIMD Dispatch

`GOEXPERIMENT=simd` enables Go-native vector kernels and binds the best available
implementation once during package initialization:

| Runtime | String scanning | 16-digit reduction |
|---|---|---|
| arm64 | NEON | NEON pairwise reduction |
| amd64 with AVX-512 | 64-byte AVX-512 | AVX reduction |
| amd64 with AVX2 | 32-byte AVX2 | AVX reduction |
| Other build or CPU | Scalar Go | Scalar Go |

Tiny inputs stay on scalar or SWAR paths when vector setup would cost more.
`CurrentSIMD()` reports the selected backend, threshold, vector width, number
backend, and detected CPU features.

The number path combines SIMD digit reduction with a correctly rounded fallback,
following the broad design in Daniel Lemire's
[Number Parsing at a Gigabyte per Second](https://arxiv.org/abs/2101.11408).

## Benchmark Methodology

The numbers above were measured on `darwin/arm64`, Apple M4 Max, using:

```text
go version go1.27-devel_d468ad36 Tue Jul 7 05:58:00 2026 -0700 darwin/arm64
```

Every decoder receives the same bytes and equivalent field layout. The suite
keeps comparisons honest by separating:

- source-backed strings from owned strings;
- fresh destinations from reused destinations;
- direct `encoding/json/v2` under `GOEXPERIMENT=simd,jsonv2`;
- easyjson's generated model from shared models, so its `UnmarshalJSON` method
  cannot intercept another library's benchmark;
- Sonic v1.15.2 in an isolated Go 1.26.4 module, because it falls back to
  `encoding/json` on Go 1.27 tip;
- all comparison dependencies from simdjson's dependency-free root `go.mod`.

Source-backed comparison:

| Decoder | 31 B | 4.2 KB | 136.6 KB |
|---|---:|---:|---:|
| **simdjson compiled, SIMD** | **27.8 ns / 0** | **2.26 us / 2** | **67.8 us / 2** |
| simdjson compiled, pure Go | 28.2 ns / 0 | 2.41 us / 2 | 71.6 us / 2 |
| Sonic v1.15.2 Fastest, Go 1.26.4 | 187.9 ns / 4 | 5.74 us / 6 | 170.5 us / 6 |

Owned-string comparison:

| Decoder | 31 B | 4.2 KB | 136.6 KB |
|---|---:|---:|---:|
| **simdjson compiled, SIMD** | **43.2 ns / 1** | **2.52 us / 3** | **74.3 us / 3** |
| go-json v0.10.6 | 53.0 ns / 2 | 5.60 us / 35 | 175.3 us / 1,027 |
| Segment encoding v0.5.4 | 58.0 ns / 2 | 5.41 us / 69 | 167.7 us / 2,058 |
| jsoniter v1.1.12 | 83.8 ns / 2 | 6.53 us / 104 | 195.7 us / 3,085 |
| easyjson v0.9.2 generated | 85.7 ns / 1 | 8.29 us / 71 | 259.3 us / 2,060 |
| `encoding/json/v2`, Go tip | 170.6 ns / 1 | 12.48 us / 39 | 379.5 us / 1,037 |
| `encoding/json`, Go tip | 200.5 ns / 1 | 15.19 us / 39 | 455.9 us / 1,037 |
| Sonic v1.15.2 Std, Go 1.26.4 | 233.0 ns / 5 | 7.82 us / 71 | 227.9 us / 2,055 |

Run the primary comparison:

```sh
cd benchmarks
GOEXPERIMENT=simd gotip test -run='^$' \
  -bench='^BenchmarkParseTyped$' \
  -benchmem -benchtime=1s -count=5 .
```

See [`benchmarks/README.md`](benchmarks/README.md) for pure-Go, json/v2, native
Sonic, environment capture, and result comparison commands.

## Correctness

simdjson validates UTF-8, escapes, surrogate pairs, number grammar, integer overflow,
depth, and trailing data. The suite includes all 318 Nicolas Seriot
JSONTestSuite cases, simdjson-derived edge cases, compiled numeric boundaries,
allocation contracts, differential fuzzing, race and `checkptr=2` runs, and
Linux cross-compiles for arm64 and amd64.

Compiled decoders and encoders support exported structs, nested named
structs, pointers, slices, fixed arrays, maps with string keys, empty
interfaces, named scalar types, booleans, strings, every integer width,
floats, and `json.Number`. Map decoding merges into existing maps and map
encoding sorts keys, both matching `encoding/json`. Values decoded into
`any` use the standard dynamic shapes and always replace the previous value;
encoding an interface compiles a plan for its concrete type on first use.
Byte slices decode from and encode to standard base64, reusing destination
capacity, and the `string` tag option quotes scalars exactly like
encoding/json. Non-empty interfaces, non-string map keys, untagged anonymous
fields, and custom unmarshaler dispatch are not supported yet. Untagged
anonymous fields are rejected rather than flattened.

Run scalar and SIMD verification against the pinned compiler:

```sh
gotip test ./...
GOEXPERIMENT=simd gotip test ./...
```
