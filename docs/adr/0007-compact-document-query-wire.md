# ADR 0007: Compact document queries and binary prepared plans

- Status: accepted design; implementation staged after the current storage
  format work
- Scope: query authoring, prepared execution, wire input, and index planning

## Decision

The canonical readable query is one compact JSON document. It follows document
shape instead of exposing SQL or storage-level pointer spelling:

```json
{
  "find": "orders",
  "where": {
    "customer": {
      "address": {"country": "PT"}
    },
    "items": {
      "$any": {
        "sku": "A-42",
        "qty": {"$gte": {"$param": 0}}
      }
    },
    "$or": [
      {"status": "paid"},
      {"priority": {"$gte": 8}}
    ]
  },
  "join": ["users", "customer.id", "id", "customer.profile"],
  "select": {
    "order": "id",
    "name": "customer.profile.name",
    "spent": {"$sum": "items.price"}
  },
  "group": ["customer.profile.region"],
  "having": {"spent": {"$gt": 1000}},
  "order": ["customer.profile.tier", "-createdAt"],
  "limit": 100
}
```

The defaults carry the common case:

- nested objects mean nested field matching;
- adjacent fields mean `AND`;
- a plain scalar means equality;
- `-field` means descending order;
- `find`, `where`, `join`, `select`, `group`, `having`, `order`, and `limit`
  compose in their ordinary relational order.

The reserved operator namespace is small: `$eq`, `$ne`, `$lt`, `$lte`, `$gt`,
`$gte`, `$in`, `$contains`, `$exists`, `$isnull`, `$any`, `$all`, `$and`,
`$or`, `$not`, `$param`, `$field`, and `$literal`. Aggregates use `$count`, `$sum`,
`$avg`, `$min`, and `$max`. An object containing a reserved operator is an
expression; `$literal` disambiguates source data whose keys begin with `$`.

One `join` tuple is `[collection, localField, foreignField, outputField]`.
Multiple joins use an array of those tuples. This compact surface is only an
authoring form: it is never retained by prepared execution.

## Field identity

The logical representation stores field paths as vectors of decoded string or
array-index segments. It does not store dotted strings or RFC 6901 text.
Readable dotted fields are shorthand compiled once by the client. A literal
field containing a dot uses the explicit segment form:

```json
{"$field": ["literal.name", "nested", 3, "value"]}
```

The pure-Go API constructs the same vectors without parsing:

```go
Field("customer", "address", "country")
Index(3)
```

Generated schema bindings can expose those values as constants. This keeps
wire and executor semantics unambiguous while preserving a short common form.

## Pure-Go construction

The convenience API mirrors the readable document:

```go
q := Find("orders", Match{
    "customer.address.country": "PT",
    "items": Any(Match{
        "sku": "A-42",
        "qty": GTE(Param(0)),
    }),
}).
    Join("users", "customer.id", "id", "customer.profile").
    Order("customer.profile.tier", "-createdAt").
    Limit(100)
```

Convenience maps and strings exist only while constructing a plan. `Prepare`
compiles them once. A lower-level builder appends typed field segments,
expressions, and relation operators directly into caller-owned storage and
uses neither maps nor reflection.

Every predicate is an expression and every relation consumes another
relation. `AND`, `OR`, `NOT`, containment, nested array predicates, joins,
projection, grouping, ordering, and limits therefore nest without
protocol-specific escape hatches. Adding a new operator means adding one typed
opcode and its validation rule, not inventing another string grammar.

## Binary plan

The hot wire accepts only the compiled plan. The readable JSON form is a
client/CLI adapter and is never parsed during execution.

The plan is one canonical post-order instruction stream plus length-prefixed
field, literal, and parameter descriptors:

```text
plan header
  magic/version/feature bits
  instruction bytes
  constant bytes
  parameter count

instruction stream
  opcode
  typed operands encoded as bounded unsigned varints

constant stream
  kind
  byte length
  exact JSON-semantic bytes
```

Post-order expressions and relations validate in one bounded linear pass with
a fixed-depth stack. Offsets, lengths, arity, types, feature bits, nesting
depth, and total work are checked before preparation. Unknown required
opcodes fail closed; optional negotiated metadata is skippable.

