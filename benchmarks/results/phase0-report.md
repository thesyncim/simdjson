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
slack); modeled is source + 16 B/entry, the analytic floor. PG sizes are
after VACUUM ANALYZE.

| corpus | ours (hash) | ours (nohash) | modeled | PG table | PG gin path_ops | PG gin ops | ours/PG(table+path) |
|---|---:|---:|---:|---:|---:|---:|---:|
| citm_perf | 400.5 MiB | 400.5 MiB | 394.8 MiB | 43.3 MiB | 4.9 MiB | 5.4 MiB | 8.31x |
| synth_hetero | 939.7 MiB | 939.7 MiB | 886.3 MiB | 520.9 MiB | 552.0 MiB | 1.46 GiB | 0.88x |
| synth_s1 | 938.7 MiB | 938.7 MiB | 885.0 MiB | 520.4 MiB | 467.7 MiB | 579.0 MiB | 0.95x |
| synth_s4 | 938.7 MiB | 938.7 MiB | 885.0 MiB | 519.9 MiB | 492.3 MiB | 582.6 MiB | 0.93x |
| synth_s64 | 938.7 MiB | 938.7 MiB | 885.0 MiB | 519.9 MiB | 520.5 MiB | 601.5 MiB | 0.90x |
| twitter_tweets | 250.5 MiB | 250.5 MiB | 247.6 MiB | 72.4 MiB | 6.9 MiB | 6.6 MiB | 3.16x |
| twitter_whole | 301.0 MiB | 301.0 MiB | 248.0 MiB | 30.8 MiB | 1.5 MiB | 1.4 MiB | 9.32x |

## Ingest

Single core both sides. Ours: ReadFrom (validate + index + arena copy;
no key postings yet). PG: COPY, then each CREATE INDEX separately.

| corpus | ours (hash) | ours (nohash) | PG COPY | + gin path_ops build | gin ops build (fu=on) | (fu=off) | ours/(COPY+path) |
|---|---:|---:|---:|---:|---:|---:|---:|
| citm_perf | 1287 MB/s | 1544 MB/s | 1914.4 ms | 477.5 ms | 1467.2 ms | 1455.9 ms | 22.93x |
| synth_hetero | 1400 MB/s | 1714 MB/s | 3402.5 ms | 38.0 s | 55.8 s | 57.3 s | 144x |
| synth_s1 | 1657 MB/s | 1960 MB/s | 3124.9 ms | 20.8 s | 37.4 s | 38.3 s | 99.23x |
| synth_s4 | 1668 MB/s | 1948 MB/s | 2850.6 ms | 23.2 s | 34.0 s | 37.7 s | 109x |
| synth_s64 | 1626 MB/s | 1895 MB/s | 2916.0 ms | 28.3 s | 35.9 s | 38.3 s | 127x |
| twitter_tweets | 1855 MB/s | 2239 MB/s | 1792.5 ms | 660.9 ms | 1590.2 ms | 1568.8 ms | 33.91x |
| twitter_whole | 499 MB/s | 510 MB/s | 1668.5 ms | 442.3 ms | 1212.8 ms | 1245.0 ms | 7.83x |

## Point extraction

Full scan of one top-level field, per document. PG: `SELECT
count(doc->>'f') FROM t` over N rows. Ours: AppendPointer and, on
clustered corpora, the ShapeCache column path. Single row: PG by ctid vs
ours Doc(i)+PointerCompiled.

| corpus | ours pointer | ours column | PG seq per row | PG/ours | PG ctid row | ours single doc |
|---|---:|---:|---:|---:|---:|---:|
| citm_perf | 91 ns | 76 ns | 567 ns | 7.50x | 0.1 ms | 37 ns |
| synth_hetero | 11 ns | n/a | 81 ns | 7.73x | 0.1 ms | 18 ns |
| synth_s1 | 32 ns | 29 ns | 98 ns | 3.39x | 0.1 ms | 20 ns |
| synth_s4 | 29 ns | 29 ns | 90 ns | 3.13x | 0.1 ms | 20 ns |
| synth_s64 | 11 ns | 12 ns | 89 ns | 8.04x | 0.1 ms | 17 ns |
| twitter_tweets | 135 ns | 138 ns | 5940 ns | 44.02x | 0.1 ms | 57 ns |
| twitter_whole | 14 ns | 13 ns | 208958 ns | 15696x | 0.3 ms | 13 ns |

## Existence and containment

Whole-corpus counts. Ours is a full column scan (the pre-phase-4
baseline: no postings, no pruning). PG existence `doc ? 'k'` can use gin
jsonb_ops; containment `doc @> '{"k":"v"}'` can use either gin index.

