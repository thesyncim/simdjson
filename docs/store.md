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
| `NewStoreBuilder` + `Append` + `Build` | bulk-validates unique keys, then packs keys/source/tapes outside the Go heap; publishes one Store | O(total input + transient construction) |
| `Put(key, json)` | `created=true` on insert; validates and copies input | O(replacement bytes + one chunk's metadata + radix height) |
| `Delete(key)` | true when the key existed | O(one chunk's metadata + radix height) |
| `Snapshot()` | immutable current view | O(1) |
| `GetRaw(key)` / `Snapshot.GetRaw(key)` | borrowed exact JSON bytes | O(heap-directory depth or mapped group probes + full-key check) |
| `Get(key)` / `Snapshot.Get(key)` | borrowed navigable `Index` | same lookup; first compact-tape widening may allocate |
| `CompileKey(key)` | caches seeded hash and verified stable slot | one ordinary key lookup |
| `GetRawKey` / `GetKey` | compiled-key read with safe full-lookup fallback | O(chunk radix height) on a stable-slot hit |
| `AppendRaw` / `AppendRawKey` | append exact JSON into caller storage | same lookup + O(value bytes); zero allocation with capacity |
| `Range(fn)` | visits live keys in chunk/slot order | O(materialized radix nodes + live keys) |
| `WriteTo(w)` | stream one full immutable checkpoint/export | O(image bytes + manifest metadata) |
| `OpenStore(image)` | validate and open a mutable Store borrowing an image | O(keys + eager page metadata + exact-index rebuild) |
| `SetTTL`, `SetDeadline` | true when the key existed | O(log4 expiring keys) |
| `Persist` | true when an expiration was removed | O(log4 expiring keys) |
| `ExpireDue(now, limit)` | number of due keys published as deleted | heap work + one rebuild per affected chunk |
| `CreateIndex(definition)` | publishes a 1-4 column exact scalar index | O(1) DDL publication |
| `AddIndex(name, Postings)` | publishes the wildcard posting family | O(1), except shared coverage copy |
| `DropIndex(name)` | detaches one logical definition | O(index-catalog size) publication |
| `BackfillIndex(name, k)` | examines at most `k` start-snapshot chunks | exact: O(k × live slots × columns); wildcard: O(k bounded chunk builds) |
| `ReclaimIndexes(k)` | rebuilds at most `k` physically indexed chunks | O(k bounded chunk builds) |
| `AppendIndexRows/Masks/Keys` | exact lookup through one declared index | O(posting chunks + exact collision checks) |
| `AppendIndexBitmap` / `AppendLiveBitmap` | append one dense stable-slot word per logical page | O(page high-water + exact lookup work) |
| `AppendStoreBitmapAnd/And3/Or/AndNot` | combine dense caller-owned workspaces | O(shortest or longest input words), zero allocation with capacity |
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
There is no compaction command or compaction threshold. A rebuilt bulk/mapped
chunk detaches from the immutable document base. The current generation drops
the base owner when its last such chunk detaches; older snapshots retain it
only for as long as their borrowed rows remain reachable.

`ChunkDocuments` bounds the row metadata touched by one ordinary mutation:

- `1`: lowest write amplification, highest per-document metadata;
- `8`: useful for write-heavy mixed workloads;
- `64`: default, best scan locality and metadata density.

The limit counts documents, not bytes. Measure with the application's document
sizes and option set.

## Bulk construction

Use `StoreBuilder` when the initial corpus is already keyed and no reader needs
to observe each individual insertion:

```go
builder, err := simdjson.NewStoreBuilder(simdjson.StoreOptions{
	ShapeTapes: true,
	Postings:  true,
})
if err != nil {
	return err
}
if err = builder.CreateIndex(simdjson.StoreIndexDefinition{
	Name:  "tenant_country",
	Paths: []string{"/tenant", "/profile/geo/country"},
}); err != nil {
	return err
}
for next() {
	if err := builder.Append(key, document); err != nil {
		return err
	}
}
store, err := builder.Build()
```

`Append` validates and copies the JSON and key; caller buffers may
be reused as soon as it returns. Keys must be unique. An invalid document or
duplicate changes no committed row. The builder belongs to one goroutine and
closes after `Build`; the returned Store is safe for concurrent use and has the
same update, delete, TTL, snapshot, and index behavior as any other Store.
`CreateIndex` may be called before or after appends. Its one-to-four nested or
compound paths are extracted at `Build`; the returned index is `Ready` in the
first reader-visible generation, with no scan fallback window.

The builder fills final micro-pages and mutates only unpublished key/chunk
radix nodes. Repeated layouts reuse immutable shape records across pages, and
each bounded source arena is sized once instead of retaining geometric growth
generations. `Build` then folds keys, row descriptors, exact source bytes,
structural tapes, and exact-index postings into Store-owned pointer-free blocks
before one publication. The default power-of-two chunk layout derives stable
locations from ordinal key references, reducing them to eight bytes. Common
row descriptors are 16 bytes. Flat scalar tapes select three, four, or five
bytes per value; repeated nested layouts keep one exact structural template and
two, three, or four span bytes per non-key entry. Width is selected per row, so
an exceptional document does not widen the whole Store. Value dictionaries
compose with templates; the optional wildcard-posting mode currently keeps
classic nested tapes for its remainder verifier. On the 16,384-document
benchmark fixture it measured 5.09-6.29 ms (156-192 MB/s) versus 43.7-45.2 ms
(21.6-22.4 MB/s) for repeated `Put`: about 7.5x median throughput, with 7.65
MiB rather than 144.6 MiB of transient allocation bytes. Including a ready
16-value exact index measured 5.99-6.31 ms and 7.85 MiB. Run
`BenchmarkStoreBulkBuild` for the exact local result.

Index construction reuses the same per-page tuple extraction, fingerprinting,
exact-recheck contract, stable-slot masks, and immutable bulk radix builders as
online backfill. It does not maintain a parallel bulk-only index format.

### Mutation measurements

Apple M4 Max, darwin/arm64, Go 1.26, shape tapes enabled, 1,024 resident small
documents, the median of six 500 ms samples:

| chunk documents | replace | delete + insert | replace bytes/op |
| ---: | ---: | ---: | ---: |
| 1 | 0.81 us | 1.99 us | 2.3 KiB |
| 8 | 0.90 us | 2.24 us | 3.2 KiB |
| 64 | 2.24 us | 5.00 us | 9.8 KiB |

`BenchmarkStoreMutation` and `BenchmarkStoreMutationModes` reproduce the
bounded-copy and full-rebuild control paths. These numbers are local regression
evidence, not cross-engine SQL latency claims.

## Snapshot and borrowing rules

`Snapshot` captures one state pointer and remains valid after later updates,
deletes, expiry, backfill, or reclamation.

| Read | Writer lock | Clock/TTL check | Steady allocation | Returned lifetime |
| --- | --- | --- | --- | --- |
| `Snapshot` | none | none | zero | independent immutable view |
| `GetRaw` | none | none | zero | borrows the snapshot graph |
| `GetRawKey` | none | none | zero | compiled stable-slot hit; borrows the snapshot graph |
| `Range` | none | none | zero | callback key/value borrow the snapshot graph |
| buffered exact/posting probes | none | none | zero with sufficient `dst` | rows, masks, and keys borrow the snapshot graph |
| warmed `query.RunSnapshotInto` | none | none | zero | result borrows its result/workspace storage |
| `Get` | no writer lock; shape cache may lock | none | first widening may allocate | index borrows the snapshot graph |

Do not modify returned bytes. Keep the snapshot, or a derived handle that pins
it, alive while using a borrowed `RawValue`, `Index`, `Node`, or key. Holding old
snapshots while repeatedly updating hot keys intentionally retains the old
immutable versions; bound snapshot age or count at the application boundary.

## Zero-allocation boundary

With sufficient caller-owned capacity, `Snapshot`, `GetRaw`, `GetRawKey`,
`AppendRaw`, `AppendRawKey`, `Range`, `AppendPointer`, `AppendPointerRows`,
buffered exact/posting probes, compiled `query.RunInto`/
`query.RunSnapshotInto`, and warmed TTL deadline changes allocate zero bytes.

`Put` and `Delete` allocate the new immutable publication. A zero-allocation
mutation would have to borrow caller memory after return or overwrite storage
that an older snapshot can still read. The API does neither. Supplying an input
or result buffer removes transient application allocation where documented; it
does not transfer ownership of published Store memory back to the caller.

For a repeated key, compile once. The handle caches the Store's seeded hash and
the key's current stable `(chunk, slot)` address:

```go
key := store.CompileKey("session:7")
raw, ok := store.Snapshot().GetRawKey(key)
```

The fast path verifies the live bit. Within the exact generation captured at
compile time, the publication identity proves the slot's complete spelling;
after any publication it rechecks the current spelling or resolves through the
key directories. Delete/reinsert, an initially absent key, and cross-Store use
therefore fall back safely. A handle stores no chunk pointer, so keeping it does
not pin an obsolete document page. Both ordinary and compiled reads allocate
zero bytes.

## Store images and external source bytes

`StoreBuilder.Build` moves its immutable key spellings, exact JSON, row
descriptors, compact structural tapes, and packed index bases into Store-owned
pointer-free blocks. On supported Unix systems those blocks are anonymous
mappings outside Go `HeapAlloc`. The GC sees a bounded owner graph rather than
one pointer-bearing object per row. These bytes still occupy address space and
RSS; external is a GC accounting boundary, not free memory.

`Store.WriteTo` emits a Store-native container of the existing bounded `DocSet`
page images plus a checksummed tail manifest. It records effective options,
stable slots and keys, generation, reusable empty page ids, ready nested or
compound index definitions, wildcard posting consumers, and TTL deadlines.
There is no second JSON, tape, or query representation.

This is a full checkpoint: each call writes every live micro-page. It is not a
per-mutation persistence requirement or an incremental durability protocol.
Mutations made after `OpenStore` are not written back into the borrowed image.
Applications can checkpoint periodically for backup or faster restart; a
durable primary store still needs the append-only page/root commit path below.

`OpenStore(image)` validates the complete directory before publishing a
mutable Store. Source bytes and native tape sections view `image` directly.
On supported Unix systems, one process-seeded Swiss-style key directory and
one 32-byte-per-row record directory live in anonymous pointer-free mappings,
outside Go `HeapAlloc`; their eight-control-byte group probes use native SWAR
and every hit still verifies the complete key. A chunk holds only Store-wide
owners and base ordinals, rather than a string header and two slice pointers
per row. Post-build/open changes use the existing immutable HAMT only as a delta.
TTL locations are packed integers, so the deadline heap and position map retain
no key strings. Exact-index bases are rebuilt into packed pointer-free posting
pages; construction still uses transient Go scratch, while the retained base
lives outside `HeapAlloc` on supported Unix systems. Distinct shape records
remain Go objects. Later `Put`, `Delete`, TTL, and index operations publish
ordinary immutable generations. A Store image cannot contain a `Building`
index: finish or drop the definition before calling `WriteTo`.

For a file-backed image, map it read-only and pass the mapped slice to
`OpenStore`. The caller owns the mapping and must keep it immutable and mapped
until the Store, every retained `Snapshot`, and every derived `RawValue`,
`Index`, or `Node` are unreachable. Do not unmap based only on dropping the
current Store variable: later states and old snapshots may still share base
pages. `AppendRaw(dst, key)` and `AppendRawKey` provide lifetime-independent
copy-out and allocate zero bytes when `dst` has enough capacity. Automatic
unmapping and finalizer-based ownership remain deliberately absent.

The image is a startup/off-heap boundary, not yet the completed 100x-RAM
engine. On the 16,384-document local fixture, the mapped image is 5.40 MB. A
key-only open takes 1.04-1.05 ms and allocates 234,688-234,689 Go-heap bytes in
273 allocations; its pointer-free metadata is 688,136 external key bytes plus
524,288 external row bytes. Compared with the former per-key HAMT/per-row
`Index` reopen (about 3.36 MiB, 19,206 allocations, and 1.74-1.82 ms), that is
about 93% less Go-heap metadata, 98.6% fewer allocations, and 40% lower open
latency. One compound exact index raises open to 2.65-2.68 ms and about 423.6
KB of transient allocation while constructing its 45,056-byte external packed
base; that build can fault document pages. `BenchmarkStorePersistOpenMapped`
reports `mapped-B`, all three external metadata classes, `B/op`, and
`allocs/op` so the RSS/heap distinction cannot disappear behind one throughput
number.

Once open, mapped source bytes add no steady allocation or ownership wrapper:
ordinary keyed reads measured 10.23-10.32 ns and generation-pinned compiled reads
4.47-4.51 ns. A nested two-column exact query selecting 32 documents from two
of 256 micro-pages measured 2.55-2.61 us, also at zero allocations. These rows
measure a hot mapping after eager open; they are not cold-storage latency.

### Dense Boolean workspaces

`StoreMask` is the compact interchange form for selective predicates: a sorted
`(chunk, uint64)` stream omits empty pages. A repeated or less-selective plan can
instead call `StoreBitmapWords`, fill reusable buffers with
`AppendIndexBitmap`/`AppendLiveBitmap`, and combine them with
`AppendStoreBitmapAnd`, `AppendStoreBitmapAnd3`, `AppendStoreBitmapOr`, or
`AppendStoreBitmapAndNot`. The word index is the logical page id and each bit is
a stable slot, so no row decoding occurs until `AppendBitmapRows` or
`AppendBitmapKeys` consumes the final candidates.

Every append form is allocation-free with sufficient caller capacity and
supports exact in-place Boolean execution. Three-way AND is fused to avoid an
intermediate write/read pass. Pinned Go 1.27 `GOEXPERIMENT=simd` builds use two
independent 256-bit vectors per loop on amd64. GOAMD64 v1/v2 performs one
process-constant AVX2 capability branch per bitmap call and otherwise executes
the scalar reference; v3+ calls AVX2 directly. All amd64 levels retain the
unrolled scalar loop below eight words, where the two-vector body cannot run.
Generated-code CI verifies that
the v1/v2 dispatch contains no vector instruction before the guard and that
the vector bodies retain `VPAND`, `VPOR`, `VPANDN`, and `VZEROUPPER`. M4 Max
NEON measured only parity with the scalar loop at roughly 75-80 GB/s, so arm64
deliberately keeps the scalar dispatch. Sparse page-id merges remain scalar
because converting a selective stream to dense words merely to reach SIMD
would lose. `BenchmarkStoreDenseBitmapPlan` measures both the fused kernel and
ordered row decoding through the public Store surface.

On the hosted x64 runner (AMD EPYC 7763), SIMD reduced the fused three-index
intersection from 815.1-823.4 ns to 174.3-174.8 ns, about 4.7x. Including
ordered decoding of 4,096 candidate rows measured 8.44-8.51 us portable versus
7.67-7.77 us SIMD, with zero allocations in both cases. These are directional
hosted-runner results rather than a cross-machine release gate; the benchmark
workflow retains the raw per-architecture artifacts.

`WriteTo` streams the same 5.40 MB image in 1.07-1.09 ms (4.96-5.04 GB/s)
with three allocations total on this fixture. Persistence headers, endian
scratch, nested page offset rebasing, and at-most-64-row page manifests use
writer-owned fixed storage; only the top-level reference list, Store manifest,
and writer object allocate. The manifest must be buffered to checksum before a
generic `io.Writer` receives it.

### Bounded page files

`Store.WritePageFile` and `OpenStorePageReader` are the immutable,
explicit-I/O path for a corpus larger than its configured frame budget. This
is separate from `WriteTo`/`OpenStore`: the latter is a mutable, eagerly opened
checkpoint image; the page reader never maps or validates the complete corpus.

Write a temporary empty file, close it, and atomically rename it into place at
the application boundary:

```go
next, err := os.OpenFile("accounts.next", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
if err != nil {
	return err
}
_, writeErr := store.WritePageFile(next, simdjson.StorePageWriteOptions{
	MaxDocumentPageBytes: 64 << 10,
})
closeErr := next.Close()
if err := errors.Join(writeErr, closeErr); err != nil {
	return err
}
if err := os.Rename("accounts.next", "accounts.pages"); err != nil {
	return err
}
```

`WritePageFile` refuses a non-empty target. It writes document extents, the
packed chunk radix, the sorted key B+tree, and the state page before syncing;
only then does it publish and sync the alternating checksummed superblock. A
partial file without that root is not recoverable. The caller owns directory
sync after rename when crash-safe name replacement matters.

Open with a complete frame budget and an explicit direct-I/O policy:

```go
reader, err := simdjson.OpenStorePageReader("accounts.pages", simdjson.StorePageOpenOptions{
	ResidentBytes:        64 << 20,
	MaxDocumentPageBytes: 64 << 10,
	DirectIO:             simdjson.StoreDirectTry,
})
if err != nil {
	return err
}
defer reader.Close()

key, ok, err := reader.PrepareKey("account:42")
if err != nil {
	return err
}
if !ok {
	return os.ErrNotExist
}
dst := make([]byte, 0, 4096)
dst, ok, err = reader.AppendRawKey(dst, key)
```

The budget is rounded down to equal maximum-extent frames and must hold at
least two. Frame bytes use anonymous external memory on supported Unix systems;
the Go heap retains O(frame-count) atomic control words, not one object per key
or database page. Metadata pages use 4 KiB physical extents but occupy one
cache frame, an intentional simple bound for format version one.

Admission performs CRC32C, Store/page identity, and complete kind-specific
schema validation before publishing an even frame epoch. Resident probes pin
through one atomic gate; eviction can atomically claim only exactly zero pins,
uses CLOCK second chances, and never reuses leased bytes. Identical concurrent
misses coalesce. A cache with no evictable frame returns explicit backpressure
as `ErrStorePageCacheFull` instead of allocating. Failed/corrupt pages remain
failed and report `ErrStorePageCorrupt` rather than a missing key. `Close`
rejects new reads with `ErrStorePageClosed` and waits for in-flight loads and
values before releasing the arena.

`ViewRaw` returns a `StorePageValue` that must be closed; its bytes become
invalid at close. `AppendRaw` copies into caller capacity and holds no frame on
return. `PrepareKey` performs the durable directory lookup once and records a
Store-bound physical reference plus stable slot, never a frame pointer. It
therefore survives eviction and reduces a repeat read to one document-page
probe while still comparing the complete key. `RangeRaw` visits callback-
scoped key/JSON bytes in logical chunk/slot order and pins the active directory
leaf while walking its documents. Do not retain or modify callback bytes.

`Stats` reports file bytes, configured capacity, logical resident bytes, frame
states, current pins, hits, misses, coalescing, physical reads/bytes, evictions,
failures, and whether direct I/O is actually active.
`StoreDirectTry` falls back only when the platform or filesystem rejects direct
I/O; `StoreDirectRequire` never silently changes semantics and reports
`ErrStoreDirectIOUnsupported` when the request cannot be honored.

The bounded smoke writes 1,155,072 bytes and opens it with 8,192 bytes of
frames: a 141.0x file/cache ratio. This proves bounded correctness, not equal
latency. Apple M4 Max measurements:

| path | result | physical reads | allocation |
| --- | ---: | ---: | ---: |
| admitted ordinary point | 150.7-153.9 ns | 0 | 0 |
| admitted prepared point | 49.3-49.8 ns | 0 | 0 |
| random point, 141x pressure, host buffer cache | 7.06-7.14 us | 5 × 4 KiB | 0 |
| ordered 4,096-row scan, 141x pressure | 371-378 us | 264 / 1,081,344 B | 0 |

In a Linux/arm64 Docker VM with `O_DIRECT` required, the pressure point
measured 168-174 us and the complete scan 9.53-9.93 ms. Those numbers measure
that VM storage stack, not NVMe. A cold read cannot match DRAM latency; the
useful guarantee is that a hot set stays resident and a cold set cannot force
unbounded Go heap or RSS growth.

This page-file surface is read-only and currently rejects TTL or secondary-
index state rather than silently dropping it. Online copy-on-write mutation,
durable index/TTL roots, overflow values, asynchronous scan batches, and
snapshot-aware extent reclamation remain the attached-database boundary. The
internal root-last committer and pure-Go Linux ring writer already provide the
bounded durability substrate, but ordinary `Put`, `Delete`, and TTL calls are
not falsely described as updating this file yet.

Chunk placement now uses 64-way packed CHAMP nodes: one occupancy word plus
densely ranked 32-byte physical references, with no empty child array or Go
pointer per chunk. Document leaves use the same stable-slot word and only two
cumulative `uint32` ends per live row. Slot identity is implicit in bitmap
rank, so the worst-case row directory is 512 bytes rather than 64 slice/string
headers. Keys and JSON occupy one canonical packed byte stream; an admitted
page returns capacity-clipped borrowed views and verifies a complete candidate
key before returning JSON. Directory nodes use the 4 KiB allocation quantum,
while a document leaf may use a larger power-of-two extent authenticated by
`PageRef.Length`. This keeps ordinary 64-row chunks contiguous without
inflating every sparse metadata node.

Exact indexes now use the posting-page codec as the live immutable base for
`StoreBuilder` and `OpenStore`, not only as a projection. Each 4 KiB physical
page packs many sorted value streams. Scattered singleton hits encode
`(chunk delta, slot)` as a uvarint—normally two bytes—while multi-hit chunks
retain a native `uint64` word for Boolean operations. A fixed 48-byte segment
record carries stream bounds, row count, tuple hash, and an optional logical
page/rank continuation; it contains no Go or physical pointer. Admission
validates sorted unique stream ids, canonical dense/singleton encodings, exact
packed offsets, row counts, continuations, and CRC before publication.

Writes never rebuild that immutable corpus base. The first mutation of one
64-row chunk copies that chunk's complete current postings into a persistent
delta and marks the chunk shadowed; later writes path-copy only changed delta
routes. Readers merge base and delta in chunk order and skip every stale base
word for a shadowed chunk. Old snapshots retain their base/delta pair, so this
reduces initial GC footprint without weakening update, delete, or snapshot
semantics. The same pages are not yet connected to the durable state root.

A ready recycle or busy-worker notification stays on the atomic fast path; a
full budget or an idle worker necessarily parks or wakes. Checksums stay scoped
to `internal/storeio` and use no handwritten assembly. Stable builds use Go's
hardware-aware CRC32C. SIMD builds dispatch pure-Go PMULL only on Darwin ARM64,
where it has a measured win, and use the standard path on Linux ARM64 and
amd64. Native Ubuntu ARM64 measured the PMULL candidate at 192.3-192.4 ns per
4 KiB versus 154.6-154.8 ns for the standard path; AMD EPYC 7763 measured the
ordinary PCLMUL candidate at 323.0-323.2 ns versus 170.7-170.8 ns. Those losing
kernels are not dispatched. The pure-Go amd64 PCLMUL and AVX-512 candidates
remain directly correctness-tested and ISA-checked so a future CPU-specific
tier can be admitted only after a native end-to-end win.

On M4 Max, stable Go CRC32C measured 383.3-387.5 ns per 4 KiB page and
5.924-6.296 us per 64 KiB page. The Darwin PMULL path measured 89.17-91.66 ns
and 1.131-1.146 us respectively (about 4.2x and 5.5x faster), with zero
allocations. The complete 4 KiB state-root page measured 170.0-171.6 ns to
encode and 152.4-153.3 ns to verify/decode. A full 64-way chunk-directory node
measured 935.2-948.9 ns to encode, 836.6-843.9 ns to verify/admit, and
7.17-7.26 ns for an admitted hit. A 64-row document page measured
747.8-753.5 ns to encode and 459.4-460.1 ns to verify/admit; JSON-only lookup
measured 2.566-2.576 ns and complete string-key verification plus JSON return
4.034-4.092 ns. A packed 1,900-singleton posting page measured 7.95-8.03 us to
encode, 7.38-7.49 us to verify/admit, 24.11-24.32 ns for an admitted stream
lookup, and 4.05-4.11 ns per decoded posting. Every result is zero-allocation.

Packed CHAMP nodes are the cold chunk-directory format. The existing
fixed-prefix directory remains preferable for the tiny heap hot overlay and
compiled stable-slot reads: a measured heap prototype saved 59% directory
bytes but made keyed lookup about 20% slower. The hybrid keeps cold footprint
low without taxing every hot hit.

A selective external query can beat a heap scan by combining resident 64-slot
index masks and never reading rejected JSON pages. A random cold point read
cannot be faster than DRAM; it pays storage latency. The 100x target is
therefore accepted only when the measured hot working set fits the configured
resident budget and the workload is indexed or locality-friendly.

### Scale smoke: 10k to 5M records

`TestStoreScaleSmoke` is an explicit, non-CI ladder:

```sh
STORE_SCALE_SMOKE=10000,100000,5000000 \
  go test . -run '^TestStoreScaleSmoke$' -v -count=1
```

It bulk-loads identical-shape documents with one nested single-column index and
one nested compound index, forces GC before measuring live heap, then measures
random keyed reads, a 1/256 compound query, updates, delete/reinsert churn, and
TTL changes. Apple M4 Max, pinned Go development toolchain, one run per scale:

| Records | Build docs/s | Source B/doc | Heap B/doc | Heap objects/doc | External B/doc | Accounted B/doc | Raw extent projection B/doc | Packed exact index B/doc | Point read | Compound query |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 10,000 | 1.33M | 92.9 | 16.0 | 0.022 | 147.2 | 163.3 | 128.2 | 7.27 | 19 ns | 3.56 us / 39 masks |
| 100,000 | 1.41M | 94.9 | 14.5 | 0.016 | 150.4 | 164.9 | 128.0 | 4.45 | 28 ns | 37.75 us / 390 masks |
| 5,000,000 | 0.95M | 98.7 | 14.4 | 0.016 | 148.7 | 163.1 | 128.0 | 4.16 | 49 ns | 7.77 ms / 19,531 masks |

At 5M rows, exact keys plus JSON are 493,480,886 bytes. Live Go heap is
71,759,480 bytes and 80,753 objects: 14.4 B/doc and 0.016 objects/doc. The
pointer-free key, document, and exact-index blocks total 743,629,378 bytes.
Honest heap-plus-external storage is therefore 815,388,858 bytes, 163.1 B/doc,
or 1.65x the logical input. Against the earlier 3,995,694,760-byte heap-only
representation, total accounted storage fell about 4.9x while GC-visible bytes
fell 98.2% and objects fell 99.4%.

The document block is 567,000,000 bytes. It includes exact JSON, compact row
descriptors, per-row adaptive scalar spans, and shared nested structural
templates. The key block is 155,831,938 bytes. The two packed exact indexes are
20,798,476 estimated bytes (4.16 B/doc), within 0.04% of the independent
20,791,296-byte posting-page projection. The separate 640,000,000-byte raw-page
projection is no longer a lower total for this corpus; it omits the key
directory and structural lookup state that the measured Store includes.

Across the isolated 5M run, indexed update averaged 5.34 us, delete plus
reinsert 11.19 us, and changing an existing TTL 46 ns. The point-read rise from
19 ns to 49 ns is a cache-footprint result, not an algorithmic complexity
change. Query time scales with the number of exact result masks because this
smoke materializes them; a Boolean consumer can combine those stable-slot
words without decoding rejected documents.

### Capacity planning for 1 TiB

There are different answers for the mutable heap Store and immutable page
reader. The current 5M-row builder Store uses 0.15 live heap bytes and 1.65
heap-plus-external bytes per source byte. A linear 1 TiB extrapolation of this
exact indexed workload would therefore require about 1.65 TiB of resident
addressable storage even though the Go GC scans only a small fraction. That is
workload evidence, not a universal multiplier: external ownership solves GC
cost, while a 1 TiB corpus on 64 GiB needs the bounded page-file representation.

The immutable page reader sizes its frame arena from the hot working set rather
than total JSON. The following estimates also describe what a future mutable
attached mode must budget. They extrapolate the measured 65,536-key
fixed and packed-CHAMP prototypes; the per-index column extrapolates the
measured 4.2 bytes/document 16-value exact index. They exclude key spelling,
TTL entries, high-cardinality value directories, allocator rounding, and the
resident JSON-page cache.

| average JSON/document | documents in 1 TiB | hot fixed directory | packed cold directory | each measured enum index |
| ---: | ---: | ---: | ---: | ---: |
| 1 KiB | 1.07 billion | 147 GiB | 60.3 GiB | 4.2 GiB |
| 4 KiB | 268 million | 36.7 GiB | 15.1 GiB | 1.05 GiB |
| 16 KiB | 67.1 million | 9.17 GiB | 3.77 GiB | 0.26 GiB |

For the 4 KiB example, a literal 100x page-cache ratio contributes another
10.24 GiB. About 32 GiB is therefore only a lower-bound mutable configuration
with a paged cold directory and a few compact indexes; 64 GiB is a practical
starting point, and 128 GiB buys a materially larger hot set. One-kilobyte
documents or many unique/high-cardinality and compound indexes can require
128–256 GiB even though JSON pages remain bounded. The current read-only file
adds common headers, compact key entries, radix references, and extent
rounding. A mutable file must additionally reserve copy-on-write generations,
persistent index/TTL dictionaries, reclamation lag, and free-space headroom;
no mutable disk multiplier is claimed before that churn benchmark exists.

## TTL

TTL metadata is writer-only: one pointer-free packed `(chunk, slot)` plus a
deadline in the indexed four-ary heap, and one integer-keyed position entry per
expiring row. It retains no key string. Changing a deadline updates that node
in place; `Persist` removes it. Delete removes the entry before its stable slot
can be reused, so repeated changes do not create stale generations or require a
slot-generation pointer.

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
plan, err := query.Select(query.Path("id"), query.Path("profile.geo.country")).Where(
	query.And(
		query.Cmp("tenant", query.Eq, "acme"),
		query.Cmp("profile.geo.country", query.Eq, "PT"),
	),
).Prepare()
if err != nil {
	return err
}

var result query.Result
var workspace query.Workspace
err = plan.RunSnapshotInto(&result, store.Snapshot(), &workspace)
```

The builder and `query.PrepareSQL` lower into this same typed `Plan`. SQL text,
lexer tokens, and builder nodes are absent from execution; paths and constants
are compiled once, and result columns are addressed by stable ordinals. This is
also the wire boundary: a future decoder can lower path/schema bytes and typed
constants into the plan without making SQL strings an intermediate format.

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
| no declared exact index | 2.24 us | 9.8 KiB | 12 |
| exact single index, tuple unchanged | 2.46 us | 9.9 KiB | 12 |
| exact compound index, tuple unchanged | 2.49 us | 9.9 KiB | 12 |
| exact single index, tuple changed | 2.84 us | 11.9 KiB | 18 |
| exact compound index, tuple changed | 3.03 us | 11.9 KiB | 18 |

On 4,096 documents, a warmed 10%-selective indexed `RunSnapshotInto` measured
12.44 ns/input document versus 67.2 ns/document for the same equality-filtered
`DocSet` scan (about 5.4x). A compound point query measured 2.82 ns/input
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
- `MappedImageBytes`, `ExternalKeyBytes`, `ExternalDocumentBytes`, and
  `ExternalIndexBytes`; mapped blocks remain RSS even when they do not
  contribute to Go `HeapAlloc`.

`Snapshot.AppendIndexes` returns reader-visible index name, kind, ordered paths,
state, covered chunks, and total chunks. `Snapshot.IndexStats` adds the physical
exact-index footprint. Alert on a stalled `Building` watermark, old retained
snapshots, or an event loop that leaves expired deadlines unpublished.

## Limits and comparison boundary

`ChunkDocuments` is in `[1,64]`. Chunk ids are `uint32`; `Put` returns
`ErrStoreTooLarge` before wraparound. Detailed source, tape, depth, retention,
and maintenance limits are in [`contracts/limits.md`](contracts/limits.md).

The mutable `Store` remains an in-memory API. `StorePageReader` is a public
immutable bounded-residency surface, not the automatic-durability path for
later mutations. The DuckDB harness compares two embedded
engines over identical key+JSON input, one execution lane, matching scalar
materialization, and verified results. It reports Store live heap and external
blocks, DuckDB's checkpointed file and WAL, and DuckDB's current warm and peak
buffer-manager bytes separately. A resident/file ratio would mix parsed state
with compressed durable storage and is therefore intentionally absent.

The frozen 10,000-row clustered smoke measures Store at 1.25x logical key+JSON
in settled heap plus owned external blocks. DuckDB records 5.75 MiB in its warm
buffer manager, 1.29x in its checkpointed database file, and a 98.35 MiB peak
during the run. Store's 4.85 MiB accounted state is 3% smaller than DuckDB's
5.01 MiB file and 15% smaller than its warm buffers on this corpus, but those
remain different accounting domains and neither is process RSS.

Store wins the frozen direct-operation timings because its surface is narrower:
an in-process key probe returns a stable-slot row without SQL parsing or plan
construction; exact filters and one-member containment start from 64-row
bitmap postings and recheck only candidates; numeric reduction and categorical
grouping fuse directly over compact structural tapes. DuckDB executes a general
vectorized SQL pipeline, and its low-cardinality ART may correctly choose a
scan. Store update/delete publish in-memory copy-on-write generations, whereas
DuckDB's per-key measurements include an ACID transaction and durability work.
That mutation ratio is not a durability-equivalent claim. DuckDB remains the
broader engine for durable SQL, joins, and general analytical plans. See the
[frozen run](../benchmarks/results/duckdb-synth-s4.md) and
[methodology](../benchmarks/duckdbbench/duckdb-methodology.md).
