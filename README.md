# simdjson

[![ci](https://github.com/thesyncim/simdjson/actions/workflows/ci.yml/badge.svg)](https://github.com/thesyncim/simdjson/actions/workflows/ci.yml)

Strict, high-performance JSON for Go tip, written entirely in Go.

simdjson provides compiled typed decoders and encoders, dynamic decoding,
zero-allocation structural indexes, JSON Pointer lookup, transforms, and
runtime-selected SIMD. The root module has no third-party dependencies,
assembly, C, `go:linkname`, or runtime map-layout assumptions.

`Marshal` and `Unmarshal` target `encoding/json` behavior for supported types,
including field dominance, tags, custom methods, merge semantics, numeric
boundaries, and escaping. Compatibility is checked against the pinned Go
standard library rather than inferred from matching API names.

> **Toolchain status:** simdjson currently requires a Go 1.27 development
> toolchain. SIMD builds additionally require `GOEXPERIMENT=simd` and Go tip's
> experimental `simd/archsimd` package. The API and toolchain are pre-release.

## Performance

The primary benchmark is Go tip's complete seven-file
`encoding/json/internal/jsontest` corpus: 6.33 MiB of real JSON covering
geospatial data, catalogs, Go source statistics, escaped and Unicode strings,
FHIR, and Twitter data. The same payloads and exact upstream concrete models
are also correctness tests.

Results below are medians of six 500 ms samples on an Apple M4 Max
(`darwin/arm64`). The compiler is pinned to Go commit
`d468ad3648be469ffc4090e4586c29709182d6b6`. Compilation is outside the timer;
typed decode reuses its destination, matching a normal hot loop. The reported
95% confidence intervals were at most 4%.

### Typed Decode

These rows use owned strings. Both libraries return data independent of the
input. simdjson owns strings by making at most one private source-sized copy;
the standard library allocates strings individually, so `B/op` can differ
substantially even when semantics match.

| Corpus | Size | `encoding/json` | simdjson pure Go | simdjson SIMD | SIMD speedup |
|---|---:|---:|---:|---:|---:|
| Canada geometry | 264 KiB | 1.270 ms | 1.148 ms | 1.154 ms | 1.10x |
| CITM catalog | 1.65 MiB | 2.581 ms | 1.190 ms | 1.205 ms | 2.14x |
| Go source | 1.85 MiB | 6.285 ms | 2.138 ms | 2.135 ms | 2.94x |
| Escaped strings | 41.1 KiB | 193.7 us | 125.8 us | 123.1 us | 1.57x |
| Unicode strings | 17.7 KiB | 40.72 us | 13.33 us | 11.90 us | 3.42x |
| Synthea FHIR | 1.92 MiB | 3.815 ms | 1.784 ms | 1.788 ms | 2.13x |
| Twitter status | 617 KiB | 1.344 ms | 602.8 us | 603.8 us | 2.23x |

SIMD and pure Go are close on structure-heavy typed decoding because field and
container work dominates string scanning. The Unicode corpus benefits most
from SIMD. No result is hidden when pure Go happens to be slightly faster.

### Source-Backed Decode

`ZeroCopy: true` aliases unescaped strings into the immutable input. It avoids
the source-sized ownership copy but is a different lifetime contract and is
therefore reported separately.

| Corpus | SIMD typed decode | Bytes/op | Allocs/op |
|---|---:|---:|---:|
| Canada geometry | 1.071 ms | 751 B | 2 |
| CITM catalog | 1.070 ms | 28.2 KiB | 1,212 |
| Go source | 1.951 ms | 10.5 KiB | 48 |
| Escaped strings | 114.6 us | 54.8 KiB | 531 |
| Unicode strings | 10.54 us | 864 B | 5 |
| Synthea FHIR | 1.622 ms | 70.3 KiB | 2,539 |
| Twitter status | 539.0 us | 104 KiB | 1,278 |

Zero-copy does not mean zero allocation: slices, maps, pointers, custom method
receivers, and decoded escape sequences still require storage.

### Dynamic Decode And Validation

Across the seven payloads, SIMD `ParseAny` with owned strings is 1.67x to 4.22x
faster than `encoding/json`, with a 2.93x geometric-mean speedup. `Valid` wins
six of seven payloads, with a 1.28x geometric-mean speedup. The exception is the
escape-dense string corpus, where simdjson validation is 1.19x slower.

### Typed Encode

Owned-output rows compare `Marshal` with `Marshal`. The compiled rows reuse a
caller-owned output buffer and are labeled separately because that allocation
contract has no direct `encoding/json.Marshal` equivalent.

| Corpus | `encoding/json.Marshal` | simdjson `Marshal` | compiled `AppendJSON` |
|---|---:|---:|---:|
| Canada geometry | 602.6 us | 523.4 us | 506.1 us |
| CITM catalog | 804.8 us | 367.2 us | 341.0 us |
| Go source | 2.663 ms | 1.416 ms | 1.344 ms |
| Escaped strings | **17.67 us** | 39.68 us | 38.32 us |
| Unicode strings | **18.19 us** | 40.20 us | 39.08 us |
| Synthea FHIR | **5.267 ms** | 5.719 ms | 5.631 ms |
| Twitter status | 589.4 us | 444.5 us | 418.8 us |

Encoding wins four payloads. The string-only encoder and custom-method-heavy
FHIR encoder remain explicit optimization targets; this README does not claim
simdjson wins every schema.

## Quick Start

Install Go tip, then add simdjson to a module using that toolchain:

```sh
go install golang.org/dl/gotip@latest
gotip download
gotip get github.com/thesyncim/simdjson@latest
```

Define a model and use the cached convenience API:

```go
type Event struct {
	ID      int       `json:"id"`
	Name    string    `json:"name"`
	Scores  []float64 `json:"scores"`
	Enabled bool      `json:"enabled"`
}

var event Event
if err := simdjson.Unmarshal(payload, &event); err != nil {
	return err
}
```

Compile once on hot paths. Plans are immutable and safe for concurrent use;
destinations are caller-owned and must not be shared concurrently:

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

Encoding mirrors decoding:

```go
encoder, err := simdjson.CompileEncoder[Event](simdjson.EncoderOptions{})
if err != nil {
	return err
}
buf, err = encoder.AppendJSON(buf[:0], &event)
```

`DecoderOptions.Replace` resets destination state not mentioned by the next
document. The default merges like `encoding/json`: absent members retain their
current values, while null clears pointers, maps, slices, and interfaces.

## Choose An API

| Job | API | Ownership |
|---|---|---|
| Stdlib-style typed decode | `Unmarshal[T]` | Owned strings; cached plan |
| Repeated typed decode | `CompileDecoder[T]` | Owned or source-backed strings |
| Stdlib-style typed encode | `Marshal[T]` | Owned output; cached plan |
| Reused-buffer encode | `CompileEncoder[T]`, `AppendJSON` | Caller-owned output |
| Ordered syntax tree | `Parse`, `Value` | Owned by default |
| Maps and slices | `ParseAny` | Owned by default |
| Structural traversal | `BuildIndex`, `Index`, `Node` | Aliases source and workspace |
| Strict JSON Pointer | `GetRaw`, `CompilePointer` | Aliases source |
| Early-exit pointer scan | `ScanRaw`, `CompilePointer` | Aliases source |
| Validation | `Valid`, `Validate` | No result allocation |
| Transforms | `AppendCompact`, `AppendIndent`, `AppendCanonicalize` | Caller-owned output |

`DecodeArray` decodes a top-level array while reusing caller-provided slice
capacity. Decode errors use `*DecodeError` with byte offset and destination
path, assembled only on error.

## Ownership Rules

| Mode | Unescaped strings | Caller obligation |
|---|---|---|
| Default | Alias one private copy of the input | None; result is independent of `src` |
| `ZeroCopy: true` | Alias caller `src` | Keep `src` alive and immutable |
| Escaped string | Decoded into owned storage | None |

`RawValue`, `Index`, and `Node` always alias their input. `Index` and `Node`
also alias the caller-provided `IndexEntry` workspace. Do not mutate either
while handles are in use.

## Safety Model

simdjson uses `unsafe` in measured internal paths, but keeps the allowed uses
narrow and testable:

- SIMD loads execute only when a complete vector remains; scalar code handles
  every tail. Tests sweep lengths, start offsets, alignments, every byte value,
  and guard bytes immediately beyond the slice.
- Typed field offsets and element strides come from public `reflect` metadata.
  Maps use public reflection APIs and never assume a runtime map layout.
- The runtime-style `noescape` helper is limited to synchronous internal
  reflection that cannot invoke user code or retain a `reflect.Value`.
- Pointer-receiver custom methods run on a heap-backed shadow that is copied
  back before return. A retained receiver remains valid across stack growth and
  GC. This is a deliberate receiver-identity difference from `encoding/json`:
  direct field writes through the retained shadow after return do not update
  the original value. The shadow is a normal shallow Go copy, so referenced
  maps, slices, and pointers retain their ordinary aliasing behavior. Types
  whose custom methods require stable receiver identity should use
  `encoding/json` for that boundary.
- Zero-copy APIs are opt-in and explicitly require immutable input. Owned APIs
  are tested by mutating the original source after decoding.
- Native CI runs pure Go and SIMD tests, race detection, `checkptr=2`, scalar
  versus SIMD differential tests, fuzz smoke, and cross-architecture builds on
  amd64 and native arm64.

The project deliberately avoids `go:linkname`, hand-written assembly, C,
runtime object-layout assumptions, and unguarded vector reads. Performance
changes are kept only when correctness remains byte-for-byte identical.

## SIMD Dispatch

`GOEXPERIMENT=simd` enables Go-native vector kernels and chooses the best
available implementation once during package initialization:

| Runtime | String scanner | 16-digit reduction |
|---|---|---|
| arm64 | NEON | NEON pairwise reduction |
| amd64 with AVX-512 | 64-byte AVX-512 | AVX reduction |
| amd64 with AVX2 | 32-byte AVX2 | AVX reduction |
| Other build or CPU | Scalar Go | Scalar Go |

Tiny inputs remain on scalar or SWAR paths when vector setup costs more.
`CurrentSIMD()` reports the selected backend, threshold, width, number backend,
and detected CPU features.

## Correctness

The suite includes:

- all 318 Nicolas Seriot JSONTestSuite parsing cases;
- all seven pinned Go tip high-level corpus payloads and exact concrete models;
- typed and dynamic differentials against `encoding/json` in owned and
  zero-copy modes;
- custom marshal/text method, map key, merge, duplicate-name, numeric boundary,
  UTF-8, escape, depth, and error-path cases;
- retained custom receiver tests across forced stack growth and GC;
- fuzzers for validators, transforms, typed decode, encode, numbers, and SIMD
  scanner parity.

Run the complete local gate with the pinned compiler:

```sh
TIP_GO="$HOME/sdk/simdjson-gotip/bin/go"
./scripts/bootstrap-gotip.sh "$HOME/sdk/simdjson-gotip"

"$TIP_GO" test ./...
GOEXPERIMENT=simd "$TIP_GO" test ./...
"$TIP_GO" vet -unsafeptr=false ./...
GOEXPERIMENT=simd "$TIP_GO" test -race \
  -skip 'Allocs|StaysOnStack|TestParseFloat64' ./...
GOEXPERIMENT=simd "$TIP_GO" test -gcflags='all=-d=checkptr=2' \
  -skip 'Allocs|StaysOnStack|TestParseFloat64' ./...
./scripts/check-stdlib-corpus.sh "$TIP_GO"
```

Vet's `unsafeptr` analyzer is disabled only because `noescape.go` contains the
runtime pointer-hiding idiom described above; the rest of vet still runs.

## Reproduce The Benchmarks

Build the exact compiler and run both modes against the same corpus:

```sh
./scripts/bootstrap-gotip.sh "$HOME/sdk/simdjson-gotip"
TIP_GO="$HOME/sdk/simdjson-gotip/bin/go"

cd tests/stdlib
"$TIP_GO" test -run '^$' -bench HighLevelCorpus -benchmem \
  -benchtime=500ms -count=6 > corpus-pure.txt
GOEXPERIMENT=simd "$TIP_GO" test -run '^$' -bench HighLevelCorpus -benchmem \
  -benchtime=500ms -count=6 > corpus-simd.txt

"$TIP_GO" run golang.org/x/perf/cmd/benchstat@v0.0.0-20260615155930-9e4b9ddef5b6 \
  corpus-pure.txt corpus-simd.txt
```

The nested corpus module contains the test-only Zstandard dependency; the root
library module remains dependency-free. `scripts/check-stdlib-corpus.sh`
verifies payloads and generated models byte-for-byte against the pinned GOROOT.

Third-party library comparisons remain isolated in `benchmarks/` so their
dependencies cannot leak into the library module:

From the repository root:

```sh
TIP_GO="$TIP_GO" ./benchmarks/run-comparison.sh
```

Those comparison results are intentionally not copied into this README unless
they are rerun on the documented compiler, machine, fixtures, and ownership
contract.
