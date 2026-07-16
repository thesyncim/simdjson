package simdjson

// Method hooks are the opt-in fast tier for typed decode and encode: the
// easyjson MarshalEasyJSON/UnmarshalEasyJSON analog, refined for this package's
// kernels. A type opts in by implementing [UnmarshalerSimd] or [MarshalerSimd]
// with signatures that shed everything that makes the standard custom methods
// slow — no per-call receiver boxing allocation, no output re-validation or
// compaction, no intermediate buffer. The compiled plan detects the interfaces
// at compile time and dispatches straight into the method, so a generated (or
// hand-written) body for a hot inner type captures the full monomorphized
// ceiling while every surrounding type stays on the zero-codegen compiled path.
//
// # The no-retention contract
//
// The [Cursor] passed to UnmarshalSimdJSON and the [Appender] passed to
// MarshalSimdJSON borrow state that lives on the enclosing decode or encode
// call's stack. Neither may be retained past the method's return: do not store
// the Cursor pointer or the Appender in a heap object, a goroutine, or a
// package variable, and do not capture either in a closure that outlives the
// call. A hook that only reads scalars, iterates members and elements, and
// returns (the shape a generator emits) satisfies this automatically. Building
// with -race, or with the simdjson_safehooks build tag, swaps in a
// memory-safe dispatch that turns a contract violation into a deterministic
// panic instead of silent corruption; see typed_hook_safe.go.
//
// # Safety
//
// The fast dispatch rebuilds the interface value from an itab captured once at
// compile time, which assumes the two-word runtime interface layout. That
// assumption is verified at package initialization (see hookLayoutOK); if it
// does not hold on the running toolchain the package falls back to a correct,
// slightly slower reflect-based dispatch rather than corrupting memory. The
// hooks never crash on a layout mismatch and never depend on an unverified
// unsafe assumption.

import (
	"reflect"
	"unsafe"
)

// UnmarshalerSimd is the opt-in decode hook. A type implements it to decode
// itself directly from the cursor, consuming exactly one complete JSON value.
// It is the simdjson-native counterpart of json.Unmarshaler, but it reads
// through the decoder's own kernels instead of receiving raw bytes, so there
// is no re-parse and no per-call allocation.
//
// The method must consume exactly one JSON value and leave the cursor
// positioned immediately after it, exactly as the compiled decoder would.
// Returning an error aborts the enclosing decode.
//
// The Cursor must not be retained past the call; see the no-retention contract
// in this file's package comment.
type UnmarshalerSimd interface {
	UnmarshalSimdJSON(c *Cursor) error
}

// MarshalerSimd is the opt-in encode hook. A type implements it to append its
// own compact JSON through the Appender's zero-cost helpers and return the
// advanced Appender. It is the simdjson-native counterpart of json.Marshaler.
//
// The by-value builder shape keeps the output buffer in registers across the
// whole body, which is measurably faster than a pointer-held writer. Bodies
// thread the Appender through and return it (w = w.Int(...), or chained). The
// output is trusted to be valid compact JSON for the value and is spliced into
// the surrounding document verbatim: there is no re-validation, compaction, or
// escape pass, which is the whole point of the hook. Emitting malformed JSON
// corrupts the surrounding document, so a generator must emit correct syntax.
//
// The Appender must not be retained past the call; see the no-retention
// contract in this file's package comment.
type MarshalerSimd interface {
	MarshalSimdJSON(w Appender) Appender
}

var (
	unmarshalerSimdReflectType = reflect.TypeFor[UnmarshalerSimd]()
	marshalerSimdReflectType   = reflect.TypeFor[MarshalerSimd]()
)

// Cursor is the public face of the typed decoder inside an UnmarshalSimdJSON
// body: a handle over the same interface-free parser the compiled interpreter
// drives, exposing the scalar kernels, the packed-key field matcher, and the
// array iterator. Generated code parses with exactly the machinery the
// compiled path uses, so a hook pays no interpretation overhead.
//
// A Cursor is only ever obtained as the argument to UnmarshalSimdJSON and must
// not be retained past that call.
type Cursor struct {
	d *decoderCursor
}

// Appender is the public face of the encoder inside a MarshalSimdJSON body: a
// by-value builder over the output buffer whose helpers are thin, inlinable
// wrappers over the package's append kernels. Bodies thread it through and
// return it, which keeps the buffer in registers for the whole method.
//
// Errors are sticky. The first helper that meets an unencodable value (a NaN
// or an infinity) poisons the builder and every later helper is a no-op; the
// enclosing encode reports the failure after the body returns, so a generated
// body stays straight-line with no per-helper error check. The poison is a
// plain bool rather than an error field so the builder stays register-sized —
// the shape the prototype's register-allocation study settled on.
type Appender struct {
	dst        []byte
	escapeHTML bool
	bad        bool
}

