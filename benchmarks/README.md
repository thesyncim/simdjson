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
| Canada geometry | 1.222 ms | **264.9 us** | 230.5 us | go-json | 769.5 us |
| CITM catalog | 2.548 ms | **1.054 ms** | 856.9 us | go-json | 1.288 ms |
| Go source | 6.300 ms | **1.305 ms** | 978.3 us | Segment | 2.200 ms |
| Escaped strings | 195.7 us | **39.4 us** | 31.3 us | go-json | 68.3 us |
| Unicode strings | 40.7 us | **9.4 us** | 6.9 us | go-json | 13.3 us |
| Synthea FHIR | 3.677 ms | **1.855 ms** | 1.600 ms | go-json | 2.051 ms |
| Twitter status | 1.365 ms | **484.7 us** | 409.4 us | go-json | 697.6 us |

Source-backed refers only to unescaped string ownership. Slices, maps,
pointers, escaped text, and custom method receivers may still allocate.

| Corpus | Source-backed bytes/op | Source-backed allocs/op |
|---|---:|---:|
| Canada geometry | 270 B | 0 |
| CITM catalog | 1.677 MiB | 1,220 |
| Go source | 8.7 KiB | 40 |
| Escaped strings | 48.0 KiB | 1 |
| Unicode strings | 18.0 KiB | 1 |
| Synthea FHIR | 1.944 MiB | 344 |
| Twitter status | 631.0 KiB | 139 |

### Dynamic decode

These rows fully materialize an owned `any` tree.

| Corpus | simdjson | Rival | Rival time | Lead |
|---|---:|---|---:|---:|
| Canada geometry | **894.2 us** | go-json | 1.842 ms | **2.06x** |
| CITM catalog | **2.730 ms** | jsoniter | 4.422 ms | **1.62x** |
| Go source | **4.910 ms** | go-json | 9.648 ms | **1.97x** |
| Escaped strings | **34.3 us** | go-json | 77.4 us | **2.25x** |
| Unicode strings | **15.3 us** | go-json | 21.0 us | **1.37x** |
| Synthea FHIR | **4.213 ms** | jsoniter | 6.751 ms | **1.60x** |
| Twitter status | **1.307 ms** | jsoniter | 1.993 ms | **1.52x** |

### Strict validation

Validation checks both JSON syntax and UTF-8 and allocates nothing for valid
input.

| Corpus | simdjson | Rival | Rival time | Lead |
|---|---:|---|---:|---:|
| Canada geometry | **125.7 us** | fastjson | 215.4 us | **1.71x** |
| CITM catalog | **640.8 us** | fastjson | 835.3 us | **1.30x** |
| Go source | **937.5 us** | Segment | 1.190 ms | **1.27x** |
| Escaped strings | **4.3 us** | Segment | 57.1 us | **13.29x** |
| Unicode strings | **3.2 us** | fastjson | 6.9 us | **2.14x** |
| Synthea FHIR | **758.2 us** | fastjson | 1.245 ms | **1.64x** |
| Twitter status | **224.9 us** | fastjson | 389.0 us | **1.73x** |

### Encode

Owned rows compare `Marshal` with `Marshal`. Compiled reuse appends into a
caller-owned buffer and removes the output allocation.

| Corpus | stdlib | simdjson owned | Compiled reuse | Rival | Rival time |
|---|---:|---:|---:|---|---:|
| Canada geometry | 690.1 us | 371.7 us | **301.6 us** | Segment | 520.1 us |
| CITM catalog | 967.3 us | 394.0 us | **223.7 us** | Segment | 380.0 us |
| Go source | 3.225 ms | 1.260 ms | **677.7 us** | Segment | 1.360 ms |
| Escaped strings | 20.7 us | 6.6 us | **3.6 us** | jsoniter | 22.4 us |
| Unicode strings | 20.8 us | 6.6 us | **3.6 us** | jsoniter | 21.9 us |
| Synthea FHIR | 5.974 ms | 2.163 ms | **1.089 ms** | Segment | 2.060 ms |
| Twitter status | 720.3 us | 320.3 us | **171.2 us** | go-json | 326.6 us |

### SIMD versus pure Go

Both binaries use the same pinned Go tip compiler and corpus. SIMD dispatch is
selected once during initialization; short runs may remain scalar or SWAR.

| simdjson path | SIMD wins | Geomean SIMD uplift |
|---|---:|---:|
| Validation | 6/7 | **1.399x** |
| Dynamic owned | 5/7 | **1.069x** |
| Dynamic source-backed | 4/7 | **1.078x** |
| Typed owned | 4/7 | **1.099x** |
| Typed source-backed | 4/7 | **1.127x** |
| Encode owned | 6/7 | **1.536x** |
| Encode compiled reuse | 7/7 | **1.822x** |

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
| Append quoted time | **15.04 ns** | `time.AppendText` 31.05 ns | **2.06x** |
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
