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
| `NewStoreBuilder` + `Append` + `Build` | bulk-validates unique keys into final pages; publishes one Store | O(total input + transient key-radix construction) |
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
There is no compaction command or compaction threshold.

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

`Append` validates and copies the JSON and clones the key; caller buffers may
be reused as soon as it returns. Keys must be unique. An invalid document or
duplicate changes no committed row. The builder belongs to one goroutine and
closes after `Build`; the returned Store is safe for concurrent use and has the
same update, delete, TTL, snapshot, and index behavior as any other Store.
`CreateIndex` may be called before or after appends. Its one-to-four nested or
compound paths are extracted at `Build`; the returned index is `Ready` in the
first reader-visible generation, with no scan fallback window.

The builder fills final micro-pages and mutates only unpublished key/chunk
radix nodes. `Build` freezes that graph and performs one publication instead of
path-copying it once per row. On the 16,384-document benchmark fixture it
measured 4.57-4.76 ms (206-214 MB/s) versus 35.8-37.1 ms (26.4-27.3 MB/s) for
repeated `Put`: about 7.7x the throughput, with 8.9 MiB rather than 143 MiB of
transient allocation bytes. Including a ready 16-value exact index measured
5.70-5.86 ms (167-172 MB/s) and 9.2 MiB. Run `BenchmarkStoreBulkBuild` for the
exact local result.

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
evidence, not Redis command latency claims.

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

## Store images and mapped source bytes

Heap-built Store source arenas are `[]byte` objects. They contain no pointers,
so the GC does not scan each JSON byte, but they still count as live heap and
therefore affect the pacer and heap target. A mapped image lowers Go
`HeapAlloc`; it does not make those bytes disappear from RSS or the Store's
total memory footprint.

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
ordinary keyed reads measured 9.22-9.29 ns and generation-pinned compiled reads
4.63-4.66 ns. A nested two-column exact query selecting 32 documents from two
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

Supporting a corpus around 100 times larger than RAM while keeping a bounded
hot set needs the next four changes:

1. address immutable chunks by logical page id and pointer-swizzle resident
   frames, reducing a hot hit to one predictable state check and a direct
   pointer;
2. store packed key, exact-index, posting, and TTL nodes in the same page
   space, keeping only measured hot upper paths resident;
3. issue explicit asynchronous page reads and physically ordered prefetch under
   a Store-owned byte budget, exposing reads, bytes, hits, queue depth, dirty
   bytes, and evictions; and
4. publish replacement pages and roots through append-only copy-on-write with
   snapshot-aware extent reclamation and checksummed root selection.

Read-only mapping remains useful for the implemented checkpoint and a hot,
read-mostly corpus. It is not the automatic 100x transactional scheduler:
demand faults can block arbitrary goroutines and do not provide Store-level
admission, eviction, prefetch, or error control. ADR 0005 records the primary
research basis and the explicit-I/O, swizzled-buffer-manager decision.

The intended attached-file mode is automatic without taxing readers. The
serialized Store writer is already single-threaded, so it can enqueue a
generation into a preallocated single-producer ring without another lock or a
heap allocation. A background writer appends changed pages and copied
directory/index/TTL paths, group-commits their root, and advances a durable
generation counter. `Flush`/`Close` fence a requested generation. A synchronous
option waits on each mutation and necessarily pays storage latency. An async
`Put` is reader-visible before it is crash-durable; hiding that distinction
would be an incorrect safety contract.

The internal I/O half of that contract is implemented. It has a lock-free SPSC
generation ring, ABA-resistant lock-free buffer/descriptor recycling, bounded
backpressure, sticky failures, natural group commit, and a zero-allocation
`Begin`/fill/`Publish`/`Wait` test. Linux uses a scoped pure-Go `io_uring`
backend with registered off-heap buffers and files; other systems, unsupported
kernels, and blocked sandboxes use positional writes and data-integrity
barriers. The root is submitted only after every data page succeeds. This is
not yet a public Store persistence mode. The internal double-superblock codec
now enforces generation-slot parity, Store identity, CRC32C/complement checks,
page-aligned bounds, file high-water bounds, and referenced-state verification;
recovery validates both state and free-tree roots and falls back when the
newest root or either referenced page is torn. Mutation attachment, key/TTL
payload schemas, large-value overflow, and extent reclamation are the remaining
correctness boundary. The common page envelope binds a 64-byte
pointer-free header and full-page CRC32C trailer to Store id, kind, stable
logical id, and generation. Its fixed 256-byte state root separates chunk, key,
exact-index, and TTL directory references, and recovery verifies each
referenced top-level page before generation selection.

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
TTL changes. Apple M4 Max, stable Go, one run:

