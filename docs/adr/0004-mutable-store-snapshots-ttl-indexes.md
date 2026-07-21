# ADR 0004: bounded immutable mutation, TTL, and online indexes

Status: accepted and implemented. Builds on ADR 0002's document substrate and
ADR 0003's posting/query primitives.

## Decision

Add a root-package `Store` that serializes mutation and publishes immutable
snapshots. Store state consists of:

- a per-store-keyed, persistent 32-way HAMT from complete key to chunk/slot;
- a persistent 32-way radix vector of immutable document chunks;
- bounded chunks containing at most 64 stable slots and one dense `DocSet`;
- writer-only O(1) sets for reusable and physically indexed chunk ids;
- writer-only TTL and online-index lifecycle metadata; and
- one atomic pointer to the complete reader-visible state.

Every ordinary write rebuilds at most one chunk and path-copies only the search
metadata it changes. A delete removes the row from the rebuilt `DocSet`; it
does not publish a tombstone. Readers load one state pointer and consult only
immutable memory.

TTL stays outside the snapshot. One indexed four-ary min-heap node represents
each expiring key. Expiry publication groups due keys by chunk, rebuilds each
affected chunk once, and publishes the batch in one generation. The background
driver is deadline-driven rather than periodic when idle.

Online posting indexes publish a logical definition before physical coverage.
Writes dual-maintain every active family; bounded backfill publishes covered
chunks until the readiness watermark reaches the live chunk count. Drop is
logical and immediate; bounded snapshot-safe reclamation removes physical
postings only after the last logical consumer disappears.

## Requirements

1. `GetRaw` must take no lock, make no clock call, inspect no TTL/version bit,
   and allocate nothing.
2. Existing snapshots must remain byte-correct across every later operation.
3. An invalid `Put` must publish nothing.
4. Update/delete/expiry work must be bounded by touched chunks, not corpus size.
5. Delete churn must create neither tombstones nor a later compaction cliff.
6. Repeated TTL changes must not create stale heap generations.
7. Index add/drop work must be incrementally schedulable and observable.
8. Index build and hash collisions may widen work but never change an answer.
9. Unsafe SIMD may optimize a proven native bitmap representation only; it may
   not weaken correctness, aliasing, bounds, or stable-toolchain fallback.

## Publication protocol and proof obligations

Writers hold `Store.mu`. They derive a new immutable state entirely before the
release publication through `atomic.Pointer.Store`. The published object points
to immutable HAMT nodes, radix nodes, chunks, key strings, source bytes, and
semantic tapes. Shape-taped `Get` may populate `DocSet`'s synchronized memoized
classic-tape cache; the cache is equivalent derived data and is never consulted
by `GetRaw`. A reader that observes the new root therefore observes the complete
new semantic graph; a reader that retained the old root continues to reach the
complete old graph.

The key directory uses `hash/maphash` with a seed created per Store. Trie
routing consumes five hash bits per level, so at most thirteen nodes cover a
64-bit hash. A leaf stores and compares the full hash and full key. Equal-hash
collisions remain in an immutable leaf chain. The keyed hash prevents a remote
caller from deterministically constructing a deep collision family, while the
full-key comparison is the semantic guard.

The chunk vector consumes five id bits per level. Set path-copies one node at
each level. Traversal descends materialized children and skips nil subtrees;
its cost is a function of live structure, not the largest chunk id ever used.
The explicit `ErrStoreTooLarge` guard runs before a 32-bit chunk counter could
wrap.

One live key's chunk/slot address stays stable until that key is deleted; a
later insert may reuse the freed address. The contained `DocSet` is dense:
`ord[slot]` maps a stable live Store slot to its current dense row. A rebuild
walks the 64-bit live mask in ascending order, validates the replacement once,
and commits nothing if any build step fails. Inserts, replacements, explicit
deletes, expiry batches, index backfill, and index reclaim share this one build
primitive to prevent semantic drift.

## Why bounded copy-on-write chunks

An immutable snapshot requires changed storage not to overwrite bytes reachable
from an older snapshot. The possible strategies are:

