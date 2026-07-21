# ADR 0002: document-store building blocks

Status: accepted (planning). Owner: the index/multi-document layer.

## Goal

Provide the building blocks from which a fast in-memory JSON document store is
assembled — the substrate under ADR 0003's query surface. The competitive
target is RedisJSON with RediSearch: a single-threaded, in-memory JSON store
with a JSONPath read API and a secondary-index/aggregation engine. The store
must beat it on single-core read speed and in-memory footprint while offering
far more expressive reads (ADR 0003), and preserve semantics a re-encoding
store discards: key order, duplicate keys, exact number spellings. This
library ships mechanism only — in-memory and serialized representations, index
structures, and read primitives. Durability, replication, and concurrency
policy belong to the consuming engine.

## Why this is winnable

RedisJSON and RediSearch pay costs this design avoids, single-threaded on both
sides (the fair comparison — Redis is single-threaded per shard, and single
core is our target):

1. **In-memory representation.** RedisJSON stores a decoded tree of nodes with
   per-node headers and pointers — it does not compress, so its footprint
   typically exceeds the minified text. We keep the original bytes plus a tape
   whose cost the levers below drive below the source, so a deduplicated
   DocSet is smaller than RedisJSON's live representation of the same corpus.
2. **Read machinery.** A JSONPath `JSON.GET` walks that tree per call; our
   compiled shapes and columnar extractors read fields at 8-12 ns/document in
   one pass over contiguous arenas.
3. **Query expressiveness and setup.** RediSearch answers filters and
   aggregations only over fields declared up front in an `FT.CREATE` schema,
   and has no JSON containment operator. Our reads need no pre-declared index,
   and `RawContains` gives containment natively — a capability edge, not only
   a speed one (ADR 0003).
4. **Index build.** RediSearch index construction is a separate, non-trivial
   pass; our ingest validates, indexes, and enriches in one streaming pass at
   7+ GiB/s, and shape/interner structures amortize across documents.

Honest caveat, recorded up front: an in-process library and a networked server
are not identical shapes. The harness (ADR 0003) measures single-core,
single-connection on shared corpora; the consuming engine spends the remaining
margin on serving and durability and must still come out ahead.

## Space model

At-rest cost per corpus = source bytes + tape + shared structures (shapes,
interner, postings). The classic tape costs 16 B/entry (~1x minified text on
dense JSON); the levers drive it down:

- **Shape-deduplicated tapes** (phase 1): keys live once in the shape;
  per-document tape = header + value entries (~48% tape reduction measured on
  clustered corpora; extraction becomes dense value-array indexing).
- **Dual-width tapes** (phase 2): documents under 64 KiB take 8-byte entries,
  halving the remaining tape.
- **Value dictionary** (Gap A): repeated value spans interned once, each later
  occurrence a 4-byte reference — structural dedup with O(1) access, no
  decompression.

Because RedisJSON does not compress and carries tree overhead, the space
comparison favors us once dedup lands: the goal is a deduplicated DocSet
materially smaller than RedisJSON's keyspace memory for the same corpus, with
the value dictionary widening the margin on real corpora rich in repeated
values. Absolute internal metrics — tape bytes/document and the dedup rate —
track progress; the competitive number is measured by the ADR 0003 harness.
Misses are reported as measured.

## Measured baseline (2026-07-21)

Internal measurement on real and synthetic corpora recorded the starting
point and the phase 1+2 result: the classic tape roughly matched the source;
shape-deduplicated dual-width tapes cut clustered-corpus tape storage 48% at a
99% dedup rate (retained bytes 938 -> 606 MiB on the 1M-document synthetic
set), and single-core ingest ran 0.5-2.2 GB/s depending on document size.
Extraction is 5-12 ns/document on conforming corpora. The value dictionary
(Gap A) targets real corpora rich in repeated values; existence and
containment await the posting layer (Gap B). These feed the ADR 0003 harness
that compares them to RedisJSON.

## Phases

**Phase 1 - shape-deduplicated tapes** (landed). The space keystone;
differential-tested byte-for-byte against the classic tape, classic the
fallback for non-conforming documents.

**Phase 2 - dual-width tapes** (landed). Opt-in; fused extractors read narrow
entries natively; oversize documents fall back to wide.

**Phase 3 - persistence.** A versioned, mmap-friendly serialization of a
DocSet (source arenas, tapes, shapes, interner, postings) for zero-parse
reopen. Formats are explicitly unstable before v1.

**Phase 4 - the inverted layer.** Key-existence postings via interner x shapes
(key ID -> shape set -> documents, already implicit in shape-deduplicated
storage); (path hash, value hash) -> document postings for containment
candidate pruning, verified by **RawContains**, a containment evaluator whose
semantics follow the documented JSON containment model (landed). This is the
execution layer for ADR 0003's `WHERE`.

## Placement

All phases live in this repository. The litmus test: anything that needs the
tape, arena, interner, or shape internals — or that answers "how fast can one
core do this to documents" — is mechanism and lives here, inheriting the
differential, corruption, and benchmark gates. The basic query surface (ADR
0003) is the thin product tier over it; a full engine's planning, joins,
multi-core scheduling, durability, and serving stay in a consuming repository.
The root module stays free of third-party dependencies; comparison tooling
with heavy dependencies is quarantined in the separate benchmarks module.

## Gap closure designs

**Gap A - real-corpus space via a value dictionary.** Repeated value spans —
the enum strings, names, and labels that recur thousands of times in real
corpora — are interned once into a corpus-wide dictionary, each later
occurrence a reference, while every value stays directly addressable at tape
speed (no decompression). Shape tapes remove key redundancy, the dictionary
removes value redundancy. Optional stdlib flate wraps only cold at-rest
residuals where random access is surrendered (phase 3). This is the lever that
widens the space margin over RedisJSON on repeat-heavy corpora; a landed
in-memory dictionary is measured first, flate at persistence.

**Gap B - existence and containment via postings.** Two opt-in posting
families: key existence resolves as interner ID -> shapes containing the key
-> their document lists plus a scan of the non-conforming remainder; value
containment prunes through (path hash, value hash) -> document postings, with
candidates verified by RawContains. RawContains is independent of the postings
and has landed. These make ADR 0003's selective `WHERE` sublinear.

**Gap C - extraction.** The phase-1 fast path fixed the shape-tape engagement
(34 -> 5 ns/document); dual-width tapes (phase 2) halved the remaining memory
traffic. Re-measured before any further mechanism.

**Gap D - large-document ingest.** Diagnosed: a 466 KiB-document corpus
ingests at 800 MB/s through Append but 338 MB/s through ReadFrom, so the cost
is ReadFrom-specific. Its one-pass fast walk caps its window (so a mid-fill
build cannot be invalidated by a later chunk roll), sending large documents to
a two-scan slow path (structural framer then build) where Append scans once.
The fix is a bounded tradeoff — walk to the buffered edge and refill-and-retry
for fully-buffered documents — in the corruption-gated stream path, without
regressing the fast small/mixed rows.

## Non-goals

Durability, replication, MVCC, query planning beyond ADR 0003's basic surface,
joins, multi-core scheduling (single-core is the target; sharding is trivial
caller-side composition), and stable on-disk formats before v1.

## Standing constraints

Every phase lands through the existing gates: differential correctness against
reference semantics, exhaustive bounded-domain equivalence checks for novel
representations, GOGC=1 corruption tests for unsafe code, the zero-regression
corpus bench gate, and honest rejection with measurements when a lever does
not pay.
