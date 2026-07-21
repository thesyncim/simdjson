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

## Phase 0 findings (measured 2026-07-21, PostgreSQL 18.4)

The first baseline run (benchmarks/results/phase0-report.md) confirmed the
model where it was right and corrected it where it was wrong. Ingest meets
its bound with room (22.9-144x). Heterogeneous space is already met (0.88x)
and clustered synthetics sit at 0.90-0.95x awaiting phase 1. Two corrections:

- **Real-corpus space is a compression battle.** PostgreSQL's TOAST
  compresses large jsonb values (citm: 128 MiB of minified source stored in a
  43 MiB table), producing 3.2-9.3x losses that no tape reduction can close.
  Phase 3's cold columnar mode is therefore load-bearing, not stretch, and
  must include source-byte compression for at-rest documents.
- **The extraction bound assumed a slower competitor.** PostgreSQL 18's
  per-row sequential `->>` costs ~100 ns on this hardware, so small-document
  corpora measure 3-8x rather than 50x. The bound stays as written — the
  distance is the roadmap: part of it was our own fast path not engaging on
  the baseline corpora (fixed in phase 1), the rest falls to phases 1-2.

Existence and containment lose up to 100x to GIN postings, as predicted;
those rows are the quantified mandate for phase 4.

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

## Gap closure designs (adopted 2026-07-21)

Phase 0 left four measured gaps. Each closes with a specific mechanism:

**Gap A — real-corpus space (3.2-9.3x, cause: TOAST compression).**
PostgreSQL compresses byte-wise and pays whole-value decompression on every
access. We compress structurally instead: a corpus-wide **value dictionary**
(the KeyInterner discipline applied to value spans) deduplicates repeated
values — the enum strings, names, and labels that make real corpora
compressible — while every value stays directly addressable at tape speed.
Shape-deduplicated tapes remove key redundancy; the value dictionary removes
value redundancy; documents become shape ref + fixed-width value refs.
Optional stdlib flate wraps only cold at-rest residuals where random access
is explicitly surrendered. Lands with phase 3 persistence, measured against
the citm row (the worst loss, 8.3x) as the acceptance driver.

**Gap B — existence and containment (up to 100x, cause: no postings).**
Two posting families, both opt-in at ingest: key existence resolves as
interner ID -> set of shapes containing the key -> shape membership doc
lists (already implicit in shape-deduplicated storage) plus a scan of the
non-conforming remainder; value containment prunes through
(path hash, value hash) -> document bitmap postings, the `jsonb_path_ops`
analogue, with candidates verified by **RawContains**, a JSONB-compatible
containment evaluator whose oracle is PostgreSQL's documented `@>`
semantics. RawContains is independent of the posting layer and lands first.

**Gap C — extraction distance (3-8x vs the 50x bound).** Decomposed by
measurement: the baseline-corpus fast-path miss is fixed in phase 1
(34 -> 5 ns/doc); dual-width tapes (phase 2) halve the remaining memory
traffic; the bound is re-measured after both land before any further
mechanism is considered.

**Gap D — large-document ingest (7.8x of 10x; ~500 MB/s on 466 KiB
documents vs 1.4-1.9 GB/s elsewhere).** Cause unknown; owned as a profiled
investigation (window interaction, chunk roll, or enrichment pass), fixed
or explained with numbers.

## Non-goals

Durability, replication, MVCC, SQL/jsonpath planning, multi-core scheduling
(single-core supremacy is the library target; sharding is trivial
caller-side composition), and stable on-disk formats before v1.

## Standing constraints

Every phase lands through the existing gates: differential correctness
against reference semantics, GOGC=1 corruption tests for unsafe code, the
zero-regression corpus bench gate, and honest rejection with measurements
when a lever does not pay.
