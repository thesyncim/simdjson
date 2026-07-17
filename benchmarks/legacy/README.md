# Native stable-toolchain controls

This isolated module measures competitors that do not build natively with the
Go tip compiler required by simdjson. It deliberately does not import the
simdjson module.

Sonic v1.15.2 selects an `encoding/json` fallback on Go 1.27 tip, so its native
arm64 implementation is built here with Go 1.26.4. The module reads the same
seven compressed payloads and uses mechanical copies of the same concrete
models. `scripts/check-stdlib-corpus.sh` verifies those copies.

## Current corpus control

Apple M4 Max, one CPU, Go 1.26.4, six 300 ms samples per row, median reported:

| Corpus | Typed owned | Dynamic owned | Owned encode | Syntax-only `Valid` |
|---|---:|---:|---:|---:|
| Canada geometry | 444.8 us | 831.8 us | 803.3 us | 191.0 us |
| CITM catalog | 1.525 ms | 3.192 ms | 977.0 us | 778.5 us |
| Go source | 4.213 ms | 7.091 ms | 4.558 ms | 1.553 ms |
| Escaped strings | 33.1 us | 33.8 us | 20.7 us | 3.3 us |
| Unicode strings | 12.2 us | 14.6 us | 20.6 us | 1.7 us |
| Synthea FHIR | 3.358 ms | 5.673 ms | 8.802 ms | 855.4 us |
| Twitter status | 767.7 us | 1.159 ms | 597.7 us | 235.7 us |

Sonic's `Valid` accepts invalid UTF-8, so that column is not equivalent to
simdjson's strict validation contract. It is reported only as implementation
context. Compiler and standard-library revisions also differ; main benchmark
tables keep these rows out of same-toolchain fastest-rival selection.

## Reproduce

```sh
GOTOOLCHAIN=go1.26.4 go test -run='^$' \
  -bench='^BenchmarkStdlibCorpusNativeSonic$' \
  -benchmem -benchtime=300ms -count=6 -cpu=1 .
```

The default `../run-comparison.sh` invokes the same contract one operation
group per fresh process. Other benchmarks in this module are exploratory and
are not part of the release publication.
