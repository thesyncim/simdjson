# simdjson benchmarks

This separate module measures the repository's release contract: strict
correctness, safe default ownership, fast ordinary paths, and zero hidden
tuning requirements. Comparison-only dependencies never enter the root module
graph.

## Publication record

Every table in this document comes from the same clean library revision and
toolchain:

| Component | Revision |
|---|---|
| simdjson | `a48608811500b6d5abc2279465181e8c4b394e4c` (`dirty=false`) |
| Go | `go1.27-devel_03845e30`, commit `03845e30f7b73d1703bd8c21017297f6eecb76d6` |
| Machine | Apple M4 Max, `darwin/arm64`, one CPU |
| Samples | six samples per row, 300 ms each, median reported |

Each `valid`, `dynamic-owned`, `dom`, `typed-reused`, and `encode`
contract runs in a fresh process. This is required: allocator-heavy dynamic
decoding changes GC and allocator state enough to perturb later DOM results in
one monolithic process.

The exact corpus contains the seven payloads pinned from Go's
`encoding/json` tests, 6.33 MiB in total. Compilation, plan creation, fixture
decode, output capacity preparation, and correctness checks happen before the
timer. Timed rows use the same input bytes and one CPU.

## What is compared

- Validation means complete RFC 8259 syntax and UTF-8 validation.
- Typed and dynamic headline rows return owned strings and values. Zero-copy
  rows are controls with a different lifetime contract and are not mixed into
  the headline.
- Owned encode compares `Marshal` with `Marshal`. Compiled reuse appends into
  caller-owned capacity and is reported separately.
- DOM means one parse followed by a complete walk that reads every value.
  The standard library first creates an owned `any` tree; simdjson creates its
  structural representation and decodes scalars during the walk.
- “Rival” is the fastest compatible per-payload row from go-json v0.10.6,
  Segment encoding v0.5.4, jsoniter v1.1.12, or fastjson v1.6.10, built with
  the same Go tip compiler.

All results are checked against `encoding/json` before timing. Encode may
differ byte-for-byte only when the decoded values are equal. easyjson's
generated methods live on separate model types so they cannot intercept other
libraries' rows.

## Headline geomeans

| Operation | vs `encoding/json` | vs fastest compatible rival | SIMD vs pure Go |
|---|---:|---:|---:|
| Strict validation | **2.916x** | **2.569x** | **1.647x** |
| Typed owned decode | **4.002x** | **1.722x** | **1.104x** |
| Dynamic owned decode | **3.621x** | **1.842x** | **1.067x** |
| Owned encode | **2.453x** | **1.431x** | **1.490x** |
| Compiled encode reuse | **4.608x** | **2.689x** | **1.791x** |
| Parse + complete walk | **5.799x** | — | **1.200x** |

These are aggregate results, not a claim that every payload is won. In
particular, owned encode is 3% behind go-json on CITM and 2% behind Segment on
Synthea. Compiled reuse leads the compatible rival on every payload.

## Per-corpus results

### Strict validation

| Corpus | `encoding/json` | simdjson | Rival | Rival time | vs stdlib | vs rival |
|---|---:|---:|---|---:|---:|---:|
| Canada geometry | 270.0 us | **130.1 us** | fastjson | 209.2 us | **2.08x** | **1.61x** |
| CITM catalog | 762.1 us | **444.5 us** | fastjson | 864.7 us | **1.71x** | **1.95x** |
| Go source | 1.408 ms | **956.0 us** | Segment | 1.190 ms | **1.47x** | **1.24x** |
| Escaped strings | 55.6 us | **4.4 us** | Segment | 56.3 us | **12.61x** | **12.77x** |
| Unicode strings | 18.0 us | **3.3 us** | fastjson | 7.1 us | **5.48x** | **2.17x** |
| Synthea FHIR | 1.033 ms | **449.0 us** | fastjson | 1.290 ms | **2.30x** | **2.87x** |
| Twitter status | 365.9 us | **169.8 us** | fastjson | 405.0 us | **2.16x** | **2.39x** |

Valid input allocates zero bytes and zero objects.

### Typed owned decode

