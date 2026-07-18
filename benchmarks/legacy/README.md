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
| Canada geometry | 421.3 us | 775.4 us | 750.9 us | 184.0 us |
| CITM catalog | 1.395 ms | 2.964 ms | 932.6 us | 751.4 us |
| Go source | 3.216 ms | 6.418 ms | 3.799 ms | 1.490 ms |
| Escaped strings | 30.2 us | 32.1 us | 19.2 us | 3.2 us |
| Unicode strings | 11.1 us | 13.0 us | 19.2 us | 1.7 us |
| Synthea FHIR | 2.630 ms | 5.186 ms | 7.645 ms | 825.3 us |
| Twitter status | 712.4 us | 1.137 ms | 557.5 us | 225.5 us |

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
