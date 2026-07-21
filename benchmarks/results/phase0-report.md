# ADR 0002 phase 0: PostgreSQL comparison baseline

Regenerate with `pgbaseline gen`, `run-pg.sh`, `pgbaseline ours`, and
`pgbaseline report`; see METHODOLOGY.md. Targets come from ADR 0002 and
bind the design, not this report: misses stay listed with causes.

## Environment

- ours: go1.27-devel_03845e30f7 Fri Jul 10 12:31:49 2026 -0700 X:simd, arm64, single goroutine, native process
- PostgreSQL: PostgreSQL 18.4 on aarch64-unknown-linux-musl, compiled by gcc (Alpine 15.2.0) 15.2.0, 64-bit
- PG setting: autovacuum = off
- PG setting: maintenance_work_mem = 1GB
- PG setting: max_parallel_maintenance_workers = 0
- PG setting: max_parallel_workers_per_gather = 0
- PG setting: max_wal_size = 4GB
- PG setting: shared_buffers = 1GB
- PG runs in a Linux container (Docker); ours runs natively on the host. Same hardware, different kernels.

## Corpora

Byte accounting is minified: source bytes are the exact bytes of every
document, no separators, pretty-printing removed.

| corpus | class | docs | shapes | source | avg doc |
|---|---|---:|---:|---:|---:|
| citm_perf | real | 72123 | 0 | 128.0 MiB | 1860 B |
| synth_hetero | heterogeneous | 1000000 | 1000000 | 382.7 MiB | 401 B |
| synth_s1 | clustered | 1000000 | 1 | 381.5 MiB | 400 B |
| synth_s4 | clustered | 1000000 | 4 | 381.5 MiB | 399 B |
| synth_s64 | clustered | 1000000 | 64 | 381.5 MiB | 399 B |
| twitter_tweets | real | 28774 | 0 | 128.0 MiB | 4664 B |
| twitter_whole | real | 288 | 0 | 128.2 MiB | 466906 B |

## Space at rest

Ours is the measured live-heap delta of the DocSet (arenas, headers,
slack); modeled is source + 16 B/entry (+16 B/header per shape-taped
document), the analytic floor. The dedup columns are the phase-1
shape-tape mode: conforming documents store value entries only, keys
deduplicated into the shape cache; tape cut is the entry storage it
dropped against the classic tape. PG sizes are after VACUUM ANALYZE. The
ratio judges the dedup variant when measured, classic otherwise.

| corpus | ours (hash) | ours (dedup) | tape cut | dedup docs | modeled | modeled dedup | PG table | PG gin path_ops | ours/PG(table+path) |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| citm_perf | 400.5 MiB | 400.5 MiB | 0% | 0% | 394.8 MiB | 394.8 MiB | 43.3 MiB | 4.9 MiB | 8.31x |
| synth_hetero | 939.7 MiB | 955.8 MiB | 0% | 0% | 886.3 MiB | 886.3 MiB | 520.9 MiB | 552.0 MiB | 0.89x |
| synth_s1 | 938.7 MiB | 606.2 MiB | 48% | 99% | 885.0 MiB | 518.8 MiB | 520.4 MiB | 467.7 MiB | 0.61x |
| synth_s4 | 938.7 MiB | 606.2 MiB | 48% | 99% | 885.0 MiB | 518.8 MiB | 519.9 MiB | 492.3 MiB | 0.60x |
| synth_s64 | 938.7 MiB | 606.3 MiB | 48% | 99% | 885.0 MiB | 518.8 MiB | 519.9 MiB | 520.5 MiB | 0.58x |
| twitter_tweets | 250.5 MiB | 250.5 MiB | 0% | 0% | 247.6 MiB | 247.6 MiB | 72.4 MiB | 6.9 MiB | 3.16x |
| twitter_whole | 301.0 MiB | 301.0 MiB | 0% | 0% | 248.0 MiB | 248.0 MiB | 30.8 MiB | 1.5 MiB | 9.32x |

## Ingest

Single core both sides. Ours: ReadFrom (validate + index + arena copy;
no key postings yet); the dedup column adds the shape-tape conformance
proof and value compaction. PG: COPY, then each CREATE INDEX separately.

