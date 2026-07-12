package simdjson

import (
	"encoding"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/bits"
	"reflect"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"unicode/utf8"
	"unsafe"
)

// EncoderOptions controls encoding output.
type EncoderOptions struct {
	// DisableHTMLEscaping leaves <, >, and & unescaped in strings, like
	// json.Encoder.SetEscapeHTML(false). The zero value matches
	// encoding/json.Marshal, which escapes them.
	DisableHTMLEscaping bool
}

// Encoder is an immutable encoder for one concrete Go type. Compile it once
// and reuse it concurrently; AppendJSON keeps all mutable state local to the
// call. Output matches encoding/json byte for byte: compact, with U+2028 and
// U+2029 escaped and invalid UTF-8 replaced by the replacement character.
type Encoder[T any] struct {
	root       *typedNode
	escapeHTML bool
}

// CompileEncoder builds an encoder for T. It supports the same types as
// CompileDecoder plus the omitempty and string tag options.
func CompileEncoder[T any](opts EncoderOptions) (Encoder[T], error) {
	typ := reflect.TypeFor[T]()
	compiler := typedCompiler{nodes: make(map[reflect.Type]*typedNode)}
	root, err := compiler.compile(typ, typ.String())
	if err != nil {
		return Encoder[T]{}, err
	}
	return Encoder[T]{root: root, escapeHTML: !opts.DisableHTMLEscaping}, nil
}

// AppendJSON appends src encoded as compact JSON to dst. It does not force src
// onto the heap, so a custom marshal method must not retain a stack-backed
// receiver after it returns. The same restriction applies to Marshal.
func (plan Encoder[T]) AppendJSON(dst []byte, src *T) ([]byte, error) {
	if plan.root == nil {
		return dst, fmt.Errorf("simdjson: zero Encoder")
	}
	if src == nil {
		return dst, fmt.Errorf("simdjson: encode source is nil")
	}
	e := encodeState{dst: dst, escapeHTML: plan.escapeHTML}
	if err := e.encode(plan.root, unsafe.Pointer(src)); err != nil {
		return dst, err
	}
	return e.dst, nil
}

// unmarshalEncoders caches one encoder per source type for Marshal.
var unmarshalEncoders sync.Map

type cachedEncoder[T any] struct {
	encoder  Encoder[T]
	err      error
	sizeHint atomic.Uint64
}

// Marshal encodes src like encoding/json.Marshal, including HTML escaping.
// The encoder for each source type is compiled once and cached for the
// process lifetime, along with a running output-size hint that presizes the
// result buffer. Hot paths that encode one type repeatedly should call
// CompileEncoder once and reuse the returned Encoder with AppendJSON to
// recycle output buffers.
func Marshal[T any](src *T) ([]byte, error) {
	entry, ok := unmarshalEncoders.Load(reflect.TypeFor[T]())
	if !ok {
		entry = newCachedEncoder[T]()
	}
	cached := entry.(*cachedEncoder[T])
	if cached.err != nil {
		return nil, cached.err
	}
	hint := cached.sizeHint.Load()
	if hint < 64 {
		hint = 64
	}
	out, err := cached.encoder.AppendJSON(make([]byte, 0, hint), src)
	if err != nil {
		return nil, err
	}
	if size := uint64(len(out)); size > cached.sizeHint.Load() {
		cached.sizeHint.Store(size)
	}
	return out, nil
}

//go:noinline
func newCachedEncoder[T any]() any {
	encoder, err := CompileEncoder[T](EncoderOptions{})
	entry, _ := unmarshalEncoders.LoadOrStore(reflect.TypeFor[T](), &cachedEncoder[T]{encoder: encoder, err: err})
	return entry
}

// EncodeError reports a Go value that cannot be represented in JSON.
type EncodeError struct {
	// Path locates the offending value using JSON member names and array
	// indexes, for example "items[3].scores[1]". It is empty when the
	// top-level value itself failed. Building the path costs nothing until
	// an error actually unwinds.
	Path string

	Reason string
}

