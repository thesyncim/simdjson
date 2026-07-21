# Methodology: the RedisJSON + RediSearch scoreboard

This directory implements the ADR 0003 comparison: the measurement protocol
and scenario matrix for a fast in-memory JSON store answering a basic query,
against the head-to-head competitor for that shape — RedisJSON with RediSearch.
The scenarios live in `targets.go` as code; the report generator refuses to
edit a miss into a win, and misses are printed against their bound.

The framing is single-core parity on the scenarios RediSearch can express,
plus one scenario it cannot express at all. Redis executes commands on a single
thread, so a single instance over a single connection is a fair single-core
comparison, not an apples-to-oranges one. Our reads are far more expressive:
containment (`@>`) has no RedisJSON or RediSearch operator, and RediSearch can
only query fields a `FT.CREATE` schema declared up front, while our column
scans need no schema and reach any path.

## Corpora

Three classes, defined in `gen.go` with deterministic seeds:

- **clustered** — synthetic NDJSON of 16-field flat records at configurable
  shape counts (1, 4, 64) and document sizes. The shape count is the
  experimental variable the shape-level storage exploits. The categorical
  field `f02` is the filter/group key, the numeric `f01` the aggregate key.
- **heterogeneous** — every document a distinct shape: the adversarial case,
  with no shape reuse to harvest.
- **real** — the repository's corpus payloads (twitter_status, citm_catalog),
  minified. Real corpora cluster into shapes and carry the clustered bounds.

All corpora are measured minified. Each corpus directory holds `docs.ndjson`
(our side's input), `docs.resp` (the same documents as `JSON.SET` commands in
the Redis RESP protocol, mass-loadable with `redis-cli --pipe`), and the
`manifest.json`/`manifest.env` query parameters plus expected results.

## The RedisJSON/RediSearch side

`run-redis.sh` drives a pinned dockerized `redis/redis-stack-server` (RedisJSON
+ RediSearch) over a single connection. It probes `docker info` first: without
a daemon this is a protocol-only run and the report says so. Per corpus it
records, into `results/redis/<corpus>.log`, one self-describing fact per line
(the script does no ratio arithmetic; `report` parses the log):

- server version and loaded modules;
- `redis-cli --pipe` mass `JSON.SET` load wall time and reply count;
- `used_memory` before load, after load (the RedisJSON keyspace), and after
  `FT.CREATE` (the delta is the RediSearch index cost);
- the `FT.CREATE ON JSON` build wall time and `FT.INFO num_docs`;
- per-scenario **server-side** execution time and the value each command
  returned.

### Timing

Scenario times are read from the Redis **SLOWLOG**. With
`slowlog-log-slower-than 0` every command is logged with its microsecond
server-side execution time; the script runs each scenario command, then reads
the newest slowlog entry's duration back within the same connection (a trailing
`EVAL` returns only that duration, so the handshake cannot pollute the reading
and the client round trip is excluded). This is Redis's own single-core compute
cost, directly comparable to our in-process nanoseconds. `report` discards the
first repetition as warm-up and takes the minimum of the rest. Ingest and index
build, being seconds-scale, are wall-clocked instead.

### The exact RediSearch commands

The schema is mandatory — only declared fields are queryable:

```
FT.CREATE idx ON JSON PREFIX 1 doc: SCHEMA \
  $.<extract_field> AS proj TEXT \
  $.<contain_key>   AS filt TAG \
  $.<sum_field>     AS agg  NUMERIC
```

| scenario | RediSearch command |
| --- | --- |
| point projection | `JSON.GET doc:<n> $.<extract_field>` |
| filtered scan | `FT.SEARCH idx '@filt:{<contain_value>}' LIMIT 0 0` |
| scalar aggregate | `FT.AGGREGATE idx '*' GROUPBY 0 REDUCE SUM 1 @agg AS total` |
| group-by aggregate | `FT.AGGREGATE idx '*' GROUPBY 1 @filt REDUCE COUNT 0 AS c LIMIT 0 <max>` |
| containment | *no operator — not expressible* |

Grouping follows SQL semantics: documents lacking the tag form one NULL group,
exactly as RediSearch collects every missing-field document into a single
empty-tag group, so both sides count the same cardinality.

## Our side

`ours.go` ingests the identical corpora into a `DocSet` (HashKeys off and on,
and the shape-tape mode over the enriched build) and reports retained bytes at
rest. The scenarios use today's public primitives — the same the `query`
subpackage will compile a plan onto once it lands:

- projection: `DocSet.AppendPointer` / `ShapeCache.AppendField`, and a
  single-document `Doc(i)` + `PointerCompiled` probe (the `JSON.GET` analogue);
- filtered scan: a full column scan of the filter field with a scalar equality;
- scalar aggregate: `ShapeCache.AppendFieldInt64` and an int64 reduce;
- group-by: a `KeyInterner` over the group field's values;
- containment: `Node.Contains` of `{contain_key: contain_value}` against each
  document — the many-documents form the `RawContains` doc directs to, the same
  containment contract, indexing the needle once.

Timings use the same single-core, minimum-of-repetitions discipline as every
benchmark in this repository.

## Comparison rules

- Ratios are computed only between measurements taken on the same machine on the
  same corpus files, recorded in the same report run.
- Speed ratios are Redis/ours (>1x means ours is faster); the space ratio is
  ours/Redis (< 1 means ours is smaller). Both engines must reproduce the
  generator's expected counts and aggregates before any ratio is reported.
- Losses are reported with causes. The comparison is an embedded library vs a
  networked server: Redis carries a protocol and re-encoding we do not, and the
  SLOWLOG timing already removes its round trip, so the numbers are read the way
  that is fairest to Redis.

## Reproduction

```sh
cd benchmarks
go run ./redisbench/cmd/redisbench gen        # write corpora/
./redisbench/run-redis.sh corpora/*           # RedisJSON/RediSearch half (docker)
go run ./redisbench/cmd/redisbench ours        # our half
go run ./redisbench/cmd/redisbench report      # scoreboard (markdown)
```

Long-running measurement is env-gated (`REDISBENCH=1`) so ordinary test runs
stay fast; `go test ./redisbench/` validates the plumbing only.
