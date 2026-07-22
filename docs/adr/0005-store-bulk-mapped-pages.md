# ADR 0005: one Store surface, bulk construction, and mapped pages

Status: accepted in stages. Transient construction, the caller-owned
Store-image boundary with pointer-free base metadata, dense stable-slot Boolean
workspaces, the scoped pure-Go Linux ring substrate, the bounded internal page
committer, and checksummed double-superblock recovery are implemented. Store
page encoding and attachment, the state-root schema, the swizzled read-page
manager, and bounded residency remain proposed. Extends ADR 0004 without
changing its borrowed-value lifetime contract.

## Decision

Make `Store` the primary collection surface for keyed, static, mutable, and
eventually mapped data. Keep the existing immutable document-set machinery as
an internal page-building engine until Store has capability and performance
parity; do not maintain two independent parsers, tape formats, shape compilers,
or query executors.

The migration has three ordered stages:

1. add a transient bulk Store builder that creates complete micro-pages, the
   key directory, and declared exact indexes before one atomic publication;
2. define a Store-compatible caller-owned mapped image with explicit lifetime
   ownership and ordinary mutable heap publications above its immutable base;
   and
3. deprecate the old set-facing API only after bulk, persistence, query, and
   memory-map users have a measured Store replacement.

Deleting the immutable core first is rejected. Store chunks already use it for
validation, tapes, shapes, sparse gathers, and exact rechecks. Reimplementing
those paths under a new name would increase code and correctness risk while
making Store slower.

## Performance target

The ordinary `Put` path is optimized for one online mutation: it validates one
document, path-copies persistent metadata, and publishes one generation. The
Redis harness measures about 155 MB/s when it intentionally loads 65,536 rows
through that path. Dense construction avoids republishing and path-copying per
row.

Bulk construction must therefore be a separate transaction, not a loop hidden
behind `Put`:

- parse and copy each document once into its final bounded page;
- build page-local shape/tape state in one forward pass;
- build the key directory through transient, uniquely owned nodes;
- sort or radix-partition index tuples once per page and bulk-build postings;
- freeze every object before it becomes reader-visible; and
- publish one immutable generation at commit.

The builder may allocate while growing. Caller-buffered input and result paths
remain zero-allocation after their capacities warm; the committed Store owns
all published memory.

The implemented builder fills final chunks, constructs the key HAMT and chunk
vector through uniquely owned transient nodes, and publishes the frozen Store
once. Declared nested and compound indexes collect sorted page-local tuples and
reuse the existing immutable bitmap/radix bulk constructors, so they are Ready
in that first reader-visible generation. On the 16,384-document microbenchmark
without a declared index, construction is about 7.7x faster than repeated
`Put` and reduces transient allocation bytes from 143 MiB to 8.9 MiB. Adding a
ready 16-value exact index costs about 1 ms and 0.2 MiB on that fixture.

## Key-directory choice and CHAMP

The in-memory key directory keeps fixed 32-way nodes on its cache-hot path and
a two-leaf bucket after 15 hash bits. The compiler inlines its complete lookup
loop. On the 65,536-key fixture, a local packed CHAMP prototype produced this
trade-off:

| directory | retained bytes | keyed lookup | allocation |
| --- | ---: | ---: | ---: |
| fixed prefix + two-leaf bucket | 9.17 MiB | 15.0-15.7 ns | zero |
| packed CHAMP prototype | 3.77 MiB | 17.7-18.6 ns | zero |

