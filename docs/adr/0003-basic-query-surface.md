# ADR 0003: a basic query surface — competing with RedisJSON

Status: accepted (planning). Builds on ADR 0002 (the document-store substrate).

## Motivation

ADR 0002 delivers building blocks: index, dedup tapes, containment, columnar
extraction. Building blocks are not a product, and a full query engine is a
separate repository's job. The useful middle is a fast in-memory JSON store
that answers a **basic SQL query** — and the head-to-head competitor for that
is RedisJSON with RediSearch: single-threaded, in-memory, JSONPath plus
`FT.AGGREGATE`. Single-threaded is the key: our single-core target is a fair,
winnable fight, not an apples-to-oranges one.

Every execution primitive this needs already exists and is measured:

- projection and typed aggregation -> `ShapeCache.AppendField(s)` and the
  typed columns (`AppendFieldInt64/Float64/Bool`), 8-12 ns/document;
- scalar predicates -> shape-compiled extraction plus a typed compare;
- containment predicates -> `Node.Contains` / `RawContains` (RedisJSON has no
  containment operator; this is a capability edge, not just a speed one);
- selective predicates -> the phase-4 postings (ADR 0002 Gap B), which this
  ADR promotes from "eventually" to "needed next";
- grouping -> the `KeyInterner` for group keys.

A query compiles once and runs over the corpus at primitive speed; the parse
and plan cost amortize to nothing across a million rows.

## Scope — deliberately basic

The table is one `DocSet`; each document is a row; columns are JSON paths.

Supported: `SELECT` of path projections and aggregates (`COUNT`, `SUM`, `AVG`,
`MIN`, `MAX`); `WHERE` with conjunctions and disjunctions of comparisons
(`= != < <= > >=`), containment (`@>`), and `IS NULL` / `EXISTS`; `GROUP BY`
with aggregates; `ORDER BY`; `LIMIT`.

Explicitly out of scope for "basic": joins, subqueries, window functions,
mutation/DDL, transactions, and full SQL-dialect coverage. Those belong to the
future engine. This revises ADR 0002's boundary only to admit a thin, single-
table, read-only query surface — mechanism plus the minimum policy to be a
product; everything above stays out.

## Execution model

The query compiles to a small plan IR (a programmatic builder is the IR; the
SQL text parser produces it). The executor is column-oriented:

- projections and aggregates read typed/raw columns directly off the tape;
- a `WHERE` predicate with no useful posting bound is a dense per-row scan;
  when the posting result covers at most half the corpus, selection is pushed
  below materialization and `AppendFieldRows` / `AppendPointerRows` gather only
  candidate cells from compact tapes before the exact predicate recheck;
- `GROUP BY` interns each group key and accumulates per group;
- results are columnar, streamed or materialized.

The sparse path is O(candidates), preserves posting order and multiplicity,
and never widens a shape-deduplicated tape. The dense path remains one pass
over the arenas; the conservative half-corpus crossover avoids paying random
gather when a streaming scan is cheaper.

## API

A `query` subpackage over the core: `query.Compile(sql string) (*Query,
error)` and `(*Query).Run(*DocSet) (Result, error)`, plus a builder that
constructs the same plan without SQL text for callers that prefer it. The core
library gains no query dependency; the subpackage depends on the core.

## Competitive target and acceptance

RedisJSON + RediSearch, pinned images via docker (the redisbench harness
under benchmarks/), single connection, single shard — the single-core rule
both sides.
Scenarios on shared corpora: path projection, filtered scan, scalar
aggregation, and group-by aggregation, against `JSON.GET` / `FT.SEARCH` /
`FT.AGGREGATE`. Acceptance: at or above parity on every scenario single-core,
with the containment predicate as a capability RedisJSON lacks; space per ADR
0002. Reproducible harness, misses reported with causes — the same honesty
rules as the gjson/sonic scoreboard.

## Reprioritization of ADR 0002

- **Postings (Gap B) move to next**: selective `WHERE` needs them; the
  `RawContains` verifier they prune for has landed.
- **Value dictionary (Gap A) continues**: it is the store's space margin over
  RedisJSON's uncompressed keyspace, and orthogonal to the query surface.
- The query surface is the new headline deliverable; the ADR 0002 substrate is
  its execution layer.

## Phases

0. Plan IR and executor for projection and aggregation over the typed columns
   (no `WHERE`) — measured against `FT.AGGREGATE`.
1. `WHERE`: scalar predicates and containment, full-scan first, then
   postings-accelerated.
2. `GROUP BY`, `ORDER BY`, `LIMIT`.
3. The SQL-text parser for the subset.
4. The RedisJSON/RediSearch scoreboard.

## Post-acceptance performance result

The first query-tier postings implementation still extracted every value and
numeric column before consulting its candidate seam. On the 20,000-document
selectivity benchmark that left scalar equality at 35.9-38.2 ns/document even
when only 0.1% of rows matched. Selection pushdown changes that row to
0.094-0.095 ns/source-document (1.87-1.91 microseconds/query), a 399x reduction
in elapsed time and a 954x reduction in allocated bytes (about 4.21 MB to
4.4 KB). At 1% it measures 0.71-0.73 ns/document and at 10% 7.08-7.16
ns/document. These are six-run, 300 ms samples on Apple M4 Max, Go 1.26.1,
darwin/arm64; the benchmark source and full selectivity sweep live in
`query/postings_bench_test.go`.

Correctness does not trust the posting hash. Accelerated queries still run the
ordinary compiled predicate over every gathered row, and the bounded
differential requires accelerated results to equal both the dense executor and
an independent reference across classic, hashed, narrow/wide shape-taped, and
dictionary-backed storage. Buffered sparse gathers are separately held to zero
steady-state allocations. The posting probes they consume have the same
contract through `AppendWhereExists` and `AppendWhereContainsIndex`: reuse the
result slice and prebuild the containment needle, and the warmed lookup makes
no heap allocation. Exact verification remains allocation-free for compact
tapes and for escaped scalar and object-key spellings of arbitrary length.

Non-goals from ADR 0002 (durability, MVCC, distribution, multi-core, full SQL)
are unchanged.
