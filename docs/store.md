# Mutable Store operations

`Store` is the keyed, mutable layer over the document primitives. It is for
applications that need updates, deletes, expirations, online secondary indexes,
and stable concurrent raw reads without putting a lock, clock read, tombstone,
or version check in that point-read path.

The zero value is ready to use. `NewStore` is convenient when options are not
zero:

```go
store := simdjson.NewStore(simdjson.StoreOptions{
	ChunkDocuments: 8,
	ShapeTapes:      true,
})

created, err := store.Put("user:42", []byte(`{"name":"Ada","team":"compiler"}`))
if err != nil {
	return err
}
if !created {
	return errors.New("user already existed")
}

snapshot := store.Snapshot()
raw, ok := snapshot.GetRaw("user:42")
if ok {
	fmt.Printf("%s\n", raw.Bytes())
}
```

`Put` copies and validates the document before publication. Caller input may be
reused as soon as it returns. An invalid document changes neither the current
view nor any existing snapshot. Replacing a key preserves its TTL; call
`Persist` to remove the TTL explicitly. `Delete` returns false for an absent key
and creates no tombstone.

## Read and snapshot contract

`Snapshot` is an immutable value containing one state pointer. Creating one is
O(1). A snapshot remains valid after any number of later writes, deletes,
expirations, index changes, or reclamation batches.

| Operation | Lock | Clock/TTL check | Heap allocation with supplied storage | Lifetime |
| --- | --- | --- | --- | --- |
| `Snapshot` | none | none | none | independent immutable view |
| `Snapshot.GetRaw` | none | none | none | bytes borrow the snapshot |
| `Snapshot.Range` | none | none | none | callback values borrow the snapshot |
| `AppendWhereExistsKeys` | none | none | none after `dst` is sized | keys borrow the snapshot |
| `AppendWhereContainsIndexKeys` | none | none | none after `dst` is sized | prebuild the needle index |
| `Snapshot.Get` | shape-cache mutex on first widening | none | first shape-tape access may allocate | navigable index borrows the snapshot |

`GetRaw` is the predictable point-read primitive. It computes one keyed hash,
walks at most thirteen five-bit trie levels, verifies the complete key, and
resolves a stable chunk slot. Hash collisions cannot return the wrong key.

A snapshot is logically immutable. With shape tapes enabled, `Get` may populate
the same synchronized widening cache used by `DocSet.Doc`; that memoized classic
tape is byte-equivalent and never changes an answer. `GetRaw`, `Range`, and the
buffered Store probes do not widen shape tapes.

`Range` visits chunk and slot order, not lexicographic key order. Deleted chunks
are absent branches in a persistent radix tree; traversal skips them rather
than walking the historical address high-water mark. A delete-heavy workload
therefore does not need compaction to recover scan speed.

Do not mutate bytes returned by a snapshot. Keep the snapshot alive for as long
as a borrowed `RawValue`, `Index`, `Node`, or key is used.

## Write model

Writes are serialized. A successful mutation:

1. validates and builds a replacement for at most one bounded document chunk;
2. path-copies the affected persistent-radix-vector nodes;
3. path-copies the keyed HAMT only when a key is inserted, moved, or removed;
4. dual-maintains every active physical index; and
5. publishes one new state with an atomic pointer store.

Readers never observe a partially rebuilt chunk. Old chunks are ordinary Go
objects and are reclaimed after the last snapshot that can reach them becomes
unreachable. There is no epoch API and no unsafe manual reclamation contract.

Chunks contain at most 64 documents. Empty slot and chunk identifiers are
reused in O(1), while sparse radix traversal skips empty subtrees. Deletes
rebuild the affected `DocSet` densely, so there are no row tombstones in scans,
no read-time delta merge, and no stop-the-world compaction threshold.

`ChunkDocuments` selects the copy-on-write page size:

- `1` minimizes update amplification and maximizes per-document metadata;
- `8` is a write-heavy compromise;
- `64` is the default and favors scan locality, posting density, and retained
  space.

Measured on Apple M4 Max with shape tapes enabled and 1,024 resident small
documents:

| chunk documents | replace | delete + insert | replace transient bytes |
| ---: | ---: | ---: | ---: |
| 1 | 0.74-0.76 us | 2.07-2.31 us | 2.5 KiB |
| 8 | 4.06-4.10 us | 8.62-9.20 us | 37.0 KiB |
| 64 | 14.0-14.2 us | 28.5-30.5 us | 53.7 KiB |