CHAMP reduced directory space by 59%, but lookup was about 20% slower and its
bitmap/data/node dispatch exceeded the compiler's inline budget. This is a
workload-specific result, not a rejection of CHAMP. The design from
[Steindorfer and Vinju](https://michael.steindorfer.name/publications/oopsla15.pdf)
uses separate data and node bitmaps, popcount rank, packed arrays, and canonical
delete compaction; those properties are particularly valuable for iteration
and sparse cold nodes. Bagwell's original
[Ideal Hash Trees](https://infoscience.epfl.ch/entities/publication/b892b2ce-7bf0-41d2-b68c-fb44a3c64a33)
also applies array-mapped tries to external blocks.

Consequently:

- keep the faster fixed-node directory for hot heap-resident point reads;
- evaluate CHAMP for the mapped logical-page directory and sparse cold index
  roots, where footprint and block density dominate a few extra instructions;
- require an end-to-end Store benchmark, retained-byte measurement, compiler
  inlining report, and churn test before changing either representation.

## Implemented Store-image boundary

`Store.WriteTo` now writes one immutable generation as a Store container around
the existing bounded `DocSet` page images. Its checksummed tail manifest holds
effective options, generation, stable slot/key maps, reusable empty page ids,
Ready index definitions, wildcard posting consumers, and TTL deadlines.
`OpenStore` validates that directory, views source/native tape sections in the
caller's image, constructs a process-seeded Swiss-style base key directory and
32-byte row descriptors in pointer-free anonymous memory, rebuilds exact-index
roots through the normal bulk constructors, and returns a Store that can
immediately be updated or deleted from. The persistent HAMT is only a post-open
mutation delta. Subsequent publications retain the mapped base owners through
the immutable state graph.

`WriteTo` is a full checkpoint/export, not the transaction persistence path.
It writes every live micro-page, and mutations after `OpenStore` do not update
the input image. Keeping this API is useful for backups, interchange, and a
bounded recovery base; requiring it after every mutation is rejected.

This stage deliberately does not own or unmap the caller's bytes. The mapping
must outlive the Store, retained snapshots, and derived borrowed handles.
Caller-buffered `AppendRaw` and `AppendRawKey` provide lifetime-independent
copy-out with zero allocation when capacity is sufficient. A `Building` index
cannot be serialized because restoring partial coverage would make latency and
fallback behavior image-dependent.

The 16,384-document fixture produces a 5.40 MB image. A key-only mapped open
takes 1.04-1.05 ms and allocates 234,688-234,689 Go-heap bytes in 273
allocations, plus 688,136 pointer-free external key bytes and 524,288 external
row bytes. Against the earlier per-key HAMT/per-row reopen, this is about 93%
less Go heap, 98.6% fewer allocations, and 40% lower open latency. One compound
exact index raises open to 2.64-2.67 ms and about 450.6 KB Go heap because its
root is not mapped yet. This is a useful off-heap payload/startup boundary, but
the remaining eager validation and root reconstruction are explicit evidence
that it is not the final greater-than-memory representation.

After open, the same mapping measured 9.22-9.29 ns for ordinary keyed reads,
4.63-4.66 ns for compiled generation-pinned stable-slot reads, and 2.55-2.61 us
for a zero-allocation compound query selecting 32 rows from two of 256
micro-pages. These are hot-mapping measurements, not promises for storage
faults.

Writing the 5.40 MB fixture measures 1.07-1.09 ms (4.96-5.04 GB/s) and three
allocations total. Generated-code inspection showed that passing stack record
headers through `io.Writer` originally created per-document escapes. Fixed
writer-owned scratch, bounded page manifests, and relative-offset rebasing now
stream every nested page without per-row or per-page heap objects.

## Greater-than-memory mode

The current Store image can be larger than physical memory as a virtual
mapping, but `OpenStore` still validates every key and row, allocates external
metadata proportional to both, and rebuilds distinct shapes and exact roots
from documents. It therefore cannot promise a bounded resident working set or
a 100x-RAM corpus yet. The mapping is bounded by virtual address space, Go's
`int` slice length, the image format, and existing per-document 32-bit
coordinate limits.

The engineering target for mapped Store is a data image at least 100 times the
configured resident budget, provided the workload's hot key/index/page working
set fits that budget. The multiplier is not a latency guarantee: a cold random
read must pay a storage fault. Selective indexed queries can nevertheless beat
a heap engine that scans more data by proving non-candidate pages from compact
resident summaries and never faulting their JSON bytes.

Keep residency controllable by separating four temperature classes:

1. a very small pinned superblock and upper key/index directory;
2. bounded swizzled frames for directory, posting, and document pages;
3. immutable background-storage pages admitted through explicit asynchronous
   reads and bounded prefetch; and
4. append-only replacement pages plus reclaimable free extents.

The Store page manager, rather than virtual mapping size, owns the resident byte
budget and replacement policy. A resident logical-page reference is pointer-
swizzled so its hot path is one predictable state check and a direct frame
pointer, not a hash-table lookup. Cold references retain their physical page
id and enter the I/O scheduler. The implementation must expose resident bytes,
page reads, prefetch hits, dirty bytes, evictions, and queue depth. A 100x corpus
is accepted only when steady hot-set RSS remains within the configured budget.

`OpenStore` may continue to mmap a read-only checkpoint for simple restart and
hot, read-mostly workloads. It is not the primary 100x transactional backend.
Relying on demand faults would block arbitrary goroutines, surrender admission
and eviction control, complicate I/O error propagation, and couple the disk
encoding to virtual-memory behavior. The automatic writer therefore uses
explicit append I/O and durability barriers; read-only mmap is an optional
access mode, never the correctness mechanism.

The proposed mutable mode is page-oriented copy-on-write, not a heap Store
whose byte slices happen to come from mmap. A logical micro-page contains:

- a generation, checksum, format version, and exact byte bounds;
- at most 64 stable slots and a dense live-row directory;
- immutable source bytes plus classic or shape-deduplicated tapes;
- page-local exact-index tuple masks; and
- no pointers, capacities, or runtime-specific object layouts.

A small durable root names logical pages by immutable physical page id. Point
lookup resolves key to `(logical page, stable slot)`, validates the page header,
and returns a scoped view. Query planning performs `AND`, `OR`, and `NOT` on
page masks before touching source pages, so a selective query reads only
candidate pages. Sequential scans submit physically ordered, bounded read
batches rather than triggering one synchronous fault per page.

Cold directory levels use packed CHAMP-style nodes with page-relative offsets
rather than Go pointers. Hot upper levels are swizzled into direct frame
pointers and may use the existing fixed fan-out form when measurement justifies
it. Posting streams are ordered by logical page, so
Boolean operators merge compressed page ids and apply one native 64-bit mask
per page; candidate page ids are then sorted by physical offset and prefetched
in bounded windows. Projection-only queries may be answered from compact
indexed scalar payloads without reading the source page, but every API that
claims exact JSON spelling still visits and verifies the source.

Large values are stored in separately checksummed overflow extents. This keeps
ordinary 64-slot metadata pages small enough for useful fault granularity and
prevents one outlier document from dragging unrelated rows into memory. Page
size, directory cache size, prefetch depth, and overflow threshold are format
or Store options with conservative defaults; none may change query semantics.

## Publication and crash consistency

An attached file persists automatically through one background writer. Store
mutation is already serialized, so publication can place a generation/change
descriptor into a preallocated single-producer ring without another mutex or a
steady heap allocation. Readers never load that queue or a durability counter.
The consumer groups adjacent generations and executes the copy-on-write commit
below once per batch.

Async mutation returns after reader publication; `DurableGeneration` reports
the newest crash-safe root and `Flush`/`Close` waits until a requested
generation reaches it. A synchronous option waits after each mutation. It
cannot be zero-latency because ordered storage writes and a durability barrier
are physical work. Queue saturation applies backpressure instead of silently
dropping durability records or retaining unbounded historical states. Any
background I/O error becomes sticky, stops durable-generation advancement, and
is returned by fencing and subsequent persistence-aware mutations.

One transaction follows a failure-atomic sequence:

1. validate replacements and build complete new page images in unused slots;
2. write checksums and monotonically increasing page versions;
3. persist the new data pages;
4. copy-on-write the changed key/index directory paths;
5. persist a new root descriptor; and
6. atomically select the descriptor through a checksummed double superblock.

The writer does not mutate durable structures through a writable mapping.
Conventional files use explicit positional writes followed by the platform's
data-integrity barrier (`fdatasync` on Linux, `File.Sync` elsewhere), then
persist the alternate superblock. The implemented internal committer accepts
already-encoded page images, recycles a fixed buffer/descriptor budget through
ABA-resistant tagged free lists, and publishes work through a preallocated
single-producer/single-consumer generation ring. Its uncontended
`Begin`/fill and publication into the generation ring are lock-free and
zero-allocation while capacity is available. Recycling and worker notification
touch no channel on their ready/busy fast paths; exhausting capacity or waking
an actually parked worker enters the runtime's blocking machinery. `Wait` and
`Flush` are explicit blocking durability fences.
On the Apple M4 Max development host, recycling one index through the tagged
pool measured 8.70-8.79 ns versus 16.47-16.74 ns through a capacity-one Go
channel, both at zero allocations. This isolates control-plane reuse and does
not include storage latency. Reserving and aborting a one-page batch—one
descriptor plus two buffers—measured 16.17-16.38 ns and zero allocations.

The committer drives either the portable device or the pure-Go Linux ring
device. The latter uses no cgo and owns one locked writer thread, registered
files, anonymous off-heap fixed buffers, fixed-buffer I/O, runtime opcode
probing, and explicit completion/overflow checks. It writes all data pages,
passes a data barrier, writes only the newest grouped root, and passes the final
barrier before advancing `DurableGeneration`. Unsupported kernels and sandbox
policies fall back to the portable device. SQ polling and direct I/O remain
opt-in benchmark decisions because each changes CPU, memory-lock, or alignment
economics. This cannot change commit semantics.

The internal root layer now writes a deterministic 128-byte record into one of
two page-isolated slots selected by generation parity. CRC32C plus stored
complements covers torn checksums and generation fields; a 128-bit Store id
rejects roots copied from another file; page-aligned extents and the exclusive
file high-water mark are checked before use. Recovery reads both slots, rejects
a valid record in the wrong parity slot, verifies the referenced state and
free-tree root bytes, and falls back to the older generation if the newest
header or either root page is torn, truncated, or corrupt. Caller-owned page
scratch bounds recovery memory, and the encode/decode/select hot paths allocate
zero bytes. The checksum implementation is scoped to `internal/storeio` and
contains no handwritten assembly: stable builds use Go's hardware-aware
CRC32C, while SIMD builds runtime-gate pure-Go PMULL on arm64 and AVX-512
VPCLMULQDQ on amd64 before falling back to the same standard path. On M4 Max,
stable Go measured 383.3-387.5 ns per 4 KiB page and 5.924-6.296 us per 64 KiB
page. The pure-Go nine-stream PMULL fold measured 89.17-91.66 ns and
1.131-1.146 us respectively: about 4.2x and 5.5x faster, with zero allocations.
Native CI retains stable and SIMD samples for x64 and arm64 separately; amd64
claims wait for those native measurements rather than cross-compiled evidence.

This is deliberately still internal: Store does not yet encode its changed
micro-pages and copied key/index/TTL paths into these buffers, nor decode the
referenced state-root schema. Therefore ordinary `Put`, `Delete`, and TTL
operations are not yet automatically durable. Read-only checkpoint mappings
never contain dirty transactional state.

The old root stays valid until the final step. Recovery chooses the newest
valid superblock and ignores unreferenced partial pages. This follows the
well-understood failure-atomic property of copy-on-write page propagation; see
the analysis in
[Building blocks for persistent memory](https://link.springer.com/article/10.1007/s00778-020-00622-9).
A WAL is still required if the durability contract must acknowledge a sequence
of transactions before their full page graph is durable; mmap alone is not a
durability protocol.

## Delete and space reclamation

Delete builds the affected page without the row and publishes a new page id.
Deleting the final row removes the logical page mapping. Readers see neither a
tombstone nor a version walk, and current-page scan density is restored by the
same operation.

Old physical pages cannot be reused until no active snapshot can reach their
generation. Reclamation is page-granular:

- snapshot leases publish the oldest reachable generation;
- retired page ids enter generation buckets;
- bounded maintenance transfers safe ids to a persistent free-page tree; and
- new writes consume safe free pages before extending the file.

This avoids global data compaction and reclaims complete obsolete versions;
it does not promise that the backing file automatically shrinks. Returning
free extents to the filesystem or eliminating long-lived physical
fragmentation is an optional relocation operation and must remain bounded.
Snapshot-aware copy-on-write storage necessarily pays either retained old
pages or reclamation work; hiding that cost would be incorrect.

## Lifetime-safe reads

The existing `RawValue` and `Index` types are borrowed handles with no mapping
owner. They cannot safely escape an automatically unmapped Store. The mapped
surface must therefore use one of these explicit contracts:

- a `ReadLease` pins a mapped generation; all borrowed values become invalid
  only after the lease closes;
- callback-scoped `ViewRaw` keeps a lease for the callback duration; or
- `AppendRaw(dst, key)` copies into caller-owned capacity and returns no mapped
  view.

The copy-out form can be zero-allocation with a sufficient destination but is
not zero-copy. An owner pointer added to every hot value handle is rejected
until its size and latency are measured. Finalizers are a leak backstop, never
the correctness mechanism for unmapping.

The explicit page manager makes this contract load-bearing: an evictable frame
cannot back an unowned `RawValue`. Attached-file mode must therefore pin frames
through a `ReadLease`/snapshot lease or require caller-buffered copy-out. A hot
resident lease may be very cheap, but it is measured separately from the
existing heap Store's 5 ns compiled-key path.

## Research basis and rejected shortcuts

[LeanStore](https://db.in.tum.de/~leis/papers/leanstore.pdf) demonstrates the
relevant low-overhead buffer-manager technique: pointer swizzling reduces a
resident page access to a predictable check while preserving explicit global
replacement beyond RAM. Store adopts that direction for logical pages, but its
stable 64-slot masks and immutable publication remain specific to JSON queries.

The CIDR paper
[Are You Sure You Want to Use MMAP in Your Database Management System?](https://db.cs.cmu.edu/papers/2022/cidr2022-p13-crotty.pdf)
documents why demand paging is rejected as the automatic transactional I/O
scheduler: blocking faults, insufficient memory/I/O control, error-handling
problems, transactional complexity, and poor scaling on fast storage.

[FASTER](https://www.microsoft.com/en-us/research/uploads/prod/2018/03/faster-sigmod18.pdf)
supports the hot-index/hybrid-log split for larger-than-memory point workloads;
[LLAMA/Bw-tree](https://www.microsoft.com/en-us/research/publication/llama-a-cachestorage-subsystem-for-modern-hardware/)
supports separating logical from physical page location while log-structuring
flushes. Store does not copy their delta-chain read paths: bounded micro-pages
and copied roots keep a current read free of version-chain consolidation debt.

The Linux
[`io_uring_setup`](https://man7.org/linux/man-pages/man2/io_uring_setup.2.html),
[`io_uring_enter`](https://man7.org/linux/man-pages/man2/io_uring_enter.2.html),
and [registered-buffer](https://man7.org/linux/man-pages/man7/io_uring_registered_buffers.7.html)
contracts define the ring mappings, submission/completion ordering, runtime
features, and long-term buffer pins used by the scoped substrate. Ring memory
mapping is control-plane queue sharing; Store data remains under explicit page
I/O rather than demand-paged writable mappings.

## Query and TTL consequences

Declared nested and compound indexes keep the same scalar fingerprint and
mandatory exact-recheck semantics. Current postings already expose sparse
page-level masks plus caller-owned dense words with one bit per stable slot.
Two- and fused three-input Boolean kernels use runtime-gated 256-bit AVX2 on
amd64 when it wins; sub-eight-word buffers and selective sparse streams stay
scalar. The external physical
posting becomes a hierarchy of the same page masks. That executor must expose
page-fault and bytes-touched metrics in addition to nanoseconds and result
counts.

TTL remains publication-based. Deadlines can live in a compact writer-side
heap for the active write process or in a persistent deadline index, but an
ordinary snapshot read still performs no clock access or expiry branch. Expiry
publishes the same page-level deletes as an explicit mutation.

At 100x-RAM scale, one heap node per expiring key cannot be assumed resident.
Use persistent deadline pages partitioned by coarse time bucket, with only the
near-term bucket frontier and its mutable four-ary heap cached in memory.
Changing TTL removes and inserts one keyed deadline record in the same
copy-on-write transaction as metadata publication; it never adds a stale
generation. Far-future buckets stay mapped and cold. Reads still pay zero TTL
instructions, while expiry cost is proportional to due records and touched
document pages rather than total expiring keys.

## Acceptance gates

The mapped Store is not complete until it passes:

- differential results against the heap Store for nested/compound indexed
  queries, updates, deletes, TTL changes, and retained snapshots;
- crash injection at every persist boundary and corrupt-page fail-closed tests;
- mapping-lifetime tests under forced GC, race, and `checkptr`;
- resident-set, Go-heap, page-fault, read-amplification, write-amplification,
  and file-fragmentation measurements;
- working sets below, near, and above physical RAM; and
- bounded reclamation under a deliberately long-lived snapshot.

Until those gates pass, `OpenStore` is a caller-owned off-heap payload boundary,
not a bounded-residency or durable mapped database. `Open` remains the
lower-level `DocSet` image mechanism used inside it.
