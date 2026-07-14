package simdjson

import (
	"encoding"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"math/bits"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
	"unsafe"

	simdkernels "github.com/thesyncim/simdjson/simd"
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
	scratch    *sync.Pool
}

// CompileEncoder builds an encoder for T. It supports the same types as
// CompileDecoder plus the omitempty and string tag options.
func CompileEncoder[T any](opts EncoderOptions) (Encoder[T], error) {
	typ := reflect.TypeFor[T]()
	escapeHTML := !opts.DisableHTMLEscaping
	compiler := typedCompiler{nodes: make(map[reflect.Type]*typedNode), escapeHTML: escapeHTML}
	root, err := compiler.compile(typ, typ.String())
	if err != nil {
		return Encoder[T]{}, err
	}
	return Encoder[T]{
		root:       root,
		escapeHTML: escapeHTML,
		scratch:    newEncoderScratchPool(compiler.encScratchTypes, compiler.encHasMap),
	}, nil
}

// AppendJSON appends src encoded as compact JSON to dst. The backing storage of
// dst must not overlap storage reachable from src. Ordinary compiled sources
// remain stack eligible. Pointer-receiver custom methods use a heap-backed
// receiver copied back before AppendJSON returns. On error AppendJSON returns
// dst unchanged in length, but its unused capacity may contain partial output.
func (plan Encoder[T]) AppendJSON(dst []byte, src *T) ([]byte, error) {
	if plan.root == nil {
		return dst, fmt.Errorf("simdjson: zero Encoder")
	}
	if src == nil {
		return dst, fmt.Errorf("simdjson: encode source is nil")
	}
	e := encodeState{dst: dst, escapeHTML: plan.escapeHTML}
	if plan.scratch != nil {
		e.scratch = plan.scratch.Get().(*encoderScratch)
	}
	err := e.encode(plan.root, unsafe.Pointer(src))
	if plan.scratch != nil {
		e.scratch.reset()
		plan.scratch.Put(e.scratch)
	}
	if err != nil {
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
	scratch    *encoderScratch
	timeCache  simdkernels.TimeCache
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
	case typedTime:
		return e.encodeTime(src)
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

func (e *encodeState) encodeTime(src unsafe.Pointer) error {
	var err error
	e.dst, err = simdkernels.AppendTimeCached(e.dst, *(*time.Time)(src), &e.timeCache)
	if err != nil {
		return &EncodeError{Reason: "MarshalJSON: Time.MarshalJSON: " + strings.TrimPrefix(err.Error(), "Time.AppendText: ")}
	}
	return nil
}

func (e *encodeState) encodeStruct(node *typedNode, src unsafe.Pointer) error {
	// encFusedExtra preserves the exact depth limit across fused static
	// levels that no longer recurse.
	if e.depth+int(node.encFusedExtra) >= defaultMaxDepth {
		return &EncodeError{Reason: "maximum nesting depth exceeded"}
	}
	e.depth++
	e.dst = append(e.dst, '{')
	if node.encSimple {
		return e.encodeSimpleStructPairs(node, src)
	}
	first := true
	for i := range node.encFields {
		encField := &node.encFields[i]
		fieldBase := src
		if encField.hop >= 0 {
			fieldBase = resolveResetHops(src, node.fieldHops[encField.hop])
			if fieldBase == nil {
				// A nil embedded pointer omits its promoted fields entirely.
				continue
			}
		}
		fieldSrc := unsafe.Add(fieldBase, encField.offset)
		if encField.omitEmpty && typedValueIsEmpty(encField.node, fieldSrc) {
			continue
		}
		name := encField.encName
		if first {
			name = name[1:]
			first = false
		}
		e.dst = append(e.dst, name...)
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
		case typedOpNumber:
			err = e.encodeNumberLiteral(*(*string)(fieldSrc))
		case typedOpInt8:
			e.dst = appendCompactInt(e.dst, int64(*(*int8)(fieldSrc)))
		case typedOpInt16:
			e.dst = appendCompactInt(e.dst, int64(*(*int16)(fieldSrc)))
		case typedOpInt32:
			e.dst = appendCompactInt(e.dst, int64(*(*int32)(fieldSrc)))
		case typedOpInt64:
			e.dst = appendCompactInt(e.dst, *(*int64)(fieldSrc))
		case typedOpUint8:
			e.dst = appendCompactUint(e.dst, uint64(*(*uint8)(fieldSrc)))
		case typedOpUint16:
			e.dst = appendCompactUint(e.dst, uint64(*(*uint16)(fieldSrc)))
		case typedOpUint32:
			e.dst = appendCompactUint(e.dst, uint64(*(*uint32)(fieldSrc)))
		case typedOpUint64:
			e.dst = appendCompactUint(e.dst, *(*uint64)(fieldSrc))
		case typedOpFloat32:
			err = e.encodeFloat(float64(*(*float32)(fieldSrc)), 32)
		case typedOpFloat64:
			err = e.encodeFloat(*(*float64)(fieldSrc), 64)
		case typedOpStruct:
			err = e.encodeStruct(encField.node, fieldSrc)
		case typedOpSlice:
			err = e.encodeSlice(encField.node, fieldSrc)
		case typedOpArray:
			err = e.encodeArray(encField.node, fieldSrc)
		case typedOpMap:
			err = e.encodeMap(encField.node, fieldSrc)
		case typedOpAny:
			err = e.encodeAny(fieldSrc)
		case typedOpQuoted:
			err = e.encodeQuoted(encField.node, fieldSrc)
		case typedOpMarshaler:
			err = e.encodeMarshaler(encField.node, fieldSrc)
		default:
			err = e.encode(encField.node, fieldSrc)
		}
		if err != nil {
			e.depth--
			return prependEncodePathField(err, node.fields[i].name)
		}
	}
	e.dst = append(e.dst, '}')
	e.depth--
	return nil
}

func (e *encodeState) encodeSimpleStructPairs(node *typedNode, src unsafe.Pointer) error {
	fields := node.encFields
	packedNames := node.encNameData
	i := 0
	for ; i+1 < len(fields); i += 2 {
		first := &fields[i]
		second := &fields[i+1]
		e.dst = appendSimpleFieldName(e.dst, first, packedNames)
		firstSrc := unsafe.Add(src, first.offset)
		secondSrc := unsafe.Add(src, second.offset)
		errorIndex := i
		var err error
		switch first.pairOp {
		case typedEncPairStringString:
			e.dst = appendEncodedJSONString(e.dst, *(*string)(firstSrc), e.escapeHTML)
			e.dst = appendSimpleFieldName(e.dst, second, packedNames)
			e.dst = appendEncodedJSONString(e.dst, *(*string)(secondSrc), e.escapeHTML)
		case typedEncPairSliceString:
			err = e.encodeSlice(first.node, firstSrc)
			if err == nil {
				e.dst = appendSimpleFieldName(e.dst, second, packedNames)
				e.dst = appendEncodedJSONString(e.dst, *(*string)(secondSrc), e.escapeHTML)
			}
		case typedEncPairSliceStruct:
			err = e.encodeSlice(first.node, firstSrc)
			if err == nil {
				e.dst = appendSimpleFieldName(e.dst, second, packedNames)
				errorIndex = i + 1
				err = e.encodeStruct(second.node, secondSrc)
			}
		case typedEncPairSliceSlice:
			err = e.encodeSlice(first.node, firstSrc)
			if err == nil {
				e.dst = appendSimpleFieldName(e.dst, second, packedNames)
				errorIndex = i + 1
				err = e.encodeSlice(second.node, secondSrc)
			}
		case typedEncPairStructStruct:
			err = e.encodeStruct(first.node, firstSrc)
			if err == nil {
				e.dst = appendSimpleFieldName(e.dst, second, packedNames)
				errorIndex = i + 1
				err = e.encodeStruct(second.node, secondSrc)
			}
		case typedEncPairMarshalerMarshaler:
			err = e.encodeMarshaler(first.node, firstSrc)
			if err == nil {
				e.dst = appendSimpleFieldName(e.dst, second, packedNames)
				errorIndex = i + 1
				err = e.encodeMarshaler(second.node, secondSrc)
			}
		case typedEncPairStructSlice:
			err = e.encodeStruct(first.node, firstSrc)
			if err == nil {
				e.dst = appendSimpleFieldName(e.dst, second, packedNames)
				errorIndex = i + 1
				err = e.encodeSlice(second.node, secondSrc)
			}
		case typedEncPairStringSlice:
			e.dst = appendEncodedJSONString(e.dst, *(*string)(firstSrc), e.escapeHTML)
			e.dst = appendSimpleFieldName(e.dst, second, packedNames)
			errorIndex = i + 1
			err = e.encodeSlice(second.node, secondSrc)
		case typedEncPairMarshalerStruct:
			err = e.encodeMarshaler(first.node, firstSrc)
			if err == nil {
				e.dst = appendSimpleFieldName(e.dst, second, packedNames)
				errorIndex = i + 1
				err = e.encodeStruct(second.node, secondSrc)
			}
		case typedEncPairMarshalerString:
			err = e.encodeMarshaler(first.node, firstSrc)
			if err == nil {
				e.dst = appendSimpleFieldName(e.dst, second, packedNames)
				e.dst = appendEncodedJSONString(e.dst, *(*string)(secondSrc), e.escapeHTML)
			}
		case typedEncPairStructString:
			err = e.encodeStruct(first.node, firstSrc)
			if err == nil {
				e.dst = appendSimpleFieldName(e.dst, second, packedNames)
				e.dst = appendEncodedJSONString(e.dst, *(*string)(secondSrc), e.escapeHTML)
			}
		case typedEncPairStringStruct:
			e.dst = appendEncodedJSONString(e.dst, *(*string)(firstSrc), e.escapeHTML)
			e.dst = appendSimpleFieldName(e.dst, second, packedNames)
			errorIndex = i + 1
			err = e.encodeStruct(second.node, secondSrc)
		case typedEncPairFloat64Int64:
			err = e.encodeFloat(*(*float64)(firstSrc), 64)
			if err == nil {
				e.dst = appendSimpleFieldName(e.dst, second, packedNames)
				e.dst = appendCompactInt(e.dst, *(*int64)(secondSrc))
			}
		case typedEncPairUint64Uint64:
			e.dst = appendCompactUint(e.dst, *(*uint64)(firstSrc))
			e.dst = appendSimpleFieldName(e.dst, second, packedNames)
			e.dst = appendCompactUint(e.dst, *(*uint64)(secondSrc))
		case typedEncPairStringFloat64:
			e.dst = appendEncodedJSONString(e.dst, *(*string)(firstSrc), e.escapeHTML)
			e.dst = appendSimpleFieldName(e.dst, second, packedNames)
			errorIndex = i + 1
			err = e.encodeFloat(*(*float64)(secondSrc), 64)
		case typedEncPairStructInt64:
			err = e.encodeStruct(first.node, firstSrc)
			if err == nil {
				e.dst = appendSimpleFieldName(e.dst, second, packedNames)
				e.dst = appendCompactInt(e.dst, *(*int64)(secondSrc))
			}
		case typedEncPairInt64Int64:
			e.dst = appendCompactInt(e.dst, *(*int64)(firstSrc))
			e.dst = appendSimpleFieldName(e.dst, second, packedNames)
			e.dst = appendCompactInt(e.dst, *(*int64)(secondSrc))
		case typedEncPairInt64String:
			e.dst = appendCompactInt(e.dst, *(*int64)(firstSrc))
			e.dst = appendSimpleFieldName(e.dst, second, packedNames)
			e.dst = appendEncodedJSONString(e.dst, *(*string)(secondSrc), e.escapeHTML)
		case typedEncPairStringInt64:
			e.dst = appendEncodedJSONString(e.dst, *(*string)(firstSrc), e.escapeHTML)
			e.dst = appendSimpleFieldName(e.dst, second, packedNames)
			e.dst = appendCompactInt(e.dst, *(*int64)(secondSrc))
		case typedEncPairInt64Slice:
			e.dst = appendCompactInt(e.dst, *(*int64)(firstSrc))
			e.dst = appendSimpleFieldName(e.dst, second, packedNames)
			errorIndex = i + 1
			err = e.encodeSlice(second.node, secondSrc)
		case typedEncPairSliceInt64:
			err = e.encodeSlice(first.node, firstSrc)
			if err == nil {
				e.dst = appendSimpleFieldName(e.dst, second, packedNames)
				e.dst = appendCompactInt(e.dst, *(*int64)(secondSrc))
			}
		case typedEncPairSliceAny:
			err = e.encodeSlice(first.node, firstSrc)
			if err == nil {
				e.dst = appendSimpleFieldName(e.dst, second, packedNames)
				errorIndex = i + 1
				err = e.encodeAny(secondSrc)
			}
		case typedEncPairAnySlice:
			err = e.encodeAny(firstSrc)
			if err == nil {
				e.dst = appendSimpleFieldName(e.dst, second, packedNames)
				errorIndex = i + 1
				err = e.encodeSlice(second.node, secondSrc)
			}
		case typedEncPairAnyAny:
			err = e.encodeAny(firstSrc)
			if err == nil {
				e.dst = appendSimpleFieldName(e.dst, second, packedNames)
				errorIndex = i + 1
				err = e.encodeAny(secondSrc)
			}
		case typedEncPairAnyInt64:
			err = e.encodeAny(firstSrc)
			if err == nil {
				e.dst = appendSimpleFieldName(e.dst, second, packedNames)
				e.dst = appendCompactInt(e.dst, *(*int64)(secondSrc))
			}
		case typedEncPairMapMap:
			err = e.encodeMap(first.node, firstSrc)
			if err == nil {
				e.dst = appendSimpleFieldName(e.dst, second, packedNames)
				errorIndex = i + 1
				err = e.encodeMap(second.node, secondSrc)
			}
		default:
			err = e.encodeStructFieldValue(first, firstSrc)
			if err == nil {
				e.dst = appendSimpleFieldName(e.dst, second, packedNames)
				errorIndex = i + 1
				err = e.encodeStructFieldValue(second, secondSrc)
			}
		}
		if err != nil {
			e.depth--
			return prependEncodePathField(err, node.encPaths[errorIndex])
		}
	}
	if i < len(fields) {
		field := &fields[i]
		e.dst = appendSimpleFieldName(e.dst, field, packedNames)
		if err := e.encodeStructFieldValue(field, unsafe.Add(src, field.offset)); err != nil {
			e.depth--
			return prependEncodePathField(err, node.encPaths[i])
		}
	}
	if len(node.encClose) == 1 {
		// The overwhelmingly common close stays an inlined single-byte
		// append; the slice form costs a memmove call per struct.
		e.dst = append(e.dst, '}')
	} else {
		e.dst = append(e.dst, node.encClose...)
	}
	e.depth--
	return nil
}

func appendSimpleFieldName(dst []byte, field *typedEncField, packed []byte) []byte {
	if field.encNameLen == 0 || cap(dst)-len(dst) < 16 {
		return append(dst, field.encName...)
	}
	start := len(dst)
	dst = dst[:start+16]
	// Base-pointer forms keep the block copy free of bounds checks; the
	// cap guard above proved sixteen writable bytes and encNameBlock
	// indexes blocks the compiler packed. The min is free — encNameLen is
	// at most sixteen by construction — and lets the final reslice prove.
	*(*[16]byte)(unsafe.Add(unsafe.Pointer(unsafe.SliceData(dst)), start)) =
		*(*[16]byte)(unsafe.Add(unsafe.Pointer(unsafe.SliceData(packed)), int(field.encNameBlock)*16))
	return dst[:start+min(int(field.encNameLen), 16)]
}

func (e *encodeState) encodeStructFieldValue(field *typedEncField, src unsafe.Pointer) error {
	switch field.encOp {
	case typedOpBool:
		if *(*bool)(src) {
			e.dst = append(e.dst, "true"...)
		} else {
			e.dst = append(e.dst, "false"...)
		}
	case typedOpString:
		e.dst = appendEncodedJSONString(e.dst, *(*string)(src), e.escapeHTML)
	case typedOpNumber:
		return e.encodeNumberLiteral(*(*string)(src))
	case typedOpInt8:
		e.dst = appendCompactInt(e.dst, int64(*(*int8)(src)))
	case typedOpInt16:
		e.dst = appendCompactInt(e.dst, int64(*(*int16)(src)))
	case typedOpInt32:
		e.dst = appendCompactInt(e.dst, int64(*(*int32)(src)))
	case typedOpInt64:
		e.dst = appendCompactInt(e.dst, *(*int64)(src))
	case typedOpUint8:
		e.dst = appendCompactUint(e.dst, uint64(*(*uint8)(src)))
	case typedOpUint16:
		e.dst = appendCompactUint(e.dst, uint64(*(*uint16)(src)))
	case typedOpUint32:
		e.dst = appendCompactUint(e.dst, uint64(*(*uint32)(src)))
	case typedOpUint64:
		e.dst = appendCompactUint(e.dst, *(*uint64)(src))
	case typedOpFloat32:
		return e.encodeFloat(float64(*(*float32)(src)), 32)
	case typedOpFloat64:
		return e.encodeFloat(*(*float64)(src), 64)
	case typedOpStruct:
		return e.encodeStruct(field.node, src)
	case typedOpSlice:
		return e.encodeSlice(field.node, src)
	case typedOpArray:
		return e.encodeArray(field.node, src)
	case typedOpMap:
		return e.encodeMap(field.node, src)
	case typedOpAny:
		return e.encodeAny(src)
	case typedOpQuoted:
		return e.encodeQuoted(field.node, src)
	case typedOpMarshaler:
		return e.encodeMarshaler(field.node, src)
	default:
		return e.encode(field.node, src)
	}
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
	if node.elem.encOp == typedOpStruct {
		return e.encodeStructSlice(node, header)
	}
	e.depth++
	// The element operation is loop invariant, so the hot scalar kinds get
	// dedicated loops. Integer elements also drop the first-element branch:
	// every value is emitted comma-prefixed and the opening bracket then
	// overwrites the leading comma in place.
	switch node.elem.encOp {
	case typedOpInt64:
		start := len(e.dst)
		for index := 0; index < header.len; index++ {
			e.dst = appendCommaCompactInt(e.dst, *(*int64)(unsafe.Add(header.data, uintptr(index)*node.elem.size)))
		}
		if header.len == 0 {
			e.dst = append(e.dst, '[')
		} else {
			e.dst[start] = '['
		}
	case typedOpUint64:
		start := len(e.dst)
		for index := 0; index < header.len; index++ {
			e.dst = appendCommaCompactUint(e.dst, *(*uint64)(unsafe.Add(header.data, uintptr(index)*node.elem.size)))
		}
		if header.len == 0 {
			e.dst = append(e.dst, '[')
		} else {
			e.dst[start] = '['
		}
	case typedOpString:
		e.dst = append(e.dst, '[')
		for index := 0; index < header.len; index++ {
			if index > 0 {
				e.dst = append(e.dst, ',')
			}
			e.dst = appendEncodedJSONString(e.dst, *(*string)(unsafe.Add(header.data, uintptr(index)*node.elem.size)), e.escapeHTML)
		}
	case typedOpFloat64:
		e.dst = append(e.dst, '[')
		for index := 0; index < header.len; index++ {
			if index > 0 {
				e.dst = append(e.dst, ',')
			}
			if err := e.encodeFloat(*(*float64)(unsafe.Add(header.data, uintptr(index)*node.elem.size)), 64); err != nil {
				e.depth--
				return prependEncodePathIndex(err, index)
			}
		}
	default:
		e.dst = append(e.dst, '[')
		for index := 0; index < header.len; index++ {
			if index > 0 {
				e.dst = append(e.dst, ',')
			}
			element := unsafe.Add(header.data, uintptr(index)*node.elem.size)
			var err error
			switch node.elem.encOp {
			case typedOpStruct:
				err = e.encodeStruct(node.elem, element)
			case typedOpSlice:
				err = e.encodeSlice(node.elem, element)
			case typedOpArray:
				err = e.encodeArray(node.elem, element)
			case typedOpFloat64:
				err = e.encodeFloat(*(*float64)(element), 64)
			default:
				err = e.encode(node.elem, element)
			}
			if err != nil {
				e.depth--
				return prependEncodePathIndex(err, index)
			}
		}
	}
	e.dst = append(e.dst, ']')
	e.depth--
	return nil
}

func (e *encodeState) encodeStructSlice(node *typedNode, header *typedSliceHeader) error {
	e.depth++
	elem := node.elem
	if elem.encSimple && header.len > 0 {
		// The depth test and the simple-struct dispatch are the same for
		// every element; run them once and drive the pair encoder
		// directly. An empty slice keeps succeeding at the depth limit,
		// exactly as the per-element check behaved, and encFusedExtra
		// accounts for static levels fused into the element's pairs.
		if e.depth+int(elem.encFusedExtra) >= defaultMaxDepth {
			e.depth--
			return &EncodeError{Reason: "maximum nesting depth exceeded"}
		}
		// Every element opens with ",{" in one append and the bracket then
		// overwrites the leading comma, removing the first-element branch.
		start := len(e.dst)
		for index := 0; index < header.len; index++ {
			element := unsafe.Add(header.data, uintptr(index)*elem.size)
			e.depth++
			e.dst = append(e.dst, ',', '{')
			if err := e.encodeSimpleStructPairs(elem, element); err != nil {
				e.depth--
				return prependEncodePathIndex(err, index)
			}
		}
		e.dst[start] = '['
		e.dst = append(e.dst, ']')
		e.depth--
		return nil
	}
	e.dst = append(e.dst, '[')
	for index := 0; index < header.len; index++ {
		if index > 0 {
			e.dst = append(e.dst, ',')
		}
		element := unsafe.Add(header.data, uintptr(index)*elem.size)
		if err := e.encodeStruct(elem, element); err != nil {
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
	if node.elem.encOp == typedOpFloat64 {
		return e.encodeFloat64Array(node, src)
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

func (e *encodeState) encodeFloat64Array(node *typedNode, src unsafe.Pointer) error {
	e.depth++
	e.dst = append(e.dst, '[')
	if node.length > 0 {
		if err := e.encodeFloat(*(*float64)(src), 64); err != nil {
			e.depth--
			return prependEncodePathIndex(err, 0)
		}
		for index := 1; index < node.length; index++ {
			e.dst = append(e.dst, ',')
			element := unsafe.Add(src, uintptr(index)*8)
			if err := e.encodeFloat(*(*float64)(element), 64); err != nil {
				e.depth--
				return prependEncodePathIndex(err, index)
			}
		}
	}
	e.dst = append(e.dst, ']')
	e.depth--
	return nil
}

// dynamicEncodeNodes caches one compiled encode plan per concrete type seen
// inside an interface value. These plans run inside whichever static plan
// encountered the interface, so they are compiled with dynamic set and must
// never carry indexes into a plan-specific scratch; see typedCompiler.dynamic.
var dynamicEncodeNodes sync.Map

type dynamicEncodeEntry struct {
	node *typedNode
	err  error
}

type dynamicEncodeKey struct {
	typ        reflect.Type
	escapeHTML bool
}

func dynamicEncodeNode(typ reflect.Type, escapeHTML bool) (*typedNode, error) {
	key := dynamicEncodeKey{typ: typ, escapeHTML: escapeHTML}
	if entry, ok := dynamicEncodeNodes.Load(key); ok {
		cached := entry.(*dynamicEncodeEntry)
		return cached.node, cached.err
	}
	compiler := typedCompiler{nodes: make(map[reflect.Type]*typedNode), escapeHTML: escapeHTML, dynamic: true}
	node, err := compiler.compile(typ, typ.String())
	entry, _ := dynamicEncodeNodes.LoadOrStore(key, &dynamicEncodeEntry{node: node, err: err})
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
	node, err := dynamicEncodeNode(value.Type(), e.escapeHTML)
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
	mapLen := mapValue.Len()
	// The entry list and numeric-key arena come from the per-call scratch
	// so sorted map encoding does not allocate per map. Ownership moves to
	// this call while it runs; a nested map sees nil scratch slices and
	// allocates its own.
	var entries []mapEncodeEntry
	var keyArena []byte
	scratch := e.scratch
	if scratch != nil {
		entries = scratch.mapEntries[:0]
		keyArena = scratch.mapKeyArena[:0]
		scratch.mapEntries = nil
		scratch.mapKeyArena = nil
	}
	if cap(entries) < mapLen {
		entries = make([]mapEncodeEntry, 0, mapLen)
	}
	numericKeys := !node.mapKeyTextEncode && (node.mapKeyKind == mapKeyInt || node.mapKeyKind == mapKeyUint)
	if numericKeys && cap(keyArena) < mapLen*20 && mapLen <= int(^uint(0)>>1)/20 {
		keyArena = make([]byte, 0, mapLen*20)
	}
	var iterator *reflect.MapIter
	if scratch != nil {
		iterator = scratch.mapIter
		scratch.mapIter = nil
	}
	if iterator == nil {
		iterator = mapValue.MapRange()
	} else {
		iterator.Reset(mapValue)
	}
	for iterator.Next() {
		key := iterator.Key()
		var name string
		if numericKeys {
			start := len(keyArena)
			if node.mapKeyKind == mapKeyInt {
				value := key.Int()
				if value < 0 {
					keyArena = appendCompactInt(keyArena, value)
				} else {
					keyArena = appendCompactUint(keyArena, uint64(value))
				}
			} else {
				keyArena = appendCompactUint(keyArena, key.Uint())
			}
			name = unsafe.String(unsafe.SliceData(keyArena[start:]), len(keyArena)-start)
		} else {
			var err error
			name, err = mapKeyName(node, key)
			if err != nil {
				e.releaseMapScratch(entries, keyArena, iterator)
				e.depth--
				return &EncodeError{Reason: err.Error()}
			}
		}
		entries = append(entries, mapEncodeEntry{name: name, value: iterator.Value()})
	}
	slices.SortFunc(entries, func(a, b mapEncodeEntry) int { return strings.Compare(a.name, b.name) })
	// The addressable element box comes from the compile-reserved scratch
	// slot when available. Reuse across nesting levels of the same map
	// type is safe: the slot is set immediately before each encode and
	// never read after it returns.
	var elementValue reflect.Value
	if scratch != nil && node.encMapElem >= 0 {
		elementValue = scratch.marshalers[node.encMapElem].value
	} else {
		elementValue = reflect.New(node.elem.typ).Elem()
	}
	elementPtr := elementValue.Addr().UnsafePointer()
	e.dst = append(e.dst, '{')
	for i := range entries {
		if i > 0 {
			e.dst = append(e.dst, ',')
		}
		e.dst = appendEncodedJSONString(e.dst, entries[i].name, e.escapeHTML)
		e.dst = append(e.dst, ':')
		elementValue.Set(entries[i].value)
		if err := e.encodeNonAddressable(node.elem, elementPtr); err != nil {
			// Clone: numeric key names alias the pooled arena, and the
			// error must outlive this call's ownership of it.
			name := strings.Clone(entries[i].name)
			e.releaseMapScratch(entries, keyArena, iterator)
			e.depth--
			return prependEncodePathField(err, name)
		}
	}
	e.dst = append(e.dst, '}')
	e.releaseMapScratch(entries, keyArena, iterator)
	e.depth--
	return nil
}

// releaseMapScratch returns encodeMap's working state to the scratch,
// keeping grown capacity for the next map. The iterator is unbound first
// so the pool never keeps the encoded map alive.
func (e *encodeState) releaseMapScratch(entries []mapEncodeEntry, keyArena []byte, iterator *reflect.MapIter) {
	scratch := e.scratch
	if scratch == nil || scratch.mapEntries != nil {
		return
	}
	scratch.mapEntries = entries[:0]
	scratch.mapKeyArena = keyArena[:0]
	if scratch.mapIter == nil {
		iterator.Reset(reflect.Value{})
		scratch.mapIter = iterator
	}
}

type mapEncodeEntry struct {
	name  string
	value reflect.Value
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
	dst, err := appendJSONFloat(e.dst, value, bits)
	if err != nil {
		return err
	}
	e.dst = dst
	return nil
}

// appendJSONFloat appends value the way encoding/json spells it, shared by
// the compiled encoder and the streaming writer.
func appendJSONFloat(dst []byte, value float64, bits int) ([]byte, error) {
	if math.IsInf(value, 0) || math.IsNaN(value) {
		return dst, &EncodeError{Reason: "unsupported float value " + strconv.FormatFloat(value, 'g', -1, bits)}
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
				return append(dst, '-', '0'), nil
			}
			return appendCompactInt(dst, int64(value)), nil
		}
		if bits == 64 && positive < 1e9 {
			if scaled := value * 1e6; math.Trunc(scaled) == scaled && scaled/1e6 == value {
				return appendScaledDecimal6(dst, value, scaled), nil
			}
		}
	}

	if bits == 32 {
		dst, _ = simdkernels.AppendFloat32(dst, float32(value))
	} else {
		dst, _ = simdkernels.AppendFloat64(dst, value)
	}
	return dst, nil
}

// appendScaledDecimal6 writes an exactly recoverable fixed decimal with up to
// six fractional digits. encodeFloat only calls it below 1e9, where adjacent
// 1e-6 grid points are wider than a float64 rounding interval.
func appendScaledDecimal6(dst []byte, value, scaled float64) []byte {
	if math.Signbit(value) {
		dst = append(dst, '-')
		scaled = -scaled
	}
	units := uint64(scaled)
	fraction := units % 1e6
	units /= 1e6
	dst = appendCompactUint(dst, units)
	dst = append(dst, '.')
	var digits [6]byte
	for i := 5; i >= 0; i-- {
		digits[i] = byte('0' + fraction%10)
		fraction /= 10
	}
	end := len(digits)
	for digits[end-1] == '0' {
		end--
	}
	return append(dst, digits[:end]...)
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

func storeCompactDigitPair(dst unsafe.Pointer, i int, pair uint64) {
	// appendCompactUint proves two output bytes and pair < 100 before each call.
	src := unsafe.Add(unsafe.Pointer(unsafe.StringData(encodeDigitPairs)), pair*2)
	*(*[2]byte)(unsafe.Add(dst, i)) = *(*[2]byte)(src)
}

// appendCompactUint formats v with two digits per store. It beats the
// general strconv path on the short integers that dominate JSON documents.
func appendCompactUint(dst []byte, v uint64) []byte {
	if v < 10 {
		return append(dst, byte('0'+v))
	}
	if v < 100 {
		return append(dst, encodeDigitPairs[v*2], encodeDigitPairs[v*2+1])
	}
	if v >= 1e10 {
		return appendCompactUintLarge(dst, v)
	}
	if v >= 1e9 {
		return appendCompactUint10(dst, v)
	}
	if v >= 1e8 {
		return appendCompactUint9(dst, v)
	}
	digits := int((bits.Len64(v)*1233)>>12) + 1
	if v < pow10Uint64[digits-1] {
		digits--
	}
	if cap(dst)-len(dst) < digits {
		return strconv.AppendUint(dst, v, 10)
	}
	start := len(dst)
	dst = dst[:start+digits]
	i := len(dst)
	base := unsafe.Pointer(unsafe.SliceData(dst))
	if digits == 8 {
		simdkernels.Store8Digits((*[8]byte)(unsafe.Add(base, start)), v)
		return dst
	}
	switch {
	case v >= 1e8:
		pair := v % 100
		v /= 100
		i -= 2
		storeCompactDigitPair(base, i, pair)
		fallthrough
	case v >= 1e6:
		pair := v % 100
		v /= 100
		i -= 2
		storeCompactDigitPair(base, i, pair)
		fallthrough
	case v >= 1e4:
		pair := v % 100
		v /= 100
		i -= 2
		storeCompactDigitPair(base, i, pair)
		fallthrough
	case v >= 100:
		pair := v % 100
		v /= 100
		i -= 2
		storeCompactDigitPair(base, i, pair)
	}
	if v >= 10 {
		i -= 2
		storeCompactDigitPair(base, i, v)
	} else {
		i--
		dst[i] = byte('0' + v)
	}
	return dst
}

//go:noinline
func appendCompactUint9(dst []byte, v uint64) []byte {
	if cap(dst)-len(dst) < 9 {
		return strconv.AppendUint(dst, v, 10)
	}
	hi := v / 1e8
	lo := v - hi*1e8
	start := len(dst)
	dst = dst[:start+9]
	base := unsafe.Pointer(unsafe.SliceData(dst))
	*(*byte)(unsafe.Add(base, start)) = byte('0' + hi)
	simdkernels.Store8Digits((*[8]byte)(unsafe.Add(base, start+1)), lo)
	return dst
}

//go:noinline
func appendCompactUint10(dst []byte, v uint64) []byte {
	if cap(dst)-len(dst) < 10 {
		return strconv.AppendUint(dst, v, 10)
	}
	hi := v / 1e8
	lo := v - hi*1e8
	start := len(dst)
	dst = dst[:start+10]
	base := unsafe.Pointer(unsafe.SliceData(dst))
	storeCompactDigitPair(base, start, hi)
	simdkernels.Store8Digits((*[8]byte)(unsafe.Add(base, start+2)), lo)
	return dst
}

//go:noinline
func appendCompactUintLarge(dst []byte, v uint64) []byte {
	digits := int((bits.Len64(v)*1233)>>12) + 1
	if v < pow10Uint64[digits-1] {
		digits--
	}
	if cap(dst)-len(dst) < digits {
		return strconv.AppendUint(dst, v, 10)
	}
	if v < 1e16 {
		var block [16]byte
		simdkernels.Store16Digits(&block, v)
		return append(dst, block[16-digits:]...)
	}
	hi := v / 1e16
	lo := v - hi*1e16
	dst = appendCompactUint(dst, hi)
	start := len(dst)
	dst = dst[:start+16]
	simdkernels.Store16Digits((*[16]byte)(dst[start:]), lo)
	return dst
}

func appendCompactInt(dst []byte, v int64) []byte {
	if v < 0 {
		dst = append(dst, '-')
		return appendCompactUint(dst, uint64(-v))
	}
	return appendCompactUint(dst, uint64(v))
}

// appendCommaCompactUint emits ",digits" with one capacity check, so slice
// loops pay no separate separator append per element.
func appendCommaCompactUint(dst []byte, v uint64) []byte {
	if v < 10 {
		return append(dst, ',', byte('0'+v))
	}
	if v < 100 {
		return append(dst, ',', encodeDigitPairs[v*2], encodeDigitPairs[v*2+1])
	}
	// The digit-count formula requires v >= 100, proved above.
	digits := int((bits.Len64(v)*1233)>>12) + 1
	if v < pow10Uint64[digits-1] {
		digits--
	}
	if v >= 1e16 || cap(dst)-len(dst) < digits+1 {
		dst = append(dst, ',')
		return appendCompactUint(dst, v)
	}
	var block [16]byte
	simdkernels.Store16Digits(&block, v)
	start := len(dst)
	dst = dst[:start+digits+1]
	dst[start] = ','
	copy(dst[start+1:], block[16-digits:])
	return dst
}

func appendCommaCompactInt(dst []byte, v int64) []byte {
	if v < 0 {
		dst = append(dst, ',', '-')
		return appendCompactUint(dst, uint64(-v))
	}
	return appendCommaCompactUint(dst, uint64(v))
}

// appendShortCleanJSONString quotes strings shorter than one vector when no
// byte needs escaping, testing and copying word-at-a-time instead of
// byte-at-a-time. ok reports whether it emitted; any flagged byte, or a dst
// too full for its unconditional word stores, defers to the general path.
func appendShortCleanJSONString(dst []byte, s string, escapeHTML bool) ([]byte, bool) {
	n := uint(len(s))
	if cap(dst)-len(dst) < int(n)+10 {
		return dst, false
	}
	p := unsafe.Pointer(unsafe.StringData(s))
	if n >= 8 {
		w0 := loadUint64LE(p)
		w1 := loadUint64LE(unsafe.Add(p, n-8))
		var mask uint64
		if escapeHTML {
			mask = simdkernels.HTMLStringSpecialMask64(w0) | simdkernels.HTMLStringSpecialMask64(w1)
		} else {
			mask = simdkernels.StringSpecialMask64(w0) | simdkernels.StringSpecialMask64(w1)
		}
		if mask != 0 {
			return dst, false
		}
		start := len(dst)
		dst = dst[:start+int(n)+2]
		dst[start] = '"'
		base := unsafe.Pointer(unsafe.SliceData(dst))
		storeUint64LE(unsafe.Add(base, start+1), w0)
		storeUint64LE(unsafe.Add(base, start+1+int(n)-8), w1)
		dst[start+1+int(n)] = '"'
		return dst, true
	}
	// Overlapped halves build the exact zero-padded word image of s, so one
	// mask probe and one store cover any length; the padding lanes are
	// discarded from the mask and overwritten by the closing quote.
	var w uint64
	switch {
	case n >= 4:
		w = uint64(loadUint32LE(p)) | uint64(loadUint32LE(unsafe.Add(p, n-4)))<<((n-4)*8)
	case n >= 2:
		w = uint64(loadUint16LE(p)) | uint64(loadUint16LE(unsafe.Add(p, n-2)))<<((n-2)*8)
	default:
		w = uint64(*(*byte)(p))
	}
	var mask uint64
	if escapeHTML {
		mask = simdkernels.HTMLStringSpecialMask64(w)
	} else {
		mask = simdkernels.StringSpecialMask64(w)
	}
	if mask&(uint64(1)<<(8*n)-1) != 0 {
		return dst, false
	}
	start := len(dst)
	dst = dst[:start+int(n)+2]
	dst[start] = '"'
	storeUint64LE(unsafe.Add(unsafe.Pointer(unsafe.SliceData(dst)), start+1), w)
	dst[start+1+int(n)] = '"'
	return dst, true
}

// appendEncodedJSONString appends s as a quoted JSON string, matching encoding/json
// with HTML escaping disabled: control bytes, quotes, and backslashes are
// escaped, invalid UTF-8 becomes �, and U+2028/U+2029 are escaped.
func appendEncodedJSONString(dst []byte, s string, escapeHTML bool) []byte {
	const fusedCopyMinBytes = 16

	if len(s) == 0 {
		return append(dst, '"', '"')
	}
	if len(s) < fusedCopyMinBytes {
		if out, ok := appendShortCleanJSONString(dst, s, escapeHTML); ok {
			return out
		}
	}
	if len(s) >= fusedCopyMinBytes {
		dst = slices.Grow(dst, len(s)+2)
	}
	dst = append(dst, '"')
	src := unsafe.Slice(unsafe.StringData(s), len(s))
	first := 0
	copiedPrefix := false
	if len(s) >= fusedCopyMinBytes {
		start := len(dst)
		dst = dst[:start+len(s)]
		if escapeHTML {
			first = simdkernels.CopyHTMLStringPrefix(dst[start:], src)
		} else {
			first = simdkernels.CopyStringPrefix(dst[start:], src)
		}
		if first >= 0 {
			if first == len(src) {
				return append(dst, '"')
			}
			dst = dst[:start+first]
			copiedPrefix = true
		} else {
			dst = dst[:start]
		}
	}
	if !copiedPrefix {
		if escapeHTML {
			first = scanEncodedHTMLSpecialFast(src, 0)
		} else {
			first = scanStringSpecial(src, 0)
		}
	}
	if first == len(src) {
		dst = append(dst, s...)
		return append(dst, '"')
	}
	unicodeClean := false
	if src[first] >= 0x80 {
		unicodeClean = validUTF8NoLineSeparatorFast(src)
	}
	start := 0
	if copiedPrefix {
		start = first
	}
	for i := first; i < len(s); {
		// The scanners stop at exactly the escape-relevant set: quotes,
		// backslashes, control bytes, non-ASCII, and in HTML mode the
		// angle brackets and ampersand encoding/json escapes by default.
		if unicodeClean && escapeHTML {
			i = scanEncodedHTMLSyntaxFast(src, i)
		} else if unicodeClean {
			i = scanStringSyntax(src, i)
		} else if escapeHTML {
			i = scanEncodedHTMLSpecialFast(src, i)
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
