# ADR 0004: immutable Store publication, TTL, and online indexes

Status: accepted and implemented. Builds on ADR 0002 and ADR 0003.

## Decision

Add a root-package `Store` that serializes mutation and atomically publishes
immutable snapshots. Reader-visible state contains:

- a per-Store-keyed persistent 32-way HAMT from complete key to chunk/slot;
- a persistent 32-way radix vector of immutable chunks;
- at most 64 stable slots per chunk and one dense `DocSet` row table; and
- the logical online-index watermark.

Writer-only state contains reusable and indexed chunk-id sets, TTL metadata,
online-index build cursors, and reclamation progress. Readers never consult it.

## Hard requirements

1. `Snapshot.GetRaw` takes no lock, reads no clock, checks no expiry,
   tombstone, or version bit, and allocates zero bytes.
2. Every prior snapshot remains byte-correct after any later operation.
3. Invalid input publishes nothing.
4. Ordinary mutation work is bounded by one chunk and persistent-tree height,
   never total Store size.
5. Delete churn creates no tombstones, delta overlay, or later compaction
   threshold.
6. TTL changes create one live heap node per expiring key, not stale deadline
   generations.
7. Index construction and reclamation are caller-budgeted and observable.
8. Hash collisions may widen work but never change an answer.

## Publication and ownership

Writers hold `Store.mu`, build a complete next state, and publish it with one
release `atomic.Pointer.Store`. A reader either reaches the complete old graph
or the complete new graph.

The HAMT and chunk vector path-copy only changed radix nodes. Complete hashes
and keys are verified at HAMT leaves. The keyed hash seed prevents a remote
caller from selecting a deterministic collision family; collision chains still
compare the full key.

Document source bytes and structural tapes are independently immutable.
Updating a row validates and copies only its replacement. Unchanged rows in the
new chunk reuse their source and classic tape backing directly; the new chunk
copies only dense row headers and the chunk-relative narrow value slab. Enabled
postings and value dictionaries are rebuilt because their ordinals and ids are
chunk-local.

Compiled shape records are immutable and may also be shared. A rebuild seeds
its shape cache only from records referenced by surviving rows. Reused classic
rows may be promoted through the same repeat-sighting and exact key-byte proof
as ordinary ingest. An updated-away shape is omitted, so optimization state is
bounded by live layouts rather than becoming a version chain. Published narrow
slabs are never reused as writable capacity.

Old storage becomes collectible when the last snapshot and current chunk stop
referencing it. The implementation stores no parent-chunk pointer, version
list, epoch token, or deferred merge obligation.

## Delete behavior

Slots are stable while a key is live. `ord[slot]` maps the slot to the current
dense `DocSet` row. Delete rebuilds the row headers without the removed slot;
all surviving source and tape storage is shared unchanged. Deleting the final
row removes the chunk leaf immediately and builds no empty replacement.

Freed slots and empty chunk ids enter O(1) writer sets and are reused. Radix
traversal descends materialized branches only, so a high historical chunk id
does not turn into scan work. There is no read-time tombstone and no compaction
operation.

## Why bounded immutable chunks

| Strategy | Reader cost | Reclamation behavior | Decision |
| --- | --- | --- | --- |
| Per-key object under a reader lock | lock or retry | immediate | rejected: taxes reads |
| Delta overlay over a base segment | extra lookup and branch | periodic merge | rejected |
| Append-only versions and tombstones | version checks | global compaction debt | rejected |
| Epoch-reused pages | direct read | caller lifetime protocol | rejected for the public Go API |
| Whole-corpus copy | direct read | O(corpus) per write | rejected |
| Bounded chunk plus persistent metadata | direct read | ordinary GC by snapshot lifetime | selected |

`ChunkDocuments` controls the copied row-table bound: 1 for the lowest write
amplification, 8 for write-heavy mixed use, and 64 by default for scan locality
and metadata density. It is a document count, not a byte limit.

## TTL

TTL is publication-based and deliberately absent from snapshots. An indexed
four-ary min-heap contains exactly one mutable node per expiring key, with a
position map for in-place deadline changes and cancellation. `Persist` removes
the node; no stale generation remains to clean later.

`ExpireDue` removes due nodes, groups them by chunk, rebuilds each affected
chunk once, and publishes the whole batch as one generation. `RunExpiry` arms
one timer for the next deadline and sleeps without a ticker when no key
expires. A key remains visible until expiry work publishes its delete, and an
older snapshot retains it permanently.

## Online posting indexes

`AddIndex` publishes a logical `Building` definition. Writes dual-maintain all
active families. `BackfillIndex(k)` resumes from an immutable radix cursor and
examines at most `k` start-snapshot chunks; an already-covered candidate still
consumes budget. Covered chunks use postings, uncovered chunks use the exact
scan fallback, and both return the same answer.

Coverage uses sparse 4,096-id bitmap pages. Empty pages disappear, and `Ready`
releases the captured vector root and collapses coverage to implicit all-live
state. Multiple logical posting names share one physical layer.

`DropIndex` removes the logical definition immediately. After the final
consumer disappears, `ReclaimIndexes(k)` rebuilds at most `k` ids taken from an
O(1) writer set; it never scans the Store merely to discover completion. Old
snapshots continue to own their physical index version through normal GC.

## Allocation contract

After caller-owned capacity is warm, snapshots, `GetRaw`, `Range`, buffered
posting probes, compiled `query.RunInto`, warmed TTL changes, and native dense
Boolean operations allocate zero bytes.

Mutations allocate the next immutable state. A zero-allocation `Put` or
`Delete` would require overwriting storage visible to an old snapshot or
borrowing caller memory after return, so it is incompatible with this lifetime
contract. The optimization target is bounded allocation proportional to the
replacement plus one chunk's small metadata, not an unsafe zero.

## Measured mutation result

On Apple M4 Max, darwin/arm64, Go 1.26, shape tapes enabled, 1,024 resident
small documents, the median of six 500 ms samples:

| chunk documents | replace | delete + insert | replace bytes |
| ---: | ---: | ---: | ---: |
| 1 | 0.856 us | 2.09 us | 2.3 KiB |
| 8 | 0.813 us | 1.88 us | 3.2 KiB |
| 64 | 2.36 us | 5.10 us | 9.8 KiB |

At the default chunk size this is about 6.2x faster for replace and 5.8x
faster for delete-plus-insert than rebuilding and reparsing every surviving
row, with 82% and 80% less allocation respectively. `Store.GetRaw` remains
19.6 ns with zero allocations. These are local regression fixtures, not
universal Redis command claims.

## Validation

Required gates include randomized map and query differentials across chunk
sizes and representation modes; retained snapshots; caller-input mutation;
live-storage reuse and obsolete-shape release; invalid-input rollback;
address reuse; TTL and index state machines; race, `checkptr`, forced GC,
portable/SIMD parity, and steady-state allocation tests.

Operational semantics, complexity, examples, and tuning live in
[`docs/store.md`](../store.md). Resource ceilings live in
[`docs/contracts/limits.md`](../contracts/limits.md).

## Non-goals

This decision does not add a WAL, crash recovery, replication, eviction,
distributed consensus, cross-Store transactions, multi-core query scheduling,
or a stable serialized Store format. Those features may consume publication
generations but cannot weaken the reader contract.
