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
| simdjson | `47bd858b21563f5c2ad009074779f6543f2bc910` (`dirty=false`) |
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
| Strict validation | **3.141x** | **2.856x** | **1.800x** |
| Typed owned decode | **4.094x** | **1.804x** | **1.129x** |
| Dynamic owned decode | **3.590x** | **1.845x** | **1.056x** |
| Owned encode | **2.470x** | **1.400x** | **1.283x** |
| Compiled encode reuse | **4.746x** | **2.689x** | **1.518x** |
| Parse + complete walk | **6.026x** | — | **1.232x** |

These are aggregate results, not a claim that every payload is won. In
particular, owned encode is 7% behind go-json on CITM and Segment on
Synthea. Compiled reuse leads the compatible rival on every payload.

## Per-corpus results

### Strict validation

| Corpus | `encoding/json` | simdjson | Rival | Rival time | vs stdlib | vs rival |
|---|---:|---:|---|---:|---:|---:|
| Canada geometry | 224.2 us | **120.3 us** | fastjson | 219.1 us | **1.86x** | **1.82x** |
| CITM catalog | 756.7 us | **354.3 us** | fastjson | 870.5 us | **2.14x** | **2.46x** |
| Go source | 1.306 ms | **945.2 us** | Segment | 1.207 ms | **1.38x** | **1.28x** |
| Escaped strings | 55.3 us | **4.4 us** | Segment | 55.6 us | **12.48x** | **12.54x** |
| Unicode strings | 20.1 us | **3.2 us** | fastjson | 7.1 us | **6.21x** | **2.18x** |
| Synthea FHIR | 1.009 ms | **360.7 us** | fastjson | 1.271 ms | **2.80x** | **3.52x** |
| Twitter status | 365.0 us | **144.2 us** | fastjson | 406.0 us | **2.53x** | **2.81x** |

Valid input allocates zero bytes and zero objects.

### Typed owned decode

| Corpus | `encoding/json` | simdjson | Rival | Rival time | vs stdlib | vs rival |
|---|---:|---:|---|---:|---:|---:|
| Canada geometry | 1.240 ms | **187.0 us** | Segment | 769.7 us | **6.63x** | **4.12x** |
| CITM catalog | 2.551 ms | **790.1 us** | go-json | 1.261 ms | **3.23x** | **1.60x** |
| Go source | 6.353 ms | **1.357 ms** | Segment | 2.237 ms | **4.68x** | **1.65x** |
| Escaped strings | 196.7 us | **36.8 us** | go-json | 65.4 us | **5.34x** | **1.78x** |
| Unicode strings | 41.7 us | **8.4 us** | go-json | 13.7 us | **4.97x** | **1.63x** |
| Synthea FHIR | 3.929 ms | **1.637 ms** | go-json | 2.075 ms | **2.40x** | **1.27x** |
| Twitter status | 1.366 ms | **452.8 us** | go-json | 706.5 us | **3.02x** | **1.56x** |

### Dynamic owned decode

| Corpus | `encoding/json` | simdjson | Rival | Rival time | vs stdlib | vs rival |
|---|---:|---:|---|---:|---:|---:|
| Canada geometry | 2.980 ms | **949.6 us** | go-json | 1.876 ms | **3.14x** | **1.98x** |
| CITM catalog | 7.994 ms | **2.435 ms** | jsoniter | 4.558 ms | **3.28x** | **1.87x** |
| Go source | 18.813 ms | **4.715 ms** | go-json | 9.938 ms | **3.99x** | **2.11x** |
| Escaped strings | 216.9 us | **33.7 us** | go-json | 75.2 us | **6.43x** | **2.23x** |
| Unicode strings | 54.0 us | **13.5 us** | go-json | 21.4 us | **4.02x** | **1.59x** |
| Synthea FHIR | 11.702 ms | **4.286 ms** | jsoniter | 7.075 ms | **2.73x** | **1.65x** |
| Twitter status | 3.543 ms | **1.335 ms** | go-json | 2.128 ms | **2.65x** | **1.59x** |

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
| Canada geometry | 630.2 us | **394.4 us** | **307.6 us** | Segment | 522.7 us |
| CITM catalog | 1.078 ms | 414.6 us | **204.8 us** | go-json | **386.5 us** |
| Go source | 3.331 ms | **1.336 ms** | **683.3 us** | Segment | 1.355 ms |
| Escaped strings | 22.2 us | **7.5 us** | **3.4 us** | jsoniter | 21.9 us |
| Unicode strings | 22.7 us | **7.5 us** | **3.5 us** | jsoniter | 22.5 us |
| Synthea FHIR | 6.002 ms | 2.157 ms | **1.042 ms** | Segment | **2.013 ms** |
| Twitter status | 752.9 us | **348.5 us** | **176.0 us** | Segment | 355.1 us |

