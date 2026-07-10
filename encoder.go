package simdjson

import (
	"fmt"
	"math"
	"reflect"
	"strconv"
	"sync"
	"unicode/utf8"
	"unsafe"
)

// Encoder is an immutable encoder for one concrete Go type. Compile it once
// and reuse it concurrently; AppendJSON keeps all mutable state local to the
// call. Output matches encoding/json with HTML escaping disabled: compact,
// with U+2028 and U+2029 escaped and invalid UTF-8 replaced by �.
type Encoder[T any] struct {
	root *typedNode
}

// CompileEncoder builds an encoder for T. It supports the same types as
// CompileDecoder plus the omitempty tag option.
func CompileEncoder[T any]() (Encoder[T], error) {
	typ := reflect.TypeFor[T]()
	compiler := typedCompiler{nodes: make(map[reflect.Type]*typedNode)}
	root, err := compiler.compile(typ, typ.String())
	if err != nil {
		return Encoder[T]{}, err
	}
	return Encoder[T]{root: root}, nil
}

// AppendJSON appends src encoded as compact JSON to dst.
func (plan Encoder[T]) AppendJSON(dst []byte, src *T) ([]byte, error) {
	if plan.root == nil {
		return dst, fmt.Errorf("simdjson: zero Encoder")
	}
	if src == nil {
		return dst, fmt.Errorf("simdjson: encode source is nil")
	}
	e := encodeState{dst: dst}
	if err := e.encode(plan.root, unsafe.Pointer(src)); err != nil {
		return dst, err
	}
	return e.dst, nil
}

// unmarshalEncoders caches one encoder per source type for Marshal.
var unmarshalEncoders sync.Map

type cachedEncoder[T any] struct {
	encoder Encoder[T]
	err     error
}

// Marshal encodes src like encoding/json.Marshal with HTML escaping disabled.
// The encoder for each source type is compiled once and cached for the
// process lifetime. Hot paths that encode one type repeatedly should call
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
	return cached.encoder.AppendJSON(nil, src)
}

//go:noinline
func newCachedEncoder[T any]() any {
	encoder, err := CompileEncoder[T]()
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
	dst   []byte
	depth int
}

func (e *encodeState) encode(node *typedNode, src unsafe.Pointer) error {
	switch node.kind {
	case typedBool:
		if *(*bool)(src) {
			e.dst = append(e.dst, "true"...)
		} else {
			e.dst = append(e.dst, "false"...)
		}
	case typedString:
		e.dst = appendEncodedJSONString(e.dst, *(*string)(src))
	case typedNumber:
		return e.encodeNumberLiteral(*(*string)(src))
	case typedInt:
		switch node.bits {
		case 8:
			e.dst = strconv.AppendInt(e.dst, int64(*(*int8)(src)), 10)
		case 16:
			e.dst = strconv.AppendInt(e.dst, int64(*(*int16)(src)), 10)
		case 32:
			e.dst = strconv.AppendInt(e.dst, int64(*(*int32)(src)), 10)
		case 64:
			e.dst = strconv.AppendInt(e.dst, *(*int64)(src), 10)
		}
	case typedUint:
		switch node.bits {
		case 8:
			e.dst = strconv.AppendUint(e.dst, uint64(*(*uint8)(src)), 10)
		case 16:
			e.dst = strconv.AppendUint(e.dst, uint64(*(*uint16)(src)), 10)
		case 32:
			e.dst = strconv.AppendUint(e.dst, uint64(*(*uint32)(src)), 10)
		case 64:
			e.dst = strconv.AppendUint(e.dst, *(*uint64)(src), 10)
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
		fieldSrc := unsafe.Add(src, field.offset)
		if field.omitEmpty && typedValueIsEmpty(field.node, fieldSrc) {
			continue
		}
		if !first {
			e.dst = append(e.dst, ',')
		}
		first = false
		e.dst = append(e.dst, field.encName...)
		var err error
		switch field.op {
		case typedOpBool:
			if *(*bool)(fieldSrc) {
				e.dst = append(e.dst, "true"...)
			} else {
				e.dst = append(e.dst, "false"...)
			}
		case typedOpString:
			e.dst = appendEncodedJSONString(e.dst, *(*string)(fieldSrc))
		case typedOpInt64:
			e.dst = strconv.AppendInt(e.dst, *(*int64)(fieldSrc), 10)
		case typedOpUint64:
			e.dst = strconv.AppendUint(e.dst, *(*uint64)(fieldSrc), 10)
		case typedOpFloat64:
			err = e.encodeFloat(*(*float64)(fieldSrc), 64)
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

// encodeFloat matches encoding/json: shortest representation, with the 'e'
// format only for large or small magnitudes, and a trimmed exponent digit.
func (e *encodeState) encodeFloat(value float64, bits int) error {
	if math.IsInf(value, 0) || math.IsNaN(value) {
		return &EncodeError{Reason: "unsupported float value " + strconv.FormatFloat(value, 'g', -1, bits)}
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
	switch node.kind {
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
	case typedSlice:
		return (*typedSliceHeader)(src).len == 0
	case typedPointer:
		return *(*unsafe.Pointer)(src) == nil
	default:
		return false
	}
}

const encodeHexDigits = "0123456789abcdef"

// appendEncodedJSONString appends s as a quoted JSON string, matching encoding/json
// with HTML escaping disabled: control bytes, quotes, and backslashes are
// escaped, invalid UTF-8 becomes �, and U+2028/U+2029 are escaped.
func appendEncodedJSONString(dst []byte, s string) []byte {
	dst = append(dst, '"')
	src := unsafe.Slice(unsafe.StringData(s), len(s))
	start := 0
	for i := 0; i < len(s); {
		// scanStringSpecial stops at exactly the escape-relevant set: quotes,
		// backslashes, control bytes, and non-ASCII.
		i = scanStringSpecial(src, i)
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
