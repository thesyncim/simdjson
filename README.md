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
| Validate | Strict JSON and UTF-8 | **7/7** | **7/7** | **2.38x** | **2.18x** |
| Typed decode | Owned strings, reused destination | **7/7** | **7/7** | **4.70x** | **1.87x** |
| Dynamic decode | Owned `any` tree | **7/7** | **7/7** | **4.26x** | **1.93x** |
| Encode | Owned output | **7/7** | **7/7** | **2.36x** | **1.41x** |
| Encode | Reused output buffer | **7/7** | **7/7** | **2.52x** | **1.50x** |

The scorecard does not mix ownership contracts. Source-backed decode and reused
output are reported separately below. Native Sonic uses a different compiler
and is never included in the Go tip rival or winner columns.

### Typed Decode

The owned row is the fair conventional comparison. `ZeroCopy: true` aliases
unescaped strings into immutable input and therefore has a different lifetime
contract.

| Corpus | `encoding/json` | simdjson owned | simdjson source-backed | Fastest tip rival | Native Sonic owned |
|---|---:|---:|---:|---:|---:|
| Canada geometry | 1.255 ms | **236.8 us** | 226.6 us | go-json 765.2 us | 441.2 us |
| CITM catalog | 2.589 ms | **772.2 us** | 698.1 us | go-json 1.083 ms | 1.433 ms |
| Go source | 6.532 ms | **1.130 ms** | 1.055 ms | Segment 2.196 ms | 3.321 ms |
| Escaped strings | 188.1 us | **27.44 us** | 25.39 us | go-json 62.10 us | 30.80 us |
| Unicode strings | 40.51 us | **5.636 us** | 4.756 us | go-json 11.64 us | 11.02 us |
| Synthea FHIR | 3.831 ms | **1.396 ms** | 1.322 ms | go-json 1.719 ms | 2.695 ms |
| Twitter status | 1.381 ms | **379.7 us** | 351.4 us | go-json 608.4 us | 707.9 us |

Source-backed allocation counts are not zero for container-heavy models:

| Corpus | Bytes/op | Allocs/op |
|---|---:|---:|
| Canada geometry | 407 B | 1 |
| CITM catalog | 1.677 MiB | 1,227 |
| Go source | 13.85 KiB | 64 |
| Escaped strings | 48.0 KiB | 1 |
| Unicode strings | 18.0 KiB | 1 |
| Synthea FHIR | 1.948 MiB | 356 |
| Twitter status | 631.3 KiB | 140 |

Slices, maps, pointers, escaped text, and custom method receivers still require
storage. Source-backed refers specifically to unescaped string ownership.

### Dynamic And Validation

Dynamic rows fully materialize owned `any` trees. Validation only checks strict
JSON syntax and allocates nothing on valid input.

| Corpus | simdjson dynamic | Fastest tip rival | Native Sonic dynamic | simdjson strict valid | Fastest strict tip rival | Native Sonic syntax-only |
|---|---:|---:|---:|---:|---:|---:|
| Canada geometry | **704.9 us** | go-json 1.465 ms | 746.3 us | **124.2 us** | fastjson 210.9 us | 191.5 us |
| CITM catalog | **1.878 ms** | go-json 3.358 ms | 2.649 ms | **659.9 us** | fastjson 864.1 us | 786.1 us |
| Go source | **3.401 ms** | jsoniter 7.559 ms | 5.584 ms | **952.1 us** | Segment 1.215 ms | 1.549 ms |
| Escaped strings | **26.78 us** | go-json 67.99 us | 32.00 us | **4.406 us** | Segment 58.39 us | 3.329 us |
| Unicode strings | **9.009 us** | go-json 16.53 us | 12.64 us | **3.257 us** | fastjson 7.093 us | 1.744 us |
| Synthea FHIR | **2.748 ms** | go-json 4.598 ms | 4.472 ms | **773.2 us** | fastjson 1.274 ms | 858.0 us |
| Twitter status | **916.4 us** | go-json 1.445 ms | 992.9 us | **231.2 us** | fastjson 396.3 us | 233.9 us |

Sonic v1.15.2 documents that its native `Valid` does not check invalid UTF-8,
so that column is syntax-only context and is excluded from strict winners and
speedups. simdjson wins all seven strict validation and owned dynamic rows
against the compatible same-toolchain field.

### Encode

Owned rows compare `Marshal` with `Marshal`. Compiled reuse appends into a
caller-owned buffer and is shown separately.

