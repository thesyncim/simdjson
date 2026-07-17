# Native stable-toolchain controls

This isolated module measures competitors that do not build natively with the
Go tip compiler required by simdjson. It deliberately does not import the
simdjson module.

Sonic v1.15.2 selects an `encoding/json` fallback on Go 1.27 tip, so its native
arm64 implementation is built here with Go 1.26.4. The module reads the same
seven compressed payloads and uses mechanical copies of the same concrete
models. `scripts/check-stdlib-corpus.sh` verifies those copies.

## Current corpus control

<!-- benchpublish:legacy-control:start -->
Apple M4 Max, one CPU, Go 1.26.4, six 300 ms samples per row, median reported:

| Corpus | Typed owned | Dynamic owned | Owned encode | Syntax-only `Valid` |
|---|---:|---:|---:|---:|
| Canada geometry | 438.6 us | 852.3 us | 799.7 us | 191.1 us |
| CITM catalog | 1.436 ms | 3.230 ms | 961.1 us | 786.8 us |
| Go source | 3.380 ms | 7.040 ms | 3.977 ms | 1.551 ms |
| Escaped strings | 32.4 us | 34.0 us | 20.5 us | 3.4 us |
| Unicode strings | 12.0 us | 13.7 us | 20.5 us | 1.7 us |
| Synthea FHIR | 2.842 ms | 5.652 ms | 8.213 ms | 859.9 us |
| Twitter status | 754.5 us | 1.235 ms | 584.5 us | 235.9 us |

Sonic's `Valid` accepts invalid UTF-8, so that column is not equivalent to
simdjson's strict validation contract. It is reported only as implementation
context. Compiler and standard-library revisions also differ; main benchmark
tables keep these rows out of same-toolchain fastest-rival selection.
<!-- benchpublish:legacy-control:end -->

## Reproduce

```sh
GOTOOLCHAIN=go1.26.4 go test -run='^$' \
  -bench='^BenchmarkStdlibCorpusNativeSonic$' \
  -benchmem -benchtime=300ms -count=6 -cpu=1 .
```

The default `../run-comparison.sh` invokes the same contract one operation
group per fresh process. Other benchmarks in this module are exploratory and
are not part of the release publication.