| corpus | ours (hash) | ours (dedup) | ours (nohash) | PG COPY | + gin path_ops build | gin ops build (fu=on) | (fu=off) | ours/(COPY+path) |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| citm_perf | 1207 MB/s | 1203 MB/s | 1252 MB/s | 1914.4 ms | 477.5 ms | 1467.2 ms | 1455.9 ms | 21.45x |
| synth_hetero | 1311 MB/s | 997 MB/s | 1503 MB/s | 3402.5 ms | 38.0 s | 55.8 s | 57.3 s | 103x |
| synth_s1 | 1519 MB/s | 1073 MB/s | 1803 MB/s | 3124.9 ms | 20.8 s | 37.4 s | 38.3 s | 64.22x |
| synth_s4 | 1527 MB/s | 1046 MB/s | 1729 MB/s | 2850.6 ms | 23.2 s | 34.0 s | 37.7 s | 68.18x |
| synth_s64 | 1510 MB/s | 1091 MB/s | 1723 MB/s | 2916.0 ms | 28.3 s | 35.9 s | 38.3 s | 85.01x |
| twitter_tweets | 1797 MB/s | 1744 MB/s | 2165 MB/s | 1792.5 ms | 660.9 ms | 1590.2 ms | 1568.8 ms | 31.87x |
| twitter_whole | 468 MB/s | 478 MB/s | 464 MB/s | 1668.5 ms | 442.3 ms | 1212.8 ms | 1245.0 ms | 7.51x |

## Point extraction

Full scan of one top-level field, per document. PG: `SELECT
count(doc->>'f') FROM t` over N rows. Ours: AppendPointer and, on
clustered corpora, the ShapeCache column path. Single row: PG by ctid vs
ours Doc(i)+PointerCompiled.

| corpus | ours pointer | ours column | dedup pointer | dedup column | PG seq per row | PG/ours | PG ctid row | ours single doc |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| citm_perf | 130 ns | 113 ns | 129 ns | 100 ns | 567 ns | 5.66x | 0.1 ms | 33 ns |
| synth_hetero | 11 ns | n/a | 11 ns | n/a | 81 ns | 7.22x | 0.1 ms | 19 ns |
| synth_s1 | 37 ns | 29 ns | 8 ns | 8 ns | 98 ns | 12.22x | 0.1 ms | 37 ns |
| synth_s4 | 30 ns | 31 ns | 8 ns | 8 ns | 90 ns | 11.92x | 0.1 ms | 34 ns |
| synth_s64 | 13 ns | 13 ns | 9 ns | 9 ns | 89 ns | 10.07x | 0.1 ms | 33 ns |
| twitter_tweets | 205 ns | 182 ns | 204 ns | 179 ns | 5940 ns | 33.18x | 0.1 ms | 53 ns |
| twitter_whole | 14 ns | 12 ns | 16 ns | 14 ns | 208958 ns | 14889x | 0.3 ms | 16 ns |

## Existence and containment

Whole-corpus counts. Ours is a full column scan (the pre-phase-4
baseline: no postings, no pruning). PG existence `doc ? 'k'` can use gin
jsonb_ops; containment `doc @> '{"k":"v"}'` can use either gin index.

| corpus | ours exist | PG exist seq | PG exist gin | PG/ours | ours contain | PG contain seq | PG gin ops | PG gin path | PG/ours |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| citm_perf | 8.6 ms | 41.3 ms | 43.3 ms | 4.80x | 10.0 ms | 42.9 ms | 1.8 ms | 45.9 ms | 0.18x |
| synth_hetero | 11.6 ms | 69.8 ms | 0.1 ms | 0.01x | 11.5 ms | 95.4 ms | 0.1 ms | 0.1 ms | 0.01x |
| synth_s1 | 13.5 ms | 111.0 ms | 112.8 ms | 8.23x | 18.0 ms | 114.3 ms | 23.7 ms | 20.1 ms | 1.12x |
| synth_s4 | 10.1 ms | 102.9 ms | 115.2 ms | 10.18x | 10.1 ms | 111.7 ms | 10.1 ms | 5.5 ms | 0.54x |
| synth_s64 | 10.6 ms | 84.9 ms | 12.6 ms | 1.20x | 9.9 ms | 97.3 ms | 1.2 ms | 0.4 ms | 0.04x |
| twitter_tweets | 4.4 ms | 173.5 ms | 28.6 ms | 6.55x | 4.9 ms | 172.2 ms | 1.8 ms | 171.7 ms | 0.36x |
| twitter_whole | 0.0 ms | 60.8 ms | 60.4 ms | 13539x | n/a | 60.4 ms | 1.6 ms | 63.5 ms | n/a |