// --- Appender: encode helpers ----------------------------------------------

// Raw appends lit verbatim. The caller vouches that lit is valid JSON for the
// position; it is spliced in with no validation or escaping.
func (w Appender) Raw(lit string) Appender {
	w.dst = append(w.dst, lit...)
	return w
}

// RawBytes appends lit verbatim, the []byte form of Raw.
func (w Appender) RawBytes(lit []byte) Appender {
	w.dst = append(w.dst, lit...)
	return w
}

// RawByte appends one byte verbatim, typically a structural delimiter.
func (w Appender) RawByte(b byte) Appender {
	w.dst = append(w.dst, b)
	return w
}

// Null appends the JSON null literal.
func (w Appender) Null() Appender {
	w.dst = append(w.dst, "null"...)
	return w
}

// Bool appends true or false.
func (w Appender) Bool(v bool) Appender {
	if v {
		w.dst = append(w.dst, "true"...)
	} else {
		w.dst = append(w.dst, "false"...)
	}
	return w
}

// Int appends v in base 10.
func (w Appender) Int(v int64) Appender {
	w.dst = appendCompactInt(w.dst, v)
	return w
}

// Uint appends v in base 10.
func (w Appender) Uint(v uint64) Appender {
	w.dst = appendCompactUint(w.dst, v)
	return w
}

// String appends s as a JSON string under the encoder's escaping options,
// matching encoding/json: control characters, quotes, and backslashes are
// escaped, invalid UTF-8 becomes the replacement character, and HTML-sensitive
// bytes are escaped unless the encoder disabled HTML escaping.
func (w Appender) String(s string) Appender {
	w.dst = appendEncodedJSONString(w.dst, s, w.escapeHTML)
	return w
}

// Float64 appends v in encoding/json's shortest round-trippable form. A NaN or
// an infinity has no JSON form and poisons the Appender; the enclosing encode
// then reports the value as unsupported.
func (w Appender) Float64(v float64) Appender {
	dst, err := appendJSONFloat(w.dst, v, 64)
	if err != nil {
		w.bad = true
		return w
	}
	w.dst = dst
	return w
}

// Float32 appends v in encoding/json's shortest round-trippable form for a
// 32-bit float. A NaN or an infinity poisons the Appender.
func (w Appender) Float32(v float32) Appender {
	dst, err := appendJSONFloat(w.dst, float64(v), 32)
	if err != nil {
		w.bad = true
		return w
	}
	w.dst = dst
	return w
}

// EscapeHTML reports whether the encoder escapes HTML-sensitive bytes, so a
// body that formats its own output through Raw can match the option.
func (w Appender) EscapeHTML() bool { return w.escapeHTML }

// --- Cursor: object framing ------------------------------------------------

// ObjectOpen consumes the opening brace of an object, applying the decoder's
// depth guard. typeName names the type in the error if the next value is not
// an object.
func (c *Cursor) ObjectOpen(typeName string) error { return c.d.BeginObject(typeName) }

// ArrayOpen consumes the opening bracket of an array, applying the depth guard.
func (c *Cursor) ArrayOpen(typeName string) error { return c.d.BeginArray(typeName) }

// NextField is the general object-member iterator. Pass first=true only for
// the first call after ObjectOpen; it returns the next member's key and true,
// or "" and false at the closing brace. The returned key aliases the source
// (or the escaped-string arena) under the active decode mode and must not be
// mutated. Use it for arbitrary member order, unknown members, and duplicates;
// a straight-line body pairs it with a [FieldSet] for the packed-key match.
func (c *Cursor) NextField(first bool) (key string, ok bool, err error) {
	return c.d.NextObjectField(first)
}

// Field matches one expected member name with the packed one-word compare,
// consuming the comma (when first is false), the quoted name, and the colon on
// success, and leaving the cursor on the member value. It reports false without
// advancing when the next member is not name, so a body can fall back to
// NextField. Expected-order bodies chain Field calls; the first miss should
// drop to a NextField loop keyed by a [FieldSet].
func (c *Cursor) Field(first bool, f *Field) bool {
	return c.d.matchObjectFieldExpected(first, &f.f)
}

