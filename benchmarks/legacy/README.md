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
Apple M4 Max, one CPU, `go1.26.4 darwin/arm64`, six approximately 300 ms samples per row, median reported:

| Corpus | Typed owned | Dynamic owned | Owned encode | Syntax-only `Valid` |
|---|---:|---:|---:|---:|
| Canada geometry | 452.8 us | 844.0 us | 803.5 us | 195.3 us |
| CITM catalog | 1.432 ms | 3.286 ms | 981.6 us | 808.5 us |
| Go source | 3.373 ms | 7.223 ms | 4.045 ms | 1.593 ms |
| Escaped strings | 32.5 us | 35.9 us | 21.0 us | 3.5 us |
| Unicode strings | 12.1 us | 14.6 us | 21.2 us | 1.8 us |
| Synthea FHIR | 2.853 ms | 5.816 ms | 8.268 ms | 875.6 us |
| Twitter status | 761.2 us | 1.296 ms | 591.2 us | 238.9 us |

Sonic's `Valid` accepts invalid UTF-8, so that column is implementation
context rather than a strict-validation comparison. Compiler and standard-
library revisions also differ; the main tables exclude these rows from
same-toolchain fastest-rival selection.
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
