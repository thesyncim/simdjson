# Phase 0 methodology: the PostgreSQL comparison baseline

This directory implements ADR 0002 phase 0: the measurement protocol and
acceptance table for the document-store building blocks, committed before any
optimization so later phases cannot move the goalposts. The acceptance rows
live in `targets.go`; the report generator refuses to edit a miss into a win —
misses are printed against their bound, with the pending phase noted.

## Corpora

Three classes, defined in `gen.go` with deterministic seeds:

- **real** — the repository's corpus payloads (twitter_status, citm_catalog),
  minified. Real corpora cluster into shapes and therefore carry the
  clustered space bound.
- **clustered** — synthetic NDJSON of 16-field flat records at configurable
  shape counts (1, 4, 64) and document sizes (~200 B–1 KiB). The shape count
  is the experimental variable the shape-level design exploits.
- **heterogeneous** — every document a distinct shape: the adversarial case,
  with no shape reuse to harvest.

All corpora are measured minified, because JSONB stores a re-encoded form of
the minified content; comparing against pretty-printed input would flatter
our side (we keep source bytes) and is disallowed.

## The PostgreSQL side

`run-pg.sh` drives a pinned dockerized server (the major version is the pin;
the exact tag lands in every log) over a single connection with parallelism
disabled — the single-core comparison rule. It records raw `psql` logs: COPY
wall time, table and index sizes after VACUUM ANALYZE, CREATE INDEX wall
times for gin `jsonb_ops` (fastupdate on and off) and gin `jsonb_path_ops`,
and timed extraction/existence/containment queries with and without each
index, plus EXPLAIN (ANALYZE, BUFFERS) for every query form. The logs are the
artifact; `report` parses them, discarding the first repetition as warm-up
and taking the minimum of the rest.

## Our side

`ours.go` ingests the identical corpora into a `DocSet` (HashKeys off and on)
and reports retained bytes from the arena and structure sizes — source
arenas, entry arenas, and, as later phases land, shapes, interner, and
postings. Space is retained bytes at rest, not allocation traffic. Timings
use the same single-core discipline as every benchmark in this repository.

## Comparison rules

- Ratios are computed only between measurements taken on the same machine on
  the same corpus files, recorded in the same report run.
- PostgreSQL gets its best applicable plan per row: existence and containment
  compare against the fastest of sequential scan and each GIN variant.
- Ours-side rows that depend on unlanded phases are measured with today's
  machinery and reported against the target anyway, marked with the pending
  phase — the starting point is part of the record.
- Losses are reported with causes. A full DBMS carries costs (WAL, tuple
  headers, buffer management) that an embedded library does not; the targets
  are set high precisely so the comparison stays meaningful despite that
  asymmetry, and the consuming engine must keep its own margin.

## Reproduction

```sh
cd benchmarks
go run ./pgbaseline/cmd/pgbaseline gen        # write corpora/
./pgbaseline/run-pg.sh corpora/*              # PostgreSQL half (docker)
go run ./pgbaseline/cmd/pgbaseline ours       # our half
go run ./pgbaseline/cmd/pgbaseline report     # acceptance report (markdown)
```

Long-running measurement is env-gated (`PGBASELINE=1`) so ordinary test runs
stay fast; `go test ./pgbaseline/` validates the plumbing only.
