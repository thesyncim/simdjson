# simdjson comparison benchmarks

This directory is a separate Go module. Comparison-only dependencies never
enter simdjson's root module graph.

## Fairness rules

- `BenchmarkStdlibCorpus` uses the seven exact payloads and concrete models
  pinned from Go tip. Every result is checked against `encoding/json` before
  timing; encode accepts byte differences only when decoded values are equal.
- Go-tip typed decoders consume the same byte slices and decode equivalent
  `TypedSmall` or `TypedDocument` schemas.
- easyjson types live in `easyjsonmodel`. Attaching easyjson's generated
  `UnmarshalJSON` method to the shared types would cause stdlib, Segment,
  go-json, and jsoniter to call easyjson instead of their own decoder.
- `simdjson-Compiled-zero-copy` is compared with Sonic `ConfigFastest`, whose
  `CopyString` setting is false. Owned simdjson is compared with owned-string rows.
- Fresh and reused destinations have different benchmark names.
- Sonic native results come from `legacy`, because Sonic v1.15.2 falls back to
  `encoding/json` on Go 1.27 tip.
- `encoding/json/v2` is imported directly and built only with the
  `goexperiment.jsonv2` tag.

The synthetic fixtures are 31 bytes, 4,240 bytes (32 records), and 136,586
bytes (1,024 records). Exact-corpus results use six 300 ms single-CPU samples; synthetic
published results use five one-second samples.

## Exact standard-library corpus

```sh
GOEXPERIMENT=simd "$TIP_GO" test -run='^$' \
  -bench='^BenchmarkStdlibCorpus$' \
  -benchmem -benchtime=300ms -count=6 -cpu=1 .
```

The benchmark exposes `valid`, `dynamic-owned`, `typed-reused`, and `encode`
groups. Owned and source-backed rows are distinct. Sonic is omitted when its
API reports stdlib fallback; run `BenchmarkStdlibCorpusNativeSonic` in
`legacy/` with Go 1.26.4 for native numbers.

## Published corpus snapshot

These are medians of six 300 ms, single-CPU samples on an Apple M4 Max
(`darwin/arm64`) using Go commit
`03845e30f7b73d1703bd8c21017297f6eecb76d6`. Compilation happens before the
timer. Every result is checked against `encoding/json` before measurement.

"Rival" is the fastest compatible same-toolchain result from go-json v0.10.6,
Segment encoding v0.5.4, jsoniter v1.1.12, or fastjson v1.6.10. The headline
speedups in the root README are geometric means over all seven payloads.

### Typed decode

The owned row is the conventional comparison. Source-backed decoding aliases
unescaped strings into immutable input and has a different lifetime contract.

| Corpus | `encoding/json` | simdjson owned | Source-backed | Rival | Rival time |
|---|---:|---:|---:|---|---:|
| Canada geometry | 1.239 ms | **258.6 us** | 221.4 us | Segment | 770.7 us |
| CITM catalog | 2.520 ms | **1.061 ms** | 872.8 us | go-json | 1.325 ms |
| Go source | 6.279 ms | **1.372 ms** | 1.020 ms | Segment | 2.290 ms |
| Escaped strings | 205.6 us | **40.1 us** | 31.8 us | go-json | 68.7 us |
| Unicode strings | 43.7 us | **10.3 us** | 7.2 us | go-json | 14.0 us |
| Synthea FHIR | 3.793 ms | **1.931 ms** | 1.629 ms | go-json | 2.113 ms |
| Twitter status | 1.410 ms | **509.8 us** | 428.1 us | go-json | 749.6 us |

Source-backed refers only to unescaped string ownership. Slices, maps,
pointers, escaped text, and custom method receivers may still allocate.

| Corpus | Source-backed bytes/op | Source-backed allocs/op |
|---|---:|---:|
| Canada geometry | 263 B | 0 |
| CITM catalog | 1.677 MiB | 1,221 |
| Go source | 9.1 KiB | 42 |
| Escaped strings | 48.0 KiB | 1 |
| Unicode strings | 18.0 KiB | 1 |
| Synthea FHIR | 1.944 MiB | 345 |
| Twitter status | 631.0 KiB | 139 |

### Dynamic decode

These rows fully materialize an owned `any` tree.

