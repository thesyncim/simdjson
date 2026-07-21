# ADR 0002: document-store building blocks (a JSONB+GIN-class substrate)

Status: accepted (planning). Owner: the index/multi-document layer.

## Goal

Provide the building blocks from which a document store can be assembled that
beats PostgreSQL's JSONB storage plus GIN indexing on both space and
performance, while preserving semantics JSONB discards (key order, duplicate
keys, exact number spellings). This library ships mechanism only: in-memory
and serialized representations, index structures, and query primitives.
Durability, WAL, MVCC, concurrency policy, and distribution belong to the
consuming engine.

## Why this is winnable

JSONB and GIN pay four structural costs this design avoids:

1. **Representation bloat.** JSONB re-encodes documents into a tree with
   per-node headers and alignment, typically inflating minified text by
   20-60%, and converts numbers lossily. We keep the original bytes (exact by
   construction) plus a tape whose cost is attackable (below).
2. **Posting amplification.** GIN stores one index entry per key per row.
   Real corpora cluster into shapes; a shape is a pre-aggregated posting
   list. Key -> shape -> documents turns millions of postings into a handful.
3. **Per-row machinery.** Tuple headers, buffer management, and operator
   dispatch dominate JSONB point access. Our extraction primitives run at
   8-12 ns/document single-core.
4. **Index build cost.** GIN builds are notoriously slow. Our ingest path
   validates, indexes, and enriches at 7+ GiB/s, and shape/interner
   structures amortize across documents.

An honest caveat is recorded up front: comparing an in-process library to a
full DBMS is not apples-to-apples. The comparison harness (phase 5) measures
the building blocks' budget; the consuming engine spends the remaining
margin on durability and concurrency and must still come out ahead.

## Space model and targets

At-rest cost per corpus = source bytes + tape + shared structures (shapes,
interner, postings). Today the tape costs 16 B/entry (~1x minified text on
dense JSON), which loses to JSONB storage alone. The planned levers change
the model:

- **Shape-deduplicated tapes** (phase 1): keys live once in the shape;
  per-document tape = header + value entries (~50% tape reduction on
  object-heavy corpora, and extraction becomes dense value-array indexing).
- **Dual-width tapes** (phase 2): documents under 64 KiB take 8-byte entries
  (16-bit offsets), halving the remaining tape.
- **Cold columnar mode** (phase 3, optional per corpus): with keys in
  shapes, value spans can be stored without repeated key text at rest -
  smaller than the original text while staying exact.

Targets, to be validated by the phase-5 harness on shared corpora
(twitter/citm-class plus synthetic homogeneous and heterogeneous sets):

- Space: documents + all index structures <= 0.6x PostgreSQL (table + GIN,
  `jsonb_path_ops`) on shape-clustered corpora; <= 1.0x on adversarially
  heterogeneous ones. Stretch with cold columnar mode: <= 0.4x.
- Ingest: >= 10x COPY + GIN build throughput, single core.
- Point extraction (`->`/`->>` equivalent): >= 50x.
- Existence (`?`): >= 10x. Containment (`@>`): >= 5x with candidate pruning.

Misses are reported as measured; targets bind the design, not the report.

## Phases

**Phase 0 - baseline and methodology.** Corpus set, PostgreSQL measurement
protocol (versions, fillfactor, fastupdate, jsonb_ops vs jsonb_path_ops),
space accounting rules, and the acceptance table above committed to the
harness before optimization begins.

**Phase 1 - shape-deduplicated tapes** (in flight as the flagship slice).
The space keystone. Differential-tested byte-for-byte against the classic
tape; classic remains the fallback for non-conforming documents.

**Phase 2 - dual-width tapes.** Opt-in DocSet mode; fused extractors read
narrow entries natively; oversize documents fall back to wide.

**Phase 3 - persistence.** A versioned, mmap-friendly serialization of a
DocSet (source arenas, tapes, shapes, interner, postings) for zero-parse
reopen; alternatively text-only storage with rebuild at ingest speed.
Format versioning policy decided here; pre-v1 formats are explicitly
unstable.

**Phase 4 - the inverted layer.** Key-existence postings via
interner x shapes (key ID -> shape set -> documents); path+value hash
postings (`jsonb_path_ops` analogue) for containment candidate pruning; a
JSONB-compatible containment evaluator (`RawContains`) verified against
PostgreSQL's documented `@>` semantics as the oracle.

**Phase 5 - the comparison harness.** Reproducible PostgreSQL comparison
(pinned version, documented settings) measuring the acceptance table;
published alongside the existing competitor scoreboard with the same
honesty rules: losses reported with causes.

## Placement

All phases live in this repository. The litmus test: anything that needs the
tape, arena, interner, or shape internals — or that answers "how fast can one
core do this to documents" — is mechanism and lives here, inheriting the
differential, corruption, and benchmark gates. Anything that decides which
operation runs, when, with what guarantees — query languages, planning,
multi-core scheduling, durability, replication, serving — is policy and
belongs to a consuming engine repository. The root module stays free of
third-party dependencies through every phase; comparison tooling with heavy
dependencies is quarantined in the separate benchmarks module. The v1 API
boundary (ADR 0001) is the checkpoint for partitioning the exported surface,
including a possible store subpackage once the layout freezes.

## Non-goals

Durability, replication, MVCC, SQL/jsonpath planning, multi-core scheduling
(single-core supremacy is the library target; sharding is trivial
caller-side composition), and stable on-disk formats before v1.

## Standing constraints

Every phase lands through the existing gates: differential correctness
against reference semantics, GOGC=1 corruption tests for unsafe code, the
zero-regression corpus bench gate, and honest rejection with measurements
when a lever does not pay.
