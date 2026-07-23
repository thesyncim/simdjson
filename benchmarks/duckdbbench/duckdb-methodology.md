# Methodology: Store and DuckDB

This harness compares the keyed mutable heap `Store`, the durable bounded-cache
`FileStore`, and DuckDB as embedded engines over the same deterministic JSON
rows. It is a scoped comparison, not a claim that the systems have identical
APIs or operational envelopes.

The comparison has four non-negotiable rules:

1. both engines consume the same `docs.ndjson` and the same logical keys;
2. both materialize the same nested scalar used by the exact filter;
3. every observed count and aggregate must match the generator before ratios
   are rendered; and
4. heap state, admitted cache, fixed off-heap capacity, durable file bytes, WAL
   bytes, and working buffers stay in
   separate columns.

## Reproducible engine

`run-duckdb.sh` defaults to the official DuckDB 1.5.4 image pinned by manifest
digest. Override `DUCKDB_IMAGE` only when the resulting report is labelled as a
different run. The runner passes `threads=1` and Docker's `--cpus=1` so query
parallelism and CPU capacity are bounded together.

The mechanisms used here are documented by DuckDB itself:

- the [`JSON` logical type](https://duckdb.org/docs/current/data/json/json_type)
  validates JSON and is physically stored as `VARCHAR`;
- [`read_ndjson_objects`](https://duckdb.org/docs/current/data/json/loading_json)
  reads one raw JSON object per NDJSON row;
- [JSON extraction](https://duckdb.org/docs/current/data/json/overview) resolves
  the configured nested path;
- [ART indexes](https://duckdb.org/docs/current/sql/indexes) persist key and
  scalar lookup structures;
- the [indexing guide](https://duckdb.org/docs/current/guides/performance/indexing)
  explains that ART scans are for point or highly selective predicates, that
  only a single plain column is eligible for an index scan, and that index
  buffers can remain resident;
- [`CHECKPOINT`](https://duckdb.org/docs/current/sql/statements/checkpoint)
  merges WAL state into the database file; and
- the [JSON profiler](https://duckdb.org/docs/current/configuration/pragmas#profiling)
  supplies high-resolution latency, rows scanned, buffer peak, temporary-space
  peak, and byte-I/O fields.

No network protocol is in the measurement path. DuckDB runs in its CLI only as
a pinned, dependency-free host for the embedded engine. Container and process
startup are excluded by using DuckDB's own query profiles.

## Corpus contract

`duckdbbench gen` writes, per corpus:

- `docs.ndjson`: one exact minified JSON object per line;
- `manifest.json`: query parameters, logical byte counts, NDJSON SHA-256, and
  expected results;
- `manifest.env`: the same safe alphanumeric parameters for the shell runner.

Keys are deterministic strings `doc:0`, `doc:1`, and so on. `SourceBytes` is
the sum of JSON bytes without newlines. `KeyBytes` is the sum of key bytes.
`LogicalBytes = SourceBytes + KeyBytes`; NDJSON separators are transport
framing and do not enter a payload ratio.

The synthetic families control structural diversity:

- `synth_s1`, `synth_s4`, and `synth_s64` have 1, 4, and 64 repeated shapes;
- `synth_hetero` gives every row a unique key set; and
- the real-derived corpora use records from the repository's pinned standard
  corpus with exact provenance.

The generator uses fixed PCG seeds. Generating the same specification twice
must yield byte-identical NDJSON and manifests. The runner recomputes SHA-256
before load, records it with DuckDB's raw artifacts, and the report rejects a
digest or byte-count mismatch.

## Physical state under test

Store bulk-loads through `StoreBuilder`, which copies every key and document,
then compacts its immutable key directory before publication. It subsequently
creates/backfills one exact index on the configured JSON pointer; completion
folds the transient posting tree into packed pointer-free posting pages. This
keeps load and index construction separately timed while exercising the same
mutable Store returned to applications.

FileStore starts from the same completed heap Store and writes one compact
mutable generation with `Store.WriteFileStore`. The creator repacks source
chunks into eight stable slots per document micro-page, writes exact JSON once,
packs many exact-posting streams per physical page, builds key/chunk/index/TTL
directories bottom-up, and then performs one data/tree fence plus one
superblock fence. It creates no per-row durable generations, free-tree history,
or persistent load commit arena. The timer includes NDJSON ingestion,
StoreBuilder completion, all page construction, both fences, and file close.

Packed posting pages are immutable bases. An online mutation redirects only
the affected stream to an isolated copy-on-write posting page and does not
retire shared base storage. This is a format invariant, not a benchmark-only
shortcut.

The measured file is reopened with synchronous mutations, an 8 MiB read-cache
capacity, a workload-sized 512-entry reusable-extent arena, and the smallest
power-of-two maximum document page that fits the corpus geometry. The latter
also bounds commit staging: the harness does not charge every 4 KiB document
page as though it required a 64 KiB buffer.

DuckDB bulk-loads this table:

```sql
CREATE TABLE docs AS
SELECT
    row_number() OVER () - 1 AS id,
    'doc:' || cast(row_number() OVER () - 1 AS VARCHAR) AS key,
    json AS doc,
    json_extract_string(json, '$.<filter path>') AS filter_value,
    try_cast(json_extract_string(json, '$.<sum path>') AS BIGINT) AS metric
FROM read_ndjson_objects('/corpus/docs.ndjson');
```

Index build is timed after load:

```sql
CREATE UNIQUE INDEX docs_key ON docs(key);
CREATE INDEX docs_filter ON docs(filter_value);
```

Materializing `filter_value` is intentional. It gives DuckDB a normal typed
column and a single-column ART index, rather than forcing repeated JSON parsing
or creating an expression index that DuckDB cannot use for an ART index scan.
The `metric` column makes the scalar aggregate comparable with Store's compiled
column path.

The harness does not create a compound index merely to advertise one. DuckDB
can persist compound and expression indexes, but its current index-scan path is
eligible only for a single untransformed column. Such an index would add load,
storage, and mutation cost without accelerating these scenarios.

## Operations

All read scenarios execute twice as unprofiled warmups, then `REPS` times with
JSON profiling enabled. Store also warms its lookup/query state and uses the
minimum repetition. The report uses DuckDB's `latency`, not shell wall time.

| label | heap Store | durable FileStore | DuckDB |
|---|---|---|---|
| point | `Snapshot.Get` plus compiled pointer | copied JSON plus structural index and compiled pointer | key ART lookup plus `json_extract_string` |
| filter | exact bitmap candidates plus scalar recheck | collision-free exact certificate + bitmap; document recheck fallback | `count(*) where filter_value = ?` |
| sum | compiled numeric column reduction | persistent typed cover when configured; JSON/page fallback otherwise | `sum(metric)` |
| group | compiled grouped count | bounded page scan and grouped count | SQL `group by filter_value` |
| contain | structural JSON containment | scalar-leaf object lowering + exact certificate; structural fallback | `json_contains(doc, ?::JSON)` |

The filter is deliberately low cardinality in clustered synthetic data. DuckDB
documents ART as a point/high-selectivity structure and may choose a vectorized
scan. The report prints the profiler's rows-scanned field, and the raw profiles
are retained so plan changes can be audited. We do not describe a query as
indexed merely because an index exists.

### Mutations

The mutation smoke chooses the first `min(rows, 256)` keys. Each key is updated
to the valid object `{"bench_mutation":true}`, clearing the indexed scalar and
metric, and is then deleted.

Each heap Store call publishes independently and remains an in-memory latency
diagnostic. Each FileStore mutation uses `Synchronous` and waits for its
checksummed data/root durability fence. DuckDB wraps each individual SQL
statement in its own explicit `BEGIN`/`COMMIT`; the reported per-operation
latency sums transaction start, mutation, and commit profiles. The durable
ratio is therefore FileStore versus one DuckDB transaction per key, not a SQL
batch versus a Go loop. This aligns transaction boundaries, not operating
system, container, filesystem, or storage-stack durability latency. FileStore
closes and reopens after the mutation sequence and accepts cardinality only
from the recovered root.

## Verification

The manifest records expected:

- row and projected-field counts;
- exact filter and structural-containment counts;
- numeric sum;
- group cardinality, including one SQL `NULL` group when the path is absent;
- row count after deterministic deletes.

Store rejects its artifact immediately on a mismatch. `ParseDuckDBRun` rejects
malformed facts or profiler streams, and the report labels any mismatch as a
failure before displaying ratios. Raw DuckDB JSON profiles are never rewritten.

## Storage and memory accounting

After load and both indexes, the runner executes `CHECKPOINT` and records:

- host byte length of `store.duckdb`;
- byte length of `store.duckdb.wal`, normally zero after checkpoint;
- profiler `system_peak_buffer_memory`;
- profiler `system_peak_temp_dir_size`; and
- database byte length after the mutation smoke; and
- WAL bytes after the mutation smoke.

Store records settled `runtime.MemStats.HeapAlloc` delta with input and caller
keys released, plus `Store.Stats` external key, document-directory, and index
blocks. The report shows heap, external blocks, and their sum separately. That
sum includes exact JSON, structural representation, key lookup, chunks,
snapshots, and the exact index, but remains an engine-accounted resident view
rather than process RSS. The index model is also reported as a diagnostic
subset.

FileStore records four distinct steady-state categories after recovery and
warm reads: settled Go heap, currently admitted cache extents, fixed
mmap-backed commit capacity, and the fixed pointer-free reusable-extent arena.
`accounted warm` adds heap, admitted—not configured—cache bytes, the complete
commit arena, and any reusable arena bytes outside the Go heap. The full
read-cache capacity remains separate. Query batch/merge high-water is caller
execution state and is not folded into the retained total. These are bounded
engine-accounting views, not process RSS.

FileStore file length is recorded after flushed close and compared directly
with DuckDB's checkpointed database length over the same logical payload and
index contract. Copy-on-write high water after mutations and already reusable
extent bytes are also reported beside DuckDB's post-mutation database and WAL
bytes. A reusable extent remains allocated file space, so it is not subtracted
from disk usage.

After the checkpoint, DuckDB runs the representative point, filter, aggregate,
group, and containment working set in one process and records the current
`duckdb_memory()` total plus its `ART_INDEX` subset. This is shown separately
from the maximum `system_peak_buffer_memory` observed across profiled stages.

These categories answer different questions:

- `Store accounted resident / logical payload` estimates current Store-owned
  heap plus pointer-free external expansion;
- `DuckDB file / logical payload` estimates durable compressed storage;
- DuckDB warm buffers estimate current engine-managed memory after the common
  working set;
- DuckDB buffer peak estimates engine-managed working memory for this run; and
- WAL bytes show uncheckpointed durable state.

Dividing Store resident bytes by DuckDB file bytes would mix resident memory
with durable compressed storage and is therefore forbidden by the report
generator. A resident-memory comparison needs process RSS or equivalent on
both sides under the same cache state; `duckdb_memory()` and Store's accounted
bytes are useful diagnostics but do not include identical runtime/process
overhead. A durable storage comparison needs Store's completed page format and
retained-generation policy.

## Timing boundaries

- Store and FileStore timings are direct in-process Go calls.
- DuckDB `latency` includes parsing, binding, planning, optimization, and
  execution inside the engine; it excludes container startup and CLI output.
- Both sides use one CPU execution lane.
- Load consumes the same NDJSON through each engine's intended bulk path.
- Index build is separate from load.
- FileStore bulk creation writes one generation, including its frozen index,
  and completes two durability fences before the load timer stops.
- FileStore recovery and synchronous mutation timings are separate from the
  compact creation timing.
- Read ratios are `DuckDB / Store`; greater than 1 means Store took less time.
- Storage ratios are each engine's bytes divided by logical key+JSON, never an
  engine-to-engine heap/disk ratio.

## Scale smoke: 10K, 100K, and 5M rows

From the `benchmarks` module:

```bash
# Repeat for 100000 and 5000000. Use a fresh output root for each scale.
rows=10000
root="results/scale-${rows}"

go run ./duckdbbench/cmd/duckdbbench gen \
  -dir "$root/corpora" -docs "$rows" -docbytes 400 -realbytes 1 -only synth_s4

go run ./duckdbbench/cmd/duckdbbench ours \
  -dir "$root/corpora" -out "$root/ours.json" -reps 3 -only synth_s4 \
  -host "machine model and OS version"

RESULTS="$root/duckdb" REPS=7 \
  ./duckdbbench/run-duckdb.sh "$root/corpora/synth_s4"

go run ./duckdbbench/cmd/duckdbbench report \
  -ours "$root/ours.json" -duckdb "$root/duckdb" \
  -out "$root/report.md"
```

The 5M run is intentionally not an ordinary test: `ours` now measures both the
heap Store and the durable FileStore, and `-reps 3` creates three complete
durable candidates before retaining the fastest. Use `-reps 1` for a capacity
smoke. Four-hundred-byte JSON rows plus both engines' indexes, durable state,
working buffers, and retained artifacts can consume several gigabytes. Record
machine model, available RAM, operating system, image digest, Go version, and a
clean working tree alongside any published run. A killed or swapping run is a
capacity result, not a latency result.

Ordinary plumbing validation is fast:

```bash
go test ./duckdbbench/...
```

For retained-heap attribution of one Store corpus, set
`DUCKDBBENCH_HEAP_PROFILE=/absolute/path/heap.pprof` on the `ours` command and
inspect it with `go tool pprof`. Profiling forces an additional GC and is a
diagnostic run, not timing evidence.

For the recovered FileStore side, use
`DUCKDBBENCH_FILE_HEAP_PROFILE=/absolute/path/filestore.pprof`. The fixed cache,
commit, and reusable-extent arenas can live outside Go heap accounting and are
reported separately, so a heap profile alone is not a resident-memory total.

The pinned container smoke is opt-in:

```bash
DUCKDBBENCH=1 go test ./duckdbbench -run TestPinnedDuckDBEndToEnd -count=1
```
