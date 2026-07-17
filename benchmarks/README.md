# simdjson benchmark report

The benchmark module is separate from the library. Comparison-only
dependencies never enter simdjson's root module graph.

## Current snapshot

Regenerated 2026-07-16 on an Apple M4 Max (`darwin/arm64`) with one logical
CPU, six 300 ms samples, and this pinned compiler:

```text
go version go1.27-devel_03845e30 Fri Jul 10 12:31:49 2026 -0700 darwin/arm64
commit 03845e30f7b73d1703bd8c21017297f6eecb76d6
code e0d1941
```

The corpus is the exact seven-file, 6.33 MiB set copied from Go tip's
`encoding/json` tests. Each table entry below is a geometric mean of the
seven per-file medians. Lower time is better. Ratios are competitor time
divided by simdjson time.

| Owned operation | simdjson SIMD | `encoding/json` | Fastest third-party Go | simdjson pure Go | Native Sonic |
|---|---:|---:|---:|---:|---:|
| Strict validate | **90.0 us** | 252.6 us (2.81x) | fastjson 260.4 us (2.89x) | 151.1 us (1.68x) | 111.9 us (1.24x)* |
| Typed decode, reused destination | **240.1 us** | 953.7 us (3.97x) | go-json 438.2 us (1.82x) | 276.1 us (1.15x) | 405.4 us (1.69x) |
| Dynamic `any` decode | **565.3 us** | 2.030 ms (3.59x) | go-json 1.085 ms (1.92x) | 618.5 us (1.09x) | 619.9 us (1.10x) |
| Marshal | **171.6 us** | 445.8 us (2.60x) | Segment 260.9 us (1.52x) | 231.7 us (1.35x) | 480.0 us (2.80x) |

The Sonic validation row is syntax-only: Sonic 1.15.2 does not reject invalid
UTF-8. simdjson and the Go-tip validation competitors in the table enforce
strict JSON and UTF-8.

### SIMD uplift

This is the same simdjson code and compiler with and without
`GOEXPERIMENT=simd`.

| simdjson path | SIMD | Pure Go | SIMD speedup |
|---|---:|---:|---:|
| Strict validate | **90.0 us** | 151.1 us | **1.68x** |
| Typed owned | **240.1 us** | 276.1 us | **1.15x** |
| Typed source-backed | **180.2 us** | 214.1 us | **1.19x** |
| Dynamic owned | **565.3 us** | 618.5 us | **1.09x** |
| Dynamic source-backed | **518.0 us** | 583.0 us | **1.13x** |
| Marshal | **171.6 us** | 231.7 us | **1.35x** |
| Compiled append, reused buffer | **98.1 us** | 147.1 us | **1.50x** |

### Other contracts

These rows are useful, but they are not folded into the owned leaderboard.

| Path | simdjson SIMD | Comparison | Why separate |
|---|---:|---:|---|
| Typed source-backed | **180.2 us** | Sonic source-backed 270.7 us | Unescaped strings may alias input |
| Compiled append | **98.1 us** | Segment Marshal 260.9 us | simdjson reuses caller output storage |
| Parse plus full DOM walk | **546.5 us** | stdlib tree plus walk 2.118 ms | Source-backed index versus owned `any` tree |
| Reused `BuildIndex` | **95.7 us** | C++ DOM parse 129.6 us | Different native structural representations |

`encoding/json/v2` geometric means on the same owned contracts are 746.4 us
for typed decode, 1.091 ms for dynamic decode, and 416.7 us for Marshal.
simdjson leads those rows by 3.11x, 1.93x, and 2.43x.

## Removed assembly

The production tree contains no `.s` or `.S` files. The final Go tip SIMD
index was measured against the last arm64 assembly build with eight 500 ms
samples. Both implementations used caller-provided storage and reported zero
allocations.

| Corpus | Removed assembly | Go SIMD | Go change |
|---|---:|---:|---:|
| Canada geometry | 132.4 us | **129.8 us** | -1.91% |
| CITM catalog | **442.0 us** | 445.0 us | +0.68% |
| Go source | 994.2 us | **926.9 us** | -6.77% |
| Escaped strings | 5.287 us | **4.607 us** | -12.86% |
| Unicode strings | 4.013 us | **3.405 us** | -15.15% |
| Synthea FHIR | 580.0 us | **478.5 us** | -17.51% |
| Twitter status | **174.1 us** | 177.6 us | +2.00% |
| Geometric mean | 103.2 us | **95.3 us** | **-7.65%** |

