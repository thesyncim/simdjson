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
| **SIMD, source-backed** | **33.4 ns / 0 allocs** | **3.03 us / 2 allocs** | **92.4 us / 2 allocs** |
| Pure Go, source-backed | 33.5 ns / 0 allocs | 3.21 us / 2 allocs | 97.3 us / 2 allocs |
| **SIMD, owned strings** | **48.9 ns / 1 alloc** | **3.39 us / 3 allocs** | **101.1 us / 3 allocs** |

Reusing the destination removes the remaining container allocation in
source-backed mode: `2.80 us / 0 allocs` for 32 records and
`88.2 us / 0 allocs` for 1,024 records.

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

Compile the decoder once and reuse it concurrently:

```go
decoder, err := simdjson.CompileDecoder[Event](simdjson.TypedOptions{
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

## Choose An API

| Job | API | Data model |
|---|---|---|
| Fast concrete structs | `CompileDecoder[T]` | Compiled fields and scalar operations |
| Repeated zero-allocation traversal | `BuildIndex`, `Index`, `Node` | Source and caller workspace backed |
| Strict JSON Pointer lookup | `GetRaw`, `CompilePointer` | Validates the complete document |
| Early-exit JSON Pointer scan | `ScanRaw`, `CompilePointer` | Stops after validating the target |
| Ordered syntax tree | `Parse`, `Value` | Owned strings and ordered members |
| Standard maps and slices | `ParseAny` | Normal Go dynamic values |
| Validation only | `Valid`, `Validate` | No result allocation |
| Transforms | `AppendCompact`, `AppendIndent`, `AppendCanonicalize` | Caller-owned destination |

The typed plan is immutable after compilation and safe for concurrent use.
Compilation is excluded from the benchmark timer. The plan uses packed
expected-key matching, exact scalar operations, lazy replacement resets, and
specialized fixed-float arrays.

| Workload | SIMD | Pure Go |
|---|---:|---:|
| 31 B, fresh | **33.38 ns / 0 allocs** | 33.49 ns / 0 allocs |
| 4.2 KB, fresh | **3.029 us / 2 allocs** | 3.207 us / 2 allocs |
| 4.2 KB, reused | **2.802 us / 0 allocs** | 2.964 us / 0 allocs |
| 136.6 KB, fresh | **92.409 us / 2 allocs** | 97.271 us / 2 allocs |
| 136.6 KB, reused | **88.172 us / 0 allocs** | 93.106 us / 0 allocs |

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

| Typed mode | Unescaped strings | Cost |
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
| **simdjson compiled, SIMD** | **33.4 ns / 0** | **3.03 us / 2** | **92.4 us / 2** |
| simdjson compiled, pure Go | 33.5 ns / 0 | 3.21 us / 2 | 97.3 us / 2 |
| Sonic v1.15.2 Fastest, Go 1.26.4 | 187.9 ns / 4 | 5.74 us / 6 | 170.5 us / 6 |

Owned-string comparison:

| Decoder | 31 B | 4.2 KB | 136.6 KB |
|---|---:|---:|---:|
| **simdjson compiled, SIMD** | **48.9 ns / 1** | **3.39 us / 3** | **101.1 us / 3** |
| go-json v0.10.6 | 56.2 ns / 2 | 5.78 us / 35 | 174.4 us / 1,027 |
| Segment encoding v0.5.4 | 59.8 ns / 2 | 5.60 us / 69 | 171.5 us / 2,058 |
| jsoniter v1.1.12 | 86.4 ns / 2 | 6.68 us / 104 | 201.9 us / 3,085 |
| easyjson v0.9.2 generated | 87.3 ns / 1 | 8.51 us / 71 | 262.4 us / 2,060 |
| `encoding/json/v2`, Go tip | 170.6 ns / 1 | 12.48 us / 39 | 379.5 us / 1,037 |
| `encoding/json`, Go tip | 206.3 ns / 1 | 15.55 us / 39 | 465.9 us / 1,037 |
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

Compiled decoders support exported structs, nested named structs, pointers,
slices, fixed arrays, named scalar types, booleans, strings, every integer
width, floats, and `json.Number`. Maps, interfaces, `[]byte`, and custom
unmarshaler dispatch are not supported. Untagged anonymous fields
are rejected rather than flattened.

Run scalar and SIMD verification against the pinned compiler:

```sh
gotip test ./...
GOEXPERIMENT=simd gotip test ./...
```