| corpus | ours exist | PG exist seq | PG exist gin | PG/ours | ours contain | PG contain seq | PG gin ops | PG gin path | PG/ours |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| citm_perf | 5.0 ms | 41.3 ms | 43.3 ms | 8.33x | 6.2 ms | 42.9 ms | 1.8 ms | 45.9 ms | 0.28x |
| synth_hetero | 11.1 ms | 69.8 ms | 0.1 ms | 0.01x | 11.0 ms | 95.4 ms | 0.1 ms | 0.1 ms | 0.01x |
| synth_s1 | 51.7 ms | 111.0 ms | 112.8 ms | 2.15x | 38.2 ms | 114.3 ms | 23.7 ms | 20.1 ms | 0.53x |
| synth_s4 | 33.6 ms | 102.9 ms | 115.2 ms | 3.06x | 32.8 ms | 111.7 ms | 10.1 ms | 5.5 ms | 0.17x |
| synth_s64 | 13.5 ms | 84.9 ms | 12.6 ms | 0.93x | 13.4 ms | 97.3 ms | 1.2 ms | 0.4 ms | 0.03x |
| twitter_tweets | 3.5 ms | 173.5 ms | 28.6 ms | 8.23x | 3.9 ms | 172.2 ms | 1.8 ms | 171.7 ms | 0.45x |
| twitter_whole | 0.0 ms | 60.8 ms | 60.4 ms | 15249x | n/a | 60.4 ms | 1.6 ms | 63.5 ms | n/a |

## Acceptance table

| target | corpus | measured | bound | status |
|---|---|---:|---:|---|
| space-clustered | synth_s1 | 0.95x | <= 0.60x | missed (phase 1 pending) |
| space-clustered | synth_s4 | 0.93x | <= 0.60x | missed (phase 1 pending) |
| space-clustered | synth_s64 | 0.90x | <= 0.60x | missed (phase 1 pending) |
| space-real | citm_perf | 8.31x | <= 0.60x | missed (phase 1 pending) |
| space-real | twitter_tweets | 3.16x | <= 0.60x | missed (phase 1 pending) |
| space-real | twitter_whole | 9.32x | <= 0.60x | missed (phase 1 pending) |
| space-heterogeneous | synth_hetero | 0.88x | <= 1.00x | met |
| space-stretch-columnar | synth_s1 | 0.95x | <= 0.40x | missed (phase 3 pending) |
| space-stretch-columnar | synth_s4 | 0.93x | <= 0.40x | missed (phase 3 pending) |
| space-stretch-columnar | synth_s64 | 0.90x | <= 0.40x | missed (phase 3 pending) |
| ingest | citm_perf | 22.93x | >= 10.00x | met |
| ingest | synth_hetero | 144x | >= 10.00x | met |
| ingest | synth_s1 | 99.23x | >= 10.00x | met |
| ingest | synth_s4 | 109x | >= 10.00x | met |
| ingest | synth_s64 | 127x | >= 10.00x | met |
| ingest | twitter_tweets | 33.91x | >= 10.00x | met |
| ingest | twitter_whole | 7.83x | >= 10.00x | missed |
| extract | citm_perf | 7.50x | >= 50.00x | missed |
| extract | synth_hetero | 7.73x | >= 50.00x | missed |
| extract | synth_s1 | 3.39x | >= 50.00x | missed |
| extract | synth_s4 | 3.13x | >= 50.00x | missed |
| extract | synth_s64 | 8.04x | >= 50.00x | missed |
| extract | twitter_tweets | 44.02x | >= 50.00x | missed |
| extract | twitter_whole | 15696x | >= 50.00x | met |
| exist | citm_perf | 8.33x | >= 10.00x | missed (phase 4 pending) |
| exist | synth_hetero | 0.01x | >= 10.00x | missed (phase 4 pending) |
| exist | synth_s1 | 2.15x | >= 10.00x | missed (phase 4 pending) |
| exist | synth_s4 | 3.06x | >= 10.00x | missed (phase 4 pending) |
| exist | synth_s64 | 0.93x | >= 10.00x | missed (phase 4 pending) |
| exist | twitter_tweets | 8.23x | >= 10.00x | missed (phase 4 pending) |
| exist | twitter_whole | 15249x | >= 10.00x | met |
| contain | citm_perf | 0.28x | >= 5.00x | missed (phase 4 pending) |
| contain | synth_hetero | 0.01x | >= 5.00x | missed (phase 4 pending) |
| contain | synth_s1 | 0.53x | >= 5.00x | missed (phase 4 pending) |
| contain | synth_s4 | 0.17x | >= 5.00x | missed (phase 4 pending) |
| contain | synth_s64 | 0.03x | >= 5.00x | missed (phase 4 pending) |
| contain | twitter_tweets | 0.45x | >= 5.00x | missed (phase 4 pending) |
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
- Space today is the classic tape: 16 B/entry on top of the source, so
  the ADR predicts we lose the space rows at phase 0. That starting
  point is the point of this report.
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
