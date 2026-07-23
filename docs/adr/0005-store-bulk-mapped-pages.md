# ADR 0005: heap Store, mapped checkpoints, and attached FileStore

Status: accepted and implemented for a separate attached `FileStore` surface.
Transient heap construction and mapped checkpoints remain on `Store`;
incremental durability uses explicit page I/O, bounded CLOCK residency with
buddy span allocation, copy-on-write key/chunk/index/TTL/free trees, overflow
extents, alternating superblocks, snapshot generation leases, and persistent
reclamation. Parallel file scans, direct writes, external query spill, and a
100x physical-memory correctness gate are implemented. Online file-index DDL,
distributed execution, and equal cold/resident latency remain out of scope.
Extends ADR 0004 without changing heap `Store` borrowing.

## Decision

Keep `Store` as the low-latency heap collection and mapped-checkpoint surface.
Use a distinct `FileStore` for incremental durability and bounded residency,
because its explicit leases, I/O errors, copy-out reads, and file ownership
cannot be added honestly to the heap API without taxing or weakening it. Both
reuse the existing validator, structural index, compiled pointers, exact scalar
semantics, stable-slot masks, and query planner; do not maintain independent
JSON parsers or query languages.

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
document, path-copies persistent metadata, and publishes one generation. On
the 16,384-document fixture, repeated `Put` measures about 26-27 MB/s. Dense
construction avoids republishing and path-copying per row and measures about
206-214 MB/s on the same fixture.

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

The mapped `OpenStore` checkpoint still validates every key and row and retains
metadata proportional to the corpus. `FileStore` is the bounded-residency path:
open validates only alternating roots and their referenced top-level pages;
lower key, chunk, index, TTL, and free nodes enter the page cache lazily. Corpus
size is therefore no longer a Go-heap or eager-open requirement. It remains
bounded by the on-disk ids/offsets and configured maximum key/document sizes.

This establishes a controllable working set, not a universal 100x-RAM latency
claim. A cold random read pays device latency. A corpus 100 times physical
memory is useful only when the hot key/index/page set fits that budget or the
workload has scan locality. One gate covers source key+JSON at 106.4x the
configured page cache; a second cgroup-constrained gate covers live source and
allocated filesystem blocks at more than 100x complete process memory. Both are
described below. Neither changes the API contract into a cold-latency promise.

Residency separates four temperature classes:

1. two fixed superblocks and the currently selected root metadata;
2. bounded cache-quantum spans for directory, posting, and document pages;
3. immutable background-storage pages admitted through explicit asynchronous
   reads and bounded prefetch; and
4. append-only replacement pages plus reclaimable free extents.

The page manager, rather than virtual mapping size, owns a fixed resident-byte
budget and CLOCK replacement. Its anonymous arena is divided by `PageSize`; a
4 KiB metadata node occupies one slot while a 16 KiB document occupies four.
Whole extents are admitted and evicted together. Lookup uses a pointer-free
atomic table and one pointer-free 64-byte control record per slot. Intrusive
pointer-free buddy lists split and coalesce free power-of-two spans without
rescanning the arena. The resident hit path locks only the selected frame; the
global lock is reserved for misses, admission, eviction, and statistics. Cold
references retain physical offset, length, logical id, and generation and
enter bounded read workers.
On M4 Max, a filled 1,048,576-quantum allocator geometry measured
3.73-3.77 ns per maximum-span release/reacquire with zero allocations; this is
control-plane evidence, not device-I/O latency.

`FileStore.Stats` exposes resident/dirty bytes, reads, cache/prefetch hits,
evictions, queue depths, durable generation, snapshots, retired extents, and
reusable extents.

`OpenStore` may continue to mmap a read-only checkpoint for simple restart and
hot, read-mostly workloads. It is not the primary 100x transactional backend.
Relying on demand faults would block arbitrary goroutines, surrender admission
and eviction control, complicate I/O error propagation, and couple the disk
encoding to virtual-memory behavior. The automatic writer therefore uses
explicit append I/O and durability barriers; read-only mmap is an optional
access mode, never the correctness mechanism.

The attached mutable mode is page-oriented copy-on-write, not a heap Store
whose byte slices happen to come from mmap. A logical micro-page contains:

- a generation, checksum, format version, and exact byte bounds;
- at most 64 stable slots and a dense live-row directory;
- immutable source bytes plus classic or shape-deduplicated tapes;
- page-local exact-index tuple masks; and
- no pointers, capacities, or runtime-specific object layouts.

