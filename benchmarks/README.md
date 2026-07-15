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

The benchmark exposes `valid`, `dynamic-owned`, `dom`, `typed-reused`, and
`encode` groups. Owned and source-backed rows are distinct. Sonic is omitted
when its API reports stdlib fallback; run `BenchmarkStdlibCorpusNativeSonic` in
`legacy/` with Go 1.26.4 for native numbers.

## Published corpus snapshot

These are medians of six 300 ms, single-CPU samples on an Apple M4 Max
(`darwin/arm64`) using Go commit
`03845e30f7b73d1703bd8c21017297f6eecb76d6`. Compilation happens before the
timer. Every result is checked against `encoding/json` before measurement.

> **Strict validation and Parse/DOM refreshed 2026-07-15; other tables retain
> the 2026-07-14 run.** The validation and Parse/DOM rows were regenerated on
> the current build under the six-sample protocol. The typed, dynamic, encode,
> SIMD-versus-pure, native-Sonic, and `encoding/json/v2` tables keep their
> 2026-07-14 numbers: the 2026-07-15 regeneration ran on a machine under heavy
> background load, and while the zero-allocation paths (validation and the
> compiled reused-buffer encoder) reproduced or improved on their published
> times, every path that allocates a fresh owned tree or output buffer inflated
> by 20-90% for simdjson *and* for the competitors alike — the signature of
> allocator and memory-bandwidth contention, not a code change. Same-process
> leads and ratios held to within a few percent through that contention, so the
> published absolutes remain the honest idle-machine figures for this same
> (unregressed) build. They will be re-pinned when an idle window is available.

"Rival" is the fastest compatible same-toolchain result from go-json v0.10.6,
Segment encoding v0.5.4, jsoniter v1.1.12, or fastjson v1.6.10. The headline
speedups in the root README are geometric means over all seven payloads.

### Typed decode

The owned row is the conventional comparison. Source-backed decoding aliases
unescaped strings into immutable input and has a different lifetime contract.

| Corpus | `encoding/json` | simdjson owned | Source-backed | Rival | Rival time |
|---|---:|---:|---:|---|---:|
| Canada geometry | 1.246 ms | **171.8 us** | 160.6 us | go-json | 746.1 us |
| CITM catalog | 2.556 ms | **623.7 us** | 558.5 us | go-json | 1.060 ms |
| Go source | 6.234 ms | **1.078 ms** | 1.002 ms | Segment | 2.172 ms |
| Escaped strings | 189.3 us | **28.2 us** | 25.5 us | go-json | 62.3 us |
| Unicode strings | 39.1 us | **5.6 us** | 4.7 us | go-json | 12.0 us |
| Synthea FHIR | 3.744 ms | **1.307 ms** | 1.240 ms | go-json | 1.708 ms |
| Twitter status | 1.359 ms | **350.1 us** | 323.8 us | go-json | 608.1 us |

Source-backed refers only to unescaped string ownership. Slices, maps,
pointers, escaped text, and custom method receivers may still allocate.

| Corpus | Source-backed bytes/op | Source-backed allocs/op |
|---|---:|---:|
| Canada geometry | 189 B | 0 |
| CITM catalog | 29.9 KiB | 1,209 |
| Go source | 8.9 KiB | 41 |
| Escaped strings | 30.0 KiB | 5 |
| Unicode strings | 6.0 KiB | 3 |
| Synthea FHIR | 25.1 KiB | 334 |
| Twitter status | 68.7 KiB | 144 |

### Dynamic decode

These rows fully materialize an owned `any` tree.

| Corpus | simdjson | Rival | Rival time | Lead |
|---|---:|---|---:|---:|
| Canada geometry | **686.8 us** | go-json | 1.456 ms | **2.12x** |
| CITM catalog | **1.689 ms** | go-json | 3.291 ms | **1.95x** |
| Go source | **3.262 ms** | jsoniter | 7.344 ms | **2.25x** |
| Escaped strings | **26.1 us** | go-json | 68.5 us | **2.62x** |
| Unicode strings | **8.7 us** | go-json | 16.3 us | **1.87x** |
| Synthea FHIR | **2.499 ms** | go-json | 4.436 ms | **1.78x** |
| Twitter status | **882.7 us** | go-json | 1.419 ms | **1.61x** |

### Parse and full DOM walk

