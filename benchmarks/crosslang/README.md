# Equivalent C++/Go corpus benchmark

This directory provides one direct cross-language comparison:
`parse+semantic-digest`. It uses the same seven-payload corpus, machine, source
bytes, traversal order, number categories, decoded strings, and digest in both
implementations.

Representation-specific DOM, typed decode, validation-only, and serialization
measurements do different work and are intentionally not ranked here.

## Enforced contract

Every timed iteration:

1. parses the complete, already-loaded source with reusable parser storage;
2. visits every array element and object member in source order;
3. decodes every object key and string value;
4. decodes every number to the same signed, unsigned, binary64, or big-integer
   category; and
5. hashes the complete semantic event stream with the same 64-bit FNV-1a
   algorithm.

File I/O, capacity discovery, reusable storage allocation, and the reference
digest are outside the timer. Grammar validation, unescaping, number
conversion, complete traversal, and digest construction are inside it.

The runner compares every C++ and Go digest and exits with an error before
publication if any pair differs. It also refuses a dirty repository by default,
pins C++ simdjson 4.6.4, and verifies the downloaded amalgamation by SHA-256.

## Current release-candidate result

| Component | Revision |
|---|---|
| Go simdjson | `a48608811500b6d5abc2279465181e8c4b394e4c` (`dirty=false`) |
| Go compiler | `go1.27-devel_03845e30`, `GOEXPERIMENT=simd` |
| C++ simdjson | 4.6.4, arm64 implementation, clang 21 |
| Machine | Apple M4 Max, single thread |

Six approximately 300 ms samples are taken per operation; the median is
reported.

| Corpus | Digest | C++ | Go |
|---|---|---:|---:|
| Canada geometry | `99bfa84117bedba4` | **362.3 us** | 970.7 us |
| CITM catalog | `aa5480c889a90335` | **1.007 ms** | 1.341 ms |
| Go source | `143678d948841678` | **3.312 ms** | 4.947 ms |
| Escaped strings | `ceb1fff950644c35` | 69.7 us | **49.4 us** |
| Unicode strings | `ceb1fff950644c35` | **22.6 us** | 40.3 us |
| Synthea FHIR | `3d3241a500faabe1` | **1.840 ms** | 3.819 ms |
| Twitter status | `7fd8ebd3db991240` | **683.8 us** | 1.300 ms |

The identical digest for the two string fixtures is expected: they decode to
the same semantic value even though one source uses escapes and the other uses
literal Unicode. That is also why the two rows have very different parsing
costs.

## Reproduce

The runner requires `clang++`, `cargo`, `curl`, `zstd`, git, and the pinned Go
binary:

```sh
TIP_GO="$HOME/sdk/simdjson-gotip/bin/go" ./benchmarks/crosslang/run.sh
```

It prints the exact repository commit, dirty status, toolchains, implementation
selection, per-row digests, and timings. C++ simdjson defaults to 4.6.4; an
overridden version is a different benchmark record and must be labelled as
such.