// CaseSensitive reports whether the decoder was compiled with
// DecoderOptions.CaseSensitive, so a NextField loop can fold key comparisons to
// match the decoder's own field matching.
func (c *Cursor) CaseSensitive() bool { return c.d.CaseSensitive() }

// --- Cursor: array framing -------------------------------------------------

// NextElement reports whether another array element follows. Pass first=true
// only for the first call after ArrayOpen. It consumes the comma between
// elements and the closing bracket at the end.
func (c *Cursor) NextElement(first bool) (bool, error) { return c.d.NextArrayElement(first) }

// --- Cursor: low-level positioning -----------------------------------------

// Expect consumes ch when it is the next byte, without skipping whitespace, and
// reports whether it did. It lets a body stay on the packed path for a compact
// document and fall back explicitly when a delimiter is missing.
func (c *Cursor) Expect(ch byte) bool {
	d := c.d
	if i := d.i; i < len(d.src) && d.src[i] == ch {
		d.i = i + 1
		return true
	}
	return false
}

// ExpectObjectClose consumes a closing brace, updating the depth bookkeeping,
// and reports whether it did. A body uses it to close an object opened with
// ObjectOpen after matching every member in order.
func (c *Cursor) ExpectObjectClose() bool {
	d := c.d
	if i := d.i; i < len(d.src) && d.src[i] == '}' {
		d.i = i + 1
		d.depth--
		return true
	}
	return false
}

// Skip validates and consumes exactly one JSON value without materializing it,
// for a member or element a body does not model.
func (c *Cursor) Skip() error { return c.d.Skip() }

// Null consumes a null literal and reports true, or leaves a non-null value in
// place and reports false. A body calls it before a scalar read to distinguish
// an absent value.
func (c *Cursor) Null() (bool, error) { return c.d.TryNull() }

// Raw captures the raw bytes of the next JSON value, validating and consuming
// exactly one value. The returned RawValue aliases the source buffer, so it is
// valid only under the input's lifetime and, in zero-copy mode, only while the
// input is unmodified.
func (c *Cursor) Raw() (RawValue, error) {
	d := c.d
	start := d.i
	if err := d.Skip(); err != nil {
		return RawValue{}, err
	}
	return RawValue{src: d.src[start:d.i]}, nil
}

// --- Cursor: scalar reads --------------------------------------------------

// Bool decodes a JSON boolean into dst.
func (c *Cursor) Bool(dst *bool) error { return c.d.Bool(dst) }

// Int decodes a JSON number into an int.
func (c *Cursor) Int(dst *int) error { return c.d.Int(dst) }

// Int8 decodes a JSON number into an int8.
func (c *Cursor) Int8(dst *int8) error { return c.d.Int(dst) }

// Int16 decodes a JSON number into an int16.
func (c *Cursor) Int16(dst *int16) error { return c.d.Int(dst) }

// Int32 decodes a JSON number into an int32.
func (c *Cursor) Int32(dst *int32) error { return c.d.Int(dst) }

// Int64 decodes a JSON number into an int64.
func (c *Cursor) Int64(dst *int64) error { return c.d.Int(dst) }

// Uint decodes a JSON number into a uint.
func (c *Cursor) Uint(dst *uint) error { return c.d.Uint(dst) }

// Uint8 decodes a JSON number into a uint8.
func (c *Cursor) Uint8(dst *uint8) error { return c.d.Uint(dst) }

// Uint16 decodes a JSON number into a uint16.
func (c *Cursor) Uint16(dst *uint16) error { return c.d.Uint(dst) }

// Uint32 decodes a JSON number into a uint32.
func (c *Cursor) Uint32(dst *uint32) error { return c.d.Uint(dst) }

// Uint64 decodes a JSON number into a uint64.
func (c *Cursor) Uint64(dst *uint64) error { return c.d.Uint(dst) }

// Float32 decodes a JSON number into a float32.
func (c *Cursor) Float32(dst *float32) error { return c.d.Float(dst) }

// Float64 decodes a JSON number into a float64.
func (c *Cursor) Float64(dst *float64) error { return c.d.Float(dst) }

// String decodes a JSON string into dst, unescaping as needed. In zero-copy
// mode an unescaped string aliases the source.
func (c *Cursor) String(dst *string) error { return c.d.String(dst) }

// Number decodes a JSON number as its literal text, for a json.Number-style
// field that preserves the exact digits.
func (c *Cursor) Number(dst *string) error { return c.d.Number(dst) }

// --- Field / FieldSet: packed-key matchers ---------------------------------

