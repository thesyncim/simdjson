# simdjson

[![ci](https://github.com/thesyncim/simdjson/actions/workflows/ci.yml/badge.svg)](https://github.com/thesyncim/simdjson/actions/workflows/ci.yml)

> [!IMPORTANT]
> **Go tip is required.** simdjson does not currently build with a stable Go
> release. Any current Go tip toolchain can be used; the exact Go commit shown
> in the benchmark section is only a reproducibility pin. Set
> `GOEXPERIMENT=simd` to enable the Go-native SIMD kernels. The same Go tip
> compiler builds portable fallbacks when the experiment is omitted.

Strict, high-performance JSON for Go, written entirely in Go.

simdjson combines compiled typed codecs, dynamic parsing, structural indexes,
JSON Pointer lookup, strict validation, transforms, and reusable SIMD kernels.
The root module has no third-party dependencies, generated codecs, assembly,
C, `go:linkname`, or runtime map-layout assumptions.

[Quick start](#quick-start) | [Choose an API](#choose-an-api) | [Performance](#performance) | [SIMD package](#simd-package) | [Safety](#safety-and-correctness) | [Reproduce](#reproduce-benchmarks)

## Quick Start

Install any current Go tip toolchain. `gotip` is the simplest option:

```sh
go install golang.org/dl/gotip@latest
gotip download
```

Then, from your module:

```sh
gotip get github.com/thesyncim/simdjson@latest
```

Use the cached convenience API for ordinary calls:

```go
import "github.com/thesyncim/simdjson"

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

Compile a plan once on hot paths. Plans are immutable and safe for concurrent
use; destinations and output buffers remain caller-owned.

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

Enable SIMD when building or testing:

```sh
GOEXPERIMENT=simd gotip test ./...
```

Without `GOEXPERIMENT=simd`, simdjson keeps the same API and behavior while
using its portable Go implementations.

## Choose an API

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
| Streaming write | `NewWriter`, `EncodeTo`, token methods | One reused buffer |
| Streaming read | `NewReader`, `Next`, `DecodeTo`, `DecodeNext` | Rolling buffer; values alias until `Next` |
| Both directions bundled | `CompileCodec[T]` | Per-codec size hint; reused buffers |
| Reusable byte kernels | `simd` subpackage | Caller-provided storage |

### Ownership

| Decode mode | Unescaped strings | Caller obligation |
|---|---|---|
| Default | Alias one private copy of input | None; result is independent of `src` |
| `ZeroCopy: true` | Alias caller input | Keep `src` alive and immutable |
| Escaped string | Decode into owned storage | None |

`RawValue`, `Index`, and `Node` always alias input. `Index` and `Node` also
alias the caller-provided `IndexEntry` workspace.

`DecoderOptions.Replace` resets destination state not mentioned by the next
document. The default merges like `encoding/json`: absent members retain their
current values, while `null` clears pointers, maps, slices, and interfaces.

`DecodeArray` decodes a top-level array while reusing caller-provided slice
capacity. Decode and encode errors include a typed path assembled only on
failure.

`AppendJSON` requires its destination backing storage to be disjoint from
storage reachable through the source value. On error it returns the original
destination length, but unused capacity may contain partial output.

### Streaming

`Writer` streams NDJSON or concatenated values through one reused buffer,
either from compiled encoders or through token methods that track container
state and refuse to emit malformed JSON. `Reader` iterates top-level values
from any `io.Reader`; each `Next` validates one complete value, values alias
the rolling buffer only until the following `Next`, and a value split across
reads costs one compacting copy. `DecodeNext` fuses iteration and typed
decoding into one pass — the decoder itself finds the value boundary — and
is the fastest way through a typed stream. Steady-state streaming in both
directions performs no per-value allocations. `CompileCodec` bundles both
directions with a per-codec size hint.

```go
codec, _ := simdjson.CompileCodec[Event](simdjson.CodecOptions{})

w := simdjson.NewWriter(out)
for _, event := range events {
    codec.EncodeTo(w, &event)
    w.Newline()
}
w.Close()

r := simdjson.NewReader(in)
var event Event
for simdjson.DecodeNext(r, codec.Decoder(), &event) {
    // use event
}
err := r.Err()
```

## Performance

Apple M4 Max, one CPU, six 300 ms samples, exact 6.33 MiB Go
`encoding/json` corpus. Lower time is better; the table reports geometric-mean
speedup across all seven payloads.

| Operation | Contract | vs stdlib | vs fastest rival | vs native Sonic | SIMD vs pure Go |
|---|---|---:|---:|---:|---:|
| Validate | Strict JSON + UTF-8 | **2.40x** | **2.22x** | **1.09x** | **1.436x** |
| Typed decode | Owned strings | **3.46x** | **1.54x** | **1.51x** | **1.092x** |
| Dynamic decode | Owned `any` tree | **3.29x** | **1.71x** | **1.02x** | **1.066x** |
| Encode | Owned output | **2.54x** | **1.49x** | **2.72x** | **1.510x** |
| Encode | Reused output buffer | **4.52x** | **2.66x** | — | **1.780x** |

Every stdlib row wins all seven payloads. Every rival row wins all seven except
owned encode, which wins six. Comparisons use the same Go tip compiler and do
not mix owned and source-backed results. The Sonic column compares against
native Sonic v1.15.2 compiled with the previous stable Go (1.26.4) in an
isolated module, because Sonic falls back to `encoding/json` on Go tip; it is
excluded from the fastest-rival column, its `Valid` is syntax-only where ours
also enforces UTF-8, and it has no reused-buffer `Marshal` counterpart. The
SIMD column compares the same code, compiler, and corpus with and without
`GOEXPERIMENT=simd`.

[Full per-corpus results, allocations, SIMD uplift, versions, and exact commands](benchmarks/README.md#published-corpus-snapshot).
For context beyond Go — C++ simdjson and Rust serde_json/simd-json on the
same corpus and machine — see the
[cross-language benchmarks](benchmarks/README.md#cross-language-context).

## SIMD Package

`github.com/thesyncim/simdjson/simd` owns every architecture-specific kernel,
runtime feature probe, and dispatch decision. It can be imported independently
and adds no module dependencies.

The fixed-array digit API makes load and store widths explicit:

```go
if len(src) >= 16 {
	digits := (*[16]byte)(src)
	if simd.All16Digits(digits) {
		value := simd.Parse16Digits(digits)
		_ = value
	}
}
```

The package includes:

- fixed-width decimal parsing and formatting;
- `encoding/json`-compatible float formatting;
- quoted RFC3339Nano time formatting;
- JSON and HTML-safe string scans and fused prefix copies;
- strict UTF-8, U+2028/U+2029, and contiguous `\uXXXX` kernels;
- safe public scanners plus an explicit precondition-based `simd.Unchecked`
  surface; and
- `Current`, which reports selected backends, vector widths, and CPU features.

All APIs have portable fallbacks. [Kernel benchmark results](benchmarks/README.md#simd-kernel-snapshot)
use guarded public APIs and report zero allocations.

### Runtime Dispatch

`GOEXPERIMENT=simd` enables the vector implementations. Runtime capabilities
are read once, and implementation choices are fixed during package
initialization.

| Runtime | String scanning | Decimal parse | Decimal and time format |
|---|---|---|---|
| arm64 | NEON on sustained runs; overlap-vector tails | NEON 16-digit reduction | NEON 16-digit and RFC3339 formatting |
| amd64 with AVX-512 | 64-byte AVX-512 | AVX 16-digit reduction | Scalar SWAR |
| amd64 with AVX2 | 32-byte AVX2 | AVX 16-digit reduction | Scalar SWAR |
| Other build or CPU | Scalar Go | Scalar Go | Scalar SWAR |

Other vector paths include strict UTF-8 validation, contiguous `\uXXXX`
validation, U+2028/U+2029 detection, string encoding scans, common
shortest-float placement, and typed decimal formatting. Every vector load is
length-guarded.

## Safety and Correctness

Unsafe code is restricted to measured internal paths:

- vector loads and stores require a complete guarded block;
- public scanners clamp offsets, while `simd.Unchecked` documents its bounds
  precondition;
- fused-copy kernels reject short or overlapping destinations;
- packed field-name stores use compiler-owned blocks and cannot extend past a
  successful result;
- float and integer stores prove output size and table bounds first;
- typed offsets and strides come from public `reflect` metadata;
- maps use public reflection APIs and never assume runtime layout;
- source-backed APIs require immutable input; and
- pointer-receiver custom methods use GC-safe heap-backed shadows.

The test suite covers:

- all 318 Nicolas Seriot JSONTestSuite parsing cases;
- all seven pinned Go tip high-level payloads and exact concrete models;
- typed and dynamic differentials against `encoding/json`;
- fields, tags, merge behavior, duplicate names, number boundaries, UTF-8,
  escapes, custom methods, map keys, depth, and error paths;
- 500,000 randomized float spellings and 700,000 randomized or boundary
  timestamps;
- randomized scalar/SIMD differentials across lengths and alignments; and
- validation, transform, typed decode, encode, number, and SIMD fuzzers.

Correctness fixes that touch a hot path require before-and-after benchmarks.
A regression is optimized back before merge; correctness is never traded away
to recover it.

The local release gate uses the pinned compiler only so results are exactly
reproducible:

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
pointer-hiding helper in `noescape.go`; all other vet analyzers remain enabled.

## Reproduce Benchmarks

Comparison libraries live in nested modules and never enter the root module.
The complete suite builds the pinned Go tip compiler, verifies the copied
stdlib corpus, and runs pure Go, SIMD, jsonv2, and native Sonic controls:

```sh
./scripts/bootstrap-gotip.sh "$HOME/sdk/simdjson-gotip"
TIP_GO="$HOME/sdk/simdjson-gotip/bin/go" ./benchmarks/run-comparison.sh
```

[Benchmark methodology and individual commands](benchmarks/README.md)