A small durable root names logical pages by immutable physical references.
Point lookup resolves a key to `(logical chunk, stable slot)`, validates the
document page, and returns a scoped view. Explicit exact-index probes produce
page masks without scanning rejected JSON. The general query executor
late-binds the frozen catalog, chooses the widest compound equality bound,
combines bounded `AND` and fully bounded `OR` plans, and rechecks the complete
predicate over every candidate for row-producing plans. A fully indexed
`COUNT(*)` consumes certified exact masks directly and reopens documents only
for ambiguous streams. Unbounded plans retain the ordered physical chunk scan.
Direct sequential scans and key prefetch submit bounded, physically ordered
work rather than relying on demand faults.

Chunk-directory levels use implemented 64-way packed CHAMP nodes with one
occupancy word and densely ranked fixed-width physical references rather than
Go pointers. Admitted pages are found in the bounded cache; cold references
enter its read workers. Posting keys are ordered by index, tuple hash, and
logical chunk and carry one native 64-bit mask. Every exact probe that returns
rows verifies complete scalar values either from a collision-free posting
certificate or, for an ambiguous stream, from the candidate documents.

The implemented document payload makes slot identity implicit in one 64-bit
live word and stores only cumulative key and JSON ends: eight directory bytes
per live row and one canonical packed byte stream. Admitted point reads use one
bitmap probe and popcount; complete key comparison remains mandatory on a
fingerprint hit. Metadata nodes use a 4 KiB allocation quantum, while a
document `PageRef` can name a larger power-of-two extent. This covers ordinary
variable-size chunks without forcing every sparse node to the maximum size.
Format v2 may append a frozen set of typed float64 covers to that same
micro-page: one stable-slot mask per RFC 6901 path followed by only the finite
values selected by the mask. JSON bounds remain capacity-clipped before the
cover section. Compact groups route to detached typed extents instead: one
extent may cover multiple document groups in a bounded allocation micro-region.
Each column independently selects exact unsigned 8-, 16-, or 32-bit values
when every value fits, and otherwise stores IEEE float64. Neither representation
adds a Go pointer per row or key.

Compact generation creation can instead select a `PageDocumentGroup` when its
rounded physical extent is strictly smaller than the same consecutive chunks
as independent pages. A group contains at most 128 rows while preserving the
configured logical chunk and stable-slot boundaries. The chunk tree may
therefore map several consecutive lanes to one immutable physical reference;
ordinary online pages remain one reference per chunk.

The group payload has five independently bounded layers:

1. a fixed chunk directory with live masks and packed row ranges;
2. one row directory containing cumulative key/body ends, decoded length,
   template id, and stable slot;
3. exact key bytes and per-row scalar token streams;
4. page-local structural templates plus at most 128 profitable complete-scalar
   dictionary values; and
5. a bounded reference to the shared typed sidecar; finite masks and values
   remain directly addressable there.

Dictionary ids occupy tokens 0 through 127. Tokens 128 through 254 encode
literal lengths 1 through 127 directly; token 255 carries the longer canonical
uvarint length. This removes one length byte from the common literal without
changing exact JSON spelling. Candidate strings are read-only views of the
source during construction, so a reused workspace does not allocate per
scalar. A caller-sized output buffer makes point reconstruction
zero-allocation. Page admission validates every directory, token length,
decoded length, finite column, common CRC32C, and reconstructed JSON before the
extent becomes visible.

The grouped page is an immutable base, not the online mutation unit. Updating
or deleting one row reconstructs only its bounded logical chunk as an ordinary
document page. Other chunk lanes continue to reference the group. A bounded
radix-range check visits each covered 64-lane leaf once and retires the group
exactly when its final mapping is peeled; generation leases still fence
physical reuse. Sequential scans and numeric reductions coalesce equal
references and acquire each group or shared sidecar once, including runs
crossing a radix-leaf boundary. A sidecar is retired only when its final
deriving chunk is peeled. Grouping is currently a bulk/compact-generation optimization:
correctness, scan density, and space reclamation do not require regrouping
after online churn, but recovering its best compression after widespread
updates requires creating another compact generation.

The clean state root may additionally name a checksummed catalog of contiguous
aggregate-only stripes. A stripe stores dense adaptive typed values but no
stable-slot masks or JSON, and covers ordinary, overflow-backed, and grouped
chunks alike. Predicate-free numeric aggregates can therefore avoid both the
chunk tree and document extents. The first document mutation clears this root
field, coalesces the now-unreachable stripe/catalog run into bounded retirement
metadata, and uses the authoritative sidecar/document overlay path. TTL-only
publications retain it. Recovery validates physical and logical ordering,
exact chunk coverage, per-column
encoding, CRC32C, and root generation before selecting the shortcut.

