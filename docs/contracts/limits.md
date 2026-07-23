# Resource and input limits

This page records the limits that affect acceptance, memory growth, and
retention. A per-value or per-state limit is not a process-wide memory budget;
applications still need protocol-level limits for total input, work, and
concurrency.

## Configured input limits

### Nesting depth

Parsing, validation, selection, indexing, decoding, and transforms default to
10,000 nested arrays and objects. A positive `Options.MaxDepth`,
`DecoderOptions.MaxDepth`, or `document.IndexOptions.MaxDepth` replaces that
default for the APIs that accept the corresponding options. Values at or below
zero select the default. Convenience APIs without a depth option use the
default.

`MaxDepth` counts container nesting. It does not limit bytes, string or number
length, members in one container, or members in the whole document.

Encoding follows the supported compiler's `encoding/json` behavior. Go 1.26
has no fixed encoder nesting limit; it begins identity-based cycle detection
after a recursive path crosses 1,000 pointer-, map-, or slice-bearing values.
Go 1.27 and later reject encoding beyond 10,000 nested containers and also
bound a chain of pointer hops. There is no encoder option that changes these
toolchain-specific guards.

### Streamed value size

`ReaderOptions.MaxValueBytes` limits one top-level value when positive. Zero,
the default, means unbounded. The limit does not cap the number of values,
total stream bytes, elapsed work, or the Reader's initial buffer capacity. A
value over the limit stops iteration and is reported by `Reader.Err`.

`NewReader` and a zero `ReaderOptions.BufferSize` use a 64 KiB initial buffer.
A positive `BufferSize` chooses initial capacity only; values below 512 bytes
are rounded up to 512. Negative option values are rejected. The rolling buffer
grows when a complete value does not fit, so callers handling untrusted input
should set a positive `MaxValueBytes` and an application-level total-stream
policy.

One-shot byte-slice APIs do not impose a separate package-wide limit on input,
string, or number length. Available memory, Go's `int` and address space, and
the representation-specific index limits below still apply.

## Structural-index limits

`IndexEntry` is four `uint32` words (16 bytes). `BuildIndexOptions` rejects a
source length or caller-provided storage capacity greater than `math.MaxUint32`
with `document.ErrIndexTooLarge`; offsets and entry links must fit the same
32-bit representation. Insufficient caller storage returns
`document.ErrIndexFull`.

A container's direct member count occupies 26 bits, so one array or object can
hold at most 2^26 - 1 direct members in an `Index`. Exceeding that count returns
`document.ErrIndexTooLarge`. This is not a separate total-member limit across
the whole document. `RequiredIndexEntries` reports the exact caller storage
needed at the default depth limit.

The 64-level bitmap index machine is an internal fast-path limit, not an input
contract. Deeper accepted documents fall back to the general builder, which
enforces the caller's `MaxDepth`.

## Retained memory

### Reader, Writer, and returned values

- A `Reader` retains its rolling buffer, which can grow to accommodate the
  largest value encountered. `Reader.Close` releases the Reader's references to
  its input and buffer. Any slice already returned by `Bytes` remains retained
  by its caller.
- A `Writer` buffers one complete top-level value before flushing. Its size is
  a flush threshold, not a maximum value size. `Flush` and `Close` reuse the
  grown buffer; `Close` does not shrink it, release it, or close the underlying
  writer.
- Caller-owned output capacity, decoded destination storage, an owning
  `Value`'s source copy and index, and caller-provided `Index` storage live as
  long as their owners retain them. Pool budgets do not apply to these values.

### Pooled operation state

The following are retention budgets for one reusable state, not aggregate
process limits:

- Encoder map-entry storage and rendered map-key bytes each have an independent
  512 KiB retention budget. Each eligible typed-value backing slot also has a
  512 KiB budget. A compiled plan can own multiple fixed-type slots; oversized
  maps use one-call storage instead of replacing pooled scratch.
- A structural decoder state returned to the package pool retains at most
  2 MiB of `uint32` positions. Larger tapes are dropped on release.
- A compiled decoder caches at most four operation states. The cumulative
  shallow map key/value box budget is at most 64 KiB per state; ineligible or
  oversized boxes use operation-local storage.