## Acceptance table

| target | corpus | measured | bound | status |
|---|---|---:|---:|---|
| space-clustered | synth_s1 | 0.61x | <= 0.60x | missed (phase 1 pending) |
| space-clustered | synth_s4 | 0.60x | <= 0.60x | met |
| space-clustered | synth_s64 | 0.58x | <= 0.60x | met |
| space-real | citm_perf | 8.31x | <= 0.60x | missed (phase 1 pending) |
| space-real | twitter_tweets | 3.16x | <= 0.60x | missed (phase 1 pending) |
| space-real | twitter_whole | 9.32x | <= 0.60x | missed (phase 1 pending) |
| space-heterogeneous | synth_hetero | 0.89x | <= 1.00x | met |
| space-stretch-columnar | synth_s1 | 0.61x | <= 0.40x | missed (phase 3 pending) |
| space-stretch-columnar | synth_s4 | 0.60x | <= 0.40x | missed (phase 3 pending) |
| space-stretch-columnar | synth_s64 | 0.58x | <= 0.40x | missed (phase 3 pending) |
| ingest | citm_perf | 21.45x | >= 10.00x | met |
| ingest | synth_hetero | 103x | >= 10.00x | met |
| ingest | synth_s1 | 64.22x | >= 10.00x | met |
| ingest | synth_s4 | 68.18x | >= 10.00x | met |
| ingest | synth_s64 | 85.01x | >= 10.00x | met |
| ingest | twitter_tweets | 31.87x | >= 10.00x | met |
| ingest | twitter_whole | 7.51x | >= 10.00x | missed |
| extract | citm_perf | 5.66x | >= 50.00x | missed |
| extract | synth_hetero | 7.22x | >= 50.00x | missed |
| extract | synth_s1 | 12.22x | >= 50.00x | missed |
| extract | synth_s4 | 11.92x | >= 50.00x | missed |
| extract | synth_s64 | 10.07x | >= 50.00x | missed |
| extract | twitter_tweets | 33.18x | >= 50.00x | missed |
| extract | twitter_whole | 14889x | >= 50.00x | met |
| exist | citm_perf | 4.80x | >= 10.00x | missed (phase 4 pending) |
| exist | synth_hetero | 0.01x | >= 10.00x | missed (phase 4 pending) |
| exist | synth_s1 | 8.23x | >= 10.00x | missed (phase 4 pending) |
| exist | synth_s4 | 10.18x | >= 10.00x | met |
| exist | synth_s64 | 1.20x | >= 10.00x | missed (phase 4 pending) |
| exist | twitter_tweets | 6.55x | >= 10.00x | missed (phase 4 pending) |
| exist | twitter_whole | 13539x | >= 10.00x | met |
| contain | citm_perf | 0.18x | >= 5.00x | missed (phase 4 pending) |
| contain | synth_hetero | 0.01x | >= 5.00x | missed (phase 4 pending) |
| contain | synth_s1 | 1.12x | >= 5.00x | missed (phase 4 pending) |
| contain | synth_s4 | 0.54x | >= 5.00x | missed (phase 4 pending) |
| contain | synth_s64 | 0.04x | >= 5.00x | missed (phase 4 pending) |
| contain | twitter_tweets | 0.36x | >= 5.00x | missed (phase 4 pending) |
| contain | twitter_whole | n/a | >= 5.00x | n/a |

Target notes:

