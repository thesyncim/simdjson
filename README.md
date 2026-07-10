# simdjson

Fast, strict JSON for Go tip, written entirely in Go: a drop-in
`encoding/json` replacement that leads every measured category — typed
decoding, encoding, validation, and dynamic parsing — by significant margins.

simdjson combines compiled typed decoders and encoders, source-backed
selectors, caller-owned structural indexes, and runtime-selected SIMD. The
root module has no third-party dependencies, assembly, C, `go:linkname`, or
runtime map-layout tricks. Behavior matches `encoding/json` and is enforced
by differential tests and fuzzers: field resolution, custom marshalers,
merge semantics, escape rules, and byte-identical Marshal output.

> **Toolchain status:** simdjson currently requires a pinned **Go 1.27 development
> toolchain**. Typed decoding uses generic methods, and SIMD builds use the
> experimental `simd/archsimd` package. The scalar build works without
> `GOEXPERIMENT=simd`, but it still requires Go tip today.

## Results

On an Apple M4 Max, `CompileDecoder[T]` parsed the benchmark fixtures in:

| Mode | 31 B object | 4.2 KB / 32 records | 136.6 KB / 1,024 records |
|---|---:|---:|---:|
| **SIMD, source-backed** | **29.9 ns / 0 allocs** | **2.36 us / 2 allocs** | **75.9 us / 2 allocs** |
| **SIMD, owned strings** | **43.3 ns / 1 alloc** | **2.62 us / 3 allocs** | **84.1 us / 3 allocs** |

The large source-backed fixture decodes at 1.8 GB/s. Reusing the destination
removes the remaining container allocation in source-backed mode:
`2.20 us / 0 allocs` for 32 records and `74.8 us / 0 allocs` for 1,024
records. Robustness of the fast path, measured on the same large document:
two-space indentation (222 KB) decodes at 1.9 GB/s, rotating every record's
member order costs 3%, and untagged Go field names matching lowercase keys
case-insensitively cost under 1%.

