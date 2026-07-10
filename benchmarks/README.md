# simdjson comparison benchmarks

This directory is a separate Go module. Comparison-only dependencies never
enter simdjson's root module graph.

## Fairness rules

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

Fixtures are 31 bytes, 4,240 bytes (32 records), and 136,586 bytes (1,024
records). Published results use the median of five one-second samples.

## Compiled decoder

These results exclude `CompileDecoder[T]` construction. Plans are initialized
before timing with case-sensitive field matching.

| Workload | SIMD, source-backed | SIMD, owned |
|---|---:|---:|
| Small, fresh | **29.9 ns / 0** | 43.3 ns / 1 |
| Medium, fresh | **2.36 us / 2** | 2.62 us / 3 |
| Medium, reused | **2.20 us / 0** | 2.48 us / 1 |
| Large, fresh | **75.9 us / 2** | 84.1 us / 3 |
| Large, reused | **74.8 us / 0** | 80.8 us / 1 |

The scalar-versus-SIMD control lives in the root module's `BenchmarkDecode*`
benchmarks, which build in seconds and compare the same binary pair.

Reproduce the compiled rows with:

```sh
GOEXPERIMENT=simd /Users/thesyncim/sdk/gotip/bin/go test \
  -run='^$' \
  -bench='^BenchmarkParseTyped/(small|medium|large)/simdjson-Compiled-(zero-copy|owned)(-reused)?$' \
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
  -bench='BenchmarkParseTyped/(small|medium|large)/simdjson-Compiled-(zero-copy|owned)(-reused)?$' \
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
  compiled zero-copy, compiled owned, and competitor modes.
- `BenchmarkParseTypedJSONV2`: direct `encoding/json/v2` typed decoding.
- `Valid`: syntax validation only.
- `ParseAny`: full conventional `any` materialization.
- `ParseAnyNumbers16`: materialization of 1,024 exact 16-digit integers.
- `ParseNative`: each library's native structural representation.
- `Index`: simdjson validation plus caller-owned structural index construction.

The module pins exact competitor versions in `go.mod` and `go.sum`.
simdjson-go v0.4.5 remains skipped on arm64 because its implementation requires
amd64 AVX2 and CLMUL.
