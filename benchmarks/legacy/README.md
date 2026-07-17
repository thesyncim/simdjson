# Stable-toolchain competitor benchmarks

This nested module benchmarks native implementations that do not build under
the Go tip toolchain required by simdjson. It deliberately does not import simdjson.

Sonic v1.15.2 supports Go 1.26 but selects an `encoding/json` fallback on Go
1.27 tip. The module therefore contains a mechanical copy of the pinned Go-tip
corpus models and reads the same compressed payloads as the main comparison
module. `scripts/check-stdlib-corpus.sh` verifies both model copies.

The current exact-corpus snapshot was regenerated on 2026-07-16 with Go
1.26.4 on the same Apple M4 Max as the Go-tip run:

```sh
GOTOOLCHAIN=go1.26.4 go test -run='^$' \
  -bench='^BenchmarkStdlibCorpusNativeSonic$' \
  -benchmem -benchtime=300ms -count=6 -cpu=1 .
```

Geometric means of the seven per-corpus medians:

| Native Sonic path | Time |
|---|---:|
| `Valid`, syntax-only | 111.9 us |
| Dynamic owned decode | 619.9 us |
| Typed `ConfigStd`, owned | 405.4 us |
| Typed `ConfigFastest`, source-backed | 270.7 us |
| Marshal | 480.0 us |

Sonic's `Valid` accepts invalid UTF-8, so it is context for simdjson's strict
validator rather than a strict-validation competitor. `ConfigFastest` may
alias input strings and is compared only with simdjson's source-backed mode.

Go 1.26 and Go tip timings remain separate because the compiler and standard
library revisions differ. Other benchmark groups cover synthetic typed models,
16-digit arrays, dynamic values, and public `Node.LoadAll`; those are not
mixed into the exact-corpus headline.