| Corpus | `encoding/json` | simdjson | Rival | Rival time | vs stdlib | vs rival |
|---|---:|---:|---|---:|---:|---:|
| Canada geometry | 1.352 ms | **192.9 us** | Segment | 779.6 us | **7.01x** | **4.04x** |
| CITM catalog | 2.580 ms | **953.8 us** | go-json | 1.277 ms | **2.71x** | **1.34x** |
| Go source | 6.526 ms | **1.636 ms** | Segment | 2.241 ms | **3.99x** | **1.37x** |
| Escaped strings | 202.3 us | **36.4 us** | go-json | 67.1 us | **5.56x** | **1.85x** |
| Unicode strings | 41.6 us | **8.1 us** | go-json | 13.6 us | **5.14x** | **1.68x** |
| Synthea FHIR | 3.911 ms | **1.583 ms** | go-json | 1.980 ms | **2.47x** | **1.25x** |
| Twitter status | 1.395 ms | **453.9 us** | go-json | 708.5 us | **3.07x** | **1.56x** |

### Dynamic owned decode

| Corpus | `encoding/json` | simdjson | Rival | Rival time | vs stdlib | vs rival |
|---|---:|---:|---|---:|---:|---:|
| Canada geometry | 3.166 ms | **934.9 us** | go-json | 1.899 ms | **3.39x** | **2.03x** |
| CITM catalog | 7.792 ms | **2.553 ms** | jsoniter | 4.476 ms | **3.05x** | **1.75x** |
| Go source | 18.108 ms | **4.744 ms** | go-json | 9.812 ms | **3.82x** | **2.07x** |
| Escaped strings | 216.8 us | **32.3 us** | go-json | 76.0 us | **6.70x** | **2.35x** |
| Unicode strings | 55.1 us | **13.1 us** | go-json | 21.1 us | **4.21x** | **1.62x** |
| Synthea FHIR | 11.652 ms | **4.222 ms** | jsoniter | 6.881 ms | **2.76x** | **1.63x** |
| Twitter status | 3.489 ms | **1.316 ms** | go-json | 2.080 ms | **2.65x** | **1.58x** |

Dynamic `any` values use ordinary Go interface construction. There is no
hand-built interface header or runtime layout dependency. That keeps the
default representation safe and produces this current allocation profile:

| Corpus | Bytes/op | Allocs/op |
|---|---:|---:|
| Canada geometry | 1,440,704 | 22,228 |
| CITM catalog | 6,193,280 | 41,187 |
| Go source | 7,611,600 | 103,805 |
| Escaped strings | 41,112 | 77 |
| Unicode strings | 34,968 | 76 |
| Synthea FHIR | 8,630,136 | 64,558 |
| Twitter status | 2,404,688 | 11,209 |

Avoiding those scalar interface boxes safely requires a different public
representation; the standard `any` contract does not justify runtime-layout
manipulation.

### Encode

| Corpus | stdlib | Owned | Compiled reuse | Rival | Rival time |
|---|---:|---:|---:|---|---:|
| Canada geometry | 641.7 us | **402.2 us** | **313.4 us** | Segment | 528.7 us |
| CITM catalog | 1.071 ms | 416.8 us | **211.4 us** | go-json | **404.3 us** |
| Go source | 3.316 ms | **1.317 ms** | **691.2 us** | Segment | 1.411 ms |
| Escaped strings | 22.1 us | **7.7 us** | **3.7 us** | jsoniter | 22.7 us |
| Unicode strings | 22.2 us | **7.7 us** | **3.7 us** | jsoniter | 22.8 us |
| Synthea FHIR | 6.022 ms | 2.098 ms | **1.047 ms** | Segment | **2.055 ms** |
| Twitter status | 742.0 us | **337.7 us** | **177.2 us** | go-json | 359.0 us |

### Parse and complete walk

| Corpus | stdlib `any` + walk | simdjson parse + walk | Lead |
|---|---:|---:|---:|
| Canada geometry | 3.054 ms | **870.5 us** | **3.51x** |
| CITM catalog | 8.432 ms | **1.075 ms** | **7.84x** |
| Go source | 19.229 ms | **4.340 ms** | **4.43x** |
| Escaped strings | 207.4 us | **44.5 us** | **4.66x** |
| Unicode strings | 51.2 us | **8.6 us** | **5.93x** |
| Synthea FHIR | 12.723 ms | **1.364 ms** | **9.33x** |
| Twitter status | 3.770 ms | **538.0 us** | **7.01x** |

### Reusable structural index