| Records | Build docs/s | Source B/doc | Live heap B/doc | Heap objects/doc | Packed document extents B/doc | Packed exact indexes B/doc | Point read | Compound query |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 10,000 | 1.42M | 92.9 | 520.5 | 0.256 | 128.2 | 7.27 | 19 ns | 4.15 us / 39 masks |
| 100,000 | 1.48M | 94.9 | 521.0 | 0.251 | 128.0 | 4.45 | 30 ns | 48.18 us / 390 masks |
| 5,000,000 | 0.98M | 98.7 | 521.1 | 0.251 | 128.0 | 4.16 | 55 ns | 9.20 ms / 19,531 masks |

At 5M rows, keys plus JSON are 493,480,886 bytes. Packed owned keys and the two
exact-index bases reduce the current heap from the earlier 3,995,694,760 bytes
and 13,246,371 objects to 2,605,486,408 bytes and 1,252,619 objects: 65.2% of
the original heap remains, 90.5% of the objects are gone, and point lookup is
faster. Actual packed index storage
is 20,798,476 bytes (4.16 B/doc), within 0.04% of the independent 20,791,296
byte page projection. The still-unattached variable document extents project
to 640,000,000 bytes (1.30x source); document plus posting extents are about
1.34x source before key/value directories, TTL/free-space metadata, roots,
allocator slack, and retained generations.

The remaining 0.251 objects/doc is not accepted as the endpoint. A diagnostic
reopen through the existing pointer-free checkpoint reached 0.016 objects/doc
at 100k rows—more than 100x below the original 2.665—but its legacy tape image
was 5.45x source and made point/index operations slower. That path is evidence
that the GC target is feasible, not the selected format. Acceptance requires
at most 0.027 objects/doc with the new packed extents and no hot-path
regression; attaching compact document/tape frames is the remaining dominant
work.

Across the same 5M run, indexed update averaged 4.17 us, delete plus reinsert
7.95 us, and changing an existing TTL 42 ns. The point-read rise from 19 ns to
55 ns is a cache-footprint result, not an algorithmic complexity change. Query
time scales with the number of exact result masks because this smoke
materializes them; a Boolean consumer can combine those stable-slot words
without decoding rejected documents.

### Capacity planning for 1 TiB

There are two different answers until the paged mode is complete. The current
heap Store measured 62.4 MiB for 25.0 MiB of clustered source JSON with one
low-cardinality exact index: 2.50 live heap bytes per source byte. A linear
1 TiB extrapolation would therefore require about 2.50 TiB of live heap. That
is workload evidence, not a universal multiplier, but it makes clear that the
current implementation is not a 1 TiB-on-64-GiB database.

The bounded paged target sizes RAM from the hot working set rather than total
JSON. The following directory estimates extrapolate the measured 65,536-key
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
10.24 GiB. About 32 GiB is therefore only a lower-bound configuration with a
paged cold directory and a few compact indexes; 64 GiB is a practical starting
point, and 128 GiB buys a materially larger hot set. One-kilobyte documents or
many unique/high-cardinality and compound indexes can require 128–256 GiB even
though JSON pages remain bounded. Disk must additionally hold page headers,
index/value dictionaries, copy-on-write generations awaiting reclamation, and
free-space headroom; no fixed disk multiplier is claimed until the durable page
format and churn benchmark exist.

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
- `MappedImageBytes`, `ExternalKeyBytes`, and `ExternalDocumentBytes`; mapped
  bytes remain RSS even when they do not contribute to Go `HeapAlloc`.

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
compares a keyed Store plus a matching declared exact index over identical
corpora, while retaining DocSet-only rows as representation diagnostics. The
latest local 65,536-document clustered run measured 62.4 MiB live Store heap
including its 0.5 MiB exact index, versus 62.0 MiB RedisJSON keyspace plus a
17.5 MiB RediSearch index: 0.79x as many accounted bytes. Store load was 1.89x
faster, exact-index build about 415x faster, point projection 112x faster,
indexed filter 1.92x faster, group-by 24.1x faster, and SUM 47.8x faster.

Those are same-hardware single-core results, not a claim of server equivalence:
Redis ran in a Linux container, Store ran natively, Redis scenario time came
from SLOWLOG without client round-trip cost, and Store has none of the services
listed above. Match durability, protocol/client time, document distribution,
index definitions, and expiry semantics before applying the ratios. See
the [frozen run](../benchmarks/results/redis-synth-s4.md) and
[`benchmarks/redisbench/redis-methodology.md`](../benchmarks/redisbench/redis-methodology.md).