| Strategy | Read cost | Write/reclaim behavior | Decision |
| --- | --- | --- | --- |
| Per-key mutable object + reader lock | lock or retry | cheap write | rejected: taxes every read |
| Delta overlay + base segment | extra lookup/branch | periodic merge cliff | rejected |
| Append-only versions + tombstones | version/tombstone check | global compaction debt | rejected |
| Epoch-reused pages | cheap read | caller-visible lifetime protocol | rejected for the public Go API |
| Whole-corpus immutable copy | cheap read | O(corpus) write | rejected |
| Bounded immutable chunk + persistent metadata | direct read | O(chunk + trie height) | selected |

The selected design spends bounded write amplification to make the reader
contract simple and permanent. Chunk size is explicit policy: one document for
write latency, eight for a write-heavy compromise, 64 by default for scan
locality and storage density. Empty ids are reused in O(1), and nil radix
branches are skipped, so deleting most of a Store does not make later reads
wait for relocation.

The Store configures smaller first arena allocations inside its bounded
`DocSet`s. Bulk `DocSet` keeps its existing 8 KiB/512-entry minima and pays no
new hot-path cost; the hint is read only when a Store chunk allocates a new
arena. This reuses the validated ingest/index/posting implementation without
making a one-document Store rewrite buy stream-sized arena capacity.

## TTL semantics

TTL is publication-based. Assignment itself changes writer metadata but does
not create a new document snapshot. A due key remains in the current snapshot
until expiry work publishes a normal delete. An older snapshot retains it
forever as part of that immutable view.

This is deliberately different from passive expiration on `GET`. Passive
expiry would require a clock and metadata branch on every read, and a snapshot
could change meaning merely because time passed. Applications choose either:

- `RunExpiry(ctx, resolution)`, one deadline-driven worker; or
- deterministic calls to `ExpireDue(now, limit)` from an existing event loop.

The heap has one node per key and a position map. Deadline changes update that
node in place and sift in the required direction. `Persist` removes it in
O(log4 n). There are no stale generation nodes and consequently no heap cleanup
phase.

An expiry batch first removes due heap nodes into reusable writer scratch,
sorts them by chunk/slot, path-copies key-directory deletions, rebuilds each
distinct affected chunk once, and performs one publication. Its generation
increments once, matching what readers can observe atomically.

## Online index state machine

```text
absent --AddIndex--> Building --Backfill/write coverage--> Ready
  ^                        |                                  |
  |                        +---------- DropIndex ------------+
  +-- bounded physical reclamation after last consumer -------
```

`StoreIndexInfo` publishes `CoveredChunks` and `TotalChunks` with each
snapshot. A write to an uncovered live chunk builds postings before publication
and marks that chunk for every logical posting index because they share one
physical layer. A chunk that becomes empty leaves both totals. A new chunk joins
already covered. Readiness is therefore monotonic except for the explicit
creation of a new logical build.

Queries do not trust readiness for correctness. Each chunk's `DocSet` either
uses complete postings or its exact scan fallback. Readiness only states that
all live chunks can take the accelerated route.

Each build retains the immutable chunk-vector root visible at `AddIndex` and a
resumable radix cursor into it. `BackfillIndex(k)` examines at most `k`
materialized start-snapshot chunks; a candidate already covered by a concurrent
write still consumes one unit. The radix successor search skips nil subtrees in
bounded depth rather than scanning integer ids. New or reused chunks are absent
from that start snapshot but are covered by the write path before publication.
Thus a small maintenance budget bounds both rebuilds and discovery work.
Build-time coverage uses sparse 4,096-id bitmap pages. Populated pages retain
one bit per chunk while empty address ranges cost nothing, so a sparse
historical high-water mark cannot recreate compaction debt as one giant flat
bitmap. Empty pages are dropped immediately. When coverage reaches the live
total, the build releases its immutable root and collapses the entire bitmap to
an implicit all-live state. Readiness is monotonic because later writes
dual-maintain the family, so a completed index does not pin historical chunks
or retain one coverage bit per chunk forever.

Physical indexed ids live in an O(1) writer set. `ReclaimIndexes(k)` takes ids
from that set and rebuilds at most `k`; completion is `len(set)==0`. It never
rescans all chunks to discover that one posting page remains.

