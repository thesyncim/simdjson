# Hook contracts

simdjson supports the standard `json.Marshaler`, `json.Unmarshaler`,
`encoding.TextMarshaler`, and `encoding.TextUnmarshaler` contracts. Native hooks
exist for generated or deliberately custom codecs; ordinary structs should use
the compiled `Encoder` and `Decoder` paths.

## Field matching

`FieldSet` follows the same lookup order as compiled decoding and
`encoding/json`:

1. exact spelling wins;
2. an unambiguous ASCII-folded lookup handles the common case; and
3. ordered `strings.EqualFold` matching handles non-ASCII names and folded
   collisions.

The ordered fallback preserves declaration precedence. Exact names are never
displaced by a folded match. Hook, compiled, and standard-library
differentials cover Unicode simple folds and collision-heavy sets.

## Output integrity

`TrustedAppender` and methods ending in `Unchecked` accept already-valid JSON
from the hook. They do not validate because validation would duplicate work in
generated encoders. Malformed bytes can therefore corrupt the surrounding JSON
document even though they cannot violate memory safety.

The appender is passed and returned by value so a generated body can keep its
slice header in registers while chaining writes. This code shape is part of the
hook benchmark contract, not a separate ownership mode.

The `simdjson_validate_hooks` build tag validates only the span emitted by a
hook. Tests and fuzzing use this mode to attribute malformed output without
adding work to production calls.

## Receiver lifetime

Encode hooks receive ordinary GC-visible Go values. Addressable receiver
identity is preserved; non-addressable values use a typed copy when required
by `encoding/json` compatibility.

Decode hooks receive `DecodeCursor` by value and return the advanced value. The
cursor copy contains ordinary Go slice and pointer fields, never a pointer into
the decoder's stack frame. A retained copy is memory-safe and owns the input it
references, but it is disconnected from the enclosing decode; only the value
returned by the method advances that operation.

Addressable decode receivers use ordinary Go pointer identity. Reused roots,
fields, and slice elements need no receiver shadow. A fresh stack-local root may
escape once because arbitrary user code can retain its receiver; hiding that
escape would be unsafe. The performance contract is zero allocations for reused
destinations and no allocation proportional to hook or element count.