Generated-code checks for the fused stage-2 index machine show no calls, no
hot-path stack growth, no remaining bounds checks in its position/tape loop,
and no heap escape. The assembly replacement is therefore a real Go SIMD
kernel, not a wrapper around C or hidden assembly.

## Fairness rules

- Every library receives the same decompressed bytes and equivalent concrete
  models. Setup, plan compilation, correctness checks, and corpus loading happen
  before the timer.
- Owned decode rows require strings independent of input. Source-backed rows
  are named separately.
- Reused destinations and reused output buffers are named separately from fresh
  allocation.
- Validation rows must reject invalid syntax and invalid UTF-8. Sonic's weaker
  native `Valid` is labeled syntax-only and excluded from strict rankings.
- Same-process competitors use the same Go tip binary. Sonic uses the isolated
  Go 1.26.4 module because its native backend rejects Go 1.27 tip.
- C++ and Rust results are same-machine context, not a merged leaderboard.
  Their native representations do different work.
- `minio/simdjson-go` 0.4.5 is present in
  `BenchmarkStdlibCorpusNativeParse`. It requires amd64 AVX2 and CLMUL, so it
  is explicitly skipped on this arm64 host. The row runs on a supported amd64
  machine; no arm64 number is inferred or substituted.
- All competitor versions are pinned by `go.mod`, `go.sum`, the C++ commit
  in `crosslang/run.sh`, and the committed Rust `Cargo.lock`.

## Reproduce

Build the pinned tip compiler and run the complete Go matrix:

```sh
./scripts/bootstrap-gotip.sh "$HOME/sdk/simdjson-gotip"
TIP_GO="$HOME/sdk/simdjson-gotip/bin/go" ./benchmarks/run-comparison.sh
```

The script runs pure Go, Go SIMD, native structural APIs, jsonv2, and native
Sonic with `-cpu=1 -benchtime=300ms -count=6`. Override `BENCHTIME`,
`COUNT`, or `CPU` explicitly for a different protocol.

The exact SIMD headline command from `benchmarks/` is:

```sh
GOEXPERIMENT=simd "$TIP_GO" test -run='^$' \
  -bench='^BenchmarkStdlibCorpus$' \
  -benchmem -benchtime=300ms -count=6 -cpu=1 .
```

The pure-Go simdjson control is:

```sh
"$TIP_GO" test -run='^$' \
  -bench='^BenchmarkStdlibCorpus$/.*$/.*$/^simdjson' \
  -benchmem -benchtime=300ms -count=6 -cpu=1 .
```

Native structural APIs, including minio where supported:

```sh
GOEXPERIMENT=simd "$TIP_GO" test -run='^$' \
  -bench='^BenchmarkStdlibCorpusNativeParse$' \
  -benchmem -benchtime=300ms -count=6 -cpu=1 .
```

Native Sonic:

```sh
cd legacy
GOTOOLCHAIN=go1.26.4 go test -run='^$' \
  -bench='^BenchmarkStdlibCorpusNativeSonic$' \
  -benchmem -benchtime=300ms -count=6 -cpu=1 .
```

Cross-language front ends:

```sh
./benchmarks/crosslang/run.sh
```

The cross-language script pins C++ simdjson 4.6.4 at commit
`1bcf71bd85059ab6574ea1159de9298dcc1212c5`, and builds Rust with
`--locked`, native CPU features, LTO, and one codegen unit.

## Benchmark groups

- `BenchmarkStdlibCorpus`: strict validation, dynamic owned decode, Parse plus
  full walk, typed reused decode, and encode across Go libraries.
- `BenchmarkStdlibCorpusNativeParse`: simdjson `Index` and
  minio/simdjson-go's reusable parsed representation.
- `BenchmarkStdlibCorpusJSONV2`: direct `encoding/json/v2` owned operations.
- `BenchmarkStdlibCorpusNativeSonic`: native Sonic in the stable-toolchain
  module.
- `BenchmarkGapIndex`: production structural-index corpus benchmark.
- `BenchmarkParseTyped`: synthetic small, medium, and large typed models.

Current Go competitors are go-json 0.10.6, Segment encoding 0.5.4, jsoniter
1.1.12, fastjson 1.6.10, Sonic 1.15.2, and minio/simdjson-go 0.4.5.
