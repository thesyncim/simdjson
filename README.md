# simdjson

[![ci](https://github.com/thesyncim/simdjson/actions/workflows/ci.yml/badge.svg)](https://github.com/thesyncim/simdjson/actions/workflows/ci.yml)

Strict JSON processing for Go, written entirely in Go. It provides
`encoding/json`-style `Marshal` and `Unmarshal` for supported values, reusable
typed plans, a structural document index with multi-document batch primitives,
and optional Go-native SIMD.
The root module has no third-party module dependencies, assembly, C,
`go:linkname`, or private runtime-layout assumptions.

This project is pre-v1: APIs may change while the accepted
[v1 boundary](docs/adr/0001-v1-api.md) is implemented. It is an independent Go
implementation, not the C++ [`simdjson`](https://github.com/simdjson/simdjson)
project. Algorithm and corpus relationships are recorded in
[the provenance inventory](docs/provenance.md).

## Install

Go 1.26 builds the supported portable backend:

```sh
go get github.com/thesyncim/simdjson@latest
```

The optional SIMD backend requires the exact Go 1.27 development compiler
pinned by the repository:

```sh
./scripts/bootstrap-gotip.sh "$HOME/sdk/simdjson-gotip"
GOEXPERIMENT=simd "$HOME/sdk/simdjson-gotip/bin/go" test ./...
```

`GOEXPERIMENT=simd` enables compiler support; CPU selection and supported
compiler windows are documented in the [toolchain policy](docs/toolchain.md).

## Quick start: typed decode

```go
package main

import "github.com/thesyncim/simdjson"

type Event struct {
	ID   int      `json:"id"`
	Name string   `json:"name"`
	Tags []string `json:"tags"`
}

func decode(data []byte) (Event, error) {
	var event Event
	err := simdjson.Unmarshal(data, &event)
	return event, err
}
```

For encoding, `Marshal` is the allocating convenience for occasional calls;
hot paths compile once and append into caller-owned capacity:

```go
var eventEncoder simdjson.Encoder[Event]

func init() {
	var err error
	eventEncoder, err = simdjson.CompileEncoder[Event](simdjson.EncoderOptions{})
	if err != nil {
		panic(err)
	}
}

func encode(event *Event) ([]byte, error) {
	buf := make([]byte, 0, 4096)
	return eventEncoder.AppendJSON(buf, event)
}
```

## The document index

`BuildIndex` validates a document once and lays out a structural index in
caller-owned storage — a flat tape of 16-byte entries, sized exactly by
`RequiredIndexEntries`. A `Node` then navigates the document without
allocating or rescanning text, with `encoding/json`-compatible last-duplicate
and escaped-key semantics:

```go
n, err := simdjson.RequiredIndexEntries(data)
if err != nil {
	return err
}
index, err := simdjson.BuildIndex(data, make([]simdjson.IndexEntry, 0, n))
if err != nil {
	return err
}
user, ok := index.Root().Get("user")
name, ok := user.Get("screen_name")
text, ok := name.StringBytes() // zero-copy view into data
```

The layer around that core, one line each:

- **Key-hash enrichment** — `simdjson.BuildIndexOptions(data, storage,
  document.IndexOptions{HashKeys: true})` stores a hash per object key, so
  lookups reject non-matching members on one word compare and flat objects
  take an unrolled scan over the index itself.
- **Compiled queries** — `key := simdjson.CompileKey("user_id")` and
  `ptr := simdjson.MustCompilePointer("/statuses/0/user/name")` hash and
  parse once; `node.GetCompiled(key)` and `index.PointerCompiled(ptr)` reuse
  them across calls and documents.
- **Constant-time member lookup** — `probe, ok :=
  simdjson.BuildObjectProbe(node, slots)` builds an open-addressed table over
  one wide object; `probe.Get(key)` answers hits and misses in constant
  expected time.
- **Document batches** — `var set simdjson.DocSet; set.ReadFrom(r)` ingests a
  stream of NDJSON or concatenated documents straight into shared arenas;
  `set.Doc(i)` returns each document's ordinary `Index`, valid across later
  appends.
- **Columnar extraction** — `vals = cache.AppendField(vals, &set, "user_id")`
  on a `simdjson.ShapeCache` extracts one field across every document through
  cached object layouts; `cache.AppendFieldInt64(dst, valid, &set, "user_id")`
  produces a dense typed column with a validity mask in the same pass.
- **Buffered posting queries** — with `DocSet.Postings` enabled,
  `rows = set.AppendWhereExists(rows[:0], "status")` and
  `rows = set.AppendWhereContainsIndex(rows[:0], "status", needle)` reuse the
  caller's result storage. Build the needle index once; warmed lookups allocate
  nothing, including long escaped strings and exact verification over compact
  shape tapes.
- **Reusable query execution** — `query.Compile` or the builder API produces an
  immutable query. Keep one `query.Result` and `query.Workspace` per worker and
  call `q.RunInto(&result, &set, &workspace)`; after the retained row, posting,
  decoded-text, and group capacities warm, projection, containment, stable
  ordering, aggregates, and grouping execute with zero heap allocations.

Ownership is uniform: an `Index` and its nodes borrow the source and the entry
storage; `DocSet` and the caches own their arenas, and nothing they hand out is
invalidated by growth.

## Mutable documents, TTL, and online indexes

`Store` adds keyed updates and deletes while publishing immutable snapshots.
`Snapshot.GetRaw` takes no lock, makes no clock call, inspects no tombstone, and
allocates nothing:

```go
store := simdjson.NewStore(simdjson.StoreOptions{
	ChunkDocuments: 8, // write-heavy; zero selects the read/space default of 64
	ShapeTapes:      true,
})

if _, err := store.Put("session:42", []byte(`{"user":42,"state":"open"}`)); err != nil {
	return err
}
before := store.Snapshot()
sessionKey := before.CompileKey("session:42")

store.SetTTL("session:42", 30*time.Minute)
store.Put("session:42", []byte(`{"user":42,"state":"active"}`)) // preserves TTL
store.Delete("session:42")

// The old immutable view remains valid after both mutations.
raw, ok := before.GetRaw("session:42")

// Repeated reads can bypass hashing and the key directory through a verified
// stable slot; delete/reinsert movement falls back to the complete lookup.
raw, ok = before.GetRawKey(sessionKey)
```

For an initial keyed corpus, `StoreBuilder` validates and copies rows directly
into their final micro-pages, constructs the key directory through uniquely
owned transient nodes, and publishes only the completed Store:

```go
builder, err := simdjson.NewStoreBuilder(simdjson.StoreOptions{ShapeTapes: true})
if err != nil {
	return err
}
if err = builder.CreateIndex(simdjson.StoreIndexDefinition{
	Name: "state", Paths: []string{"/state"},
}); err != nil {
	return err
}
if err = builder.Append("session:42", []byte(`{"state":"open"}`)); err != nil {
	return err
}
store, err := builder.Build()
```

The builder is single-goroutine and accepts each key once. It owns both key and
JSON bytes after `Append`; declared nested or compound indexes are returned
`Ready`, and the Store immediately supports ordinary mutation, snapshots, TTL,
and online index changes.

`Store.WriteTo` checkpoints one generation into a Store-native container of the
same bounded `DocSet` page images. `OpenStore` can borrow a caller-owned
read-only mmap and returns a normally mutable Store:

```go
var image bytes.Buffer
if _, err := store.WriteTo(&image); err != nil {
	return err
}
reopened, err := simdjson.OpenStore(image.Bytes())
if err != nil {
	return err
}
dst, ok := reopened.AppendRaw(make([]byte, 0, 256), "session:42")
```

The image bytes must remain immutable and live until the Store, retained
snapshots, and borrowed values are dead. `AppendRaw`/`AppendRawKey` make an
owned copy into caller capacity and allocate nothing after capacity is warm.
`WriteTo` remains a full checkpoint: later heap-Store mutations do not update
that image.

For incremental durability and a bounded resident set, attach a `FileStore` to
a caller-owned file. Its key, chunk, exact-index, TTL, free-space, document, and
overflow structures are checksummed copy-on-write pages selected by alternating
superblocks:

```go
file, err := os.OpenFile("events.sj", os.O_RDWR|os.O_CREATE, 0o600)
if err != nil {
	return err
}
store, err := simdjson.CreateFileStore(file, simdjson.FileStoreOptions{
	ResidentBytes: 256 << 20,
	Synchronous:   true,
	Indexes: []simdjson.StoreIndexDefinition{
		{Name: "tenant", Paths: []string{"/tenant"}},
	},
})
if err != nil {
	return err
}
defer store.Close() // the caller still closes file

_, err = store.Put("event:42", []byte(`{"tenant":"acme","score":7}`))
snapshot, err := store.Snapshot()
if err != nil {
	return err
}
defer snapshot.Close()
raw, ok, err := snapshot.AppendRaw(nil, "event:42")
```

`FileStore` opens from bounded root/page scratch instead of walking the corpus.
Its CLOCK cache, read workers, prefetch queue, commit queue, active snapshots,
and retired extents all have explicit capacities reported by `Stats`. Async
writes become reader-visible when queued; `DurableGeneration` and `Flush`
expose the durability boundary. `Synchronous` waits on each mutation. Snapshot
leases fence physical reuse, while the previous durable generation remains
recoverable if the newest data or root write is torn.

`query.Query.RunFileSnapshot` scans physical chunk leaves in order, builds
bounded batches in parallel, and externally merges ordered rows or grouped
state through temporary spill files with a 32-run fan-in. `MemoryBytes` bounds
working state, not the caller-owned final result or one document larger than
the target. The Store is deliberately single-file and single-writer: there is
no replication, distributed execution, cross-Store transaction, SQL parser,
join engine, or server protocol. Exact-index definitions are frozen when the
file is created. The complete contract and measured limits are in
[Mutable Store operations](docs/store.md).

An update parses only its replacement. Unchanged source and structural-tape
storage stays immutable and is shared into the next bounded chunk; deletes copy
only dense row metadata and remove the last-row chunk directly. There are no
version chains, tombstones, or later compaction threshold.

TTL lives in a writer-side indexed four-ary heap—one mutable node per expiring
key, no stale deadline generations—and due keys are grouped by chunk and
published in one delete batch. `RunExpiry` sleeps until the next deadline;
ordinary reads pay literally no TTL branch or time lookup.

Declared exact indexes accept one to four RFC 6901 paths, including nested
object fields and array positions. Compound equality, `AND`, `OR`, and exact
`NOT` plans combine stable-slot chunk masks before sparse column extraction:

```go
info, err := store.CreateIndex(simdjson.StoreIndexDefinition{
	Name:  "tenant_country",
	Paths: []string{"/tenant", "/profile/geo/country"},
})
for err == nil && info.State != simdjson.StoreIndexReady {
	info, err = store.BackfillIndex(info.Name, 64)
}
if err != nil {
	return err
}

q := query.Select(query.Path("id")).Where(query.And(
	query.Cmp("tenant", query.Eq, "acme"),
	query.Cmp("profile.geo.country", query.Eq, "PT"),
))
var result query.Result
var workspace query.Workspace
err = q.RunSnapshotInto(&result, store.Snapshot(), &workspace)
```

For a custom planner, `AppendIndexBitmap` and `AppendLiveBitmap` fill reusable
dense page-word buffers. `AppendStoreBitmapAnd`, `AppendStoreBitmapAnd3`,
`AppendStoreBitmapOr`, and `AppendStoreBitmapAndNot` combine them in place with
zero allocation. Pinned Go 1.27 SIMD builds use runtime-gated 256-bit AVX2 on
GOAMD64 v1/v2 and direct AVX2 on v3+, while sparse `(page, mask)` lists keep
their scalar merge path.

Online indexes publish as `Building`, dual-maintain concurrent writes, backfill
in caller-bounded chunk batches, and become `Ready` at complete coverage.
Snapshot probes remain exact during build through scan fallback. Dropping
detaches the logical index immediately. Wildcard-posting reclamation is also
caller-bounded and never performs a hidden full-store completion scan; declared
roots are reclaimed automatically with their last snapshot.

The complete API, ownership rules, expiration semantics, tuning table,
complexity bounds, zero-allocation recipes, operational counters, and Redis
comparison boundary are in [Mutable Store operations](docs/store.md).

## Performance

Single core, Apple M4 Max, pinned Go development toolchain with
`GOEXPERIMENT=simd`:

| Operation | Measured |
| --- | --- |
| Validate | 4.38 GB/s |
| Build index (validation included) | 3.18 GB/s |
| Ingest a document stream (`DocSet.ReadFrom`) | 7.44 GiB/s |
| Lookup on an indexed document, marginal per path | 107 ns |
| Lookup primitives (probe hit, hashed `Get`) | 3.8–6.4 ns |
| Extract one field across a document set | 8.1 ns/doc |
| Extract a typed `int64` column | 12 ns/doc |
| Immutable `Store.GetRaw` point read | 21.9-23.9 ns, 0 allocations |
| Compiled stable-slot `Store.GetRawKey` | 8.0-8.5 ns, 0 allocations |
| Bulk `StoreBuilder` vs repeated `Put` | about 7.7x throughput, 93.7% fewer transient bytes |
| Mapped `OpenStore`, 16,384 keys / 5.40 MB image | 1.04-1.05 ms, 234.7 KB Go heap, 1.21 MB pointer-free external metadata |
| Mapped `OpenStore`, one compound exact index | 2.64-2.67 ms, 450.6 KB Go heap, 1.21 MB pointer-free external metadata |
| Mapped keyed read, ordinary / compiled stable slot | 9.22-9.29 ns / 4.63-4.66 ns, 0 allocations |
| Mapped compound query, 32 rows in 2/256 pages | 2.55-2.61 us, 0 allocations |
| `Store.WriteTo`, 5.40 MB / 16,384 documents | 1.07-1.09 ms, 4.96-5.04 GB/s, 3 allocations |
| Default-chunk Store replace | 2.24 us median, 9.8 KiB/op |
| Exact-index replace, indexed tuple unchanged | 2.46-2.49 us, 9.9 KiB/op, no added allocations |
| Indexed Snapshot equality at 10% selectivity | 12.44 ns/input doc, 0 allocations |
| Indexed Snapshot compound point query | 2.82 ns/input doc, 0 allocations |
| Dense Store fused 3-predicate bitmap / ordered 4,096-row decode | 410-416 ns / 4.03-4.08 us, 0 allocations |
| Change an existing TTL | 45 ns, 0 allocations |
| Dense bitmap Boolean pass on M4 Max | 75-80 GB/s, 0 allocations; NEON did not beat scalar and is not dispatched |
| Packed resident document page, JSON-only / full string-key verify | 2.566-2.576 ns / 4.034-4.092 ns, 0 allocations |
| Packed 64-way chunk-directory hit | 7.17-7.26 ns, 0 allocations |
| 5M-row indexed Store scale smoke | 0.98M build docs/s, 55 ns point read, 521.1 live-heap B/doc, 0.251 objects/doc, 4.16 packed-index B/doc |

On the hosted AMD EPYC 7763 runner, AVX2 reduced the fused dense Store
three-predicate kernel from 815-823 ns to 174-175 ns (about 4.7x). The complete
kernel-plus-ordered-row decode improved from 8.44-8.51 us to 7.67-7.77 us at
zero allocations. Hosted results are directional; the benchmark artifacts
retain raw samples for both x64 and arm64.

One caveat belongs next to that table: a one-shot, single-path lookup on a
document seen once favors non-validating scanners — gjson scans only for the
queried path and sonic validates only along it, while `BuildIndex` validates
the entire input — so the break-even sits at roughly four lookups per
document, beyond which the index is ahead and stays ahead.
[`benchmarks/lookup_competitors_bench_test.go`](benchmarks/lookup_competitors_bench_test.go)
reproduces the comparison and records each competitor's validation semantics.

Performance changes must preserve correctness, ownership, retained memory,
`B/op`, and `allocs/op`. Native CI exercises matched portable and SIMD behavior
on amd64 and ARM64.

[`benchmarks/results/latest.json`](benchmarks/results/latest.json) is the latest
published machine-specific snapshot, not current-main evidence or a universal
ranking. The [benchmark contract and reproduction commands](benchmarks/README.md)
define the methodology, gates, comparison boundaries, and pinned toolchains.

![Absolute portable/SIMD and Go-library benchmark times](benchmarks/charts/go-times.svg)

## Choose an API

| Need | Start with |
| --- | --- |
| Ordinary typed JSON or strict validation | `Marshal`, `Unmarshal`, `Valid`, `Validate` |
| Repeated typed work and buffer reuse | `CompileEncoder`, `CompileDecoder` |
| Framed JSON input or token output | `Reader`, `Writer` |
| Compact, indented, or canonical output | `Compact`, `Indent`, `Canonicalize` |
| Borrowed selection or repeated document navigation | `RawValue`, `Index`/`Node`, or `Parse`/`Value` |
| Keyed datasets, including bulk construction | `StoreBuilder`, `Store`, `Snapshot` |
| Incrementally durable, bounded-residency keyed datasets | `CreateFileStore`, `OpenFileStore`, `FileStore`, `FileSnapshot` |
| Low-level immutable arenas and column extraction | `DocSet`, `ShapeCache`, `KeyInterner` |
| SQL-shaped projection, filtering, grouping, and aggregation | `query.Query.RunInto`, `query.Result`, `query.Workspace` |
| Parallel bounded-batch queries over a durable file snapshot | `query.Query.RunFileSnapshot`, `query.FileExecutionOptions` |
| Keyed updates, deletes, TTL, snapshots, exact indexes, and wildcard postings | `Store`, `Snapshot`, `StoreIndexDefinition`, `StoreStats` |

The advanced document APIs are moving into `document` during the pre-v1
migration. JSON kind values already use `document.Kind`; the remaining package
boundary and compatibility decisions are documented in the
[API ADR](docs/adr/0001-v1-api.md).

## Streaming input limits

`NewReader` and a zero `ReaderOptions.MaxValueBytes` leave each top-level value
unbounded. Set a positive protocol limit for untrusted input; the exact stream,
depth, index, and retention limits are in the
[resource contract](docs/contracts/limits.md).

## Ownership and concurrency

Default typed decoding and default `Parse` own the string storage they expose.
`ZeroCopy`, `RawValue`, `Index`, Index-derived `Node`, and reader cursors borrow
storage: keep the source alive and unmodified, and observe each API's
invalidation rule. `Index` and its nodes also borrow caller-provided entry
storage; a node obtained from an owning `Value` keeps that value's backing
arrays alive itself. `DocSet`, `ShapeCache`, and `KeyInterner` own their arena
storage and are single-writer: values they hand out stay valid as they grow,
and concurrent reads are safe once writing stops. `Store` serializes mutations
and publishes immutable snapshots; snapshot reads are concurrent-safe and never
block a writer. Values returned by a snapshot borrow that snapshot's storage.

Compiled encoders, decoders, keys, and pointers are immutable and safe for
concurrent use. Destinations and source buffers remain caller-owned; each
`Reader` or `Writer` belongs to one goroutine. The complete rules are in the
[architecture and safety record](docs/architecture.md).

## Support and project records

- [Toolchain and compiler support](docs/toolchain.md)
- [Resource and input limits](docs/contracts/limits.md)
- [Unsafe inventory](UNSAFE.md)
- [Architecture and safety](docs/architecture.md)
- [Mutable Store operations](docs/store.md)
- [Security policy](SECURITY.md)
- [Contributing and local gates](CONTRIBUTING.md)

The repository does not yet have a root project license. `LICENSE-GO` and
`LICENSE-SIMDJSON` cover identified upstream-derived material only. Selecting a
project license and completing the final notice remain release blockers; no
license is implied by this README.
