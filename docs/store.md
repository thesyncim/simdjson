# Store

`Store` is an in-memory keyed JSON collection with updates, deletes, immutable
snapshots, TTL, declared single/compound indexes, and wildcard postings. It is
designed for applications that want predictable in-process reads without a
reader lock, clock call, expiry branch, tombstone, or version check.

The zero value is ready to use. Options are frozen by the first `Put`,
`CreateIndex`, or `AddIndex`.

```go
store := simdjson.NewStore(simdjson.StoreOptions{
	ChunkDocuments: 8, // zero selects 64
	ShapeTapes:      true,
})

created, err := store.Put("user:42", []byte(`{"name":"Ada","team":"compiler"}`))
if err != nil {
	return err
}
_ = created

view := store.Snapshot()
raw, ok := view.GetRaw("user:42")
```

## Command reference

| Operation | Result | Complexity |
| --- | --- | --- |
| `Put(key, json)` | `created=true` on insert; validates and copies input | O(replacement bytes + one chunk's metadata + radix height) |
| `Delete(key)` | true when the key existed | O(one chunk's metadata + radix height) |
| `Snapshot()` | immutable current view | O(1) |
| `GetRaw(key)` / `Snapshot.GetRaw(key)` | borrowed exact JSON bytes | O(keyed-HAMT depth + full-key check) |
| `Get(key)` / `Snapshot.Get(key)` | borrowed navigable `Index` | same lookup; first compact-tape widening may allocate |
| `Range(fn)` | visits live keys in chunk/slot order | O(materialized radix nodes + live keys) |
| `SetTTL`, `SetDeadline` | true when the key existed | O(log4 expiring keys) |
| `Persist` | true when an expiration was removed | O(log4 expiring keys) |
| `ExpireDue(now, limit)` | number of due keys published as deleted | heap work + one rebuild per affected chunk |
| `CreateIndex(definition)` | publishes a 1-4 column exact scalar index | O(1) DDL publication |
| `AddIndex(name, Postings)` | publishes the wildcard posting family | O(1), except shared coverage copy |
| `DropIndex(name)` | detaches one logical definition | O(index-catalog size) publication |
| `BackfillIndex(name, k)` | examines at most `k` start-snapshot chunks | exact: O(k × live slots × columns); wildcard: O(k bounded chunk builds) |
| `ReclaimIndexes(k)` | rebuilds at most `k` physically indexed chunks | O(k bounded chunk builds) |
| `AppendIndexRows/Masks/Keys` | exact lookup through one declared index | O(posting chunks + exact collision checks) |
| `query.RunSnapshotInto` | late-bound indexed query over a snapshot | candidate masks + selected-column work |

A non-positive `ExpireDue`, `BackfillIndex`, or `ReclaimIndexes` limit means all
currently eligible work. Event loops should normally use a positive limit.

## Mutation semantics

`Put` copies caller input and validates the copy before publication. The caller
may reuse the input after return. Invalid JSON, invalid frozen options, or
address exhaustion returns an error and changes no snapshot. Updating an
existing key preserves its TTL. `Delete` removes the TTL with the document and
returns false for a missing key.

Writes are serialized. Each successful write builds a complete next graph and
publishes it through one atomic pointer store. An update parses only the
replacement document. Unchanged rows reuse their immutable source and
structural tape storage; the changed chunk copies its dense row headers and
chunk-relative narrow value slab. Published slabs are never reused as writable
capacity.

Deletes remove the row instead of recording a tombstone. Surviving document
storage is shared unchanged, freed slots and empty chunk ids are reused, and
deleting a chunk's final row removes the leaf without building an empty chunk.
There is no compaction command or compaction threshold.

`ChunkDocuments` bounds the row metadata touched by one ordinary mutation:

- `1`: lowest write amplification, highest per-document metadata;
- `8`: useful for write-heavy mixed workloads;
- `64`: default, best scan locality and metadata density.

The limit counts documents, not bytes. Measure with the application's document
sizes and option set.

### Mutation measurements

Apple M4 Max, darwin/arm64, Go 1.26, shape tapes enabled, 1,024 resident small
documents, the median of six 500 ms samples:

| chunk documents | replace | delete + insert | replace bytes/op |
| ---: | ---: | ---: | ---: |
| 1 | 0.856 us | 2.09 us | 2.3 KiB |
| 8 | 0.813 us | 1.88 us | 3.2 KiB |
| 64 | 2.36 us | 5.10 us | 9.8 KiB |

At the default size, immutable storage reuse reduced replace time by about 6.2x
and delete-plus-insert time by about 5.8x versus reparsing every surviving row;
allocation fell by about 82% and 80%. `BenchmarkStoreMutation` and
`BenchmarkStoreMutationModes` reproduce the paths. These numbers are local
regression evidence, not Redis command latency claims.

## Snapshot and borrowing rules

`Snapshot` captures one state pointer and remains valid after later updates,
deletes, expiry, backfill, or reclamation.

| Read | Writer lock | Clock/TTL check | Steady allocation | Returned lifetime |
| --- | --- | --- | --- | --- |
| `Snapshot` | none | none | zero | independent immutable view |
| `GetRaw` | none | none | zero | borrows the snapshot graph |
| `Range` | none | none | zero | callback key/value borrow the snapshot graph |
| buffered exact/posting probes | none | none | zero with sufficient `dst` | rows, masks, and keys borrow the snapshot graph |
| warmed `query.RunSnapshotInto` | none | none | zero | result borrows its result/workspace storage |
| `Get` | no writer lock; shape cache may lock | none | first widening may allocate | index borrows the snapshot graph |

Do not modify returned bytes. Keep the snapshot, or a derived handle that pins
it, alive while using a borrowed `RawValue`, `Index`, `Node`, or key. Holding old
snapshots while repeatedly updating hot keys intentionally retains the old
immutable versions; bound snapshot age or count at the application boundary.

## Zero-allocation boundary

With sufficient caller-owned capacity, `Snapshot`, `GetRaw`, `Range`,
`AppendPointer`, `AppendPointerRows`, buffered exact/posting probes, compiled
`query.RunInto`/`query.RunSnapshotInto`, and warmed TTL deadline changes
allocate zero bytes.

`Put` and `Delete` allocate the new immutable publication. A zero-allocation
mutation would have to borrow caller memory after return or overwrite storage
that an older snapshot can still read. The API does neither. Supplying an input
or result buffer removes transient application allocation where documented; it
does not transfer ownership of published Store memory back to the caller.

## TTL

TTL metadata is writer-only: one indexed four-ary heap node and one position
entry per expiring key. Changing a deadline updates that node in place;
`Persist` removes it. Repeated changes do not create stale generations.

```go
store.SetTTL("session:7", 30*time.Minute) // ttl <= 0 deletes immediately
store.SetDeadline("session:7", deadline) // a past deadline deletes immediately
store.Persist("session:7")               // remove expiration only

ctx, cancel := context.WithCancel(context.Background())
defer cancel()
go store.RunExpiry(ctx, time.Millisecond)
```

Expiration is publication-based:

- a due key remains visible until `ExpireDue` or `RunExpiry` publishes delete;
- an older snapshot keeps the key permanently;
- `TTLAt` may return a negative duration while publication is pending; and
- ordinary reads never call `time.Now` or inspect TTL state.

`ExpireDue` groups due keys by chunk, rebuilds each touched chunk once, and
publishes the batch as one generation. `RunExpiry` uses one timer for the next
deadline and sleeps without a ticker when the heap is empty. `NextExpiration`
is the integration point for an external event loop.

## Declared exact indexes

`CreateIndex` declares one to four RFC 6901 paths. One path is a column index;
multiple paths form one order-sensitive compound key. Paths may descend nested
objects or arrays, and use normal pointer escaping (`~0` for `~`, `~1` for
`/`). Missing paths, incompatible traversal steps, and containers are omitted.
`null`, booleans, exact JSON numbers, and decoded strings are indexed.

```go
info, err := store.CreateIndex(simdjson.StoreIndexDefinition{
	Name:  "tenant_country_status",
	Paths: []string{"/tenant", "/profile/geo/country", "/status"},
})
if err != nil {
	return err
}
for info.State != simdjson.StoreIndexReady {
	info, err = store.BackfillIndex(info.Name, 64)
	if err != nil {
		return err
	}
}
```

Each chunk is a bounded 64-slot micro-page. A posting stores one `uint64` per
materialized chunk, so Boolean operations process 64 documents per scalar
instruction before any row decoding. Up to four `(chunk, mask)` pairs stay
inline. Wider postings use a sparse persistent radix vector; deleting words
demotes back to inline storage and contracts redundant radix levels
immediately. Updates path-copy only the affected posting paths. When an update
does not change the indexed tuple, it publishes no new index nodes or catalog
slices.

The fingerprint directory is collision-resistant for caller-controlled string
content and avalanches low bits before radix routing. Fingerprints are never a
trust boundary: every public exact lookup re-resolves the indexed paths and
compares scalar values exactly. Numeric equality does not round through
`float64`; spellings such as `1`, `1.0`, and `1e0` agree, while distinct wide
numbers sharing a coarse candidate bucket are separated by the recheck.

Use prebuilt scalar indexes when the lookup repeats, or pass raw scalar JSON:

```go
keys := make([]string, 0, 32)
keys, err = view.AppendIndexRawKeys(
	keys[:0], "tenant_country_status",
	[]byte(`"acme"`), []byte(`"PT"`), []byte(`"active"`),
)
```

`AppendIndexRows` returns stable `(chunk, slot)` addresses for sparse gathers.
`AppendIndexMasks` keeps the native Boolean form. `AppendPointerRows` and
`AppendRowKeys` materialize only those candidates. All are zero-allocation
after caller buffers have sufficient capacity.

The query engine binds indexes at execution time, so a compiled query can
outlive create, backfill, and drop:

```go
q := query.Select(query.Path("id"), query.Path("profile.geo.country")).Where(
	query.And(
		query.Cmp("tenant", query.Eq, "acme"),
		query.Cmp("profile.geo.country", query.Eq, "PT"),
	),
)

var result query.Result
var workspace query.Workspace
err = q.RunSnapshotInto(&result, store.Snapshot(), &workspace)
```

The planner prefers the widest matching compound index, intersects indexed
`AND` branches, unions `OR` only when every branch is bounded, and complements
exact `NOT` masks against live slots. It uses sparse gathers at 50% selectivity
or below and always evaluates the original predicate over survivors. Building
indexes remain correct through dense scan fallback; readiness changes latency,
not answers.

`Snapshot.IndexStats` reports current physical `Fingerprints`, `ChunkWords`,
candidate bits, directory/bitmap nodes, and `EstimatedBytes` without allocating.
The estimate counts reachable index-owned objects but not allocator size-class
rounding. On the repository's 65,536-document, 16-value enum fixture, both a
single and a two-column exact index are about **4.2 index bytes/document**.
Cardinality and value distribution materially change that number; measure the
production definition instead of treating the fixture as a guarantee.

Measured on Apple M4 Max, darwin/arm64, Go 1.26, 1,024 resident shape-taped
documents and 64-document chunks:

| replacement | time | bytes/op | allocs/op |
| --- | ---: | ---: | ---: |
| no declared exact index | 2.38 us | 9.8 KiB | 12 |
| exact single index, tuple unchanged | 2.64 us | 9.9 KiB | 12 |
| exact compound index, tuple unchanged | 2.64 us | 9.9 KiB | 12 |
| exact single index, tuple changed | 3.10 us | 11.9 KiB | 18 |
| exact compound index, tuple changed | 3.12 us | 11.9 KiB | 18 |

On 4,096 documents, a warmed 10%-selective indexed `RunSnapshotInto` measured
12.9 ns/input document versus 67.2 ns/document for the same equality-filtered
`DocSet` scan (about 5.2x). A compound point query measured 3.01 ns/input
document. These local microbenchmarks exclude protocol, durability, and client
costs and are reproducible with `BenchmarkStoreExactIndex*` and
`BenchmarkQueryRunSnapshotIndexed`.

## Wildcard postings

`StoreIndexPostings` accelerates top-level existence and exact scalar
containment.

```go
info, err := store.AddIndex("search", simdjson.StoreIndexPostings)
if err != nil {
	return err
}
for info.State != simdjson.StoreIndexReady {
	info, err = store.BackfillIndex("search", 64)
	if err != nil {
		return err
	}
}
```

The definition is visible immediately as `Building`. Writes dual-maintain it
before publication. Covered chunks use postings; uncovered chunks use the exact
scan fallback, so readiness affects latency, not correctness. Already-covered
chunks still consume a backfill budget unit, keeping the call's work bounded.

Multiple logical posting names share one physical layer. `DropIndex` detaches a
logical name immediately. After the final consumer disappears, reclaim in
bounded batches:

```go
for done := false; !done; {
	_, done = store.ReclaimIndexes(64)
}
```

Build a containment needle once and reuse result capacity:

```go
n, err := simdjson.RequiredIndexEntries([]byte(`"compiler"`))
if err != nil {
	return err
}
needle, err := simdjson.BuildIndex(
	[]byte(`"compiler"`),
	make([]simdjson.IndexEntry, 0, n),
)
if err != nil {
	return err
}

keys := make([]string, 0, store.Len())
keys = store.AppendWhereContainsIndexKeys(keys[:0], "team", needle)
```

Posting hashes are candidate filters only. Exact verification removes hash
collisions and escaped-spelling aliases before returning a key.

## Operations

`Store.Stats` briefly takes the writer mutex and returns an O(1),
allocation-free operational snapshot:

- `Generation`: atomic publications, including one per expiry batch;
- `Keys`, `Chunks`, `ChunkDocuments`;
- `ChunkHighWater`: vector address span, not scan or compaction work;
- `ReusableChunks`: partial or empty ids available to writes;
- `ExpiringKeys`: exact heap-node count;
- `Indexes`, `IndexedChunks`, and `IndexReclaiming`.

`Snapshot.AppendIndexes` returns reader-visible index name, kind, ordered paths,
state, covered chunks, and total chunks. `Snapshot.IndexStats` adds the physical
exact-index footprint. Alert on a stalled `Building` watermark, old retained
snapshots, or an event loop that leaves expired deadlines unpublished.

## Limits and comparison boundary

`ChunkDocuments` is in `[1,64]`. Chunk ids are `uint32`; `Put` returns
`ErrStoreTooLarge` before wraparound. Detailed source, tape, depth, retention,
and maintenance limits are in [`contracts/limits.md`](contracts/limits.md).

The Store is in-memory. It has no WAL, crash recovery, replication, eviction,
cluster protocol, or cross-process snapshot. The RedisJSON/RediSearch harness
compares the static `DocSet` and query executor over matched corpora. A mutable
Store comparison must additionally match durability, protocol/client time,
document distribution, index definitions, and expiry semantics. See
[`benchmarks/redisbench/redis-methodology.md`](../benchmarks/redisbench/redis-methodology.md).