These ranges are six 500 ms samples. They are in-process microbenchmarks, not
Redis command comparisons. Re-run `BenchmarkStoreMutation` on the target
document distribution before selecting a page size. Large documents naturally
cost at least their owned byte and tape size; the document-count bound is not a
byte quota.

Immutable publication requires owned storage, so `Put` and `Delete` are not
zero-allocation operations. Claiming otherwise would require either borrowing
mutable caller memory or reusing storage still reachable by an old snapshot.
The zero-allocation contract applies to buffered reads, posting probes, compiled
query execution, and warmed TTL changes.

## TTL without a read tax

TTL metadata is deliberately outside published snapshots. The writer owns an
indexed four-ary minimum heap with one node per expiring key and a key-to-heap
position map:

```go
store.SetTTL("session:7", 30*time.Minute) // assign or change
store.Persist("session:7")                // cancel

ctx, cancel := context.WithCancel(context.Background())
defer cancel()
go store.RunExpiry(ctx, time.Millisecond)
```

Changing a deadline edits the existing heap node in O(log4 n); it does not add
a stale generation. Cancelling removes that node immediately. Repeated TTL
changes therefore cannot accumulate cleanup debt.

`RunExpiry` is deadline-driven. With no TTL it sleeps without a ticker. With
TTLs it arms one timer for the earliest deadline, and a newly assigned earlier
deadline wakes and retargets the timer. `resolution` rounds the wake time upward
and bounds coalescing lateness.

When deadlines are due, `ExpireDue(now, limit)` removes them from the heap,
groups them by chunk, rebuilds every affected chunk once, and publishes the
whole batch as one generation. It is also the deterministic integration point
for applications that already own an event loop.

Expiration is publication-based, not passive-on-read:

- a key remains visible until `ExpireDue` or `RunExpiry` publishes its delete;
- a snapshot taken before that publication continues to contain the key;
- ordinary reads never call `time.Now`, inspect TTL metadata, or branch on an
  expiry flag;
- `TTLAt` may return a negative duration while publication is pending.

This differs intentionally from Redis passive expiry. It buys a literal zero
TTL tax on reads and gives snapshots a coherent, timeless meaning.

## Online indexes

The first online index kind is `StoreIndexPostings`, which accelerates
top-level existence and exact containment probes:

```go
info, err := store.AddIndex("search", simdjson.StoreIndexPostings)
if err != nil {
	return err
}
for info.State != simdjson.StoreIndexReady {
	info, err = store.BackfillIndex("search", 64) // at most 64 chunks
	if err != nil {
		return err
	}
}
```

`AddIndex` publishes the logical definition immediately. Existing chunks are
`Building`; every subsequent write builds postings before its new snapshot is
visible. `BackfillIndex` examines at most the caller's chunk budget from a
resumable immutable-radix cursor, rebuilds only uncovered live chunks among
them, and atomically publishes changed coverage. Already-covered candidates
still consume budget, preventing concurrent writes from turning a one-chunk
maintenance call into a hidden search across the corpus. Snapshot probes remain
exact throughout: indexed chunks use postings and uncovered chunks use the scan
fallback.

Multiple logical posting indexes share one physical posting layer. Adding a
second name copies coverage metadata rather than duplicating per-document
postings.

Dropping is two-phase:

```go
if err := store.DropIndex("search"); err != nil {
	return err
}
for done := false; !done; {
	_, done = store.ReclaimIndexes(64)
}
```

`DropIndex` removes the logical definition in one publication. When the last
consumer is gone, `ReclaimIndexes` physically removes postings from at most the
requested number of indexed chunks. The writer tracks those chunk identifiers
directly, so a one-chunk reclamation does not hide a full-store completion
scan. Old snapshots retain their old physical representation until normal
garbage collection.

Build a containment needle once with caller-owned entry storage, then reuse the
destination:

```go
need, err := simdjson.RequiredIndexEntries([]byte(`"compiler"`))
if err != nil {
	return err
}
needle, err := simdjson.BuildIndex(
	[]byte(`"compiler"`),
	make([]simdjson.IndexEntry, 0, need),
)
if err != nil {
	return err
}

keys := make([]string, 0, store.Len())
keys = store.AppendWhereContainsIndexKeys(keys[:0], "team", needle)
```

