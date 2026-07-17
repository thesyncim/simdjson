# Ownership and lifetimes

simdjson keeps every live Go pointer visible to the garbage collector. It does
not use `uintptr` storage, private runtime layouts, noescape declarations, or
interface-header rewriting. `unsafe` is used for checked addressing and
zero-copy views; the complete scope inventory is in [`UNSAFE.md`](../../UNSAFE.md).

## Public values

| Operation | Storage relationship | Invalidation rule |
| --- | --- | --- |
| Default typed decode | Unescaped strings may share one private copy of the input. Containers belong to the destination. | Independent of later mutation of the caller's input. |
| Typed decode with `ZeroCopy` | Unescaped strings alias the caller's input. Escaped strings use owned decoded storage. | Do not mutate the input while an aliased result is in use. |
| Default `Parse` | `Value` owns the source and structural entries it needs. | The result remains valid after the input variable is released. |
| `Parse` with `ZeroCopy` | The tree retains zero-copy views of the input. | Do not mutate the input while the tree is in use. |
| `Index`, `Node`, and `RawValue` | Results refer to validated caller-provided source and, for an index, caller-provided entry storage. | Treat both buffers as immutable until every derived handle is discarded. |
| `Reader.Bytes`, `Reader.Raw`, and `Reader.Cursor` | Results refer to the reader's rolling buffer. | Invalid after the next `Next`, `DecodeNext`, or `Close`. |
| Encoder and writer output | Returned bytes belong to the caller. | A source graph must not overlap the output capacity being appended to. |

Borrowing avoids a copy; it does not hide the pointer. `Node` stores typed
pointers to source and entry storage, strings are ordinary Go strings, and
slices are ordinary Go slices. The runtime can therefore keep backing storage
alive for as long as a derived Go value remains reachable.

## Compiled destinations

The typed compiler records reflect-provided field offsets, element sizes, and
pointer hops. Executors address a destination only after the corresponding
pointer has been allocated and the slice or array bound has been established.
Slice growth happens before an element pointer is formed. A plan is immutable;
all cursor, arena, error-path, and scratch state belongs to one call.

Default decoding merges into existing state like `encoding/json`. Replace mode
resets absent state deliberately. This distinction is a semantic contract, not
an ownership optimization.

## User methods and hooks

Addressable encode receivers follow ordinary Go method semantics. If a method
can retain its receiver, escape analysis keeps that receiver alive. A
non-addressable map or interface value is copied into typed addressable storage
before dispatch.

Decode methods receive a heap-backed receiver shadow that is copied back before
return. A native decode hook receives a heap-backed cursor handle that is
invalidated on success, error, or panic. Retaining the handle therefore traps
instead of observing a reused parser frame.

The lifetime contract is enforced by the ownership, GC, corruption, hook
retention, reader lifecycle, route differential, race, and checkptr suites.