| Corpus | simdjson | Rival | Rival time | Lead |
|---|---:|---|---:|---:|
| Canada geometry | **921.4 us** | go-json | 1.842 ms | **2.00x** |
| CITM catalog | **2.990 ms** | jsoniter | 4.571 ms | **1.53x** |
| Go source | **4.967 ms** | go-json | 9.877 ms | **1.99x** |
| Escaped strings | **34.5 us** | go-json | 78.3 us | **2.27x** |
| Unicode strings | **15.6 us** | go-json | 22.3 us | **1.43x** |
| Synthea FHIR | **4.400 ms** | jsoniter | 6.927 ms | **1.57x** |
| Twitter status | **1.382 ms** | go-json | 2.109 ms | **1.53x** |

### Strict validation

Validation checks both JSON syntax and UTF-8 and allocates nothing for valid
input.

| Corpus | simdjson | Rival | Rival time | Lead |
|---|---:|---|---:|---:|
| Canada geometry | **131.3 us** | fastjson | 217.8 us | **1.66x** |
| CITM catalog | **642.0 us** | fastjson | 859.8 us | **1.34x** |
| Go source | **940.8 us** | Segment | 1.201 ms | **1.28x** |
| Escaped strings | **4.4 us** | Segment | 59.3 us | **13.50x** |
| Unicode strings | **3.3 us** | fastjson | 7.1 us | **2.17x** |
| Synthea FHIR | **777.7 us** | fastjson | 1.305 ms | **1.68x** |
| Twitter status | **226.7 us** | fastjson | 394.4 us | **1.74x** |

### Encode

Owned rows compare `Marshal` with `Marshal`. Compiled reuse appends into a
caller-owned buffer and removes the output allocation.

| Corpus | stdlib | simdjson owned | Compiled reuse | Rival | Rival time |
|---|---:|---:|---:|---|---:|
| Canada geometry | 677.6 us | 381.0 us | **307.5 us** | Segment | 513.5 us |
| CITM catalog | 1.012 ms | 397.7 us | **240.5 us** | go-json | 396.8 us |
| Go source | 3.283 ms | 1.305 ms | **696.6 us** | Segment | 1.389 ms |
| Escaped strings | 21.3 us | 6.9 us | **3.7 us** | jsoniter | 22.8 us |
| Unicode strings | 21.5 us | 6.6 us | **3.7 us** | jsoniter | 23.1 us |
| Synthea FHIR | 6.066 ms | 2.257 ms | **1.237 ms** | Segment | 2.142 ms |
| Twitter status | 742.6 us | 336.3 us | **184.4 us** | Segment | 354.7 us |

### SIMD versus pure Go

Both binaries use the same pinned Go tip compiler and corpus. SIMD dispatch is
selected once during initialization; short runs may remain scalar or SWAR.

| simdjson path | SIMD wins | Geomean SIMD uplift |
|---|---:|---:|
| Validation | 6/7 | **1.385x** |
| Dynamic owned | 5/7 | **1.067x** |
| Dynamic source-backed | 5/7 | **1.073x** |
| Typed owned | 5/7 | **1.093x** |
| Typed source-backed | 4/7 | **1.113x** |
| Encode owned | 7/7 | **1.550x** |
| Encode compiled reuse | 6/7 | **1.800x** |

### Native Sonic context

Sonic v1.15.2 falls back to `encoding/json` on Go 1.27. Its native results use
an isolated Go 1.26.4 module and are excluded from the headline winner and
speedup calculations. Sonic's `Valid` is syntax-only because it accepts
invalid UTF-8.

| Corpus | Typed owned | Dynamic owned | Encode owned | Syntax-only `Valid` |
|---|---:|---:|---:|---:|
| Canada geometry | 438.9 us | 809.6 us | 794.8 us | 188.8 us |
| CITM catalog | 1.685 ms | 3.254 ms | 975.8 us | 784.7 us |
| Go source | 4.318 ms | 6.918 ms | 4.627 ms | 1.551 ms |
| Escaped strings | 33.2 us | 33.9 us | 20.9 us | 3.4 us |
| Unicode strings | 12.2 us | 14.3 us | 20.9 us | 1.7 us |
| Synthea FHIR | 3.456 ms | 5.635 ms | 8.988 ms | 867.0 us |
| Twitter status | 768.7 us | 1.230 ms | 611.9 us | 235.8 us |

## SIMD kernel snapshot

These Apple M4 Max medians use eight 500 ms samples and guarded public APIs.
All rows report zero allocations.