func (e *EncodeError) Error() string {
	if e.Path != "" {
		return fmt.Sprintf("simdjson: cannot encode value at %s: %s", e.Path, e.Reason)
	}
	return "simdjson: cannot encode value: " + e.Reason
}

func prependEncodePathField(err error, name string) error {
	if e, ok := err.(*EncodeError); ok {
		switch {
		case e.Path == "":
			e.Path = name
		case e.Path[0] == '[':
			e.Path = name + e.Path
		default:
			e.Path = name + "." + e.Path
		}
	}
	return err
}

func prependEncodePathIndex(err error, index int) error {
	if e, ok := err.(*EncodeError); ok {
		segment := "[" + strconv.Itoa(index) + "]"
		if e.Path == "" || e.Path[0] == '[' {
			e.Path = segment + e.Path
		} else {
			e.Path = segment + "." + e.Path
		}
	}
	return err
}

type encodeState struct {
	dst        []byte
	depth      int
	escapeHTML bool
}

func (e *encodeState) encode(node *typedNode, src unsafe.Pointer) error {
	return e.encodeKind(node, src, node.encKind)
}

func (e *encodeState) encodeKind(node *typedNode, src unsafe.Pointer, kind typedKind) error {
	switch kind {
	case typedBool:
		if *(*bool)(src) {
			e.dst = append(e.dst, "true"...)
		} else {
			e.dst = append(e.dst, "false"...)
		}
	case typedString:
		e.dst = appendEncodedJSONString(e.dst, *(*string)(src), e.escapeHTML)
	case typedNumber:
		return e.encodeNumberLiteral(*(*string)(src))
	case typedInt:
		switch node.bits {
		case 8:
			e.dst = appendCompactInt(e.dst, int64(*(*int8)(src)))
		case 16:
			e.dst = appendCompactInt(e.dst, int64(*(*int16)(src)))
		case 32:
			e.dst = appendCompactInt(e.dst, int64(*(*int32)(src)))
		case 64:
			e.dst = appendCompactInt(e.dst, *(*int64)(src))
		}
	case typedUint:
		switch node.bits {
		case 8:
			e.dst = appendCompactUint(e.dst, uint64(*(*uint8)(src)))
		case 16:
			e.dst = appendCompactUint(e.dst, uint64(*(*uint16)(src)))
		case 32:
			e.dst = appendCompactUint(e.dst, uint64(*(*uint32)(src)))
		case 64:
			e.dst = appendCompactUint(e.dst, *(*uint64)(src))
		}
	case typedFloat:
		if node.bits == 32 {
			return e.encodeFloat(float64(*(*float32)(src)), 32)
		}
		return e.encodeFloat(*(*float64)(src), 64)
	case typedStruct:
		return e.encodeStruct(node, src)
	case typedSlice:
		return e.encodeSlice(node, src)
	case typedArray:
		return e.encodeArray(node, src)
	case typedMap:
		return e.encodeMap(node, src)
	case typedAny:
		return e.encodeAny(src)
	case typedMarshalerJSON, typedMarshalerText:
		return e.encodeMarshalerKind(node, src, kind)
	case typedIface:
		value := reflect.NewAt(node.typ, noescape(src)).Elem()
		if value.IsNil() {
			e.dst = append(e.dst, "null"...)
			return nil
		}
		return e.encodeDynamicValue(value.Elem())
	case typedBytes:
		header := (*typedSliceHeader)(src)
		if header.data == nil {
			e.dst = append(e.dst, "null"...)
			return nil
		}
		raw := unsafe.Slice((*byte)(header.data), header.len)
		e.dst = append(e.dst, '"')
		e.dst = base64.StdEncoding.AppendEncode(e.dst, raw)
		e.dst = append(e.dst, '"')
		return nil
	case typedPointer:
		pointer := *(*unsafe.Pointer)(src)
		if pointer == nil {
			e.dst = append(e.dst, "null"...)
			return nil
		}
		if e.depth >= defaultMaxDepth {
			return &EncodeError{Reason: "maximum nesting depth exceeded"}
		}
		e.depth++
		err := e.encode(node.elem, pointer)
		e.depth--
		return err
	default:
		return &EncodeError{Reason: "invalid compiled operation"}
	}
	return nil
}