Both columns are "one parse plus a complete traversal" of the whole document.
The shapes differ on purpose: `simdjson.Parse` builds only the structural
index and decodes each scalar on demand as the `Value` walk reaches it, whereas
the `encoding/json` column materializes a complete owned `any` tree with
`Unmarshal` and then walks the finished nodes. The comparison measures the
end-to-end cost of parsing and then reading every value, not two identical data
structures. Refreshed 2026-07-15.

| Corpus | `encoding/json` any tree + walk | simdjson `Parse` + walk | Lead |
|---|---:|---:|---:|
| Canada geometry | 2.803 ms | **1.014 ms** | **2.76x** |
| CITM catalog | 8.344 ms | **2.262 ms** | **3.69x** |
| Go source | 18.991 ms | **4.829 ms** | **3.93x** |
| Escaped strings | 218.9 us | **66.3 us** | **3.30x** |
| Unicode strings | 56.1 us | **15.7 us** | **3.59x** |
| Synthea FHIR | 12.420 ms | **3.354 ms** | **3.70x** |
| Twitter status | 3.838 ms | **960.2 us** | **4.00x** |

### Strict validation

Validation checks both JSON syntax and UTF-8 and allocates nothing for valid
input.

| Corpus | simdjson | Rival | Rival time | Lead |
|---|---:|---|---:|---:|
| Canada geometry | **124.9 us** | fastjson | 190.7 us | **1.53x** |
| CITM catalog | **518.6 us** | fastjson | 811.1 us | **1.56x** |
| Go source | **931.0 us** | Segment | 1.181 ms | **1.27x** |
| Escaped strings | **4.4 us** | Segment | 56.4 us | **12.89x** |
| Unicode strings | **3.2 us** | fastjson | 7.1 us | **2.19x** |
| Synthea FHIR | **600.8 us** | fastjson | 1.289 ms | **2.14x** |
| Twitter status | **226.6 us** | fastjson | 391.0 us | **1.73x** |

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
| Dynamic owned | 7/7 | **1.121x** |
| Dynamic source-backed | 7/7 | **1.124x** |
| Typed owned | 6/7 | **1.159x** |
| Typed source-backed | 6/7 | **1.191x** |
| Encode owned | 7/7 | **1.720x** |
| Encode compiled reuse | 7/7 | **1.791x** |

### Native Sonic context

Sonic v1.15.2 falls back to `encoding/json` on Go 1.27. Its native results use
an isolated Go 1.26.4 module and are excluded from the headline winner and
speedup calculations. Sonic's `Valid` is syntax-only because it accepts
invalid UTF-8.

| Corpus | Typed owned | Dynamic owned | Encode owned | Syntax-only `Valid` |
|---|---:|---:|---:|---:|
| Canada geometry | 443.3 us | 742.7 us | 742.1 us | 185.9 us |
| CITM catalog | 1.429 ms | 2.589 ms | 814.3 us | 764.9 us |
| Go source | 3.431 ms | 5.633 ms | 3.538 ms | 1.502 ms |
| Escaped strings | 31.8 us | 33.2 us | 18.4 us | 3.2 us |
| Unicode strings | 11.0 us | 12.8 us | 18.6 us | 1.7 us |
| Synthea FHIR | 2.627 ms | 4.320 ms | 6.702 ms | 834.0 us |
| Twitter status | 715.6 us | 1.005 ms | 542.7 us | 227.9 us |

### encoding/json/v2

`encoding/json/v2` (Go tip, built with `GOEXPERIMENT=jsonv2`) is measured by
`BenchmarkStdlibCorpusJSONV2` over the same corpus, models, and contracts,
in the same session as the decode tables above. Geometric means over the
seven payloads: our typed owned decode leads v2 by **3.92x**, dynamic owned
by **2.50x**, and owned `Marshal` by **3.34x**.

| Corpus | v2 typed | v2 dynamic `any` | v2 `Marshal` |
|---|---:|---:|---:|
| Canada geometry | 1.120 ms | 1.407 ms | 613.4 us |
| CITM catalog | 2.070 ms | 3.576 ms | 789.9 us |
| Go source | 5.359 ms | 8.220 ms | 2.689 ms |
| Escaped strings | 138.5 us | 143.2 us | 18.6 us |
| Unicode strings | 21.7 us | 26.8 us | 18.4 us |
| Synthea FHIR | 3.176 ms | 4.530 ms | 5.171 ms |
| Twitter status | 997.0 us | 1.593 ms | 581.5 us |

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
  `Parse` plus full DOM walk, typed reused decode, and encode across
  conventional libraries.
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
