# simdjson

[![ci](https://github.com/thesyncim/simdjson/actions/workflows/ci.yml/badge.svg)](https://github.com/thesyncim/simdjson/actions/workflows/ci.yml)

Strict, high-performance JSON for Go tip, written entirely in Go.

simdjson provides compiled typed decoding and encoding, owned and source-backed
dynamic decoding, caller-backed structural indexes, JSON Pointer lookup,
validation, and transforms. The root module has no third-party dependencies,
generated codecs, assembly, C, `go:linkname`, or runtime map-layout assumptions.

> **Pre-release toolchain:** simdjson currently requires a Go 1.27 development
> toolchain. SIMD builds additionally require `GOEXPERIMENT=simd` and Go tip's
> experimental `simd/archsimd` package. Both surfaces may change before release.

## Performance

The primary benchmark is the complete seven-file
`encoding/json/internal/jsontest` corpus from pinned Go tip: 6.33 MiB of
geospatial data, catalogs, Go source statistics, escaped and Unicode strings,
FHIR, and Twitter data. The benchmark uses the exact upstream concrete models,
not reduced lookalikes.

These are medians of six 200 ms samples on an Apple M4 Max (`darwin/arm64`) with
Go commit `d468ad3648be469ffc4090e4586c29709182d6b6`. Compilation is outside the
timer. Results and encoded output are checked against `encoding/json` before
timing.

### Scorecard

"Rival" means the fastest compatible library on the same Go tip compiler:
go-json v0.10.5, Segment encoding v0.5.4, jsoniter v1.1.12, and fastjson v1.6.4
for validation. Speedups are geometric means across all seven payloads.

| Operation | Contract | Wins vs stdlib | Wins vs rival | vs stdlib | vs rival |
|---|---|---:|---:|---:|---:|
| Validate | Syntax only | **7/7** | **7/7** | **1.95x** | **1.79x** |
| Typed decode | Owned strings, reused destination | **7/7** | **7/7** | **3.82x** | **1.54x** |
| Dynamic decode | Owned `any` tree | **7/7** | **7/7** | **4.15x** | **1.89x** |
| Encode | Owned output | **7/7** | 4/7 | **1.94x** | **1.15x** |
| Encode | Reused output buffer | **7/7** | 4/7 | **2.09x** | **1.24x** |

The scorecard does not mix ownership contracts. Source-backed decode and reused
output are reported separately below. Native Sonic uses a different compiler
and is never included in the Go tip rival or winner columns.

### Typed Decode

The owned row is the fair conventional comparison. `ZeroCopy: true` aliases
unescaped strings into immutable input and therefore has a different lifetime
contract.

| Corpus | `encoding/json` | simdjson owned | simdjson source-backed | Fastest tip rival | Native Sonic owned |
|---|---:|---:|---:|---:|---:|
| Canada geometry | 1.248 ms | **359.2 us** | 343.9 us | go-json 756.0 us | 440.5 us |
| CITM catalog | 2.519 ms | **760.1 us** | 667.7 us | go-json 1.047 ms | 1.423 ms |
| Go source | 6.154 ms | **1.646 ms** | 1.572 ms | Segment 2.139 ms | 3.345 ms |
| Escaped strings | 187.9 us | **27.45 us** | 25.01 us | go-json 63.27 us | 30.69 us |
| Unicode strings | 38.84 us | **8.255 us** | 7.378 us | go-json 11.31 us | 11.28 us |
| Synthea FHIR | 3.688 ms | **1.416 ms** | 1.292 ms | go-json 1.670 ms | 2.653 ms |
| Twitter status | 1.307 ms | **398.1 us** | 353.0 us | go-json 595.1 us | 699.8 us |

Source-backed allocation counts are not zero for container-heavy models:

| Corpus | Bytes/op | Allocs/op |
|---|---:|---:|
| Canada geometry | 601 B | 2 |
| CITM catalog | 1.68 MiB | 1,227 |
| Go source | 21.1 KiB | 98 |
| Escaped strings | 48.0 KiB | 1 |
| Unicode strings | 18.0 KiB | 1 |
| Synthea FHIR | 1.95 MiB | 355 |
| Twitter status | 631 KiB | 140 |

Slices, maps, pointers, escaped text, and custom method receivers still require
storage. Source-backed refers specifically to unescaped string ownership.

### Dynamic And Validation

Dynamic rows fully materialize owned `any` trees. Validation only checks strict
JSON syntax and allocates nothing on valid input.

| Corpus | simdjson dynamic | Fastest tip rival | Native Sonic dynamic | simdjson valid | Fastest tip rival | Native Sonic valid |
|---|---:|---:|---:|---:|---:|---:|
| Canada geometry | **695.2 us** | go-json 1.432 ms | 737.5 us | **165.9 us** | fastjson 212.8 us | 189.0 us |
| CITM catalog | **1.717 ms** | jsoniter 3.191 ms | 2.627 ms | **673.8 us** | fastjson 847.7 us | 780.7 us |
| Go source | **3.302 ms** | jsoniter 7.337 ms | 5.523 ms | **1.010 ms** | Segment 1.178 ms | 1.552 ms |
| Escaped strings | **25.96 us** | go-json 68.37 us | 32.02 us | 4.357 us | Segment 54.36 us | **3.327 us** |
| Unicode strings | **11.15 us** | go-json 15.99 us | 12.96 us | 6.077 us | fastjson 6.940 us | **1.742 us** |
| Synthea FHIR | **2.562 ms** | go-json 4.384 ms | 4.402 ms | **832.3 us** | fastjson 1.269 ms | 854.6 us |
| Twitter status | **898.4 us** | go-json 1.397 ms | 1.002 ms | 274.7 us | fastjson 389.6 us | **233.8 us** |

Sonic v1.15.2 remains faster on three specialist validation rows. simdjson wins
all seven owned dynamic rows against both the same-toolchain field and native
Sonic.

### Encode

Owned rows compare `Marshal` with `Marshal`. Compiled reuse appends into a
caller-owned buffer and is shown separately.

| Corpus | stdlib | simdjson owned | compiled reuse | Fastest tip rival | Native Sonic |
|---|---:|---:|---:|---:|---:|
| Canada geometry | 594.0 us | **465.9 us** | **453.8 us** | Segment 478.2 us | 782.8 us |
| CITM catalog | 801.4 us | 357.9 us | 334.3 us | **Segment 316.5 us** | 836.2 us |
| Go source | 2.795 ms | 1.371 ms | 1.297 ms | **Segment 1.220 ms** | 3.711 ms |
| Escaped strings | 18.82 us | **10.13 us** | **9.123 us** | jsoniter 21.02 us | 19.73 us |
| Unicode strings | 18.98 us | **10.20 us** | **9.118 us** | go-json 20.59 us | 19.76 us |
| Synthea FHIR | 5.547 ms | 2.317 ms | 2.213 ms | **Segment 1.811 ms** | 6.834 ms |
| Twitter status | 609.0 us | **288.0 us** | **255.7 us** | go-json 295.6 us | 552.9 us |

The remaining encode gaps are explicit: Go source is shortest-float heavy, and
Synthea invokes value-receiver custom marshalers thousands of times. simdjson
keeps safe public reflection there instead of using runtime interface-layout
or map-layout tricks.

### SIMD Versus Pure Go

Both binaries use the same pinned tip compiler and corpus. SIMD dispatch is
selected once at initialization; short runs remain scalar or SWAR.

| simdjson path | SIMD wins | Geomean SIMD uplift |
|---|---:|---:|
| Validation | **7/7** | **1.26x** |
| Dynamic owned | **7/7** | **1.08x** |
| Dynamic source-backed | **7/7** | **1.09x** |
| Typed owned | 3/7 | **1.06x** |
| Typed source-backed | 6/7 | **1.09x** |
| Encode owned | 5/7 | **1.24x** |
| Encode compiled reuse | 4/7 | **1.29x** |

Owned typed decode includes a source-sized copy that SIMD cannot accelerate;
the source-backed row exposes parser uplift more directly. SIMD is not claimed
as universally faster per row, only where the complete measured path improves.

Native Sonic v1.15.2 falls back to stdlib on Go 1.27, so its numbers come from
the isolated Go 1.26.4 module using six 300 ms samples. They are useful context,
not compiler-identical comparisons.

## Quick Start

Install Go tip, then add simdjson from that toolchain:

```sh
go install golang.org/dl/gotip@latest
gotip download
gotip get github.com/thesyncim/simdjson@latest
```

Use the cached convenience API for ordinary calls:

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

encoded, err := simdjson.Marshal(&event)
```

Compile once on hot paths. Plans are immutable and safe for concurrent use;
destinations remain caller-owned.

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

encoder, err := simdjson.CompileEncoder[Event](simdjson.EncoderOptions{})
if err != nil {
	return err
}
buf, err = encoder.AppendJSON(buf[:0], &event)
```

`DecoderOptions.Replace` resets destination state not mentioned by the next
document. The default merges like `encoding/json`: absent members retain their
current values, while null clears pointers, maps, slices, and interfaces.

## API Map

| Job | API | Ownership |
|---|---|---|
| Stdlib-style typed decode | `Unmarshal[T]` | Owned strings; cached plan |
| Repeated typed decode | `CompileDecoder[T]` | Owned or source-backed strings |
| Stdlib-style typed encode | `Marshal[T]` | Owned output; cached plan |
| Reused-buffer encode | `CompileEncoder[T]`, `AppendJSON` | Caller-owned output |
| Conventional dynamic tree | `ParseAny` | Owned by default |
| Ordered value tree | `Parse`, `Value` | Owned by default |
| Structural traversal | `BuildIndex`, `Index`, `Node` | Aliases source and workspace |
| JSON Pointer | `GetRaw`, `ScanRaw`, `CompilePointer` | Aliases source |
| Strict validation | `Valid`, `Validate` | No result allocation |
| Transforms | `AppendCompact`, `AppendIndent`, `AppendCanonicalize` | Caller-owned output |

`DecodeArray` decodes a top-level array while reusing caller-provided slice
capacity. Decode errors use `*DecodeError` with byte offset and destination
path; the path is assembled only on error.

## Ownership

| Mode | Unescaped strings | Caller obligation |
|---|---|---|
| Default | Alias one private copy of input | None; result is independent of `src` |
| `ZeroCopy: true` | Alias caller input | Keep `src` alive and immutable |
| Escaped string | Decode into owned storage | None |

`RawValue`, `Index`, and `Node` always alias input. `Index` and `Node` also
alias the caller-provided `IndexEntry` workspace.

## SIMD Dispatch

`GOEXPERIMENT=simd` enables Go-native kernels. Runtime capabilities are read
once, and implementation choices are fixed during package initialization.

| Runtime | String scanning | Decimal reduction |
|---|---|---|
| arm64 | NEON on sustained runs; scalar/SWAR tails | NEON pairwise 16-digit reduction |
| amd64 with AVX-512 | 64-byte AVX-512 | AVX 16-digit reduction |
| amd64 with AVX2 | 32-byte AVX2 | AVX 16-digit reduction |
| Other build or CPU | Scalar Go | Scalar Go |

Other vector paths include strict UTF-8 validation, contiguous `\uXXXX`
validation, U+2028/U+2029 detection, and syntax scans used by string encoding.
Every vector load is length-guarded. `CurrentSIMD()` reports the selected
backend, vector width, threshold, number backend, and CPU features.

## Safety Model

simdjson uses `unsafe` only in measured internal paths and keeps those uses
narrow:

- vector loads run only when a complete block remains;
- typed offsets and strides come from public `reflect` metadata;
- maps use public reflection APIs and never assume runtime layout;
- no `go:linkname`, hand-written assembly, C, or interface-layout fabrication;
- source-backed APIs explicitly require immutable input;
- pointer-receiver custom methods use heap-backed shadows that remain valid
  across stack growth and GC;
- pure and SIMD builds run differential tests, race detection, `checkptr=2`,
  fuzz smoke, and amd64/arm64 build gates.

The custom receiver shadow is a normal shallow Go copy. A method that retains
its receiver can safely use it after return, but later direct field writes to
that retained shadow do not update the original value. Types that require
stable receiver identity should keep that boundary on `encoding/json`.

## Correctness

The suite includes:

- all 318 Nicolas Seriot JSONTestSuite parsing cases;
- all seven pinned Go tip high-level payloads and exact concrete models;
- typed and dynamic differentials against `encoding/json`;
- field dominance, tags, merge behavior, duplicate names, numeric boundaries,
  UTF-8, escapes, custom methods, map keys, depth, and error paths;
- randomized scalar/SIMD differentials across lengths and alignments;
- fuzzers for validation, transforms, typed decode, encode, numbers, and SIMD
  scanners.

Run the local release gate with the pinned compiler:

```sh
./scripts/bootstrap-gotip.sh "$HOME/sdk/simdjson-gotip"
TIP_GO="$HOME/sdk/simdjson-gotip/bin/go"

"$TIP_GO" test ./...
GOEXPERIMENT=simd "$TIP_GO" test ./...
"$TIP_GO" vet -unsafeptr=false ./...
GOEXPERIMENT=simd "$TIP_GO" test -race \
  -skip 'Allocs|StaysOnStack|TestParseFloat64' ./...
GOEXPERIMENT=simd "$TIP_GO" test -gcflags='all=-d=checkptr=2' \
  -skip 'Allocs|StaysOnStack|TestParseFloat64' ./...
./scripts/check-stdlib-corpus.sh "$TIP_GO"
```

Vet's `unsafeptr` analyzer is disabled only for the documented runtime-style
pointer-hiding helper in `noescape.go`; the rest of vet remains enabled.

## Reproduce Benchmarks

The comparison dependencies live in nested modules and never enter the root
library graph.

```sh
./scripts/bootstrap-gotip.sh "$HOME/sdk/simdjson-gotip"
TIP_GO="$HOME/sdk/simdjson-gotip/bin/go"

cd benchmarks
"$TIP_GO" test -run '^$' -bench '^BenchmarkStdlibCorpus$' -benchmem \
  -benchtime=200ms -count=6 . > corpus-pure.txt
GOEXPERIMENT=simd "$TIP_GO" test -run '^$' \
  -bench '^BenchmarkStdlibCorpus$' -benchmem \
  -benchtime=200ms -count=6 . > corpus-simd.txt

"$TIP_GO" run golang.org/x/perf/cmd/benchstat@v0.0.0-20260615155930-9e4b9ddef5b6 \
  corpus-pure.txt corpus-simd.txt

cd legacy
GOTOOLCHAIN=go1.26.4 go test -run '^$' \
  -bench '^BenchmarkStdlibCorpusNativeSonic$' -benchmem \
  -benchtime=300ms -count=6 . > corpus-sonic.txt
```

`scripts/check-stdlib-corpus.sh` verifies the compressed payloads and generated
model copies byte-for-byte against the pinned GOROOT. easyjson is omitted from
the exact-model table because attaching generated methods to the shared model
would cause other libraries to call easyjson instead of their own decoder. It
remains in the separate synthetic comparison suite.

Run every comparison suite from the repository root:

```sh
TIP_GO="$TIP_GO" ./benchmarks/run-comparison.sh
```