| Corpus | stdlib | simdjson owned | compiled reuse | Fastest tip rival | Native Sonic |
|---|---:|---:|---:|---:|---:|
| Canada geometry | 594.1 us | 451.9 us | **443.3 us** | Segment 483.5 us | 772.3 us |
| CITM catalog | 825.3 us | 298.5 us | **284.9 us** | Segment 319.6 us | 843.0 us |
| Go source | 2.731 ms | 1.120 ms | **1.061 ms** | Segment 1.210 ms | 3.716 ms |
| Escaped strings | 17.80 us | 7.268 us | **6.705 us** | Segment 19.29 us | 19.43 us |
| Unicode strings | 17.92 us | 7.369 us | **6.760 us** | Segment 19.55 us | 19.29 us |
| Synthea FHIR | 5.459 ms | 1.741 ms | **1.638 ms** | Segment 1.826 ms | 6.854 ms |
| Twitter status | 596.2 us | 242.7 us | **219.5 us** | go-json 287.0 us | 554.6 us |

Both owned and compiled-reuse encoding win every exact-model row. Reuse also
eliminates the output allocation. The hot struct plan classifies common adjacent
field operations once, stores the fused opcode without enlarging the 40-byte
field record, and emits two fields per dispatch. Integer map keys use one local
byte arena and public reflection iteration; no interface-layout or runtime
map-layout assumptions are involved. Decimal integers use compact pair stores,
an exact 8-digit SWAR lane, a cold 10-digit lane, and a 16-digit SIMD formatter
for large values without enlarging the common short-number instruction path.

### SIMD Versus Pure Go

Both binaries use the same pinned tip compiler and corpus. SIMD dispatch is
selected once at initialization; short runs remain scalar or SWAR.

| simdjson path | SIMD wins | Geomean SIMD uplift |
|---|---:|---:|
| Validation | 6/7 | **1.39x** |
| Dynamic owned | 3/7 | **1.08x** |
| Dynamic source-backed | 2/7 | **1.09x** |
| Typed owned | 4/7 | **1.14x** |
| Typed source-backed | 5/7 | **1.16x** |
| Encode owned | 6/7 | **1.40x** |
| Encode compiled reuse | 6/7 | **1.43x** |

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
| SIMD byte kernels | `simd` subpackage | Caller-provided slices and fixed blocks |

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

## Reusable SIMD Kernels

`github.com/thesyncim/simdjson/simd` owns every architecture-specific kernel,
runtime feature probe, and dispatch decision. The root parser contains only
JSON policy adapters. The subpackage can be used independently and adds no
module dependencies.

The fixed-array digit API proves load and store widths at the call site, does
not allocate, and keeps validation explicit:

```go
if len(src) >= 16 {
	digits := (*[16]byte)(src)
	if simd.All16Digits(digits) {
		value := simd.Parse16Digits(digits)
		_ = value
	}
}
```

The package also exposes:

- `Store8Digits` and `Store16Digits` for fixed-width decimal formatting;
- JSON and HTML-safe string scanners, including syntax-only variants;
- fused `CopyStringPrefix` and `CopyHTMLStringPrefix` scanners;
- strict UTF-8, U+2028/U+2029, and contiguous `\uXXXX` kernels;
- 64-bit string classification masks; and
- `Current`, which reports the runtime-selected backends and CPU features.

All APIs have portable fallbacks. On the M4 Max benchmark host, eight 500 ms
samples produced these medians, all with zero allocations:

| Kernel | Selected | Control | Speedup |
|---|---:|---:|---:|
| Parse 16 digits | **1.031 ns** | scalar 6.664 ns | **6.46x** |
| Store 8 digits | **1.497 ns** | `strconv` 5.783 ns | **3.86x** |
| Store 16 digits | **2.225 ns** | scalar 3.142 ns | **1.41x** |
| Store 16 digits | **2.225 ns** | `strconv` 8.187 ns | **3.68x** |

## SIMD Dispatch

`GOEXPERIMENT=simd` enables Go-native kernels. Runtime capabilities are read
once, and implementation choices are fixed during package initialization.

| Runtime | String scanning | Decimal parse | Decimal format |
|---|---|---|---|
| arm64 | NEON on sustained runs; scalar/SWAR tails | NEON 16-digit reduction | NEON 16-digit formatting |
| amd64 with AVX-512 | 64-byte AVX-512 | AVX 16-digit reduction | Scalar SWAR |
| amd64 with AVX2 | 32-byte AVX2 | AVX 16-digit reduction | Scalar SWAR |
| Other build or CPU | Scalar Go | Scalar Go | Scalar SWAR |

Other vector paths include strict UTF-8 validation using ARM64 `TBL` lookup,
contiguous `\uXXXX` validation, U+2028/U+2029 detection, syntax scans used by
string encoding, and shape-dispatched typed decimals. Decimal parsing uses
register-only SWAR for 8-byte blocks and Go-native SIMD for full 16-digit runs.
Every vector load is length-guarded. `simd.Current()` reports independent
string, decimal-parse, and decimal-format backends and widths, the string
threshold, and detected CPU features.

## Safety Model

simdjson uses `unsafe` only in measured internal paths and keeps those uses
narrow:

- vector loads and stores run only when a complete block remains;
- public fused-copy kernels reject short or overlapping destinations;
- integer pair stores follow a capacity extension and a table index below 100;
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