func (e *encodeState) encodeStruct(node *typedNode, src unsafe.Pointer) error {
	if e.depth >= defaultMaxDepth {
		return &EncodeError{Reason: "maximum nesting depth exceeded"}
	}
	e.depth++
	e.dst = append(e.dst, '{')
	first := true
	for i := range node.fields {
		field := &node.fields[i]
		encField := &node.encFields[i]
		fieldBase := src
		if field.hop >= 0 {
			fieldBase = resolveResetHops(src, node.fieldHops[field.hop])
			if fieldBase == nil {
				// A nil embedded pointer omits its promoted fields entirely.
				continue
			}
		}
		fieldSrc := unsafe.Add(fieldBase, field.offset)
		if encField.omitEmpty && typedValueIsEmpty(field.node, fieldSrc) {
			continue
		}
		if !first {
			e.dst = append(e.dst, ',')
		}
		first = false
		if e.escapeHTML {
			e.dst = append(e.dst, encField.encName...)
		} else {
			e.dst = append(e.dst, encField.encNamePlain...)
		}
		var err error
		switch encField.encOp {
		case typedOpBool:
			if *(*bool)(fieldSrc) {
				e.dst = append(e.dst, "true"...)
			} else {
				e.dst = append(e.dst, "false"...)
			}
		case typedOpString:
			e.dst = appendEncodedJSONString(e.dst, *(*string)(fieldSrc), e.escapeHTML)
		case typedOpInt64:
			e.dst = appendCompactInt(e.dst, *(*int64)(fieldSrc))
		case typedOpUint64:
			e.dst = appendCompactUint(e.dst, *(*uint64)(fieldSrc))
		case typedOpFloat64:
			err = e.encodeFloat(*(*float64)(fieldSrc), 64)
		case typedOpQuoted:
			err = e.encodeQuoted(field.node, fieldSrc)
		case typedOpMarshaler:
			err = e.encodeMarshaler(field.node, fieldSrc)
		default:
			err = e.encode(field.node, fieldSrc)
		}
		if err != nil {
			e.depth--
			return prependEncodePathField(err, field.name)
		}
	}
	e.dst = append(e.dst, '}')
	e.depth--
	return nil
}

func (e *encodeState) encodeSlice(node *typedNode, src unsafe.Pointer) error {
	header := (*typedSliceHeader)(src)
	if header.data == nil {
		e.dst = append(e.dst, "null"...)
		return nil
	}
	if e.depth >= defaultMaxDepth {
		return &EncodeError{Reason: "maximum nesting depth exceeded"}
	}
	e.depth++
	e.dst = append(e.dst, '[')
	for index := 0; index < header.len; index++ {
		if index > 0 {
			e.dst = append(e.dst, ',')
		}
		element := unsafe.Add(header.data, uintptr(index)*node.elem.size)
		if err := e.encode(node.elem, element); err != nil {
			e.depth--
			return prependEncodePathIndex(err, index)
		}
	}
	e.dst = append(e.dst, ']')
	e.depth--
	return nil
}

func (e *encodeState) encodeArray(node *typedNode, src unsafe.Pointer) error {
	if e.depth >= defaultMaxDepth {
		return &EncodeError{Reason: "maximum nesting depth exceeded"}
	}
	e.depth++
	e.dst = append(e.dst, '[')
	for index := 0; index < node.length; index++ {
		if index > 0 {
			e.dst = append(e.dst, ',')
		}
		element := unsafe.Add(src, uintptr(index)*node.elem.size)
		if err := e.encode(node.elem, element); err != nil {
			e.depth--
			return prependEncodePathIndex(err, index)
		}
	}
	e.dst = append(e.dst, ']')
	e.depth--
	return nil
}