Values beyond the configured inline extent use a checksummed overflow chain
whose descriptor binds total length and owner slot. Directory cache size,
prefetch depth, and overflow threshold are `FileStoreOptions`; none changes
query semantics.

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
policies fall back to the portable device. SQ polling remains an opt-in
benchmark decision. Reads and writes independently offer buffered, try-direct,
and require-direct modes. Each Linux direct lane reopens the same inode through
`/proc/self/fd` with `O_DIRECT`, leaving the caller-owned descriptor and flags
unchanged. The direct writer feeds either commit device and prevents sustained
ingestion from populating the kernel page cache. Cache and staging arenas plus
all page offsets and lengths are at least 4 KiB aligned.
`Stats.DirectReads` and `Stats.DirectWrites` make fallback observable. These
choices cannot change commit semantics.

The internal root layer now writes a deterministic 128-byte record into one of
two page-isolated slots selected by generation parity. Commits clear and write
the complete root page so direct I/O is aligned and no stale tail survives;
recovery still decodes the fixed 128-byte prefix. CRC32C plus stored
complements covers torn checksums and generation fields; a 128-bit Store id
rejects roots copied from another file; page-aligned extents and the exclusive
file high-water mark are checked before use. Recovery reads both slots, rejects
a valid record in the wrong parity slot, verifies the referenced state and
free-tree root bytes, and falls back to the older generation if the newest
header or either root page is torn, truncated, or corrupt. Caller-owned page
scratch bounds recovery memory, and the encode/decode/select hot paths allocate
zero bytes. The checksum implementation is scoped to `internal/storeio` and
contains no handwritten assembly. Stable builds use Go's hardware-aware
CRC32C. SIMD builds dispatch pure-Go PMULL on Darwin ARM64, where it wins, and
retain the standard path on Linux ARM64 and amd64. Native Ubuntu ARM64 measured
PMULL at 192.3-192.4 ns per 4 KiB versus 154.6-154.8 ns standard. AMD EPYC 7763
measured the ordinary PCLMUL candidate at 323.0-323.2 ns versus
170.7-170.8 ns standard. Both losing dispatches are therefore rejected. The
pure-Go amd64 PCLMUL and AVX-512 bodies remain directly correctness- and
ISA-tested candidates; feature availability alone is not evidence of a win.
On M4 Max, stable Go measured 383.3-387.5 ns per 4 KiB page and
5.924-6.296 us per 64 KiB page. The pure-Go nine-stream PMULL fold measured
89.17-91.66 ns and 1.131-1.146 us respectively: about 4.2x and 5.5x faster,
with zero allocations. Native CI retains stable and SIMD samples for x64 and
arm64 separately. Emulation proves correctness and instruction coverage, not
performance.

Every attached-Store page now has a deterministic 64-byte pointer-free header
and an eight-byte CRC32C/complement trailer. The header binds Store id, physical
page size, kind, stable logical id, and copy-on-write generation. Encoders clear
reused buffers, expose only a capacity-clipped payload window, require zero
padding before sealing caller-filled pages, and checksum the complete physical
page. Readers perform one checksum pass, reject reserved fields, and expose
neither padding nor the trailer. The fixed 256-byte state-root payload records
Store options and counts plus independent chunk, key, exact-index, and TTL
directory roots. Each reference carries a physical offset and immutable
logical-id/generation pair, so unchanged roots can be shared without a Go
pointer or a global lookup. Recovery validates those top-level directory pages
and their identities before selecting a generation; a torn or mismatched newer
directory therefore falls back to the older root. On M4 Max, the complete
pure-Go SIMD state-root encoder measured 170.0-171.6 ns per 4 KiB page and the
decoder 152.4-153.3 ns, both at zero allocations.

Heap `Store` retains its packed multi-stream exact-index base plus dirty-chunk
delta. `FileStore` uses a different durable representation: its frozen catalog
hash is in the state root, a copy-on-write index tree maps
`(index id, tuple hash, chunk)` to one stable-slot posting page, and each
mutation updates affected postings in the same transaction as the document and
key roots. Posting v2 stores an optional validated scalar or compound-tuple
certificate. A certificate with no collision flag proves every bit in that
stream after one semantic query comparison. Encountering a distinct tuple with
the same hash sets a sticky flag; missing, legacy, oversized, or colliding
certificates retain exact document recheck. The bitmap is never trusted merely
because its 64-bit hash matched.
The file query planner late-binds those definitions, selects one widest
compound probe before overlapping singles, and routes ordered candidate masks
into sparse document-page reads. Its routing masks are explicitly a hash-bounded
superset; every survivor executes the original predicate, so that single
document pass is also the mandatory collision check. Planner statistics expose
total, candidate, and scanned rows; an index I/O or validation error fails the
query instead of silently selecting a different physical plan.