- **space-clustered**: documents + index structures vs PG table + gin jsonb_path_ops; assumes shape-deduplicated tapes (phase 1) and dual-width tapes (phase 2).
- **space-real**: real corpora cluster into shapes and carry the clustered bound.
- **space-heterogeneous**: adversarial: every document a distinct shape, no shape reuse to harvest.
- **space-stretch-columnar**: stretch goal: cold columnar mode (phase 3), keys stored only in shapes.
- **ingest**: single-core ReadFrom vs COPY + gin jsonb_path_ops build; our side builds no key postings yet (phase 4 adds them).
- **extract**: per-document ->>-equivalent, full scan on both sides.
- **exist**: ours is a full column scan until the inverted layer (phase 4); PG may use gin jsonb_ops.
- **contain**: ours is a column scan with scalar equality until RawContains + pruning (phase 4); PG may use either gin index.

## Verification

Both engines must agree with the generator's expected counts before any
ratio is meaningful. Ours-side counts were verified during measurement;
this table cross-checks PostgreSQL's query results.

| corpus | check | expected | PG | status |
|---|---|---:|---:|---|
| citm_perf | rowcount | 72123 | 72123 | ok |
| citm_perf | extract hits | 72123 | 72123 | ok |
| citm_perf | exist count | 72123 | 72123 | ok |
| citm_perf | contain count | 72123 | 72123 | ok |
| synth_hetero | rowcount | 1000000 | 1000000 | ok |
| synth_hetero | extract hits | 1 | 1 | ok |
| synth_hetero | exist count | 1 | 1 | ok |
| synth_hetero | contain count | 0 | 0 | ok |
| synth_s1 | rowcount | 1000000 | 1000000 | ok |
| synth_s1 | extract hits | 1000000 | 1000000 | ok |
| synth_s1 | exist count | 1000000 | 1000000 | ok |
| synth_s1 | contain count | 31551 | 31551 | ok |
| synth_s4 | rowcount | 1000000 | 1000000 | ok |
| synth_s4 | extract hits | 250000 | 250000 | ok |
| synth_s4 | exist count | 250000 | 250000 | ok |
| synth_s4 | contain count | 7766 | 7766 | ok |
| synth_s64 | rowcount | 1000000 | 1000000 | ok |
| synth_s64 | extract hits | 15625 | 15625 | ok |
| synth_s64 | exist count | 15625 | 15625 | ok |
| synth_s64 | contain count | 545 | 545 | ok |
| twitter_tweets | rowcount | 28774 | 28774 | ok |
| twitter_tweets | extract hits | 28774 | 28774 | ok |
| twitter_tweets | exist count | 4314 | 4314 | ok |
| twitter_tweets | contain count | 27624 | 27624 | ok |
| twitter_whole | rowcount | 288 | 288 | ok |
| twitter_whole | extract hits | 288 | 288 | ok |
| twitter_whole | exist count | 288 | 288 | ok |

## Honesty notes

- The comparison is library vs full DBMS: PostgreSQL pays for tuple
  headers, buffer management, WAL, and a client protocol that we do not.
  The consuming engine must spend our margin on those and still win.
- Our ingest builds the structural tape (and optional key hashes) but no
  key postings; PostgreSQL's CREATE INDEX builds a queryable GIN. The
  existence/containment rows show what each side bought: PG existence
  with gin jsonb_ops beats our full scan wherever selectivity is low —
  until phase 4 lands postings, that loss is structural.
- The dedup variant is phase 1's shape-tape mode. Its space and query
  rows apply only where documents conform (flat object roots matching a
  recurring shape): the clustered synthetics dedup nearly everything,
  while nested real corpora (tweets, CITM performances) stay classic and
  keep phase 0's numbers — their space losses are TOAST compression
  territory, owned by phase 3, not by representation. The single-doc
  probe on a dedup variant reports the widening contract's steady state:
  its first probe per document materializes the classic tape.
- citm_perf: PG's table (43.3 MiB) is smaller than the minified source (128.0 MiB) —
  TOAST compresses large documents; we store exact source bytes.
- twitter_tweets: PG's table (72.4 MiB) is smaller than the minified source (128.0 MiB) —
  TOAST compresses large documents; we store exact source bytes.
- twitter_whole: PG's table (30.8 MiB) is smaller than the minified source (128.2 MiB) —
  TOAST compresses large documents; we store exact source bytes.
- PostgreSQL runs single-backend with parallelism disabled
  (max_parallel_workers_per_gather=0, max_parallel_maintenance_workers=0)
  per the single-core comparison rule; the planner is otherwise free, and
  the EXPLAIN captures in the session logs show which plan actually ran.