// dynamicEncodeNodes caches one compiled encode plan per concrete type seen
// inside an interface value.
var dynamicEncodeNodes sync.Map

type dynamicEncodeEntry struct {
	node *typedNode
	err  error
}

func dynamicEncodeNode(typ reflect.Type) (*typedNode, error) {
	if entry, ok := dynamicEncodeNodes.Load(typ); ok {
		cached := entry.(*dynamicEncodeEntry)
		return cached.node, cached.err
	}
	compiler := typedCompiler{nodes: make(map[reflect.Type]*typedNode)}
	node, err := compiler.compile(typ, typ.String())
	entry, _ := dynamicEncodeNodes.LoadOrStore(typ, &dynamicEncodeEntry{node: node, err: err})
	cached := entry.(*dynamicEncodeEntry)
	return cached.node, cached.err
}

// encodeAny encodes the concrete value stored in an empty interface,
// compiling a plan for its type on first use.
func (e *encodeState) encodeAny(src unsafe.Pointer) error {
	value := *(*any)(src)
	switch concrete := value.(type) {
	case nil:
		e.dst = append(e.dst, "null"...)
		return nil
	case bool:
		if concrete {
			e.dst = append(e.dst, "true"...)
		} else {
			e.dst = append(e.dst, "false"...)
		}
		return nil
	case string:
		e.dst = appendEncodedJSONString(e.dst, concrete, e.escapeHTML)
		return nil
	case float64:
		return e.encodeFloat(concrete, 64)
	case int:
		e.dst = appendCompactInt(e.dst, int64(concrete))
		return nil
	case int64:
		e.dst = appendCompactInt(e.dst, concrete)
		return nil
	}
	if e.depth >= defaultMaxDepth {
		return &EncodeError{Reason: "maximum nesting depth exceeded"}
	}
	return e.encodeDynamicValue(reflect.ValueOf(value))
}

// encodeDynamicValue encodes a concrete reflect value through a cached plan
// for its type.
func (e *encodeState) encodeDynamicValue(value reflect.Value) error {
	if e.depth >= defaultMaxDepth {
		return &EncodeError{Reason: "maximum nesting depth exceeded"}
	}
	node, err := dynamicEncodeNode(value.Type())
	if err != nil {
		return &EncodeError{Reason: err.Error()}
	}
	box := reflect.New(value.Type())
	box.Elem().Set(value)
	e.depth++
	encodeErr := e.encodeNonAddressable(node, box.UnsafePointer())
	e.depth--
	return encodeErr
}

func (e *encodeState) encodeNonAddressable(node *typedNode, src unsafe.Pointer) error {
	return e.encodeKind(node, src, node.encNonAddrKind)
}