For a fully indexed `COUNT(*)`, the planner asks for final masks instead.
Collision-free certificates decide those streams directly; only ambiguous
streams admit and recheck documents. Consecutive streams stored in one packed
posting page share one lease and decode, while their ordered decisions remain
independent.

Key, chunk, exact-index, TTL, free, document, and overflow pages are all
attached to public `FileStore` mutation batches. `Put`, `Delete`, deadline
changes, persistence, and expiry publish complete page graphs. Heap `Store`
checkpoint mappings remain read-only export images and are intentionally not
updated by those heap mutations.

The old root stays valid until the final step. Recovery chooses the newest
valid superblock and ignores unreferenced partial pages. This follows the
well-understood failure-atomic property of copy-on-write page propagation; see
the analysis in
[Building blocks for persistent memory](https://link.springer.com/article/10.1007/s00778-020-00622-9).
A WAL is still required if the durability contract must acknowledge a sequence
of transactions before their full page graph is durable; mmap alone is not a
durability protocol.

## Delete and space reclamation

Delete builds the affected logical chunk without the row and publishes a new
page id. Deleting the final row removes the logical chunk mapping. A compact
group remains shared by untouched chunks and is retired after its final mapping
is removed or peeled. Readers see neither a tombstone nor a version walk, and
current-chunk scan density is restored by the same operation.

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

The implemented `FileSnapshot` pins one root generation, not every cache frame.
It must be closed explicitly; `FileStore.Close` refuses to finish while a
snapshot lease remains. `AppendRaw(dst, key)` acquires scoped page leases and
copies into caller-owned capacity, so returned bytes survive eviction and
snapshot close. `RangeRaw` borrows only for its callback and reuses one overflow
buffer. The query executor copies selected values into its result.

This is load-bearing: an evictable frame cannot safely back an unowned
`RawValue`. The file surface therefore does not return one. A sufficient
destination makes an inline resident `AppendRaw` allocation-free, but it is
still a copy and is measured separately from heap `Store` reads.

## Research basis and rejected shortcuts

[LeanStore](https://db.in.tum.de/~leis/papers/leanstore.pdf) demonstrates the
relevant low-overhead buffer-manager direction: explicit replacement preserves
control beyond RAM, and pointer swizzling can reduce resident access cost.
`FileStore` adopts explicit replacement but currently uses a bounded cache
lookup rather than claiming a swizzled fast path. Warm acquire/release remains
zero-allocation and measured 25.5 ns on the M4 Max; independent resident pages
no longer share one cache mutex. Its stable 64-slot masks and immutable
publication remain specific to JSON queries.

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
features, and fixed write buffers used by the scoped substrate. Speculative
reads deliberately use non-fixed `IORING_OP_READ` into stable, reserved cache
mmap spans: registering the entire cache would impose unnecessary long-term
pins, while staging buffers would add a copy. One locked-thread issuer owns the
ring, batches up to `ReadQueueDepth`, and publishes a frame only after exact
completion length, identity, CRC32C, and typed validation succeed. Ring memory
mapping is control-plane queue sharing; Store data remains under explicit page
I/O rather than demand-paged writable mappings.

## Query and TTL consequences

Declared nested and compound indexes keep the same scalar fingerprint and
exact-answer semantics. Collision-free certificates avoid document I/O;
ambiguous streams recheck. `FileSnapshot.AppendIndexMasks` returns the same
sparse `(chunk, mask)` interchange form as heap snapshots.
`RunFileSnapshot` late-binds that catalog for equality and supported
containment predicates, lowers scalar-leaf object containment to exact nested
equalities, prefers the widest compound bound, intersects `AND`, and unions
only a completely bounded `OR`. Fully certified `COUNT(*)` popcounts the masks
without admitting JSON. Arrays, empty objects, `NOT`, range predicates, and any
partially unbounded `OR` retain the physical scan because complementing an
approximate candidate universe would be unsafe.

The planner also recognizes an unfiltered scalar aggregate containing only
`COUNT(*)` and numeric aggregates whose paths are all frozen covers. It
preflights the complete list and fuses distinct columns into one typed-extent
walk, without admitting JSON rows or launching workers. Numeric masks
deliberately cannot answer `COUNT(path)`, because present non-numeric values
must count there. An untouched compact generation reads its contiguous dense
scan stripes. After the first document mutation the same API walks
authoritative detached sidecars plus peeled document pages, preserving
correctness without pretending that a stale clean stripe includes overlays.

TTL is publication-based and persistent. A deadline is stored beside its key
and in an ordered copy-on-write TTL tree. Changing or removing it updates both
paths in one generation. `ExpireDue` reads the earliest record and publishes
ordinary deletes up to the caller's limit. Far-future pages stay cold; ordinary
reads perform no clock access or expiry branch.

Ordered projections and grouped query state can exceed the memory target, so
`RunFileSnapshot` emits sorted temporary runs and merges with a maximum fan-in
of 32. Batch parsing/indexing is parallel; publication order is restored before
partial reductions merge. Final output remains caller-owned and therefore is
not counted against the transient working-memory target.

## Validation and remaining gates

The implementation now has deterministic randomized mutation/TTL parity
against heap `Store`, retained-snapshot and reopen continuation tests, exact
index update/delete/reopen tests, bounded fan-in spill differentials, async
flush tests, allocation checks, long-lived-snapshot reclamation/file-growth
tests, page corruption tests, crash images spanning every changed-page
boundary and every root-record byte,
direct read/write descriptor tests, concurrent direct reader/writer pressure,
an explicit greater-than-cache gate, direct arena-read completion tests, and
native/portable queue-depth pressure sweeps. The
latter stores 21,347,320 source key+JSON bytes behind 200,704 resident page
bytes (106.4x), reopens twice, performs an ordered full scan, probes distant
keys, and preserves update, delete, and changed TTL. Its 256 MiB Docker/Linux
run used direct reads and writes, reached a 120,057,856-byte physical
high-water, and completed in 11.63 seconds. The 21,347,320-byte scan took
260.9 ms at 78.0 MiB/s, with 3.50 MiB sampled Go heap, 17.0 MiB current RSS,
18.1 MiB peak RSS, 2,393 minor faults, and 15 major faults. It runs with:

```text
SIMDJSON_FILESTORE_100X=1 \
  go test . -run '^TestFileStoreHundredXResidentSmoke$' -v -count=1
```

The physical-memory gate builds its Linux test binary before entering a
64 MiB cgroup, requires `O_DIRECT` on both lanes, and checks `st_blocks` so a
sparse logical file cannot pass. The measured ARM64 Docker volume stored 2,137
large documents and one nested exact index: 6,713,852,053 live source bytes,
6,923,669,504 bytes at file high-water, and 6,920,364,032 allocated bytes
under a 52,576,256-byte cgroup peak. The source/peak and allocated/peak ratios
were 127.7x and 131.6x. Reopen, distant and nested-index probes, update,
delete, and mutable TTL completed under eviction in 14.79 seconds:

```text
scripts/run-filestore-physical-scale.sh
```

Direct full scans use a chunk-ordered read-ahead window bounded by one half of
cache bytes, the configured queue, 64 extents, and either native
`ReadQueueDepth` or four requests per portable worker. Extents are submitted in
physical order while callbacks remain logically ordered. Buffered files stay
serial and use kernel readahead. On the same Linux/ARM64 container, a
2,048-document pressure benchmark measured one-second medians of 60.18 MiB/s
serial, 75.03 MiB/s with four positional-read workers, and 182.16 MiB/s with
the zero-copy native issuer at depth 64. The native lane was 2.43x portable
read-ahead and 3.03x serial; all paths remained 0 B/op and 0 allocs/op.
`ReadBackend`, `AsyncReadBatches`, and `LargestReadBatch` expose the selected
engine and actual batch high-water.

The full race suite and portable Linux/Windows compile checks are release gates.

The physical 100x correctness boundary is now demonstrated. Equal cold and
resident latency is not a credible storage contract. Remaining performance and
release gates are:

- working-set, read/write amplification, and fragmentation sweeps below, near,
  and above physical RAM;
- cold NVMe workloads, not only container-backed direct I/O;
- device/firmware power-loss campaigns and long recovery fuzzing in addition
  to deterministic write-boundary tearing; and
- persistent range/ordered indexes, safe `NOT` complements, and persisted
  cardinality estimates for a pre-probe cost model. The current crossover
  fixture rejects an arbitrary 10-25% cutoff: exact equality remained useful
  until approximately 94-97% selectivity on the measured buffered/direct
  hosts.

`OpenStore` remains the caller-owned mapped heap-Store checkpoint.
`OpenFileStore` is the incremental durable and bounded-residency surface. The
latter is usable without claiming that every workload performs well at an
arbitrary disk-to-RAM multiplier.
