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
| Canada geometry | 1.203 ms | **160.3 us** | 148.8 us | go-json | 710.5 us |
| CITM catalog | 2.499 ms | **747.5 us** | 643.1 us | go-json | 1.015 ms |
| Go source | 6.043 ms | **1.040 ms** | 987.6 us | go-json | 2.132 ms |
| Escaped strings | 186.3 us | **26.7 us** | 24.5 us | go-json | 61.8 us |
| Unicode strings | 39.2 us | **5.4 us** | 4.6 us | go-json | 10.8 us |
| Synthea FHIR | 3.781 ms | **1.410 ms** | 1.285 ms | go-json | 1.654 ms |
| Twitter status | 1.319 ms | **365.8 us** | 331.5 us | go-json | 588.9 us |

Source-backed refers only to unescaped string ownership. Slices, maps,
pointers, escaped text, and custom method receivers may still allocate.

| Corpus | Source-backed bytes/op | Source-backed allocs/op |
|---|---:|---:|
| Canada geometry | 267 B | 0 |
| CITM catalog | 1.677 MiB | 1,224 |
| Go source | 9.2 KiB | 42 |
| Escaped strings | 48.0 KiB | 1 |
| Unicode strings | 18.0 KiB | 1 |
| Synthea FHIR | 1.945 MiB | 347 |
| Twitter status | 631.1 KiB | 139 |

### Dynamic decode

These rows fully materialize an owned `any` tree.

| Corpus | simdjson | Rival | Rival time | Lead |
|---|---:|---|---:|---:|
| Canada geometry | **681.4 us** | go-json | 1.418 ms | **2.08x** |
| CITM catalog | **1.660 ms** | jsoniter | 3.184 ms | **1.92x** |
| Go source | **3.154 ms** | jsoniter | 7.243 ms | **2.30x** |
| Escaped strings | **25.9 us** | go-json | 64.1 us | **2.47x** |
| Unicode strings | **8.5 us** | go-json | 15.9 us | **1.87x** |
| Synthea FHIR | **2.465 ms** | go-json | 4.256 ms | **1.73x** |
| Twitter status | **849.2 us** | go-json | 1.366 ms | **1.61x** |

### Strict validation

Validation checks both JSON syntax and UTF-8 and allocates nothing for valid
input.

| Corpus | simdjson | Rival | Rival time | Lead |
|---|---:|---|---:|---:|
| Canada geometry | **126.5 us** | fastjson | 203.7 us | **1.61x** |
| CITM catalog | **610.6 us** | fastjson | 807.3 us | **1.32x** |
| Go source | **936.8 us** | Segment | 1.172 ms | **1.25x** |
| Escaped strings | **4.3 us** | Segment | 59.8 us | **13.84x** |
| Unicode strings | **3.2 us** | fastjson | 6.9 us | **2.15x** |
| Synthea FHIR | **639.6 us** | fastjson | 1.267 ms | **1.98x** |
| Twitter status | **226.8 us** | fastjson | 385.4 us | **1.70x** |

### Encode

Owned rows compare `Marshal` with `Marshal`. Compiled reuse appends into a
caller-owned buffer and removes the output allocation.

| Corpus | stdlib | simdjson owned | Compiled reuse | Rival | Rival time |
|---|---:|---:|---:|---|---:|
| Canada geometry | 580.4 us | 310.8 us | **301.6 us** | Segment | 465.6 us |
| CITM catalog | 773.1 us | 221.6 us | **205.4 us** | Segment | 309.2 us |
| Go source | 2.626 ms | 735.1 us | **674.3 us** | Segment | 1.148 ms |
| Escaped strings | 17.3 us | 4.2 us | **3.6 us** | jsoniter | 20.1 us |
| Unicode strings | 17.0 us | 4.1 us | **3.6 us** | jsoniter | 19.7 us |
| Synthea FHIR | 5.064 ms | 1.111 ms | **1.015 ms** | Segment | 1.710 ms |
| Twitter status | 561.7 us | 197.6 us | **172.3 us** | go-json | 272.2 us |

### SIMD versus pure Go

Both binaries use the same pinned Go tip compiler and corpus. SIMD dispatch is
selected once during initialization; short runs may remain scalar or SWAR.

| simdjson path | SIMD wins | Geomean SIMD uplift |
|---|---:|---:|
| Validation | 7/7 | **1.436x** |
| Dynamic owned | 4/7 | **1.092x** |
| Dynamic source-backed | 4/7 | **1.123x** |
| Typed owned | 5/7 | **1.150x** |
| Typed source-backed | 6/7 | **1.176x** |
| Encode owned | 7/7 | **1.720x** |
| Encode compiled reuse | 7/7 | **1.791x** |

### Native Sonic context

Sonic v1.15.2 falls back to `encoding/json` on Go 1.27. Its native results use
an isolated Go 1.26.4 module and are excluded from the headline winner and
speedup calculations. Sonic's `Valid` is syntax-only because it accepts
invalid UTF-8.

| Corpus | Typed owned | Dynamic owned | Encode owned | Syntax-only `Valid` |
|---|---:|---:|---:|---:|
| Canada geometry | 421.6 us | 709.3 us | 742.1 us | 185.9 us |
| CITM catalog | 1.362 ms | 2.454 ms | 814.3 us | 764.9 us |
| Go source | 3.100 ms | 5.253 ms | 3.538 ms | 1.502 ms |
| Escaped strings | 29.3 us | 31.1 us | 18.4 us | 3.2 us |
| Unicode strings | 10.6 us | 11.9 us | 18.6 us | 1.7 us |
| Synthea FHIR | 2.379 ms | 4.134 ms | 6.702 ms | 834.0 us |
| Twitter status | 672.6 us | 944.5 us | 542.7 us | 227.9 us |

## Cross-language context

[crosslang/](crosslang/) benchmarks C++ simdjson and Rust
serde_json/simd-json over the same seven payloads on the same machine, with
contract notes for each row. Headlines from the 2026-07-14 snapshot: C++
simdjson's tape parse reaches 4.4-5.0 GB/s on object-dense payloads (ahead
of our validation scan) but falls behind it on number- and escape-dense
input; our typed decode beats serde_json's dynamic tree and simd-json
borrowed on all seven payloads; and our compiled encoder leads every
serializer measured except on the date-saturated FHIR payload, where C++
replays pre-parsed strings while we format native `time.Time` values.

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
