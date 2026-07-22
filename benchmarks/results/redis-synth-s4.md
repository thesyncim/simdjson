# ADR 0003: RedisJSON + RediSearch scoreboard

Generated 2026-07-22 with `redisbench gen`, `run-redis.sh`, `redisbench ours`,
and `redisbench report`; see
[`redis-methodology.md`](../redisbench/redis-methodology.md). The framing is
single-core parity on scenarios RediSearch can express, with containment as a
predicate it has no operator for. Misses stay listed with causes.

## Environment

- ours: go1.27-devel_03845e30 Fri Jul 10 12:31:49 2026 -0700, arm64, single
  goroutine, native process
- RedisJSON/RediSearch: Redis 7.4.2, image
  `redis/redis-stack-server:7.4.0-v3`
- modules: ReJSON 20808, search 21015, bf 20805, timeseries 11205,
  redisgears_2 20020
- Redis is single-threaded for command execution; a single instance and
  connection provide the single-core comparison. Redis ran in a Linux Docker
  container and Store ran natively on the same Apple M4 Max host.

## Corpus

Byte accounting is minified: source bytes are the exact bytes of every
document, with no separators or pretty-printing.

| corpus | class | docs | shapes | source | avg doc |
| --- | --- | ---: | ---: | ---: | ---: |
| synth_s4 | clustered | 65,536 | 4 | 25.0 MiB | 399 B |

## Space at rest

The comparison numerator is the measured live-heap delta of the complete keyed
Store, including immutable chunks, key HAMT, snapshot metadata, and the exact
index used by the filter. The DocSet column is a representation diagnostic,
not the numerator. RedisJSON keyspace is attributable `used_memory` after load;
the RediSearch index is the subsequent delta after `FT.CREATE`.

| keyed Store + exact index | exact index modeled | DocSet shape-dedup | tape cut | RedisJSON keyspace | RediSearch index | Store/(keyspace+index) |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 62.4 MiB | 0.5 MiB | 39.2 MiB | 48% | 62.0 MiB | 17.5 MiB | 0.79x |

## Ingest and index build

Store is a fresh keyed `Put` pass followed by `CreateIndex` and full
`BackfillIndex`. Redis is `redis-cli --pipe JSON.SET` followed by `FT.CREATE`
and indexing drain. Load and index construction are timed separately.

| Store load | Redis JSON.SET load | Store/Redis | Store exact-index build | RediSearch index build |
| ---: | ---: | ---: | ---: | ---: |
| 155 MB/s | 82 MB/s | 1.89x | 2.7 ms | 1,120.2 ms |

## Scenario matrix

Speed is single-core. The ratio is Redis/Store, so greater than 1x means Store
uses less time. Point projection is warmed `Snapshot.Get` plus a compiled JSON
pointer. The filter uses the declared exact index matching RediSearch's TAG
field and still rechecks exact JSON scalar equality. Sum, group, and containment
run through `RunSnapshotInto`; the DocSet corpus projection is a schema-free
capability row.

| scenario | RedisJSON/RediSearch | Store | ratio (Redis/Store) | competitor expressiveness |
| --- | ---: | ---: | ---: | --- |
| point projection | 15.0 us | 134 ns | 112x | native (`JSON.GET`, point) |
| corpus projection | not native | 4 ns (pointer) | capability | workaround (`FT.SEARCH RETURN`, schema) |
| indexed filter | 102.0 us | 53.2 us | 1.92x | native (`FT.SEARCH TAG`, schema) |
| group-by aggregate | 47.7 ms | 2.0 ms | 24.07x | native (`FT.AGGREGATE GROUPBY`, schema) |
| containment `@>` | not expressible | 42.8 ms | capability | **not expressible** |
| scalar aggregate SUM | 47.2 ms | 988.2 us | 47.76x | native (`FT.AGGREGATE SUM`, schema) |

## Acceptance and verification

| target | measured | bound | status |
| --- | ---: | ---: | --- |
| clustered space | 0.79x | <= 1.00x | met |
| point projection | 112x | >= 1.00x | met |
| indexed filter | 1.92x | >= 1.00x | met |
| SUM | 47.76x | >= 1.00x | met |
| group-by | 24.07x | >= 1.00x | met |

| check | expected | RediSearch | status |
| --- | ---: | ---: | --- |
| index documents | 65,536 | 65,536 | ok |
| filter count | 502 | 502 | ok |
| group cardinality | 33 | 33 | ok |
| sum | 8,175,996 | 8,175,996 | ok |

## Honesty notes

- This compares an embedded library with a networked server. Redis scenario
  times come from SLOWLOG and exclude client round-trip and process-spawn cost;
  they are Redis's own command execution time. Store is in-process. The
  SLOWLOG microsecond resolution makes the point ratio a lower bound. Ingest
  and index build are wall-clocked because they are seconds-scale phases.
- RediSearch requires a predeclared schema. The head-to-head filter declares
  the same field in Store's exact index; aggregates and diagnostic DocSet
  columns need no declaration and can reach nested RFC 6901 paths.
- RedisJSON and RediSearch have no containment operator. It is a capability
  result, never treated as a speed ratio.
- RedisJSON stores a re-encoded object per key and RediSearch a separate index;
  the space comparison charges both. Store retains exact source bytes plus
  structural metadata, keys, snapshots, and its exact index.
- Store in this result has no WAL, replication, eviction, cluster protocol, or
  cross-process recovery. These measurements do not claim server equivalence.