// Field is a precompiled member-name matcher: the packed first-word compare the
// interpreter uses for expected-order matching. Build one per member at init
// time with [MakeField] and reuse it for the life of the program; it is
// immutable and safe to share across goroutines.
type Field struct {
	f typedField
}

// Name reports the member name the Field matches.
func (f Field) Name() string { return f.f.name }

// FieldSet groups a struct's Fields so an arbitrary-order body can resolve a
// key from NextField to a member index with one lookup, the same case-folding
// the compiled decoder applies. Build it once with [MakeFieldSet].
type FieldSet struct {
	fields     []Field
	byName     map[string]int
	byNameFold map[string]int
}

// MakeField packs name for the one-word member match. Names of seven bytes or
// fewer pack the closing quote and the following colon into the same word, so a
// single masked compare matches the name, its terminator, and the separator at
// once. Names longer than 255 bytes fall back to the cursor's general key path.
func MakeField(name string) Field {
	var f typedField
	f.name = name
	if len(name) <= 7 {
		for byteIndex := range len(name) {
			ch := name[byteIndex]
			f.key |= uint64(ch) << (byteIndex * 8)
			if lower := ch | 0x20; 'a' <= lower && lower <= 'z' {
				f.keyFold |= 0x20 << (byteIndex * 8)
			}
		}
		f.key |= uint64('"') << (len(name) * 8)
		if len(name) <= 6 {
			f.key |= uint64(':') << ((len(name) + 1) * 8)
			f.keyMask = ^uint64(0) >> ((6 - len(name)) * 8)
		} else {
			f.keyMask = ^uint64(0)
		}
		f.keyLen = uint8(len(name))
	} else if len(name) <= 255 {
		for byteIndex := range 8 {
			ch := name[byteIndex]
			f.key |= uint64(ch) << (byteIndex * 8)
			if lower := ch | 0x20; 'a' <= lower && lower <= 'z' {
				f.keyFold |= 0x20 << (byteIndex * 8)
			}
		}
		f.keyMask = ^uint64(0)
		f.keyLen = uint8(len(name))
	}
	return Field{f: f}
}

// MakeFieldSet builds a FieldSet over the given member names, indexed by
// declaration order. The returned set both exposes each name's packed [Field]
// (via Field) for the expected-order fast path and resolves an arbitrary key to
// its index (via Lookup) for the general path.
func MakeFieldSet(names ...string) FieldSet {
	set := FieldSet{
		fields:     make([]Field, len(names)),
		byName:     make(map[string]int, len(names)),
		byNameFold: make(map[string]int, len(names)),
	}
	for i, name := range names {
		set.fields[i] = MakeField(name)
		set.byName[name] = i
		if fold := foldFieldKey(name); fold != "" {
			// Last writer wins on a fold collision, matching encoding/json's
			// first-declared preference only when names are distinct; a
			// generator should keep member names unique when folded.
			if _, ok := set.byNameFold[fold]; !ok {
				set.byNameFold[fold] = i
			}
		}
	}
	return set
}

// Len reports the number of members in the set.
func (s FieldSet) Len() int { return len(s.fields) }

// Field returns the packed matcher for member index i, for the expected-order
// fast path: c.Field(first, set.Field(i)).
func (s FieldSet) Field(i int) *Field { return &s.fields[i] }

// Lookup resolves a key from NextField to a member index and true, or -1 and
// false for an unknown member. It matches exactly when caseSensitive is true
// and otherwise falls back to a case-folded match, mirroring the compiled
// decoder's own field matching. Pass c.CaseSensitive() so a body honours
// DecoderOptions.CaseSensitive.
func (s FieldSet) Lookup(key string, caseSensitive bool) (int, bool) {
	if i, ok := s.byName[key]; ok {
		return i, true
	}
	if caseSensitive {
		return -1, false
	}
	if i, ok := s.byNameFold[foldFieldKey(key)]; ok {
		return i, true
	}
	return -1, false
}

// foldFieldKey lower-cases the ASCII letters of a key for the case-insensitive
// index. A key with no ASCII letters returns itself. It is a fast, allocation-
// light fold that matches strings.EqualFold on pure-ASCII names, which struct
// tags almost always are; non-ASCII keys still resolve exactly through byName.
func foldFieldKey(key string) string {
	needs := false
	for i := 0; i < len(key); i++ {
		if c := key[i]; 'A' <= c && c <= 'Z' {
			needs = true
			break
		}
	}
	if !needs {
		return key
	}
	b := make([]byte, len(key))
	for i := 0; i < len(key); i++ {
		c := key[i]
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		b[i] = c
	}
	return string(b)
}

