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
- equivalent SQL text and programmatic builder front ends; and
- one public immutable typed `Plan` below both front ends.

Compilation produces one immutable plan. `Run` is the allocating convenience.
`RunInto` accepts a reusable `Result` and `Workspace`; after their row,
posting, decoded-text, ordering, and group capacities warm, execution allocates
zero bytes.

## Front-end and transport boundary

SQL is syntax, not execution state. `PrepareSQL` parses SQL and the builder's
`Prepare` method lowers builder values into the same `Plan`. The plan retains
compiled pointers, typed constants, numeric path/aggregate/group slots, and
late-bound index probes. It does not retain SQL source, lexer tokens, or the
builder predicate tree. Execution consequently performs no SQL parsing and no
string dispatch.

Schemaless JSON field names cannot disappear without an application-provided
schema: a peer must transmit each path's bytes at least once. They remain cold,
immutable compiled-pointer metadata. Result headers are likewise display and
compatibility metadata, kept separately from the hot operator array. Output
columns have stable ordinal IDs through `Plan.AppendSchema`; result cells are
typed and `Cell.AppendJSON` writes to a caller buffer. A future binary protocol
can therefore encode schema/path bytes once, then carry ordinals, type tags,
constants, bitmap batches, and typed cells without making SQL or formatted
strings its intermediate representation.

This ADR intentionally does not freeze a network byte layout before versioning,
limits, ownership, and compatibility rules exist. A decoder will be another
front end to the same typed plan, not a second executor.

## Execution

Dense predicates and columns stream over compact tapes. A selective DocSet or
heap-Store posting probe runs first and sparse row gathers materialize only
candidates. Those hash-bounded routes recheck the complete compiled predicate,
so a collision can increase work but cannot admit a false result. A
FileStore's persistent exact probe can instead return final masks when a
collision-free scalar or compound certificate proves the complete stream;
legacy, missing, oversized, and collision-marked certificates retain the same
document-recheck fallback.

Projection, aggregation, and grouping consume typed columns directly.
Shape-taped rows are read at their native narrow or wide width and are not
widened into classic tapes. Group keys use the same byte-exact semantics as the
document layer. Stable ordering retains input order for equal keys.

`Cell` stores raw JSON, decoded text only when the value is a string, and one
tagged numeric/boolean word. On 64-bit targets it is 56 bytes rather than the
former 72-byte parallel integer/float representation. Result materialization
therefore writes 22% fewer bytes per cell; four-column projection on the M4 Max
fixture improved from 165.4-166.7 to 145.4-146.0 ns/document while remaining
zero-allocation. Aggregate and grouping scans stayed within benchmark noise.
Computed aggregates no longer grow or borrow an eagerly formatted number arena:
`Int64`/`Float64` consume them directly and `AppendJSON` formats only when a
text encoding is actually requested. The convenience `JSON` accessor may
allocate for a computed number; its caller-buffered counterpart is the
zero-allocation transport contract.

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
