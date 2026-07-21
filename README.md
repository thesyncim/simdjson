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

store.SetTTL("session:42", 30*time.Minute)
store.Put("session:42", []byte(`{"user":42,"state":"active"}`)) // preserves TTL
store.Delete("session:42")

// The old immutable view remains valid after both mutations.
raw, ok := before.GetRaw("session:42")
```

TTL lives in a writer-side indexed four-ary heap—one mutable node per expiring
key, no stale deadline generations—and due keys are grouped by chunk and
published in one delete batch. `RunExpiry` sleeps until the next deadline;
ordinary reads pay literally no TTL branch or time lookup.

Online posting indexes publish as `Building`, dual-maintain concurrent writes,
backfill in caller-bounded chunk batches, and become `Ready` at complete
coverage. Snapshot probes remain exact during build through per-chunk scan
fallback. Dropping detaches the logical index immediately; physical reclamation
is also caller-bounded and never performs a hidden full-store completion scan.

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
| Immutable `Store.GetRaw` point read | 19-21 ns, 0 allocations |
| Change an existing TTL | 43 ns, 0 allocations |
| Native-bitmap SIMD `AND` vs scalar | 2.1-2.6x six-run median, 0 allocations |

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
| Batches of documents, columnar field extraction | `DocSet`, `ShapeCache`, `KeyInterner` |
| SQL-shaped projection, filtering, grouping, and aggregation | `query.Query.RunInto`, `query.Result`, `query.Workspace` |
| Keyed updates, deletes, TTL, snapshots, and online postings | `Store`, `Snapshot`, `StoreStats` |

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