// --- plan dispatch ---------------------------------------------------------

// hookIface mirrors the runtime's two-word non-empty interface value: a pointer
// to the itab and a pointer to the data. The plan compiler captures the itab
// for *T -> hook interface once at compile time (via one ordinary assertion on
// a probe value); dispatch then rebuilds the interface with two word stores,
// avoiding reflect.NewAt, a runtime type assertion, and any allocation on the
// per-element hot path. The itab is process-stable, so caching it on the
// immutable plan is sound. The layout this type assumes is verified at package
// init by verifyHookLayout; hookLayoutOK gates every use of it.
type hookIface struct {
	tab  unsafe.Pointer
	data unsafe.Pointer
}

// captureHookTabs records, on the node, the itab words for whichever hook
// interfaces *typ implements, so later dispatch is two word stores plus the
// call. It runs at compile time only. When the runtime interface layout failed
// its init self-test (hookLayoutOK is false) the itabs are left nil and every
// dispatch takes the reflect fallback, so a layout mismatch degrades to correct
// and slower rather than unsafe.
func captureHookTabs(node *typedNode, typ reflect.Type, decode, encode bool) {
	if !hookLayoutOK {
		return
	}
	probe := reflect.New(typ).Interface()
	if decode {
		hook := probe.(UnmarshalerSimd)
		node.decHookTab = (*hookIface)(unsafe.Pointer(&hook)).tab
	}
	if encode {
		hook := probe.(MarshalerSimd)
		node.encHookTab = (*hookIface)(unsafe.Pointer(&hook)).tab
	}
}

// decodeViaSimdHook decodes the value at dst through its own UnmarshalSimdJSON.
// On the fast path it rebuilds the *T -> UnmarshalerSimd interface from the
// compile-time itab with two word stores and one indirect call; the cursor
// pointer is laundered through noescape so the opaque interface call does not
// force every enclosing cursor to the heap, which the no-retention contract
// keeps sound. When the itab is absent (layout self-test failed, or a
// safe-hooks build) it dispatches through reflect instead — correct and
// memory-safe even if the body violates the contract.
func (cursor *decoderCursor) decodeViaSimdHook(node *typedNode, dst unsafe.Pointer) error {
	if node.decHookTab == nil {
		return cursor.decodeViaSimdHookReflect(node, dst)
	}
	return decodeViaSimdHookFast(cursor, node.decHookTab, dst)
}

// decodeViaSimdHookReflect is the safe, reflect-based decode dispatch: the
// itab-fallback path and the whole safe-hooks build route through it. It boxes
// a genuine *T receiver, so the interface value and the pointer it carries are
// ordinary heap-safe values and a retained Cursor cannot alias freed stack.
//
// dst is the real decode destination — the caller's value, or a field within
// it — alive for the whole call, so laundering it through noescape (the same
// idiom valueInterfaceAt uses) keeps this reflect branch from forcing every
// enclosing decode's destination to the heap even in a build where this branch
// is present but rarely taken.
func (cursor *decoderCursor) decodeViaSimdHookReflect(node *typedNode, dst unsafe.Pointer) error {
	hook, ok := reflect.NewAt(node.typ, noescape(dst)).Interface().(UnmarshalerSimd)
	if !ok {
		return &DecodeError{Offset: cursor.i, Type: node.typ, Reason: "invalid compiled operation"}
	}
	return dispatchDecodeHook(hook, cursor)
}

// encodeViaSimdHook encodes the value at src through its own MarshalSimdJSON,
// splicing the type's own compact JSON with none of the validation and
// re-escape passes a std-signature marshaler forces. On the fast path the
// interface is rebuilt from the compile-time itab and the buffer travels by
// value through the builder and back through the return, so nothing escapes and
// nothing allocates.
func (e *encodeState) encodeViaSimdHook(node *typedNode, src unsafe.Pointer) error {
	var w Appender
	if node.encHookTab == nil {
		hook, ok := reflect.NewAt(node.typ, noescape(src)).Interface().(MarshalerSimd)
		if !ok {
			return &EncodeError{Reason: "invalid compiled operation"}
		}
		w = dispatchEncodeHook(hook, Appender{dst: e.dst, escapeHTML: e.escapeHTML})
	} else {
		w = encodeViaSimdHookFast(node.encHookTab, src, Appender{dst: e.dst, escapeHTML: e.escapeHTML})
	}
	e.dst = w.dst
	if w.bad {
		return &EncodeError{Reason: "MarshalSimdJSON: unsupported value"}
	}
	return nil
}
