# ADR 0005: one Store surface, bulk construction, and mapped pages

Status: accepted in stages. Transient Store construction, including declared
exact indexes, is implemented; mapped mutable pages remain proposed. Extends
ADR 0004 without changing its current lifetime contract.

## Decision

Make `Store` the primary collection surface for keyed, static, mutable, and
eventually mapped data. Keep the existing immutable document-set machinery as
an internal page-building engine until Store has capability and performance
parity; do not maintain two independent parsers, tape formats, shape compilers,
or query executors.

The migration has three ordered stages:

1. add a transient bulk Store builder that creates complete micro-pages, the
   key directory, and declared exact indexes before one atomic publication;
2. define a Store-compatible read-only mapped image with explicit lifetime
   ownership; and
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

## Greater-than-memory mode

The current mutable Store is heap-resident and cannot promise a data set larger
than physical memory. A serialized immutable image opened from a caller-owned
mmap can already be larger than RAM: the operating system faults in only the
working set. This is bounded by virtual address space, Go's `int` slice length,
the image format, and the existing per-document 32-bit coordinate limits.

The engineering target for mapped Store is a data image at least 100 times the
configured resident budget, provided the workload's hot key/index/page working
set fits that budget. The multiplier is not a latency guarantee: a cold random
read must pay a storage fault. Selective indexed queries can nevertheless beat
a heap engine that scans more data by proving non-candidate pages from compact
resident summaries and never faulting their JSON bytes.

Keep residency controllable by separating four temperature classes:

1. a very small pinned superblock and upper key/index directory;
2. bounded caches of decoded directory and posting pages;
3. memory-mapped immutable document micro-pages, admitted on demand; and
4. append-only replacement pages plus reclaimable free extents.

The operating system remains the final page cache, but Store must expose a byte
budget and counters for resident directory pages, document-page faults,
prefetch hits, bytes read, and eviction advice. A 100x corpus is accepted only
when steady hot-set RSS remains within the configured budget; virtual mapping
size alone does not satisfy the target.

The proposed mutable mode is page-oriented copy-on-write, not a heap Store
whose byte slices happen to come from mmap. A logical micro-page contains:

- a generation, checksum, format version, and exact byte bounds;
- at most 64 stable slots and a dense live-row directory;
- immutable source bytes plus classic or shape-deduplicated tapes;
- page-local exact-index tuple masks; and
- no pointers, capacities, or runtime-specific object layouts.

A small mapped root names logical pages by immutable physical page id. Point
lookup resolves key to `(logical page, stable slot)`, validates the page header,
and returns a scoped view. Query planning performs `AND`, `OR`, and `NOT` on
page masks before touching source pages, so a selective query faults only
candidate pages. Sequential scans use page order and OS readahead.

Cold directory levels use packed CHAMP-style nodes with offsets rather than Go
pointers. Hot upper levels may be decoded into the existing fixed fan-out form
when measurement justifies it. Posting streams are ordered by logical page, so
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

One transaction follows a failure-atomic sequence:

1. validate replacements and build complete new page images in unused slots;
2. write checksums and monotonically increasing page versions;
3. persist the new data pages;
4. copy-on-write the changed key/index directory paths;
5. persist a new root descriptor; and
6. atomically select the descriptor through a checksummed double superblock.

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

## Query and TTL consequences

Declared nested and compound indexes keep the same scalar fingerprint and
mandatory exact-recheck semantics. Their physical posting becomes a hierarchy
of page-level masks; Boolean composition remains one native word per 64 rows.
The executor must expose page-fault and bytes-touched metrics in addition to
nanoseconds and result counts.

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

Until those gates pass, `Store` remains an in-memory engine and `Open` remains
the read-only mapped-corpus mechanism.
