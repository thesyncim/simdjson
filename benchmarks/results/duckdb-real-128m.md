# Real-derived Store and DuckDB comparison

This is a correctness-gated mechanism run over the repository-pinned Twitter
and CITM corpora. Natural records are minified and cycled in source order to
128 MiB per corpus; “real-derived” does not mean every replicated row is
distinct. Both engines consumed the same NDJSON and logical `doc:N` keys.

- FileStore: Go 1.27 development snapshot `03845e30`, darwin/arm64, Apple M4
  Max, macOS 26.5, one query worker, three repetitions. The FileStore side was
  rerun after the categorical group catalog landed; DuckDB values below remain
  the pinned raw run named here.
- DuckDB: v1.5.4, pinned official linux/arm64 image, one CPU/thread, three
  profiled repetitions after two warmups.
- DuckDB image:
  `duckdb/duckdb:1.5.4@sha256:d5e66353428256453574ddfd4ee446ef37510e61619bb5a8f63b988165bd70b8`.
- Run date: 2026-07-23.

## Corpus and correctness

| corpus | rows | exact JSON | key bytes | source pretty bytes | NDJSON SHA-256 | Store / FileStore / DuckDB |
|---|---:|---:|---:|---:|---|---|
| `citm_perf` | 72,123 | 134,220,202 | 637,997 | 1,727,204 | `caf0cc98f054cec124eac6329b86d3073657976663052988eadc817c3db330aa` | verified |
| `twitter_tweets` | 28,774 | 134,221,139 | 247,856 | 631,514 | `198f610f7f86f161c100ce9310ec47cabcd8848165a8df6e24c2743732f82de3` | verified |

The verification gate checks row count, projected-field count, exact filter
and structural-containment counts, group cardinality, numeric sum when
defined, and cardinality after 256 durable deletes.

## Durable storage and bounded memory

Both file columns include exact JSON, logical keys, and the declared key and
filter indexes. FileStore also includes its configured numeric cover; DuckDB
materializes the comparable filter and metric columns and is checkpointed.

| corpus | FileStore | FileStore / payload | DuckDB | DuckDB / payload | FileStore / DuckDB |
|---|---:|---:|---:|---:|---:|
| `citm_perf` | 41,275,392 B | 0.31x | 33,304,576 B | 0.25x | 1.24x |
| `twitter_tweets` | 213,884,928 B | 1.59x | 144,191,488 B | 1.07x | 1.48x |

FileStore’s conservative accounted warm state was 16.09 MiB for CITM and
15.99 MiB for Twitter: settled Go heap, admitted pages from an 8 MiB cache,
the complete 8 MiB commit arena, and the fixed reusable-extent arena. DuckDB
reported 29.75 MiB and 137.25 MiB of current engine-managed buffers,
respectively. These are engine-accounting views, not identical process RSS
domains.

After the same 256 updates and 256 deletes, FileStore high-water was 43.00 MiB
for CITM and 207.18 MiB for Twitter, with 3.67 MiB and 4.99 MiB already reusable.
DuckDB high-water was 61.01 MiB and 273.76 MiB with a zero-byte WAL.

The compact categorical cover costs one 4 KiB page in each file. CITM contains
one `venueCode` group and Twitter two `lang` groups; the page stores only
value/count/first-row records and a covered-index bitmap, not one entry per
input row.

## Recovered durable reads

Ratios are DuckDB latency divided by FileStore latency; greater than one means
FileStore completed sooner.

| corpus | operation | FileStore | DuckDB | ratio | FileStore lane |
|---|---|---:|---:|---:|---|
| `citm_perf` | nested point | 8.709 us | 114.250 us | 13.12x | exact JSON + compiled pointer |
| `citm_perf` | filter | 954.708 us | 3.606 ms | 3.78x | exact certificate, 0 JSON rows |
| `citm_perf` | containment | 993.459 us | 83.332 ms | 83.88x | exact certificate, 0 JSON rows |
| `citm_perf` | group | 542 ns | 445.083 us | 821.19x | one-group categorical catalog, 0 posting/JSON pages |
| `twitter_tweets` | nested point | 9.709 us | 143.292 us | 14.76x | exact JSON + compiled pointer |
| `twitter_tweets` | filter | 521.000 us | 1.415 ms | 2.72x | exact certificate, 0 JSON rows |
| `twitter_tweets` | covered SUM | 51.125 us | 151.833 us | 2.97x | dense typed stripe, 0 JSON rows |
| `twitter_tweets` | containment | 518.000 us | 47.266 ms | 91.25x | exact certificate, 0 JSON rows |
| `twitter_tweets` | group | 792 ns | 275.000 us | 347.22x | two-group categorical catalog, 0 posting/JSON pages |

The result is deliberately not summarized into one score. Point, certified
filter/containment, covered SUM, and clean categorical grouping lead on these
inputs. The group values are especially low-cardinality and therefore approach
fixed execution overhead; the retained 100K-row/32-group microbenchmark is
8.88-9.09 us. High-cardinality, compound, container-valued, or mutated indexes
retain the certified-posting/residual-JSON or general scan lane.

## Durable mutations

Every operation is one independently double-fenced FileStore publication or
one explicit DuckDB transaction. FileStore update/delete averaged
10.61/10.44 ms on CITM and 9.73/9.80 ms on Twitter. DuckDB averaged
0.93/0.60 ms and 1.18/0.74 ms. The transaction boundaries match; the
darwin-versus-Linux-container filesystems and durability stacks do not, so
these numbers diagnose mechanisms rather than provide a device-neutral ratio.

Reproduce this lane with the commands in
[`duckdb-methodology.md`](../duckdbbench/duckdb-methodology.md).