- `Parse` seeds each pooled estimate buffer with 1,024 `IndexEntry` values
  (16 KiB). If a document needs a larger index, that grown index belongs to the
  returned `Value` rather than replacing the pooled estimate.

These states use `sync.Pool`. The runtime may discard pooled objects during
garbage collection, but the package does not place a deterministic aggregate
cap on the number of states retained across processors, compiled plans, or
concurrent calls.

### Compiled-plan caches and Marshal hints

The convenience `Marshal` and `Unmarshal` paths cache compiled plans by Go
type. Concrete types reached through interface values have additional encode
and decode plan caches; encode entries distinguish HTML-escaping mode. These
`sync.Map` caches have no entry-count limit or eviction policy and live for the
process lifetime. Programs that synthesize an unbounded number of `reflect`
types should avoid sending them through these paths. Explicitly compiled plans
live as long as their `Encoder` or `Decoder` owner.

`Marshal` retains an integer output-size estimate, not an output buffer.
Ordinary observations through 256 KiB update it immediately, with a 64-byte
minimum initial capacity. A first larger observation is stored as an
unconfirmed outlier and the next call starts with 512 bytes; a repeated equal
large observation confirms exact presizing, even above 256 KiB. A smaller
observation replaces the estimate. Therefore 256 KiB is an outlier-confirmation
threshold, not an output-size or retained-memory ceiling.

### Mutable Store

`StoreOptions.ChunkDocuments` must be from 1 through 64; zero selects 64. The
limit bounds documents rebuilt by an ordinary mutation, index-maintenance step,
or one affected chunk in an expiry batch. It is not a byte-size limit: a single
document may approach the structural-index limits above, and a chunk of large
documents can retain correspondingly large source and tape storage.

Chunk addresses are `uint32`. `Put` returns `ErrStoreTooLarge` before an append
could wrap the persistent vector's address space. Empty ids are reused, making
the theoretical limit unreachable under ordinary churn, but this is not a
process memory budget.

Each live Store key is present in one chunk slot and is reachable through the
heap key directory, the mapped immutable base directory, or a post-open heap
delta. Mapped-base entries can remain after delete, but the stable-slot live bit
and an exact spelling check make them unreachable; they are not reader-visible
tombstones or version chains. Each expiring key adds exactly one pointer-free
packed stable-slot/deadline heap node and one integer-keyed position-map entry;
changing a TTL does not add a stale generation or retain the key string. Each
physical posting chunk appears exactly once in writer reclamation metadata,
even when multiple logical posting index names share it.

A declared exact index has one to four RFC 6901 paths. Missing paths,
incompatible traversal steps, and JSON containers are not indexed. Each
distinct composite fingerprint owns an adaptive stable-slot posting. A
bulk-built or reopened immutable base packs multiple compressed streams per
physical page; later writes wholly shadow touched chunks in an inline/sparse
persistent-radix delta. Online indexes use that delta representation while
backfill is incomplete. Each materialized word addresses 64 document slots;
empty address ranges use no word. Fingerprint collisions can increase candidate
bits but exact rechecks preserve answers. `Snapshot.IndexStats` reports packed
base and delta footprint separately; no distribution-independent bytes/key
bound exists because value cardinality and frequency determine both.

Snapshots pin every immutable node, chunk, source arena, tape, declared-index
root, and physical wildcard-index version reachable from their publication
state. Holding old snapshots while updating hot keys intentionally retains old
versions. The package cannot bound that memory without invalidating the
snapshot contract; applications must bound snapshot age or count according to
their workload.

The current Store version may share unchanged source and structural-tape
backing with an older publication, but it retains no parent chunk or historical
version list. Replacing or deleting a row makes that row's old backing
collectible as soon as no retained snapshot reaches it. Compact narrow-value
slabs and dense row headers are publication-private.

A building online index temporarily retains the immutable chunk-vector root
captured by `AddIndex` or `CreateIndex` so its bounded cursor cannot miss an
original live chunk. Coverage is a sparse-paged bitmap: dense populated pages
cost one bit per chunk, while empty historical address ranges retain no pages.
Reaching `Ready` or dropping the definition releases the root; completion also
collapses the coverage bitmap to an implicit all-live state.

`BackfillIndex`, `ReclaimIndexes`, and `ExpireDue` accept caller work limits.
A non-positive limit means all currently eligible work, which may be large.
Production event loops should normally choose a positive batch size and expose
`Store.Stats`/`Snapshot.AppendIndexes` as progress metrics.

