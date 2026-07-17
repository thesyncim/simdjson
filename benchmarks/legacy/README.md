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
| Canada geometry | 439.1 us | 818.9 us | 832.7 us | 189.3 us |
| CITM catalog | 1.438 ms | 3.109 ms | 971.5 us | 787.3 us |
| Go source | 3.426 ms | 6.964 ms | 4.025 ms | 1.530 ms |
| Escaped strings | 31.9 us | 33.8 us | 20.6 us | 3.3 us |
| Unicode strings | 11.9 us | 13.7 us | 20.6 us | 1.7 us |
| Synthea FHIR | 2.853 ms | 5.622 ms | 8.096 ms | 860.1 us |
| Twitter status | 758.5 us | 1.218 ms | 585.4 us | 234.1 us |

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