Every number in this README comes from one measurement session on one
machine, as medians of five one-second samples; they are not claims about
every schema. The [benchmark methodology](#benchmark-methodology) records the
exact compiler, ownership rules, fixtures, competitor versions, and commands.

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
| **simdjson `AppendJSON`, reused buffer** | **87.1 us** | **0** |
| simdjson `Marshal` | 93.2 us | 1 |
| Segment encoding v0.5.4 | 133.7 us | 1 |
| go-json v0.10.6 | 159.2 us | 1 |
| `encoding/json`, Go tip | 287.1 us | 1 |
| jsoniter v1.1.12 | 335.4 us | 2 |

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

The root module's own benchmarks compare the SIMD and scalar builds of the
same decode:

| Workload | SIMD | Pure Go |
|---|---:|---:|
| 31 B, fresh | **28.4 ns / 0 allocs** | 29.2 ns / 0 allocs |
| 4.2 KB, fresh | **2.48 us / 2 allocs** | 2.51 us / 2 allocs |
| 136.6 KB, fresh | **74.4 us / 2 allocs** | 76.5 us / 2 allocs |
| 136.6 KB, reused | **70.8 us / 0 allocs** | 73.2 us / 0 allocs |

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

Source-backed comparison (the Sonic rows come from its isolated Go 1.26.4
module, measured on the same machine and fixtures):

| Decoder | 31 B | 4.2 KB | 136.6 KB |
|---|---:|---:|---:|
| **simdjson compiled, SIMD** | **29.9 ns / 0** | **2.36 us / 2** | **75.9 us / 2** |
| Sonic v1.15.2 Fastest, Go 1.26.4 | 187.9 ns / 4 | 5.74 us / 6 | 170.5 us / 6 |

Owned-string comparison:

| Decoder | 31 B | 4.2 KB | 136.6 KB |
|---|---:|---:|---:|
| **simdjson compiled, SIMD** | **43.3 ns / 1** | **2.62 us / 3** | **84.1 us / 3** |
| go-json v0.10.6 | 55.8 ns / 2 | 5.65 us / 35 | 182.3 us / 1,027 |
| Segment encoding v0.5.4 | 62.7 ns / 2 | 5.62 us / 69 | 180.6 us / 2,058 |
| jsoniter v1.1.12 | 90.2 ns / 2 | 6.71 us / 104 | 212.8 us / 3,085 |
| easyjson v0.9.2 generated | 88.4 ns / 1 | 8.97 us / 71 | 282.7 us / 2,060 |
| `encoding/json/v2`, Go tip | 198.2 ns / 1 | 14.82 us / 39 | 464.6 us / 1,037 |
| `encoding/json`, Go tip | 206.6 ns / 1 | 15.01 us / 39 | 460.2 us / 1,037 |
| Sonic v1.15.2 Std, Go 1.26.4 | 233.0 ns / 5 | 7.82 us / 71 | 227.9 us / 2,055 |

Dynamic decoding into `any`:

| Decoder | 31 B | 4.2 KB | 136.6 KB |
|---|---:|---:|---:|
| **simdjson `ParseAny`, zero copy** | **185.6 ns / 4** | **9.80 us / 297** | **198.8 us / 9,225** |
| simdjson `ParseAny`, owned | 203.7 ns / 5 | 12.01 us / 298 | 206.7 us / 9,226 |
| Segment encoding v0.5.4 | 302.8 ns / 9 | 28.92 us / 559 | 947.9 us / 17,428 |
| go-json v0.10.6 | 306.2 ns / 12 | 27.54 us / 818 | 745.9 us / 25,619 |
| jsoniter v1.1.12 | 331.6 ns / 12 | 24.75 us / 950 | 810.3 us / 29,724 |
| `encoding/json`, Go tip | 758.7 ns / 12 | 47.69 us / 823 | 1,599.6 us / 25,651 |

Run the primary comparison:

```sh
cd benchmarks
GOEXPERIMENT=simd gotip test -run='^$' \
  -bench='^BenchmarkParseTyped$' \
  -benchmem -benchtime=1s -count=5 .
```

See [`benchmarks/README.md`](benchmarks/README.md) for pure-Go, json/v2, native
Sonic, environment capture, and result comparison commands.

## Validation

`Valid` is a recursive descent validator with SWAR and SIMD string scanning:

| Validator | 31 B | 4.2 KB | 136.6 KB |
|---|---:|---:|---:|
| **simdjson** | **22.5 ns** | **1.94 us** | **63.2 us** |
| Segment encoding v0.5.4 | 31.1 ns | 2.65 us | 99.5 us |
| `encoding/json`, Go tip | 50.3 ns | 3.08 us | 98.4 us |
| fastjson v1.6.4 | 37.5 ns | 3.89 us | 151.5 us |

## Correctness

simdjson validates UTF-8, escapes, surrogate pairs, number grammar, integer overflow,
depth, and trailing data. The suite includes all 318 Nicolas Seriot
JSONTestSuite cases, simdjson-derived edge cases, compiled numeric boundaries,
allocation contracts, differential tests and fuzzers against encoding/json
for every stdlib behavior claimed above, and Linux cross-compiles for arm64
and amd64. Memory-safety runs use race and `checkptr=2` instrumentation with
the allocation-contract tests skipped, since instrumentation itself
allocates:

```sh
GOEXPERIMENT=simd gotip test -gcflags='all=-d=checkptr=2' \
  -skip 'Allocs|StaysOnStack|TestParseFloat64' ./...
```

The compiled decoder and encoder cover encoding/json's supported type
universe: structs with flattened anonymous embedding and stdlib's exact
dominance rules, pointers, slices, fixed arrays, maps with string, integer,
and text-marshaling keys, empty and non-empty interfaces with stdlib's
pointer-indirection rules, byte slices as base64, custom
json.Marshaler/Unmarshaler and TextMarshaler/TextUnmarshaler dispatch
(including time.Time), the omitempty and string tag options, named scalar
types, every integer width, floats, and json.Number. Decoding merges into
existing values like encoding/json unless DecoderOptions.Replace is set.
Types stdlib rejects (channels, functions, complex numbers) are rejected at
compile time. One rule is stricter than stdlib: custom un/marshalers must
not retain their receiver after returning.

Run scalar and SIMD verification against the pinned compiler:

```sh
gotip test ./...
GOEXPERIMENT=simd gotip test ./...
gotip vet -unsafeptr=false ./...
```

The vet flag is required because noescape.go deliberately uses the runtime's
pointer-hiding idiom, documented in that file, to keep decode destinations
off the heap while supporting custom un/marshalers.