| Kernel | Selected | Control | Speedup |
|---|---:|---:|---:|
| Parse 16 digits | **1.031 ns** | scalar 6.664 ns | **6.46x** |
| Store 8 digits | **1.497 ns** | `strconv` 5.783 ns | **3.86x** |
| Store 16 digits | **2.228 ns** | scalar 3.128 ns | **1.40x** |
| Store 16 digits | **2.228 ns** | `strconv` 8.112 ns | **3.64x** |
| Append float64 | **15.49 ns** | `strconv` 24.09 ns | **1.56x** |
| Append quoted time | **19.53 ns** | `time.AppendText` 31.64 ns | **1.62x** |
| Store date/time digits | **3.138 ns** | scalar 4.686 ns | **1.49x** |

## Compiled decoder

These results exclude `CompileDecoder[T]` construction. Plans are initialized
before timing with case-sensitive field matching.

| Workload | SIMD, source-backed | SIMD, owned |
|---|---:|---:|
| Small, fresh | **26.0 ns / 0** | 40.2 ns / 1 |
| Medium, fresh | **2.15 us / 2** | 2.47 us / 3 |
| Medium, reused | **1.96 us / 0** | 2.26 us / 1 |
| Large, fresh | **65.3 us / 2** | 73.6 us / 3 |
| Large, reused | **61.8 us / 0** | 68.8 us / 1 |

The scalar-versus-SIMD control lives in the root module's `BenchmarkDecode*`
benchmarks, which build in seconds and compare the same binary pair.

Reproduce the compiled rows with:

```sh
GOEXPERIMENT=simd "$TIP_GO" test \
  -run='^$' \
  -bench='^BenchmarkParseTyped/(small|medium|large)/simdjson-Compiled-(zero-copy|owned)(-reused)?$' \
  -benchmem -benchtime=1s -count=5 .
```

## Go tip runs

The published SIMD command was:

```sh
GOEXPERIMENT=simd "$TIP_GO" test \
  -run='^$' -bench='^BenchmarkParseTyped$' \
  -benchmem -benchtime=1s -count=5 .
```

Pure-Go simdjson control:

```sh
"$TIP_GO" test \
  -run='^$' \
  -bench='BenchmarkParseTyped/(small|medium|large)/simdjson-Compiled-(zero-copy|owned)(-reused)?$' \
  -benchmem -benchtime=1s -count=5 .
```

Direct `encoding/json/v2`:

```sh
GOEXPERIMENT=simd,jsonv2 "$TIP_GO" test \
  -run='^$' -bench='^BenchmarkParseTypedJSONV2' \
  -benchmem -benchtime=1s -count=5 .
```

The measured tip compiler was:

```text
go version go1.27-devel_03845e30 Fri Jul 10 12:31:49 2026 -0700 darwin/arm64
commit 03845e30f7b73d1703bd8c21017297f6eecb76d6
```

From the repository root, build that exact toolchain and set `TIP_GO` with:

```sh
./scripts/bootstrap-gotip.sh "$HOME/sdk/simdjson-gotip"
export TIP_GO="$HOME/sdk/simdjson-gotip/bin/go"
```

## Native stable-toolchain runs

Sonic v1.15.2 supports Go 1.26 but not this Go 1.27 development revision. Its
native arm64 benchmark is isolated under `legacy` and does not import simdjson:

```sh
cd legacy
GOTOOLCHAIN=go1.26.4 go test \
  -run='^$' \
  -bench='BenchmarkSonicNativeParseTyped(Fastest|Std)$|BenchmarkSonicNativeParseTypedReused(Fastest|Std)$' \
  -benchmem -benchtime=1s -count=5 .
```

`run-comparison.sh` runs the broad pure-Go, SIMD, jsonv2, and stable native
suites. Override `TIP_GO`, `LEGACY_GO`, `BENCH`, `BENCHTIME`, or `COUNT` to use
another pinned environment.

## Benchmark groups

- `BenchmarkStdlibCorpus`: exact Go-tip corpus validation, dynamic decode,
  typed reused decode, and encode across conventional libraries.
- `BenchmarkParseTyped`: typed fresh and reused decoding across conventional,
  compiled zero-copy, compiled owned, and competitor modes.
- `BenchmarkParseTypedJSONV2`: direct `encoding/json/v2` typed decoding.
- `Valid`: strict JSON syntax and UTF-8 validation.
- `ParseAny`: full conventional `any` materialization.
- `ParseAnyNumbers16`: materialization of 1,024 exact 16-digit integers.
- `ParseNative`: each library's native structural representation.
- `Index`: simdjson validation plus caller-owned structural index construction.

The module pins exact competitor versions in `go.mod` and `go.sum`.
simdjson-go v0.4.5 remains skipped on arm64 because its implementation requires
amd64 AVX2 and CLMUL.
