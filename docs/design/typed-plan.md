# Typed plan and specialization policy

Typed encoding and decoding have three layers:

1. a generic correct implementation for every supported Go shape;
2. an immutable reflect-compiled graph of `typedNode` and field operations; and
3. isolated executors for shapes that have an end-to-end measured benefit.

Compilation resolves field selection, tags, pointer hops, element sizes,
custom methods, reset behavior, encoder scratch slots, and eligible structural
routes. Calls then keep only mutable cursor or encoder state. Plans contain no
source or destination pointer and are safe for concurrent use.

## Operation matrix

`typedOp` is the shared vocabulary between the compiler and the executors. Its
scalar ordering is load-bearing because error retagging and structural
eligibility use range checks. Repetitive decode and encode switches must be
generated from one declarative operation table. Hand-written code remains only
where a route has different semantics, such as structural string advancement,
custom method selection, or a shape-specific fused program.

Generated output is committed. `go generate ./...` must reproduce it exactly,
and CI rejects a dirty result. Adding an operation therefore requires updating
the table and every explicitly exceptional route in the same change.

## Specialization budget

A specialization is accepted only with:

- a named end-to-end benchmark on the input shape it serves;
- a forced-route differential against the generic executor;
- allocation, malformed-input, and partial-failure coverage; and
- a removal threshold recorded with the benchmark.

The default removal threshold is a repeatable improvement of at least 3% in
`sec/op` on its target end-to-end benchmark without increasing `B/op` or
`allocs/op`. A synthetic kernel result alone is insufficient. Compiler changes
can invalidate code-shape wins, so the threshold is reevaluated when the pinned
Go revision advances.

Current shape programs include ordered small records, structural record
layouts, fixed float arrays, homogeneous scalar slices, and paired encoder
fields. A miss always returns to the generic plan; it never changes JSON
semantics.

## Dynamic interface types

Concrete types encountered through interface values are compiled on first use.
Encode plans are keyed by concrete type and HTML-escaping mode; decode plans
are keyed by concrete destination type. These `sync.Map` caches live for the
process lifetime so their hit path is deterministic and lock-free.

This policy is intended for the finite type sets used by normal Go programs.
Programs that synthesize unbounded `reflect.StructOf` types should not send them
through interface-valued codec paths. `TestDynamicEncodePlanCacheHighTypeCardinality`
exercises many distinct runtime types, verifies one entry per key, and proves
that repeated lookups reuse the same plan.
