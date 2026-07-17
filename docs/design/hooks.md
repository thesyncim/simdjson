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

Decode hooks receive a heap-backed `DecodeCursor` handle that owns its parser
state copy. The handle is invalidated on every exit path, including panic, and
the advanced state is copied back only through the controlled dispatch point.
Retaining the handle is a usage error and cannot expose a reused stack frame.

Native decode hooks allocate for their safe receiver and cursor ownership and
are not a general speed path. New complexity in that route requires a real code
generator, end-to-end evidence, and the same retention and panic-path tests as
the compiled decoder.