### Parse and complete walk

| Corpus | stdlib `any` + walk | simdjson parse + walk | Lead |
|---|---:|---:|---:|
| Canada geometry | 2.988 ms | **861.0 us** | **3.47x** |
| CITM catalog | 8.481 ms | **1.035 ms** | **8.20x** |
| Go source | 19.062 ms | **4.205 ms** | **4.53x** |
| Escaped strings | 212.7 us | **44.4 us** | **4.79x** |
| Unicode strings | 51.7 us | **8.6 us** | **6.04x** |
| Synthea FHIR | 12.707 ms | **1.277 ms** | **9.95x** |
| Twitter status | 3.820 ms | **491.8 us** | **7.77x** |

### Reusable structural index

`BuildIndex` validates the input and builds a caller-owned navigable tape.
Correctly sized entry storage is reused; all rows allocate zero bytes and zero
objects. This benchmark is included in both the regular publication runner and
the before/after performance gate. The production index and stage-2 machines
are Go-native SIMD or portable Go; the repository contains no assembly path or
safety build variant.

| Corpus | Time | Throughput |
|---|---:|---:|
| Canada geometry | **124.2 us** | **2.18 GB/s** |
| CITM catalog | **402.1 us** | **4.30 GB/s** |
| Go source | **874.3 us** | **2.22 GB/s** |
| Escaped strings | **4.8 us** | **8.84 GB/s** |
| Unicode strings | **3.5 us** | **5.16 GB/s** |
| Synthea FHIR | **445.4 us** | **4.51 GB/s** |
| Twitter status | **158.0 us** | **4.00 GB/s** |

## Native hook cost

Hooks keep the public API composable without weakening default ownership.
Decode uses one retainable receiver shadow plus one heap object that owns the
cursor copy and is invalidated on return. Encode passes ordinary GC-visible
receivers and allocates nothing after warmup.

| Case | Interpreter | Native hook | Hook / interpreter | Bytes/op | Allocs/op |
|---|---:|---:|---:|---:|---:|
| Decode small | 45.7 ns | 131.4 ns | 2.87x | 144 | 2 |
| Decode 1,024 records | 70.6 us | 166.7 us | 2.36x | 147,456 | 2,048 |
| Encode small | 36.1 ns | **32.8 ns** | 0.91x | 0 | 0 |
| Encode 1,024 records | 40.5 us | **39.6 us** | 0.98x | 13 | 0 |

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
| Validation | 6/7 | **1.800x** |
| Dynamic owned | 3/7 | **1.056x** |
| Dynamic zero-copy | 3/7 | **1.065x** |
| Parse + complete walk | 6/7 | **1.232x** |
| Typed owned | 6/7 | **1.129x** |
| Typed zero-copy | 5/7 | **1.161x** |
| Encode owned | 7/7 | **1.283x** |
| Encode compiled reuse | 7/7 | **1.518x** |
| Reusable structural index | 7/7 | **1.754x** |

## Additional Go context

`encoding/json/v2` is built from the same pinned Go tip with
`GOEXPERIMENT=jsonv2`. Geometric means of v2 time divided by simdjson time are
3.292x for typed owned decode, 1.971x for dynamic owned decode, and 2.308x for
owned encode.

Sonic v1.15.2 is measured in the isolated `legacy` module with Go 1.26.4
because it falls back to `encoding/json` on Go tip. Sonic time divided by
simdjson time is 1.687x for typed owned decode, 1.121x for dynamic owned decode,
and 2.534x for owned encode. Sonic's syntax-only validation accepts invalid
UTF-8, so its 1.374x ratio is context, not a contract-equivalent headline.

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
dynamic decode, and encode. It exits non-zero for any statistically significant
`sec/op` regression above 2% and for any significant `B/op` or `allocs/op`
increase; `-r` changes the time threshold and `-d .` selects root-package
resource and hook contracts. Pull requests run these checks on the dedicated
`simdjson-performance` runner rather than a noisy shared host. The nested
module pins every comparison dependency in `go.mod` and `go.sum`.
