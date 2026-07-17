# Cross-language corpus benchmarks

This harness runs native JSON front ends over the exact seven payloads used by
the Go benchmark module. It is same-machine context, not a single-contract
leaderboard: each output representation is different.

## Snapshot

Apple M4 Max, one thread, regenerated 2026-07-16.

- Go: `go1.27-devel_03845e30`, `GOEXPERIMENT=simd`
- C++ simdjson 4.6.4, commit
  `1bcf71bd85059ab6574ea1159de9298dcc1212c5`, arm64 backend
- Rust serde_json 1.0.228 and simd-json 0.15.1 from the committed lockfile
- C++: `-O3 -DNDEBUG -march=native`
- Rust: `target-cpu=native`, LTO, one codegen unit

Throughput is decimal GB/s of input; higher is better.

| Corpus | Go strict validate | Go reused `Index` | C++ stage 1 | C++ DOM parse | simd-json borrowed | serde_json `Value` |
|---|---:|---:|---:|---:|---:|---:|
| Canada geometry | 2.06 | 2.07 | 6.47 | 1.59 | 0.51 | 0.54 |
| CITM catalog | 4.42 | 3.83 | 9.63 | 4.97 | 1.30 | 0.81 |
| Go source | 2.14 | 2.10 | 5.71 | 1.97 | 0.74 | 0.36 |
| Escaped strings | 9.75 | 9.14 | 10.27 | 0.86 | 0.60 | 0.93 |
| Unicode strings | 5.74 | 5.35 | 6.25 | 4.84 | 2.92 | 1.26 |
| Synthea FHIR | 4.54 | 4.13 | 9.67 | 4.97 | 1.19 | 0.52 |
| Twitter status | 3.70 | 3.55 | 8.60 | 4.40 | 1.39 | 0.54 |

## Contracts

- **Go strict validate** checks grammar and UTF-8 and produces no
  representation.
- **Go reused Index** checks grammar and UTF-8 and writes exact source ranges,
  kinds, counts, and subtree links into caller-owned 16-byte entries. It leaves
  number conversion and string unescaping on demand.
- **C++ stage 1** emits structural indexes only. It does not run the grammar or
  build a tape, so it is intentionally not compared as a complete parser.
- **C++ DOM parse** builds simdjson's native tape, parses numbers, and maintains
  its string arena with a reused parser.
- **simd-json borrowed** mutates a scratch copy and may borrow strings from it.
- **serde_json Value** materializes Rust's dynamic value tree.

The closest native front-end comparison is Go `Index` versus C++ DOM parse,
but it is still not identical work. Across all seven files, Go records a
95.7 us timing geomean and C++ 129.6 us, or 3.82 versus 2.82 GB/s. That
geomean is driven by Go's large lead on escaped strings. C++ is still faster on
the object-dense rows: 1.30x on CITM, 1.20x on FHIR, and 1.24x on Twitter.
Those three rows are the remaining structural performance target.

## Reproduce

The script downloads C++ simdjson only when the pinned checkout is absent,
verifies its commit, compiles the C++ harness, and runs Rust with
`cargo --locked`.

```sh
./benchmarks/crosslang/run.sh
```

Requirements: `clang++`, `cargo`, `zstd`, and the pinned Go corpus already
materialized by the repository scripts. Each harness performs warmup and six
roughly 300 ms samples per operation, reporting medians.

Raw C++ output includes both stage-1-only and DOM parse timing. Raw Rust output
includes serde parse/encode and simd-json owned/borrowed parse timing. Keeping
the raw modes visible makes it possible to compare a specific contract without
turning unlike representations into a winner claim.
