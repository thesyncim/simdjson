# Stable-toolchain competitor benchmarks

This nested module benchmarks native implementations that do not build under
the Go tip toolchain required by simdjson. It deliberately does not import simdjson.

Sonic v1.15.2 supports Go 1.26 but selects an `encoding/json` fallback on Go
1.27 tip. The module therefore contains a mechanical copy of the pinned Go-tip
corpus models and reads the same compressed payloads as the main comparison
module. `scripts/check-stdlib-corpus.sh` verifies both model copies.

The published exact-corpus run used Go 1.26.4 on Apple M4 Max:

```sh
GOTOOLCHAIN=go1.26.4 go test -run='^$' \
  -bench='^BenchmarkStdlibCorpusNativeSonic$' \
  -benchmem -benchtime=300ms -count=6 -cpu=1 .
```

It benchmarks native validation, owned dynamic decode, typed reused decode in
`ConfigStd` and `ConfigFastest`, and encode. Go 1.26 and Go tip timings are
reported in separate columns because compiler and standard-library revisions
differ.

Medians of five samples, shown as `time / allocs-op`:

| Sonic configuration | Small | Medium | Large |
|---|---:|---:|---:|
| `ConfigFastest`, zero-copy | 187.9 ns / 4 | 5.74 us / 6 | 170.5 us / 6 |
| `ConfigStd`, owned strings | 233.0 ns / 5 | 7.82 us / 71 | 227.9 us / 2,055 |

Reused destinations:

| Sonic configuration | Medium | Large |
|---|---:|---:|
| `ConfigFastest`, zero-copy | 5.63 us / 3 | 166.6 us / 3 |
| `ConfigStd`, owned strings | 7.75 us / 68 | 233.2 us / 2,052 |

Other benchmark groups retain native validation, `any` materialization,
16-digit arrays, and public `Node.LoadAll` coverage. `Node.LoadAll` is not
treated as index-equivalent because nested containers may remain validated raw
nodes.