`BuildIndex` validates the input and builds a caller-owned navigable tape.
Correctly sized entry storage is reused; all rows allocate zero bytes and zero
objects. This benchmark is included in both the regular publication runner and
the before/after performance gate.

| Corpus | Time | Throughput |
|---|---:|---:|
| Canada geometry | **132.5 us** | **2.04 GB/s** |
| CITM catalog | **445.9 us** | **3.87 GB/s** |
| Go source | **992.7 us** | **1.95 GB/s** |
| Escaped strings | **4.8 us** | **8.79 GB/s** |
| Unicode strings | **3.6 us** | **5.11 GB/s** |
| Synthea FHIR | **504.2 us** | **3.98 GB/s** |
| Twitter status | **173.5 us** | **3.64 GB/s** |

## Native hook cost

Hooks keep the public API composable without weakening default ownership.
Decode uses one retainable receiver shadow plus one heap object that owns the
cursor copy and is invalidated on return. Encode passes ordinary GC-visible
receivers and allocates nothing after warmup.

| Case | Interpreter | Native hook | Hook / interpreter | Bytes/op | Allocs/op |
|---|---:|---:|---:|---:|---:|
| Decode small | 45.0 ns | 131.1 ns | 2.91x | 144 | 2 |
| Decode 1,024 records | 71.4 us | 165.6 us | 2.32x | 147,456 | 2,048 |
| Encode small | 36.2 ns | **33.3 ns** | 0.92x | 0 | 0 |
| Encode 1,024 records | 39.7 us | **39.0 us** | 0.98x | 12 | 0 |

The remaining decode tax is explicit and bounded: two allocations per actual
hook invocation, not per field. Recovering it further must retain the
receiver, cursor invalidation, forced-GC, stack-growth, and poisoning tests;
pooling a retainable object is not a safe shortcut.

## SIMD controls

Both binaries use the same candidate, compiler, corpus, isolated process
contract, and one CPU. Short or branch-heavy rows can remain scalar, so win
counts are reported alongside the geomean.

| Path | SIMD wins | Geomean uplift |
|---|---:|---:|
| Validation | 6/7 | **1.647x** |
| Dynamic owned | 5/7 | **1.067x** |
| Dynamic zero-copy | 5/7 | **1.082x** |
| Parse + complete walk | 6/7 | **1.200x** |
| Typed owned | 4/7 | **1.104x** |
| Typed zero-copy | 3/7 | **1.141x** |
| Encode owned | 7/7 | **1.490x** |
| Encode compiled reuse | 5/7 | **1.791x** |

## Additional Go context

`encoding/json/v2` is built from the same pinned Go tip with
`GOEXPERIMENT=jsonv2`. Geometric means of v2 time divided by simdjson time are
3.220x for typed owned decode, 2.009x for dynamic owned decode, and 2.316x for
owned encode.

Sonic v1.15.2 is measured in the isolated `legacy` module with Go 1.26.4
because it falls back to `encoding/json` on Go tip. Sonic time divided by
simdjson time is 1.732x for typed owned decode, 1.126x for dynamic owned decode,
and 2.626x for owned encode. Sonic's syntax-only validation accepts invalid
UTF-8, so its 1.238x ratio is context, not a contract-equivalent headline.

## Reproduce

Build the pinned toolchain and run the default publication path:

```sh
./scripts/bootstrap-gotip.sh "$HOME/sdk/simdjson-gotip"
TIP_GO="$HOME/sdk/simdjson-gotip/bin/go" ./benchmarks/run-comparison.sh
```

The runner refuses a dirty tree, records the repository and toolchain
revisions, uses six 300 ms one-CPU samples, starts every corpus contract in a
fresh process, runs pure-Go controls, includes reusable structural-index
performance, and then runs jsonv2 and native Sonic controls. `BENCH`,
`BENCHTIME`, and `COUNT` are available for exploratory runs; results from those
overrides are not publication records.

The equivalent C++ command and current results are documented under
[crosslang](crosslang/). That runner fails unless every semantic digest matches
and the repository is clean.

The interleaved before/after gate used for hot-path changes is:

```sh
GOTIP="$HOME/sdk/simdjson-gotip/bin/go" ./scripts/bench-gate.sh -b HEAD~1
```

Its default pattern covers validation, reusable structural indexing, typed and
dynamic decode, and encode. The nested module pins every comparison dependency
in `go.mod` and `go.sum`.
