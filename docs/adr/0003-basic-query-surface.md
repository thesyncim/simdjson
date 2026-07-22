# ADR 0003: compiled single-table query surface

Status: accepted and implemented. Builds on ADR 0002.

## Context

`DocSet` exposes the fast primitives, but applications should not have to
rebuild projection, filtering, aggregation, grouping, ordering, and posting
selection around them. A full SQL engine would put unrelated planning and
distributed policy in the core package.

## Decision

Provide a `query` subpackage for a deliberately bounded, read-only,
single-`DocSet` query:

- path projection;
- `COUNT`, `SUM`, `AVG`, `MIN`, and `MAX`;
- scalar comparisons, containment, `EXISTS`, and null tests;
- `AND`, `OR`, and `NOT` composition;
- `GROUP BY`, stable `ORDER BY`, and `LIMIT`; and
- equivalent SQL text and programmatic builder front ends.

Compilation produces one immutable plan. `Run` is the allocating convenience.
`RunInto` accepts a reusable `Result` and `Workspace`; after their row,
posting, decoded-text, ordering, and group capacities warm, execution allocates
zero bytes.

## Execution

Dense predicates and columns stream over compact tapes. A selective posting
probe runs first and sparse row gathers materialize only candidates. The
executor switches to sparse gathering only below its measured crossover, then
rechecks the complete compiled predicate over every candidate. A posting hash
collision can therefore increase work but cannot admit a false result.

Projection, aggregation, and grouping consume typed columns directly.
Shape-taped rows are read at their native narrow or wide width and are not
widened into classic tapes. Group keys use the same byte-exact semantics as the
document layer. Stable ordering retains input order for equal keys.

## Correctness and ownership

- SQL and builder plans must execute identically.
- Dense and posting-accelerated routes must execute identically.
- Classic, hashed, narrow/wide shape-taped, posting, and dictionary-backed
  `DocSet`s must execute identically.
- Numeric comparison uses exact decimal semantics; it does not route through a
  lossy `float64` conversion.
- Results and workspaces are caller-owned and belong to one executing worker.
  The compiled query is immutable and may be shared.
- Failure leaves caller-owned result prefixes in the documented transactional
  state.

The differential suite compares every accelerated route to the dense executor
and an independent reference model. Allocation tests cover warmed projection,
filtering, containment, grouping, ordering, and aggregation.

## Index interaction

`DocSet.Postings` is a physical acceleration option. ADR 0004 adds logical
Store indexes that backfill it online. A Store snapshot may contain a mixture
of covered and uncovered chunks: covered chunks use postings and uncovered
chunks use the exact scan fallback. Readiness is operational state, never a
correctness precondition.

Sorted sparse posting lists use linear merge/intersection. Native dense masks
may use the internal SIMD Boolean kernel. Transient sparse-to-dense conversion
is intentionally rejected until a complete build/combine/decode benchmark wins.

## Competitive boundary

The comparable DuckDB surface stores exact JSON, materializes the same scalar
paths, and builds single-column ART indexes for key and exact filter lookup.
Both sides use one execution lane and must reproduce generator-owned counts and
aggregates. Store timing is a direct in-process call; DuckDB's profiled latency
also includes SQL parse, bind, optimization, and execution. Durable file, WAL,
buffer peak, and Store live heap remain separate accounting domains.

The reproducible setup is
[`benchmarks/duckdbbench/duckdb-methodology.md`](../../benchmarks/duckdbbench/duckdb-methodology.md).
Machine-specific ratios belong in generated benchmark reports, not as timeless
API promises in this ADR.

## Non-goals

SQL mutation or DDL, joins, subqueries, window functions, transactions,
multi-table planning, multi-core scheduling, durability, replication, and a
complete SQL dialect are outside this package. Keyed programmatic mutation is
the responsibility of `Store` under ADR 0004.