// encodeMap writes a map with string keys as an object with byte-sorted
// members, matching encoding/json. Values are copied into one reusable
// addressable element before encoding.
func (e *encodeState) encodeMap(node *typedNode, src unsafe.Pointer) error {
	mapValue := reflect.NewAt(node.typ, noescape(src)).Elem()
	if mapValue.IsNil() {
		e.dst = append(e.dst, "null"...)
		return nil
	}
	if e.depth >= defaultMaxDepth {
		return &EncodeError{Reason: "maximum nesting depth exceeded"}
	}
	e.depth++
	entries := make([]mapEncodeEntry, 0, mapValue.Len())
	iterator := mapValue.MapRange()
	for iterator.Next() {
		name, err := mapKeyName(node, iterator.Key())
		if err != nil {
			e.depth--
			return &EncodeError{Reason: err.Error()}
		}
		entries = append(entries, mapEncodeEntry{name: name, key: iterator.Key()})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	element := reflect.New(node.elem.typ)
	elementPtr := element.UnsafePointer()
	elementValue := element.Elem()
	e.dst = append(e.dst, '{')
	for i := range entries {
		if i > 0 {
			e.dst = append(e.dst, ',')
		}
		e.dst = appendEncodedJSONString(e.dst, entries[i].name, e.escapeHTML)
		e.dst = append(e.dst, ':')
		elementValue.Set(mapValue.MapIndex(entries[i].key))
		if err := e.encodeNonAddressable(node.elem, elementPtr); err != nil {
			e.depth--
			return prependEncodePathField(err, entries[i].name)
		}
	}
	e.dst = append(e.dst, '}')
	e.depth--
	return nil
}

type mapEncodeEntry struct {
	name string
	key  reflect.Value
}

// mapKeyName renders a map key as its JSON member name, following
// encoding/json: a value-method-set TextMarshaler wins, then string kinds,
// then base 10 integers.
func mapKeyName(node *typedNode, key reflect.Value) (string, error) {
	if node.mapKeyTextEncode {
		marshaler := key.Interface().(encoding.TextMarshaler)
		text, err := marshaler.MarshalText()
		if err != nil {
			return "", err
		}
		return string(text), nil
	}
	switch node.mapKeyKind {
	case mapKeyString:
		return key.String(), nil
	case mapKeyInt:
		return strconv.FormatInt(key.Int(), 10), nil
	case mapKeyUint:
		return strconv.FormatUint(key.Uint(), 10), nil
	default:
		return "", errors.New("map key type " + key.Type().String() + " cannot be encoded")
	}
}

// encodeQuoted writes a scalar tagged with the string option: the value's
// JSON form wrapped in a string. Non-string scalars contain no characters
// that need escaping, so they are wrapped directly; strings are encoded and
// then re-encoded as string contents, like encoding/json.
func (e *encodeState) encodeQuoted(node *typedNode, src unsafe.Pointer) error {
	if node.kind == typedPointer {
		pointer := *(*unsafe.Pointer)(src)
		if pointer == nil {
			e.dst = append(e.dst, "null"...)
			return nil
		}
		node = node.elem
		src = pointer
	}
	if node.kind == typedString {
		inner := appendEncodedJSONString(nil, *(*string)(src), e.escapeHTML)
		e.dst = appendEncodedJSONString(e.dst, string(inner), false)
		return nil
	}
	e.dst = append(e.dst, '"')
	if err := e.encode(node, src); err != nil {
		return err
	}
	e.dst = append(e.dst, '"')
	return nil
}

// encodeFloat matches encoding/json: shortest representation, with the 'e'
// format only for large or small magnitudes, and a trimmed exponent digit.
func (e *encodeState) encodeFloat(value float64, bits int) error {
	if math.IsInf(value, 0) || math.IsNaN(value) {
		return &EncodeError{Reason: "unsupported float value " + strconv.FormatFloat(value, 'g', -1, bits)}
	}

	// Fast paths for values whose shortest fixed form is provably the digits
	// emitted here. Integer-valued floats below 1e15 sit in rounding
	// intervals narrower than the integer grid, so their exact integer is
	// the shortest representation. Decimals below 1e9 with one or two exact
	// fractional digits sit in intervals narrower than the 0.01 grid, so no
	// shorter or alternative fixed decimal can round to the same value; the
	// division check guarantees the digits parse back to exactly this float.
	// Shortest-representation intervals depend on the value's own precision:
	// float32 integers are only exact and unique below 2^24.
	integerLimit := 1e15
	if bits == 32 {
		integerLimit = 1 << 24
	}
	if positive := math.Abs(value); positive < integerLimit {
		if truncated := math.Trunc(value); truncated == value {
			if value == 0 && math.Signbit(value) {
				e.dst = append(e.dst, '-', '0')
				return nil
			}
			e.dst = appendCompactInt(e.dst, int64(value))
			return nil
		}
		if bits == 64 && positive < 1e9 {
			if scaled := value * 10; math.Trunc(scaled) == scaled && scaled/10 == value {
				e.dst = appendScaledDecimal(e.dst, value, scaled, 1)
				return nil
			}
			if scaled := value * 100; math.Trunc(scaled) == scaled && scaled/100 == value &&
				uint64(math.Abs(scaled))%10 != 0 {
				e.dst = appendScaledDecimal(e.dst, value, scaled, 2)
				return nil
			}
		}
	}

	format := byte('f')
	if abs := math.Abs(value); abs != 0 {
		if bits == 64 && (abs < 1e-6 || abs >= 1e21) ||
			bits == 32 && (float32(abs) < 1e-6 || float32(abs) >= 1e21) {
			format = 'e'
		}
	}
	e.dst = strconv.AppendFloat(e.dst, value, format, -1, bits)
	if format == 'e' {
		if n := len(e.dst); n >= 4 && e.dst[n-4] == 'e' && e.dst[n-3] == '-' && e.dst[n-2] == '0' {
			e.dst[n-2] = e.dst[n-1]
			e.dst = e.dst[:n-1]
		}
	}
	return nil
}

// appendScaledDecimal writes value as a fixed decimal with digits fractional
// digits, where scaled is value*10^digits and is integer valued. Trailing
// fractional zeros never reach here: a value with an exact shorter form is
// caught by the coarser fast path first.
func appendScaledDecimal(dst []byte, value, scaled float64, digits int) []byte {
	if math.Signbit(value) {
		dst = append(dst, '-')
		scaled = -scaled
	}
	units := uint64(scaled)
	var fraction uint64
	switch digits {
	case 1:
		fraction = units % 10
		units /= 10
	default:
		fraction = units % 100
		units /= 100
	}
	dst = appendCompactUint(dst, units)
	dst = append(dst, '.')
	if digits == 2 {
		dst = append(dst, encodeDigitPairs[fraction*2], encodeDigitPairs[fraction*2+1])
		return dst
	}
	return append(dst, byte('0'+fraction))
}

// encodeNumberLiteral emits a json.Number after validating its spelling,
// matching encoding/json's handling including the empty-string default.
func (e *encodeState) encodeNumberLiteral(literal string) error {
	if literal == "" {
		literal = "0"
	}
	if !ValidNumber([]byte(literal)) {
		return &EncodeError{Reason: "invalid number literal " + strconv.Quote(literal)}
	}
	e.dst = append(e.dst, literal...)
	return nil
}

// typedValueIsEmpty reports the omitempty emptiness of the value at src,
// matching encoding/json: false, zero numbers, empty strings, nil pointers,
// and zero-length slices.
func typedValueIsEmpty(node *typedNode, src unsafe.Pointer) bool {
	switch node.baseKind {
	case typedBool:
		return !*(*bool)(src)
	case typedString, typedNumber:
		return len(*(*string)(src)) == 0
	case typedInt:
		switch node.bits {
		case 8:
			return *(*int8)(src) == 0
		case 16:
			return *(*int16)(src) == 0
		case 32:
			return *(*int32)(src) == 0
		default:
			return *(*int64)(src) == 0
		}
	case typedUint:
		switch node.bits {
		case 8:
			return *(*uint8)(src) == 0
		case 16:
			return *(*uint16)(src) == 0
		case 32:
			return *(*uint32)(src) == 0
		default:
			return *(*uint64)(src) == 0
		}
	case typedFloat:
		if node.bits == 32 {
			return *(*float32)(src) == 0
		}
		return *(*float64)(src) == 0
	case typedSlice, typedBytes:
		return (*typedSliceHeader)(src).len == 0
	case typedPointer:
		return *(*unsafe.Pointer)(src) == nil
	case typedMap:
		return reflect.NewAt(node.typ, noescape(src)).Elem().Len() == 0
	case typedAny, typedIface:
		return reflect.NewAt(node.typ, noescape(src)).Elem().IsNil()
	default:
		return false
	}
}

const encodeHexDigits = "0123456789abcdef"

const encodeDigitPairs = "" +
	"00010203040506070809" +
	"10111213141516171819" +
	"20212223242526272829" +
	"30313233343536373839" +
	"40414243444546474849" +
	"50515253545556575859" +
	"60616263646566676869" +
	"70717273747576777879" +
	"80818283848586878889" +
	"90919293949596979899"

// appendCompactUint formats v with two digits per store. It beats the
// general strconv path on the short integers that dominate JSON documents.
func appendCompactUint(dst []byte, v uint64) []byte {
	var buffer [20]byte
	i := len(buffer)
	for v >= 100 {
		pair := (v % 100) * 2
		v /= 100
		i -= 2
		buffer[i] = encodeDigitPairs[pair]
		buffer[i+1] = encodeDigitPairs[pair+1]
	}
	if v >= 10 {
		i -= 2
		buffer[i] = encodeDigitPairs[v*2]
		buffer[i+1] = encodeDigitPairs[v*2+1]
	} else {
		i--
		buffer[i] = byte('0' + v)
	}
	return append(dst, buffer[i:]...)
}

func appendCompactInt(dst []byte, v int64) []byte {
	if v < 0 {
		dst = append(dst, '-')
		return appendCompactUint(dst, uint64(-v))
	}
	return appendCompactUint(dst, uint64(v))
}

// appendEncodedJSONString appends s as a quoted JSON string, matching encoding/json
// with HTML escaping disabled: control bytes, quotes, and backslashes are
// escaped, invalid UTF-8 becomes �, and U+2028/U+2029 are escaped.
func appendEncodedJSONString(dst []byte, s string, escapeHTML bool) []byte {
	dst = append(dst, '"')
	src := unsafe.Slice(unsafe.StringData(s), len(s))
	start := 0
	for i := 0; i < len(s); {
		// The scanners stop at exactly the escape-relevant set: quotes,
		// backslashes, control bytes, non-ASCII, and in HTML mode the
		// angle brackets and ampersand encoding/json escapes by default.
		if escapeHTML {
			i = scanEncodedHTMLSpecial(src, i)
		} else {
			i = scanStringSpecial(src, i)
		}
		if i >= len(s) {
			break
		}
		c := s[i]
		if c < 0x80 {
			dst = append(dst, s[start:i]...)
			switch c {
			case '"':
				dst = append(dst, '\\', '"')
			case '\\':
				dst = append(dst, '\\', '\\')
			case '\b':
				dst = append(dst, '\\', 'b')
			case '\f':
				dst = append(dst, '\\', 'f')
			case '\n':
				dst = append(dst, '\\', 'n')
			case '\r':
				dst = append(dst, '\\', 'r')
			case '\t':
				dst = append(dst, '\\', 't')
			default:
				dst = append(dst, '\\', 'u', '0', '0', encodeHexDigits[c>>4], encodeHexDigits[c&0xF])
			}
			i++
			start = i
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			dst = append(dst, s[start:i]...)
			dst = utf8.AppendRune(dst, utf8.RuneError)
			i++
			start = i
			continue
		}
		if r == '\u2028' || r == '\u2029' {
			dst = append(dst, s[start:i]...)
			dst = append(dst, '\\', 'u', '2', '0', '2', encodeHexDigits[r&0xF])
			i += size
			start = i
			continue
		}
		i += size
	}
	dst = append(dst, s[start:]...)
	return append(dst, '"')
}

func scanEncodedHTMLSpecial(src []byte, i int) int {
	for i+8 <= len(src) {
		x := binary.LittleEndian.Uint64(src[i:])
		m := stringSpecialMask(x) | byteEqMask(x, '<') | byteEqMask(x, '>') | byteEqMask(x, '&')
		if m != 0 {
			return i + bits.TrailingZeros64(m)/8
		}
		i += 8
	}
	for i < len(src) {
		c := src[i]
		if c == '"' || c == '\\' || c == '<' || c == '>' || c == '&' || c < 0x20 || c >= 0x80 {
			return i
		}
		i++
	}
	return len(src)
}
