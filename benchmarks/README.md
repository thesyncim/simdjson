# simdjson comparison benchmarks

This directory is a separate Go module. Comparison-only dependencies and code
generation tools never enter simdjson's root module graph.

## Fairness rules

- Go-tip typed decoders consume the same byte slices and decode equivalent
  `TypedSmall` or `TypedDocument` schemas.
- easyjson types live in `easyjsonmodel`. Attaching easyjson's generated
  `UnmarshalJSON` method to the shared types would cause stdlib, Segment,
  go-json, and jsoniter to call easyjson instead of their own decoder.
- `simdjson-Generated-zero-copy` is compared with Sonic `ConfigFastest`, whose
  `CopyString` setting is false. Owned simdjson is compared with owned-string rows.
- Fresh and reused destinations have different benchmark names.
- Sonic native results come from `legacy`, because Sonic v1.15.2 falls back to
  `encoding/json` on Go 1.27 tip.
- `encoding/json/v2` is imported directly and built only with the
  `goexperiment.jsonv2` tag.

Fixtures are 31 bytes, 4,240 bytes (32 records), and 136,586 bytes (1,024
records). Published results use the median of five one-second samples.

## Generated vs compile once

This comparison excludes `CompileDecoder[T]` construction. Both decoders are
initialized before timing with source-backed strings and case-sensitive field
matching.

| Workload | Compiled once | Generated | Generated speedup |
|---|---:|---:|---:|
| Small, fresh | 84.59 ns / 1 alloc | 30.31 ns / 0 allocs | **2.79x** |
| Medium, fresh | 5.462 us / 3 allocs | 2.926 us / 1 alloc | **1.87x** |
| Medium, reused | 5.191 us / 0 allocs | 2.706 us / 0 allocs | **1.92x** |
| Large, fresh | 175.894 us / 3 allocs | 88.830 us / 1 alloc | **1.98x** |
| Large, reused | 169.482 us / 0 allocs | 85.051 us / 0 allocs | **1.99x** |

The speedup column is `compiled time / generated time`. Reproduce the paired
run with:

```sh
GOEXPERIMENT=simd /Users/thesyncim/sdk/gotip/bin/go test \
  -run='^$' \
  -bench='^BenchmarkParseTyped/(small|medium|large)/simdjson-(Compiled|Generated)-zero-copy(-reused)?$' \
  -benchmem -benchtime=1s -count=5 .
```

## Go tip runs

The published SIMD command was:

```sh
GOEXPERIMENT=simd /Users/thesyncim/sdk/gotip/bin/go test \
  -run='^$' -bench='^BenchmarkParseTyped$' \
  -benchmem -benchtime=1s -count=5 .
```

Pure-Go simdjson control:

```sh
/Users/thesyncim/sdk/gotip/bin/go test \
  -run='^$' \
  -bench='BenchmarkParseTyped/(small|medium|large)/(simdjson-Generated-(zero-copy|owned)(-reused)?)$' \
  -benchmem -benchtime=1s -count=5 .
```

Direct `encoding/json/v2`:

```sh
GOEXPERIMENT=simd,jsonv2 /Users/thesyncim/sdk/gotip/bin/go test \
  -run='^$' -bench='^BenchmarkParseTypedJSONV2' \
  -benchmem -benchtime=1s -count=5 .
```

The measured tip compiler was:

```text
go version go1.27-devel_d468ad36 Tue Jul 7 05:58:00 2026 -0700 darwin/arm64
```

## Native stable-toolchain runs

Sonic v1.15.2 supports Go 1.26 but not this Go 1.27 development revision. Its
native arm64 benchmark is isolated under `legacy` and does not import simdjson:

```sh
cd legacy
GOTOOLCHAIN=local \
  /Users/thesyncim/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.4.darwin-arm64/bin/go test \
  -run='^$' \
  -bench='BenchmarkSonicNativeParseTyped(Fastest|Std)$|BenchmarkSonicNativeParseTypedReused(Fastest|Std)$' \
  -benchmem -benchtime=1s -count=5 .
```

`run-comparison.sh` runs the broad pure-Go, SIMD, jsonv2, and stable native
suites. Override `TIP_GO`, `LEGACY_GO`, `BENCH`, `BENCHTIME`, or `COUNT` to use
another pinned environment.

## Benchmark groups

- `BenchmarkParseTyped`: typed fresh and reused decoding across conventional,
  generated, compiled, zero-copy, and owned modes.
- `BenchmarkParseTypedJSONV2`: direct `encoding/json/v2` typed decoding.
- `Valid`: syntax validation only.
- `ParseAny`: full conventional `any` materialization.
- `ParseAnyNumbers16`: materialization of 1,024 exact 16-digit integers.
- `ParseNative`: each library's native structural representation.
- `Index`: simdjson validation plus caller-owned structural index construction.

The module pins exact competitor versions in `go.mod` and `go.sum`.
simdjson-go v0.4.5 remains skipped on arm64 because its implementation requires
amd64 AVX2 and CLMUL.
