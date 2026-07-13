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
Go commit `03845e30f7b73d1703bd8c21017297f6eecb76d6`. Compilation is outside the
timer. Results and encoded output are checked against `encoding/json` before
timing.

### Scorecard

"Rival" means the fastest compatible library on the same Go tip compiler:
go-json v0.10.6, Segment encoding v0.5.4, jsoniter v1.1.12, and fastjson v1.6.10
for validation. Speedups are geometric means across all seven payloads.

| Operation | Contract | Wins vs stdlib | Wins vs rival | vs stdlib | vs rival |
|---|---|---:|---:|---:|---:|
| Validate | Strict JSON and UTF-8 | **7/7** | **7/7** | **2.35x** | **2.15x** |
| Typed decode | Owned strings, reused destination | **7/7** | **7/7** | **4.69x** | **1.84x** |
| Dynamic decode | Owned `any` tree | **7/7** | **7/7** | **4.31x** | **1.93x** |
| Encode | Owned output | **7/7** | **7/7** | **2.33x** | **1.35x** |
| Encode | Reused output buffer | **7/7** | **7/7** | **2.46x** | **1.43x** |

The scorecard does not mix ownership contracts. Source-backed decode and reused
output are reported separately below. Native Sonic uses a different compiler
and is never included in the Go tip rival or winner columns.

### Typed Decode

The owned row is the fair conventional comparison. `ZeroCopy: true` aliases
unescaped strings into immutable input and therefore has a different lifetime
contract.

| Corpus | `encoding/json` | simdjson owned | simdjson source-backed | Fastest tip rival | Native Sonic owned |
|---|---:|---:|---:|---:|---:|
| Canada geometry | 1.163 ms | **232.9 us** | 227.9 us | go-json 679.8 us | 440.5 us |
| CITM catalog | 2.451 ms | **685.4 us** | 620.8 us | go-json 992.8 us | 1.423 ms |
| Go source | 5.968 ms | **1.132 ms** | 1.069 ms | go-json 2.074 ms | 3.345 ms |
| Escaped strings | 178.8 us | **26.01 us** | 23.75 us | go-json 56.77 us | 30.69 us |
| Unicode strings | 37.89 us | **5.179 us** | 4.405 us | go-json 10.61 us | 11.28 us |
| Synthea FHIR | 3.586 ms | **1.265 ms** | 1.207 ms | go-json 1.586 ms | 2.653 ms |
| Twitter status | 1.268 ms | **342.3 us** | 321.9 us | go-json 571.0 us | 699.8 us |

Source-backed allocation counts are not zero for container-heavy models:

| Corpus | Bytes/op | Allocs/op |
|---|---:|---:|
| Canada geometry | 395 B | 1 |
| CITM catalog | 1.677 MiB | 1,224 |
| Go source | 14.32 KiB | 66 |
| Escaped strings | 48.0 KiB | 1 |
| Unicode strings | 18.0 KiB | 1 |
| Synthea FHIR | 1.946 MiB | 351 |
| Twitter status | 631.2 KiB | 139 |

Slices, maps, pointers, escaped text, and custom method receivers still require
storage. Source-backed refers specifically to unescaped string ownership.

### Dynamic And Validation

Dynamic rows fully materialize owned `any` trees. Validation only checks strict
JSON syntax and allocates nothing on valid input.

| Corpus | simdjson dynamic | Fastest tip rival | Native Sonic dynamic | simdjson strict valid | Fastest strict tip rival | Native Sonic syntax-only |
|---|---:|---:|---:|---:|---:|---:|
| Canada geometry | **643.6 us** | go-json 1.292 ms | 737.5 us | **122.7 us** | fastjson 185.5 us | 189.0 us |
| CITM catalog | **1.600 ms** | go-json 3.008 ms | 2.627 ms | **608.3 us** | fastjson 783.8 us | 780.7 us |
| Go source | **3.135 ms** | jsoniter 7.020 ms | 5.523 ms | **906.5 us** | Segment 1.144 ms | 1.552 ms |
| Escaped strings | **25.40 us** | go-json 61.20 us | 32.02 us | **4.159 us** | Segment 55.21 us | 3.327 us |
| Unicode strings | **8.274 us** | go-json 14.98 us | 12.96 us | **3.050 us** | fastjson 6.971 us | 1.742 us |
| Synthea FHIR | **2.447 ms** | go-json 4.189 ms | 4.402 ms | **741.8 us** | fastjson 1.216 ms | 854.6 us |
| Twitter status | **838.6 us** | go-json 1.329 ms | 1.002 ms | **217.3 us** | fastjson 376.7 us | 233.8 us |

Sonic v1.15.2 documents that its native `Valid` does not check invalid UTF-8,
so that column is syntax-only context and is excluded from strict winners and
speedups. simdjson wins all seven strict validation and owned dynamic rows
against the compatible same-toolchain field.

### Encode

Owned rows compare `Marshal` with `Marshal`. Compiled reuse appends into a
caller-owned buffer and is shown separately.

| Corpus | stdlib | simdjson owned | compiled reuse | Fastest tip rival | Native Sonic |
|---|---:|---:|---:|---:|---:|
| Canada geometry | 557.3 us | 429.1 us | **426.4 us** | Segment 452.9 us | 782.8 us |
| CITM catalog | 762.5 us | 293.2 us | **279.0 us** | Segment 301.4 us | 836.2 us |
| Go source | 2.616 ms | 1.131 ms | **1.077 ms** | Segment 1.144 ms | 3.711 ms |
| Escaped strings | 17.09 us | 6.763 us | **6.360 us** | jsoniter 17.19 us | 19.73 us |
| Unicode strings | 17.18 us | 6.819 us | **6.405 us** | jsoniter 16.89 us | 19.76 us |
| Synthea FHIR | 5.116 ms | 1.656 ms | **1.567 ms** | Segment 1.703 ms | 6.834 ms |
| Twitter status | 560.0 us | 231.5 us | **212.4 us** | Segment 269.2 us | 552.9 us |

Both owned and compiled-reuse encoding win every exact-model row. Reuse also
eliminates the output allocation. The hot struct plan classifies common adjacent
field operations once, stores the fused opcode without enlarging the 40-byte
field record, and emits two fields per dispatch. Integer map keys use one local
byte arena and public reflection iteration; no interface-layout or runtime
map-layout assumptions are involved.

### SIMD Versus Pure Go

Both binaries use the same pinned tip compiler and corpus. SIMD dispatch is
selected once at initialization; short runs remain scalar or SWAR.

| simdjson path | SIMD wins | Geomean SIMD uplift |
|---|---:|---:|
| Validation | 5/7 | **1.43x** |
| Dynamic owned | 3/7 | **1.08x** |
| Dynamic source-backed | 3/7 | **1.09x** |
| Typed owned | 5/7 | **1.14x** |
| Typed source-backed | 6/7 | **1.16x** |
| Encode owned | 6/7 | **1.39x** |
| Encode compiled reuse | **7/7** | **1.45x** |

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

Other vector paths include strict UTF-8 validation using ARM64 `TBL` lookup,
contiguous `\uXXXX` validation, U+2028/U+2029 detection, syntax scans used by
string encoding, and shape-dispatched typed decimals. Decimal parsing uses
register-only SWAR for 8-byte blocks and Go-native SIMD for full 16-digit runs.
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