The posting hash is only a candidate filter. Exact containment verification
removes collisions, escaped-spelling aliases, and other false positives before
a key is returned.

## Boolean representation and SIMD

Boolean query planning is representation-aware:

- sparse sorted posting lists use linear merge/intersection;
- a natively dense bitmap uses allocation-free `AND`, `OR`, and `AND-NOT`
  kernels;
- the optional Go 1.27 SIMD backend processes four 128-bit vectors per loop on
  ARM64 and AMD64, with a scalar tail and an exact in-place aliasing contract.

On Apple M4 Max the native-bitmap `AND` kernel's six-run median measured
2.1-2.6x faster than its unrolled scalar reference from 64 through 16,384
words, with zero allocations.
The end-to-end density experiment also rejected an attractive but losing idea:
turning ephemeral sorted postings into bitmaps inside a query was about 2.9x
slower at 50% density and 3.4x slower at 75% density because materialization and
final ordinal decoding dominated the SIMD merge. The production sparse path
therefore does not convert merely to reach SIMD. Dense masks must be native and
reused enough to amortize construction.

This rule is a performance contract: representation changes require a complete
probe-build + Boolean-combine + decode benchmark, not only an isolated kernel
win.

## Operations and observability

`Store.Stats` is O(1) and allocation-free:

```go
stats := store.Stats()
log.Printf(
	"keys=%d generation=%d chunks=%d expiring=%d indexes=%d reclaiming=%t",
	stats.Keys,
	stats.Generation,
	stats.Chunks,
	stats.ExpiringKeys,
	stats.Indexes,
	stats.IndexReclaiming,
)
```

Important counters:

- `Generation`: successful atomic publications, not individual keys in a batch;
- `Chunks`: currently materialized chunks;
- `ChunkHighWater`: persistent-vector address span;
- `ReusableChunks`: partially filled or empty identifiers available to writes;
- `ExpiringKeys`: exact heap-node count, with no stale generations;
- `IndexedChunks`: chunks that physically retain postings;
- `IndexReclaiming`: physical cleanup remains after the last logical drop.

Use `Snapshot.AppendIndexes` to inspect name, kind, state, covered chunks, and
total live chunks for the snapshot visible to a reader. Use `NextExpiration`
to schedule an external expiry loop.

## Complexity and limits

| Operation | Work |
| --- | --- |
| `Snapshot` | O(1) |
| `GetRaw`, `Get` | O(keyed-HAMT depth + full-key check), at most 13 trie levels |
| `Range` | O(materialized radix nodes + live keys) |
| insert / replace / delete | O(documents in one chunk + log32 chunk address space) |
| `SetDeadline`, `Persist` | O(log4 expiring keys) |
| `ExpireDue` | O(due keys log4 n + distinct affected chunks rebuilt) |
| `AddIndex`, `DropIndex` | metadata publication; shared coverage copy when applicable |
| `BackfillIndex(k)` | at most k start-snapshot chunks examined/rebuilt; resumable radix cursor |
| `ReclaimIndexes(k)` | at most k indexed chunk rebuilds, no full completion scan |

`ChunkDocuments` must be in `[1,64]`. The persistent vector uses 32-bit chunk
identifiers and fails with `ErrStoreTooLarge` before wraparound. With 64
documents per chunk the theoretical address limit is roughly 274 billion
documents, far beyond practical memory.

The Store is in-memory. It does not provide a write-ahead log, crash recovery,
replication, distributed consensus, or cross-process snapshots. Applications
requiring those properties must layer them around successful publication.

## Redis comparison boundary

The RedisJSON/RediSearch scoreboard measures the static `DocSet` and compiled
query engine over identical corpora, single-core on both sides. It is not a
claim that this in-memory Store already supplies Redis durability, protocol,
replication, eviction, or cluster behavior. Conversely, Redis command latency
is not an honest direct measurement of this in-process API unless network and
client time are separated. The reproducible methodology is in
[`benchmarks/redisbench/redis-methodology.md`](../benchmarks/redisbench/redis-methodology.md).

For mutable workloads, use `BenchmarkStoreGetRaw`, `BenchmarkStoreMutation`,
`BenchmarkStoreTTLChange`, and `BenchmarkStoreIndexedSnapshotProbe`; compare
against Redis on the same machine, documents, durability settings, and
publication semantics.
