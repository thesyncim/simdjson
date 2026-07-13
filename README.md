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
go-json v0.10.6, Segment encoding v0.5.4, jsoniter v1.1.12, and fastjson v1.6.10
for validation. Speedups are geometric means across all seven payloads.

| Operation | Contract | Wins vs stdlib | Wins vs rival | vs stdlib | vs rival |
|---|---|---:|---:|---:|---:|
| Validate | Syntax only | **7/7** | **7/7** | **1.95x** | **1.79x** |
| Typed decode | Owned strings, reused destination | **7/7** | **7/7** | **4.15x** | **1.60x** |
| Dynamic decode | Owned `any` tree | **7/7** | **7/7** | **4.15x** | **1.89x** |
| Encode | Owned output | **7/7** | **5/7** | **2.27x** | **1.31x** |
| Encode | Reused output buffer | **7/7** | **7/7** | **2.40x** | **1.39x** |

The scorecard does not mix ownership contracts. Source-backed decode and reused
output are reported separately below. Native Sonic uses a different compiler
and is never included in the Go tip rival or winner columns.

### Typed Decode

The owned row is the fair conventional comparison. `ZeroCopy: true` aliases
unescaped strings into immutable input and therefore has a different lifetime
contract.

| Corpus | `encoding/json` | simdjson owned | simdjson source-backed | Fastest tip rival | Native Sonic owned |
|---|---:|---:|---:|---:|---:|
| Canada geometry | 1.197 ms | **359.7 us** | 359.5 us | go-json 698.4 us | 440.5 us |
| CITM catalog | 2.513 ms | **697.3 us** | 632.7 us | go-json 988.9 us | 1.423 ms |
| Go source | 5.925 ms | **1.144 ms** | 1.097 ms | Segment 2.070 ms | 3.345 ms |
| Escaped strings | 173.8 us | **26.04 us** | 26.10 us | go-json 56.65 us | 30.69 us |
| Unicode strings | 40.29 us | **7.586 us** | 6.881 us | go-json 10.37 us | 11.28 us |
| Synthea FHIR | 3.617 ms | **1.312 ms** | 1.214 ms | go-json 1.556 ms | 2.653 ms |
| Twitter status | 1.297 ms | **368.6 us** | 332.2 us | go-json 562.4 us | 699.8 us |

Source-backed allocation counts are not zero for container-heavy models:

| Corpus | Bytes/op | Allocs/op |
|---|---:|---:|
| Canada geometry | 629 B | 2 |
| CITM catalog | 1.68 MiB | 1,224 |
| Go source | 14.6 KiB | 68 |
| Escaped strings | 48.0 KiB | 1 |
| Unicode strings | 18.0 KiB | 1 |
| Synthea FHIR | 1.95 MiB | 351 |
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
| Canada geometry | 547.7 us | 421.0 us | **419.9 us** | Segment 444.4 us | 782.8 us |
| CITM catalog | 761.6 us | 309.2 us | **293.4 us** | Segment 297.0 us | 836.2 us |
| Go source | 2.604 ms | 1.134 ms | **1.093 ms** | Segment 1.123 ms | 3.711 ms |
| Escaped strings | 16.84 us | 7.034 us | **6.493 us** | jsoniter 16.78 us | 19.73 us |
| Unicode strings | 16.94 us | 7.098 us | **6.532 us** | jsoniter 16.87 us | 19.76 us |
| Synthea FHIR | 5.057 ms | 1.643 ms | **1.561 ms** | Segment 1.666 ms | 6.834 ms |
| Twitter status | 562.7 us | 236.2 us | **212.6 us** | go-json 270.3 us | 552.9 us |

Compiled reuse wins every exact-model row. Its hot struct plan classifies common
adjacent field operations once, stores the fused opcode without enlarging the
40-byte field record, and emits two fields per dispatch. Value-receiver custom
marshalers use pooled, ordinary Go reflection values that are cleared before
reuse; no interface-layout or runtime map-layout assumptions are involved.

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
| Encode owned | **7/7** | **1.37x** |
| Encode compiled reuse | **7/7** | **1.41x** |

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
validation, U+2028/U+2029 detection, syntax scans used by string encoding, and
shape-dispatched typed decimals that reduce 16 significant digits in one SIMD
pass. Every vector load is length-guarded. `CurrentSIMD()` reports the selected
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