## Sparse and dense Boolean work

Posting lists remain ascending sparse ordinals. AND and OR use linear merges
and caller-owned scratch. A dense representation has different economics, so
an internal word kernel provides append-style `AND`, `OR`, and `AND-NOT`, exact
in-place operation, an unrolled scalar implementation on supported stable Go,
and a four-vector Go-native SIMD implementation for the pinned Go 1.27 window.

The SIMD body uses typed `Uint64x2` loads/stores and four independent vectors
per loop. Disassembly must show vector loads, logical instructions, and stores
without calls or bounds-panic branches in the vector body. Differential tests
cover every tail length and exact aliasing; `-d=checkptr=2` and race runs cover
the unsafe address calculation.

The dispatch unit is representation, not merely density. Measurements rejected
per-query sparse-to-bitmap conversion: at 50% density the complete two-input
operation was roughly 2.9x slower than a list intersection, and at 75% roughly
3.4x slower, even though isolated native bitmap AND had a 2.1-2.6x six-run
median speedup over scalar. Construction and ordinal decode dominated.
Consequently:

- sparse postings stay sparse;
- a native persistent bitmap may use SIMD directly; and
- no planner conversion lands without an end-to-end crossover win including
  build, combine, and decode.

## Allocation contract

After retained capacity is sufficient:

- `Snapshot`, `GetRaw`, and `Range` allocate zero bytes;
- buffered Store posting probes allocate zero bytes;
- compiled `query.RunInto` allocates zero bytes across projection, filters,
  containment, grouping, ordering, and aggregation;
- changing the deadline of an already expiring key allocates zero bytes; and
- dense Boolean kernels allocate zero bytes with destination capacity.

Mutations allocate owned immutable version state. A caller buffer cannot be
reused for a published version while an old snapshot may retain it, so a
zero-allocation `Put` promise would contradict either ownership or snapshot
safety. This distinction is part of the API contract, not a benchmark caveat.

## Observability

`Store.Stats` exposes generation, keys, live chunks, chunk high-water, reusable
chunk ids, expiring keys, logical indexes, physically indexed chunks, and
reclamation state in O(1) without allocation. `Snapshot.AppendIndexes` exposes
the reader-visible build watermark. `NextExpiration` supports external event
loops.

No operation hides unbounded cleanup work behind a point read or one-chunk
maintenance request.

## Measured results and gates

Apple M4 Max, darwin/arm64:

- `Store.GetRaw`: 19.2-20.8 ns, zero allocations, over 65,536 keys;
- warmed TTL change: 42.9-43.5 ns, zero allocations;
- replace with shape tapes: 0.74-0.76 us at chunk 1, 4.06-4.10 us at chunk 8,
  and 14.0-14.2 us at chunk 64;
- indexed existence over 65,536 keys: 368-376 us, zero allocations;
- indexed 1/16-selective containment: 231-236 us, zero allocations; and
- native bitmap AND: 2.1-2.6x six-run median over scalar at 64-16,384 words,
  zero allocations.

These results establish local regression fixtures, not universal competitor
claims. Redis comparisons use the separate reproducible scoreboard and must
match machine, corpus, server configuration, durability, and semantic boundary.

Required gates are:

- randomized map differential across chunk sizes and representation options;
- retained old snapshots across mutation and expiry;
- invalid-input transactional rollback;
- delete/insert address reuse and sparse traversal beyond radix depth changes;
- concurrent readers under the race detector;
- TTL heap direction changes, cancellation, batch generation, and worker wake;
- online build, write coverage, exact fallback, drop, and bounded reclaim;
- portable/SIMD Boolean differential, alias, allocation, and disassembly checks;
- full portable and pinned-SIMD test suites; and
- `go vet`, race, and `-d=checkptr=2` runs.

## Non-goals

This decision does not add durability, a WAL, replication, eviction policy,
distributed consensus, transactions across Stores, multi-core query scheduling,
or stable serialized Store snapshots. Those features may build on publication
generation but cannot weaken this reader contract.