`Prepare` validates and lowers this logical stream once, compiles field
segments and containment needles, and returns an opaque prepared identifier.
`Execute` sends only that identifier and typed parameter bytes. The server
late-binds the current index roots and captures snapshots, but performs no
query-text parsing, map traversal, reflection, or logical-plan allocation.

Direct `Get`, `Put`, `Delete`, deadline, persist, and index-administration
messages have fixed payloads and never instantiate a query plan. Joins and
analytical operators therefore add no branch to point operations.

## JSON-semantic values and results

Wire values preserve the Store's JSON contract:

- null and booleans have single-byte tags;
- strings contain decoded UTF-8;
- numbers retain their validated decimal lexeme rather than silently becoming
  binary64;
- arrays retain order and heterogeneous values;
- objects retain ordered key/value pairs so duplicate-name handling remains
  explicit;
- every container has a bounded item count and byte length.

Raw source JSON remains available when exact whitespace or escape spelling
must round-trip. A future fused ingestion path may validate the binary value,
emit compact source bytes, and build structural metadata in one pass. Until
that exists, binary-to-source conversion is a measured cost and must not be
called zero-cost.

Query results use borrowed batches: validity bitmaps, fixed-width typed
vectors, offsets plus string/number bytes, and length-delimited nested values.
The pure-Go client exposes views valid until the next read or explicit
release. `Into` variants copy into caller storage. There is no object or
interface allocation per result row.

## Framing, flow control, and retries

The connection negotiates version and features once. Subsequent frames use a
small fixed header containing payload length, request/stream identifier,
message kind, and flags. CRC32C is negotiated for transports that do not
already provide authenticated integrity.

Streams use byte and row credits. A server cannot retain or send unbounded
result batches; releasing a borrowed batch returns credit. Cancellation is
stream-scoped and checked between page/index/join batches. Mutation requests
may carry an idempotency token and return the committed generation so a lost
response can be retried safely within a bounded deduplication window.

## Index derivation

The planner derives candidates from the same typed expression tree:

- equality and range predicates probe ordered typed field-segment indexes;
- nested array `$any`/`$all` use multikey entries that retain the parent
  stable-slot identity;
- a compound index can satisfy an equality prefix, one ordered range, and
  compatible `order` fields;
- a compatible ordered cursor applies `limit` without sorting or
  materializing the remaining rows;
- `AND` intersects stable-slot bitmaps and `OR` unions them before document
  access;
- containment indexes may only emit candidates; exact JSON-semantic
  containment remains the final authority;
- an equality join probes the indexed side with an indexed nested loop when
  selective, otherwise it uses a memory-bounded partitioned hash join;
- projections and filters are pushed below joins before build rows are
  materialized.

An index definition uses segment vectors and direction, not query strings.
Nested and compound definitions are therefore identical between the builder,
wire administration, durable catalog, and planner.

The current durable exact indexes are certified hash postings. They already
cover nested and one-to-four-column equality probes, but they do not yet
provide ordered range/order traversal, multikey array semantics, or join
planning. Those features require an ordered typed key format and bounded
backfill; this ADR does not mislabel the existing hash index as that work.

## Snapshot semantics for joins

A self-join uses one immutable snapshot. Collections currently publish
independently, so a cross-collection join captures one stable generation per
input but not one atomic database-wide instant. Atomic cross-collection joins
require a catalog commit epoch or transaction layer. The API will expose the
captured generations until that stronger contract exists.

## Performance contract

“Zero cost” here has a precise boundary:

- direct Go builders append a plan into caller-owned capacity;
- prepared execution performs no syntax parsing or logical-plan allocation;
- typed parameters and result batches reuse caller/connection buffers;
- point commands bypass the relation engine;
- index candidate algebra uses stable-slot bitmaps;
- every queue, batch, plan, nesting depth, and retained byte count is bounded.

Cold readable-JSON compilation, first preparation, network transfer, and
binary-value transcoding are real costs. Benchmarks report them separately
from steady prepared execution.