`Store.WriteTo` uses 32-bit counts and lengths for chunk/index/TTL metadata and
key/path spellings, 32-bit chunk ids, and 64-bit image offsets. It returns
`ErrStorePersistTooLarge` instead of truncating those fields. Only `Ready`
indexes can be written. `OpenStore` rejects unknown flags, nonzero reserved
fields, unaligned nested page images, inconsistent stable slots, duplicate
keys/indexes/TTLs, impossible counts, malformed nested `DocSet` images, and a
bad manifest checksum before publishing a Store. The pre-v1 format is versioned
but not yet promised stable across releases.

`OpenStore` borrows its complete input slice. A file mapping must remain
immutable and mapped until the Store, all snapshots, and every borrowed value
are unreachable. The package does not call `munmap`, retain an OS file handle,
or infer lifetime from finalizers. `AppendRaw` and `AppendRawKey` copy exact JSON
into caller storage when an independently owned result is required.

Mapped image bytes do not count toward Go `HeapAlloc`. On supported Unix
systems, the immutable base key directory and one 32-byte descriptor per row
also use pointer-free anonymous mappings outside `HeapAlloc`; they still count
toward RSS. `OpenStore` validates every key and row and still eagerly rebuilds
distinct shapes, optional accelerators, and declared exact-index roots as Go
objects. The image size is therefore not a resident-memory limit. Applications
must measure mapped image bytes, external metadata, and reconstructed heap
metadata separately; current Store images do not yet provide an eviction budget
or a 100x-RAM guarantee.

Dense Store bitmap workspaces use one `uint64` per logical chunk high-water id,
including empty historical ids. Prefer sparse `StoreMask` streams for selective
predicates or unusually sparse address spaces. Empty ids are reused under
ordinary churn. `StoreBitmapWords` panics before returning a wrapped length if
an otherwise valid address span cannot be represented by the platform's `int`
slice length.

### Attached FileStore

`FileStoreOptions` makes retained and in-flight resources explicit.
`ResidentBytes` bounds admitted page buffers; `BufferCount` and `QueueSlots`
bound commit data/descriptors; `ReadConcurrency` and `PrefetchQueue` bound read
work; `MaxSnapshotLeases` and `MaxRetiredExtents` bound lifetime/reclamation
metadata. Exhaustion returns an error or applies queue backpressure rather than
growing without limit. `Stats` reports current use and pressure.

`PageSize` is a power of two at least 4 KiB. `MaxPageSize` is a power-of-two
multiple of it. Keys and JSON are rejected above `MaxKeyBytes` and
`MaxDocumentBytes`; values above `InlineValueBytes` use bounded overflow pages.
Chunk ids remain `uint32`, physical offsets and file high-water are bounded by
signed OS file offsets, and persistent logical ids are `uint64`. At most 64
one-to-four-column exact indexes may be frozen into a file. Reopening with a
different effective catalog or format options fails instead of interpreting
old bytes under new semantics.

Opening reads bounded root/page scratch and does not scale heap with corpus
cardinality. This is not a promise that every operation fits one page: one
maximum-sized document, a copy-out destination, transaction scratch, and the
caller's final query result exist outside `ResidentBytes`. The file query
executor separately bounds raw batches and merge state with `MemoryBytes`;
one oversized row may exceed the target, and an unbounded projection or group
result necessarily consumes output-proportional memory. Spill merge opens at
most 32 runs at once and removes its temporary files on return.

The caller owns the `*os.File` and spill directory. `FileStore.Close` does not
close the file and fails while `FileSnapshot` leases remain. `RangeRaw` callback
bytes expire when the callback returns; `AppendRaw` and file-query result cells
own independent copies. The attached format is pre-v1 and version checked, but
cross-release compatibility is not yet promised.

## Limits that applications must add

The package does not provide a process-wide memory budget, total-stream byte or
value count, deadline, concurrency cap, output-size cap, or type-cache
cardinality cap. Apply those limits at the protocol or service boundary. In
particular, `BufferSize`, a Writer size, `MaxDepth`, per-state scratch budgets,
and the 256 KiB Marshal threshold must not be treated as substitutes for a
total resource budget.
