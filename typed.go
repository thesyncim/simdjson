package simdjson

import (
	"encoding"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unsafe"
)

// DecoderOptions controls decoding directly into caller-owned Go values.
type DecoderOptions struct {
	// MaxDepth limits nested arrays and objects. Values <= 0 use the default.
	MaxDepth int

	// ZeroCopy aliases unescaped strings into src. Callers must not mutate src
	// while decoded strings are in use. When false, decoded strings are
	// independent of src; the decoder may retain one private copy of the
	// input instead of allocating each string separately.
	ZeroCopy bool

	// DisallowUnknownFields rejects object keys absent from the compiled type.
	DisallowUnknownFields bool

	// CaseSensitive disables the encoding/json-compatible case-insensitive
	// fallback used after exact field-name matching.
	CaseSensitive bool

	// Replace resets destination state that the document does not mention:
	// absent struct fields become zero and null always clears. The default
	// matches encoding/json, which merges into existing values and treats
	// null as a no-op for scalars, strings, structs, and arrays. Replace is
	// the right mode for destinations reused across decodes.
	Replace bool
}

// Decoder is an immutable decoder for one concrete Go type. Compile it
// once and reuse it concurrently; Decode keeps all mutable parser state local
// to the call.
type Decoder[T any] struct {
	root      *typedNode
	rootSlice *typedNode
	options   DecoderOptions
}

// CompileDecoder builds a decoder for T. Scalar and field dispatch use the
// compiled plan; runtime reflection is limited to allocating nil pointers,
// growing dynamic slices, and inserting map entries.
func CompileDecoder[T any](opts DecoderOptions) (Decoder[T], error) {
	typ := reflect.TypeFor[T]()
	compiler := typedCompiler{nodes: make(map[reflect.Type]*typedNode)}
	root, err := compiler.compile(typ, typ.String())
	if err != nil {
		return Decoder[T]{}, err
	}
	prepareTypedResets(root, make(map[*typedNode]bool))
	return Decoder[T]{
		root: root,
		rootSlice: &typedNode{
			kind:     typedSlice,
			encKind:  typedSlice,
			baseKind: typedSlice,
			op:       typedOpSlice,
			encOp:    typedOpSlice,
			typ:      reflect.TypeFor[[]T](),
			name:     reflect.TypeFor[[]T]().String(),
			elem:     root,
		},
		options: opts,
	}, nil
}

// Decode decodes one JSON value into dst. By default it merges like
// encoding/json; DecoderOptions.Replace resets state absent from the document.
// Slice capacities already reachable through dst are retained where possible.
//
// Decode keeps ordinary compiled destinations stack eligible. Pointer-receiver
// custom methods use a heap-backed receiver copied back before Decode returns,
// so retaining that receiver cannot retain an internal stack address.
func (plan Decoder[T]) Decode(src []byte, dst *T) error {
	if plan.root == nil {
		return fmt.Errorf("simdjson: zero Decoder")
	}
	if dst == nil {
		return fmt.Errorf("simdjson: typed Decode destination is nil")
	}
	cursor := newDecoderCursor(src, plan.options)
	cursor.skipSpace()
	var err error
	switch plan.root.kind {
	case typedStruct:
		err = decodeCompiledStruct(&cursor, plan.root, unsafe.Pointer(dst))
	case typedSlice:
		err = decodeCompiledSlice(&cursor, plan.root, unsafe.Pointer(dst))
	case typedArray:
		err = decodeCompiledArray(&cursor, plan.root, unsafe.Pointer(dst))
	case typedPointer:
		err = decodeCompiledPointer(&cursor, plan.root, unsafe.Pointer(dst))
	case typedMap:
		err = decodeCompiledMap(&cursor, plan.root, unsafe.Pointer(dst))
	default:
		err = decodeCompiled(&cursor, plan.root, unsafe.Pointer(dst))
	}
	if err != nil {
		return err
	}
	return cursor.Finish()
}

// DecodeArray decodes a top-level JSON array into dst, reusing its capacity.
// The returned slice is always the authoritative result.
func (plan Decoder[T]) DecodeArray(src []byte, dst []T) ([]T, error) {
	if plan.rootSlice == nil {
		return dst[:0], fmt.Errorf("simdjson: zero Decoder")
	}
	cursor := newDecoderCursor(src, plan.options)
	cursor.skipSpace()
	if err := decodeCompiledSlice(&cursor, plan.rootSlice, unsafe.Pointer(&dst)); err != nil {
		return dst, err
	}
	if err := cursor.Finish(); err != nil {
		return dst, err
	}
	return dst, nil
}

// UnsupportedTypeError reports a type that cannot use the interface-free
// typed path.
type UnsupportedTypeError struct {
	Type   reflect.Type
	Path   string
	Reason string
}

func (e *UnsupportedTypeError) Error() string {
	return fmt.Sprintf("simdjson: typed decoder does not support %s at %s: %s", e.Type, e.Path, e.Reason)
}

// DecodeError reports valid JSON that cannot be stored in the requested
// Go type.
type DecodeError struct {
	// Offset is the byte position of the offending value within the input.
	Offset int

	// Path locates the offending value using JSON member names and array
	// indexes, for example "items[3].scores[1]". It is empty when the
	// top-level value itself failed. Building the path costs nothing until
	// an error actually unwinds.
	Path string

	Type     reflect.Type
	TypeName string
	Reason   string
}

func (e *DecodeError) Error() string {
	typeName := e.TypeName
	if e.Type != nil {
		typeName = e.Type.String()
	}
	if e.Path != "" {
		return fmt.Sprintf("simdjson: cannot decode JSON at byte %d into %s at %s: %s", e.Offset, typeName, e.Path, e.Reason)
	}
	return fmt.Sprintf("simdjson: cannot decode JSON at byte %d into %s: %s", e.Offset, typeName, e.Reason)
}

// prependDecodePathField and prependDecodePathIndex annotate decode errors
// while they unwind the compiled decode stack, so only failing decodes pay
// for path construction.
func prependDecodePathField(err error, name string) error {
	if e, ok := err.(*DecodeError); ok {
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

func prependDecodePathIndex(err error, index int) error {
	if e, ok := err.(*DecodeError); ok {
		segment := "[" + strconv.Itoa(index) + "]"
		if e.Path == "" || e.Path[0] == '[' {
			e.Path = segment + e.Path
		} else {
			e.Path = segment + "." + e.Path
		}
	}
	return err
}

type typedKind uint8

const (
	typedInvalid typedKind = iota
	typedBool
	typedString
	typedNumber
	typedInt
	typedUint
	typedFloat
	typedStruct
	typedSlice
	typedArray
	typedPointer
	typedMap
	typedAny
	typedBytes
	typedUnmarshalerJSON
	typedUnmarshalerText
	typedMarshalerJSON
	typedMarshalerText
	typedIface
	typedTime
)

type typedOp uint8

const (
	typedOpInvalid typedOp = iota
	typedOpBool
	typedOpString
	typedOpNumber
	typedOpInt8
	typedOpInt16
	typedOpInt32
	typedOpInt64
	typedOpUint8
	typedOpUint16
	typedOpUint32
	typedOpUint64
	typedOpFloat32
	typedOpFloat64
	typedOpStruct
	typedOpSlice
	typedOpArray
	typedOpPointer
	typedOpMap
	typedOpAny
	typedOpBytes
	typedOpQuoted
	typedOpUnmarshaler
	typedOpMarshaler
	typedOpIface
)

type typedNode struct {
	kind             typedKind // decode dispatch
	encKind          typedKind // encode dispatch
	encNonAddrKind   typedKind // encode dispatch for map/interface values
	baseKind         typedKind // structural layout, for resets and emptiness
	op               typedOp
	encOp            typedOp
	typ              reflect.Type
	name             string
	size             uintptr
	bits             int
	length           int
	elem             *typedNode
	mapKeyKind       typedMapKeyKind
	mapKeyTextDecode bool
	mapKeyTextEncode bool
	fields           []typedField
	fieldTable       []int16
	fieldTableMask   uint32
	encFields        []typedEncField
	encNameData      []byte
	fieldHops        [][]typedFieldHop
	hopResets        []uintptr
	reset            []typedResetOp
	ready            bool
	encSimple        bool
	allSet           uint64
	encScratch       int32
	encMapElem       int32
}

type typedField struct {
	name    string
	offset  uintptr
	node    *typedNode
	seen    uint64
	key     uint64
	keyMask uint64
	keyFold uint64
	pos     int32
	hop     int16
	keyLen  uint8
	op      typedOp
}

// typedEncField holds the complete encoder-only view of a struct field, so the
// hot encode loop does not touch the larger decoder field record.
type typedEncField struct {
	encName      string
	node         *typedNode
	offset       uintptr
	hop          int16
	encNameBlock uint16
	encOp        typedOp
	pairOp       typedEncPairOp
	encNameLen   uint8
	omitEmpty    bool
}

type typedEncPairOp uint8

const (
	typedEncPairFallback typedEncPairOp = iota
	typedEncPairStringString
	typedEncPairSliceString
	typedEncPairSliceStruct
	typedEncPairSliceSlice
	typedEncPairStructStruct
	typedEncPairMarshalerMarshaler
	typedEncPairStructSlice
	typedEncPairStringSlice
	typedEncPairMarshalerStruct
	typedEncPairMarshalerString
	typedEncPairStructString
	typedEncPairStringStruct
	typedEncPairFloat64Int64
	typedEncPairUint64Uint64
	typedEncPairStringFloat64
	typedEncPairStructInt64
	typedEncPairInt64Int64
	typedEncPairInt64String
	typedEncPairStringInt64
	typedEncPairInt64Slice
	typedEncPairSliceInt64
	typedEncPairSliceAny
	typedEncPairAnySlice
	typedEncPairAnyAny
	typedEncPairAnyInt64
	typedEncPairMapMap
)

type typedCompiler struct {
	nodes           map[reflect.Type]*typedNode
	encScratchTypes []reflect.Type
	encHasMap       bool
	escapeHTML      bool
	// dynamic marks plans compiled for interface values at encode time.
	// Their nodes run against whatever static plan is executing, so they
	// must never carry indexes into that plan's scratch slots.
	dynamic bool
}

var jsonNumberReflectType = reflect.TypeFor[json.Number]()
var timeReflectType = reflect.TypeFor[time.Time]()

func (c *typedCompiler) compile(typ reflect.Type, path string) (*typedNode, error) {
	if node := c.nodes[typ]; node != nil {
		return node, nil
	}
	node := &typedNode{typ: typ, name: typ.String(), size: typ.Size(), encScratch: -1, encMapElem: -1}
	c.nodes[typ] = node

	if err := c.compileStructural(node, typ, path); err != nil {
		// A custom un/marshaler stands in for the broken structural layout;
		// a direction that still needs structure reports failure at runtime.
		node.kind, node.encKind, node.baseKind = typedInvalid, typedInvalid, typedInvalid
		node.op, node.encOp = typedOpInvalid, typedOpInvalid
		node.fields, node.encFields, node.fieldHops, node.hopResets = nil, nil, nil, nil
		node.elem = nil
		if !c.applyInterfaceKinds(node, typ) {
			return nil, err
		}
		if node.kind == typedInvalid && node.encKind == typedInvalid {
			return nil, err
		}
		c.nodes[typ] = node
		return node, nil
	}
	node.baseKind = node.kind
	node.encKind = node.kind
	node.encOp = node.op
	node.encNonAddrKind = node.encKind
	c.applyInterfaceKinds(node, typ)
	return node, nil
}

// compileStructural fills node with typ's structural layout.
func (c *typedCompiler) compileStructural(node *typedNode, typ reflect.Type, path string) error {
	if typ == jsonNumberReflectType {
		node.kind = typedNumber
		node.op = typedOpNumber
		return nil
	}

	switch typ.Kind() {
	case reflect.Bool:
		node.kind = typedBool
		node.op = typedOpBool
	case reflect.String:
		node.kind = typedString
		node.op = typedOpString
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		node.kind = typedInt
		node.bits = typ.Bits()
		switch node.bits {
		case 8:
			node.op = typedOpInt8
		case 16:
			node.op = typedOpInt16
		case 32:
			node.op = typedOpInt32
		case 64:
			node.op = typedOpInt64
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		node.kind = typedUint
		node.bits = typ.Bits()
		switch node.bits {
		case 8:
			node.op = typedOpUint8
		case 16:
			node.op = typedOpUint16
		case 32:
			node.op = typedOpUint32
		case 64:
			node.op = typedOpUint64
		}
	case reflect.Float32, reflect.Float64:
		node.kind = typedFloat
		node.bits = typ.Bits()
		if node.bits == 32 {
			node.op = typedOpFloat32
		} else {
			node.op = typedOpFloat64
		}
	case reflect.Pointer:
		node.kind = typedPointer
		node.op = typedOpPointer
		elem, err := c.compile(typ.Elem(), path+"*")
		if err != nil {
			return err
		}
		node.elem = elem
	case reflect.Slice:
		if typ.Elem().Kind() == reflect.Uint8 {
			node.kind = typedBytes
			node.op = typedOpBytes
			break
		}
		node.kind = typedSlice
		node.op = typedOpSlice
		elem, err := c.compile(typ.Elem(), path+"[]")
		if err != nil {
			return err
		}
		node.elem = elem
	case reflect.Array:
		node.kind = typedArray
		node.op = typedOpArray
		node.length = typ.Len()
		elem, err := c.compile(typ.Elem(), path+"[]")
		if err != nil {
			return err
		}
		node.elem = elem
	case reflect.Interface:
		if typ.NumMethod() != 0 {
			node.kind = typedIface
			node.op = typedOpIface
			break
		}
		node.kind = typedAny
		node.op = typedOpAny
	case reflect.Map:
		keyKind, keyOK := classifyMapKey(typ.Key())
		if !keyOK {
			return c.unsupported(typ, path, "unsupported map key type")
		}
		node.mapKeyKind = keyKind
		node.mapKeyTextDecode = reflect.PointerTo(typ.Key()).Implements(textUnmarshalerReflectType)
		// encoding/json only consults the value method set for key encoding.
		node.mapKeyTextEncode = typ.Key().Implements(textMarshalerReflectType)
		node.kind = typedMap
		node.op = typedOpMap
		c.encHasMap = true
		if !c.dynamic {
			// Reserve an addressable element of the value type in the
			// encoder scratch so encodeMap reuses one box per map type.
			node.encMapElem = int32(len(c.encScratchTypes))
			c.encScratchTypes = append(c.encScratchTypes, typ.Elem())
		}
		elem, err := c.compile(typ.Elem(), path+"[key]")
		if err != nil {
			return err
		}
		node.elem = elem
	case reflect.Struct:
		node.kind = typedStruct
		node.op = typedOpStruct
		node.encSimple = true
		for _, resolved := range resolveStructFields(typ) {
			fieldNode, err := c.compile(resolved.typ, path+"."+resolved.name)
			if err != nil {
				return err
			}
			offset, hops, hopErr := c.fieldHops(typ, resolved.index, path+"."+resolved.name)
			if hopErr != nil {
				return hopErr
			}
			fieldIndex := len(node.fields)
			compiledField := typedField{
				name:   resolved.name,
				offset: offset,
				node:   fieldNode,
				op:     fieldNode.op,
				pos:    int32(fieldIndex),
				hop:    -1,
			}
			if hops != nil {
				node.encSimple = false
				compiledField.hop = int16(len(node.fieldHops))
				node.fieldHops = append(node.fieldHops, hops)
				// The embedded pointer slot is not a leaf field, so replace
				// style resets must clear it explicitly.
				node.hopResets = append(node.hopResets, hops[0].offset)
			}
			encField := typedEncField{
				encName:   "," + string(appendEncodedJSONString(nil, resolved.name, c.escapeHTML)) + ":",
				node:      fieldNode,
				offset:    offset,
				hop:       compiledField.hop,
				encOp:     fieldNode.encOp,
				omitEmpty: resolved.omitEmpty,
			}
			if resolved.omitEmpty {
				node.encSimple = false
			}
			if resolved.quoted {
				quotedNode := fieldNode
				if quotedNode.kind == typedPointer && resolved.typ.Name() == "" {
					quotedNode = quotedNode.elem
				}
				switch quotedNode.baseKind {
				case typedBool, typedString, typedNumber, typedInt, typedUint, typedFloat:
					compiledField.op = typedOpQuoted
					encField.encOp = typedOpQuoted
				}
			}
			node.encFields = append(node.encFields, encField)
			if fieldIndex < 64 {
				compiledField.seen = uint64(1) << fieldIndex
			}
			name := resolved.name
			if len(name) <= 7 {
				for byteIndex := range len(name) {
					c := name[byteIndex]
					compiledField.key |= uint64(c) << (byteIndex * 8)
					if lower := c | 0x20; 'a' <= lower && lower <= 'z' {
						compiledField.keyFold |= 0x20 << (byteIndex * 8)
					}
				}
				compiledField.key |= uint64('"') << (len(name) * 8)
				if len(name) <= 6 {
					compiledField.key |= uint64(':') << ((len(name) + 1) * 8)
					compiledField.keyMask = ^uint64(0) >> ((6 - len(name)) * 8)
				} else {
					compiledField.keyMask = ^uint64(0)
				}
				compiledField.keyLen = uint8(len(name))
			} else if len(name) <= 255 {
				for byteIndex := range 8 {
					c := name[byteIndex]
					compiledField.key |= uint64(c) << (byteIndex * 8)
					if lower := c | 0x20; 'a' <= lower && lower <= 'z' {
						compiledField.keyFold |= 0x20 << (byteIndex * 8)
					}
				}
				compiledField.keyMask = ^uint64(0)
				compiledField.keyLen = uint8(len(name))
			}
			node.fields = append(node.fields, compiledField)
		}
		if node.encSimple {
			for i := 0; i+1 < len(node.encFields); i += 2 {
				node.encFields[i].pairOp = classifyTypedEncPair(node.encFields[i].encOp, node.encFields[i+1].encOp)
			}
			if len(node.encFields) != 0 {
				node.encFields[0].encName = node.encFields[0].encName[1:]
			}
			const blockBytes = 16
			// A wide store is safe when successful encoding is guaranteed to
			// overwrite every byte through its end. Keep the last short-name
			// tail out of the packed table so AppendJSON never modifies bytes
			// past its result.
			tailMin := 1 // closing brace
			for i := len(node.encFields) - 1; i >= 0; i-- {
				field := &node.encFields[i]
				valueMin := minimumTypedEncodedBytes(field.node, field.encOp)
				if valueMin >= blockBytes-tailMin {
					tailMin = blockBytes
				} else {
					tailMin += valueMin
				}
				n := len(field.encName)
				if n <= blockBytes && tailMin >= blockBytes-n {
					field.encNameLen = uint8(n)
				}
				if n >= blockBytes-tailMin {
					tailMin = blockBytes
				} else {
					tailMin += n
				}
			}
			for i := range node.encFields {
				field := &node.encFields[i]
				block := len(node.encNameData) / blockBytes
				if field.encNameLen != 0 && block <= int(^uint16(0)) {
					field.encNameBlock = uint16(block)
					start := len(node.encNameData)
					node.encNameData = append(node.encNameData, make([]byte, blockBytes)...)
					copy(node.encNameData[start:], field.encName)
				} else {
					field.encNameLen = 0
				}
			}
		}
		if len(node.fields) <= 64 {
			if len(node.fields) == 64 {
				node.allSet = ^uint64(0)
			} else {
				node.allSet = uint64(1)<<len(node.fields) - 1
			}
		}
		// A fold-based fast match must never shadow another field's exact
		// match, so folding is disabled where two field names fold together.
		for i := range node.fields {
			for j := range node.fields {
				if i != j && strings.EqualFold(node.fields[i].name, node.fields[j].name) {
					node.fields[i].keyFold = 0
				}
			}
		}
		if len(node.fields) >= 8 {
			tableSize := 16
			for tableSize < len(node.fields)*2 {
				tableSize *= 2
			}
			node.fieldTable = make([]int16, tableSize)
			node.fieldTableMask = uint32(tableSize - 1)
			for i := range node.fields {
				slot := fieldNameHash(node.fields[i].name) & node.fieldTableMask
				for node.fieldTable[slot] != 0 {
					slot = (slot + 1) & node.fieldTableMask
				}
				node.fieldTable[slot] = int16(i + 1)
			}
		}
	default:
		return c.unsupported(typ, path, "kind "+typ.Kind().String()+" would require interface or reflective value dispatch")
	}
	return nil
}

func minimumTypedEncodedBytes(node *typedNode, op typedOp) int {
	switch op {
	case typedOpBool:
		return 4
	case typedOpString, typedOpQuoted:
		return 2
	case typedOpStruct, typedOpSlice, typedOpMap:
		return 2
	case typedOpArray:
		if node.length == 0 {
			return 2
		}
		element := minimumTypedEncodedBytes(node.elem, node.elem.encOp)
		if element >= (int(^uint(0)>>1)-1)/node.length {
			return 1
		}
		return 1 + node.length*(element+1)
	default:
		return 1
	}
}

func classifyTypedEncPair(first, second typedOp) typedEncPairOp {
	switch {
	case first == typedOpString && second == typedOpString:
		return typedEncPairStringString
	case first == typedOpSlice && second == typedOpString:
		return typedEncPairSliceString
	case first == typedOpSlice && second == typedOpStruct:
		return typedEncPairSliceStruct
	case first == typedOpSlice && second == typedOpSlice:
		return typedEncPairSliceSlice
	case first == typedOpStruct && second == typedOpStruct:
		return typedEncPairStructStruct
	case first == typedOpMarshaler && second == typedOpMarshaler:
		return typedEncPairMarshalerMarshaler
	case first == typedOpStruct && second == typedOpSlice:
		return typedEncPairStructSlice
	case first == typedOpString && second == typedOpSlice:
		return typedEncPairStringSlice
	case first == typedOpMarshaler && second == typedOpStruct:
		return typedEncPairMarshalerStruct
	case first == typedOpMarshaler && second == typedOpString:
		return typedEncPairMarshalerString
	case first == typedOpStruct && second == typedOpString:
		return typedEncPairStructString
	case first == typedOpString && second == typedOpStruct:
		return typedEncPairStringStruct
	case first == typedOpFloat64 && second == typedOpInt64:
		return typedEncPairFloat64Int64
	case first == typedOpUint64 && second == typedOpUint64:
		return typedEncPairUint64Uint64
	case first == typedOpString && second == typedOpFloat64:
		return typedEncPairStringFloat64
	case first == typedOpStruct && second == typedOpInt64:
		return typedEncPairStructInt64
	case first == typedOpInt64 && second == typedOpInt64:
		return typedEncPairInt64Int64
	case first == typedOpInt64 && second == typedOpString:
		return typedEncPairInt64String
	case first == typedOpString && second == typedOpInt64:
		return typedEncPairStringInt64
	case first == typedOpInt64 && second == typedOpSlice:
		return typedEncPairInt64Slice
	case first == typedOpSlice && second == typedOpInt64:
		return typedEncPairSliceInt64
	case first == typedOpSlice && second == typedOpAny:
		return typedEncPairSliceAny
	case first == typedOpAny && second == typedOpSlice:
		return typedEncPairAnySlice
	case first == typedOpAny && second == typedOpAny:
		return typedEncPairAnyAny
	case first == typedOpAny && second == typedOpInt64:
		return typedEncPairAnyInt64
	case first == typedOpMap && second == typedOpMap:
		return typedEncPairMapMap
	default:
		return typedEncPairFallback
	}
}

// typedMapKeyKind classifies how map keys convert to and from JSON member
// names, following encoding/json: text unmarshalers win on decode, string
// kinds win on encode, and integer kinds round trip through base 10.
type typedMapKeyKind uint8

const (
	mapKeyString typedMapKeyKind = iota
	mapKeyInt
	mapKeyUint
	mapKeyText
)

func classifyMapKey(keyType reflect.Type) (typedMapKeyKind, bool) {
	implementsText := keyType.Implements(textMarshalerReflectType) ||
		reflect.PointerTo(keyType).Implements(textUnmarshalerReflectType)
	switch keyType.Kind() {
	case reflect.String:
		return mapKeyString, true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return mapKeyInt, true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return mapKeyUint, true
	default:
		if implementsText {
			return mapKeyText, true
		}
		return 0, false
	}
}

var (
	jsonMarshalerReflectType   = reflect.TypeFor[json.Marshaler]()
	jsonUnmarshalerReflectType = reflect.TypeFor[json.Unmarshaler]()
	textMarshalerReflectType   = reflect.TypeFor[encoding.TextMarshaler]()
	textUnmarshalerReflectType = reflect.TypeFor[encoding.TextUnmarshaler]()
)

// applyInterfaceKinds overrides dispatch for types with custom un/marshalers,
// reporting whether any interface applies. json.Number keeps its fast path.
func (c *typedCompiler) applyInterfaceKinds(node *typedNode, typ reflect.Type) bool {
	// Interface values dispatch on their concrete type instead; json.Number
	// keeps its fast path.
	if typ == jsonNumberReflectType || typ.Kind() == reflect.Interface {
		return false
	}
	pointerType := reflect.PointerTo(typ)
	applied := false
	if typ.Implements(jsonUnmarshalerReflectType) || pointerType.Implements(jsonUnmarshalerReflectType) {
		node.kind = typedUnmarshalerJSON
		node.op = typedOpUnmarshaler
		applied = true
	} else if typ.Implements(textUnmarshalerReflectType) || pointerType.Implements(textUnmarshalerReflectType) {
		node.kind = typedUnmarshalerText
		node.op = typedOpUnmarshaler
		applied = true
	}
	if typ == timeReflectType {
		node.encKind = typedTime
		node.encNonAddrKind = typedTime
		node.encOp = typedOpMarshaler
		return true
	}
	if typ.Implements(jsonMarshalerReflectType) {
		node.encNonAddrKind = typedMarshalerJSON
		applied = true
	} else if typ.Implements(textMarshalerReflectType) {
		node.encNonAddrKind = typedMarshalerText
		applied = true
	}
	if pointerType.Implements(jsonMarshalerReflectType) {
		node.encKind = typedMarshalerJSON
		node.encOp = typedOpMarshaler
		applied = true
	} else if typ.Implements(jsonMarshalerReflectType) {
		node.encKind = typedMarshalerJSON
		node.encOp = typedOpMarshaler
		applied = true
	} else if pointerType.Implements(textMarshalerReflectType) {
		node.encKind = typedMarshalerText
		node.encOp = typedOpMarshaler
		applied = true
	} else if typ.Implements(textMarshalerReflectType) {
		node.encKind = typedMarshalerText
		node.encOp = typedOpMarshaler
		applied = true
	}
	if !c.dynamic && (typ.Implements(jsonMarshalerReflectType) || typ.Implements(textMarshalerReflectType)) {
		node.encScratch = int32(len(c.encScratchTypes))
		c.encScratchTypes = append(c.encScratchTypes, typ)
	}
	return applied
}

func validTypedTag(name string) bool {
	if name == "" {
		return false
	}
	for _, char := range name {
		if strings.ContainsRune("!#$%&()*+-./:;<=>?@[]^_{|}~ ", char) {
			continue
		}
		if !unicode.IsLetter(char) && !unicode.IsDigit(char) {
			return false
		}
	}
	return true
}

// fieldHops turns a flattened field's index path into a cumulative offset
// plus the embedded pointer dereferences on the way.
func (c *typedCompiler) fieldHops(root reflect.Type, index []int, path string) (uintptr, []typedFieldHop, error) {
	var hops []typedFieldHop
	offset := uintptr(0)
	current := root
	for position, fieldIndex := range index {
		structField := current.Field(fieldIndex)
		offset += structField.Offset
		if position == len(index)-1 {
			break
		}
		next := structField.Type
		if next.Kind() == reflect.Pointer {
			// Like encoding/json, an unexported embedded pointer only fails
			// when decoding must allocate through it.
			hops = append(hops, typedFieldHop{
				offset:     offset,
				pointee:    next.Elem(),
				unexported: !structField.IsExported(),
			})
			offset = 0
			next = next.Elem()
		}
		current = next
	}
	return offset, hops, nil
}

func (c *typedCompiler) unsupported(typ reflect.Type, path, reason string) error {
	delete(c.nodes, typ)
	return &UnsupportedTypeError{Type: typ, Path: path, Reason: reason}
}

type typedSliceHeader struct {
	data unsafe.Pointer
	len  int
	cap  int
}

func (p *parser) typedKey() (string, error) {
	start := p.i + 1
	end := scanStringSpecial(p.src, start)
	if end < len(p.src) && p.src[end] == '"' {
		p.i = end + 1
		return unsafe.String(unsafe.SliceData(p.src[start:end]), end-start), nil
	}
	zeroCopy := p.zeroCopy
	p.zeroCopy = true
	key, err := p.parseString()
	p.zeroCopy = zeroCopy
	return key, err
}

func (p *parser) skipTypedValue(depth int) error {
	value := validator{src: p.src, i: p.i, maxDepth: p.maxDepth}
	if err := value.parseValue(depth); err != nil {
		return err
	}
	p.i = value.i
	return nil
}

func (node *typedNode) findFieldSlow(key string, fold bool) *typedField {
	if node.fieldTable != nil {
		slot := fieldNameHash(key) & node.fieldTableMask
		for {
			entry := node.fieldTable[slot]
			if entry == 0 {
				break
			}
			field := &node.fields[entry-1]
			if field.name == key {
				return field
			}
			slot = (slot + 1) & node.fieldTableMask
		}
	} else {
		for i := range node.fields {
			if node.fields[i].name == key {
				return &node.fields[i]
			}
		}
	}
	if fold {
		return node.findFieldFold(key)
	}
	return nil
}

//go:noinline
func (node *typedNode) findFieldFold(key string) *typedField {
	for i := range node.fields {
		if strings.EqualFold(node.fields[i].name, key) {
			return &node.fields[i]
		}
	}
	return nil
}

func fieldNameHash(name string) uint32 {
	var head uint64
	if len(name) >= 8 {
		head = binary.LittleEndian.Uint64(unsafe.Slice(unsafe.StringData(name), len(name)))
	} else {
		for i := range len(name) {
			head |= uint64(name[i]) << (i * 8)
		}
	}
	head ^= uint64(len(name)) * 0x9e3779b97f4a7c15
	head ^= head >> 33
	return uint32(head ^ head>>32)
}

func resetTyped(node *typedNode, dst unsafe.Pointer) {
	if node.ready {
		applyTypedReset(node.reset, dst)
		return
	}
	switch node.baseKind {
	case typedBool:
		*(*bool)(dst) = false
	case typedString, typedNumber:
		*(*string)(dst) = ""
	case typedInt, typedUint:
		switch node.bits {
		case 8:
			*(*uint8)(dst) = 0
		case 16:
			*(*uint16)(dst) = 0
		case 32:
			*(*uint32)(dst) = 0
		case 64:
			*(*uint64)(dst) = 0
		}
	case typedFloat:
		if node.bits == 32 {
			*(*float32)(dst) = 0
		} else {
			*(*float64)(dst) = 0
		}
	case typedStruct:
		for i := range node.fields {
			field := &node.fields[i]
			if field.hop >= 0 {
				continue
			}
			resetTyped(field.node, unsafe.Add(dst, field.offset))
		}
		for _, hopOffset := range node.hopResets {
			*(*unsafe.Pointer)(unsafe.Add(dst, hopOffset)) = nil
		}
	case typedSlice, typedBytes:
		(*typedSliceHeader)(dst).len = 0
	case typedArray:
		for i := 0; i < node.length; i++ {
			resetTyped(node.elem, unsafe.Add(dst, uintptr(i)*node.elem.size))
		}
	case typedPointer:
		*(*unsafe.Pointer)(dst) = nil
	case typedMap, typedIface:
		reflect.NewAt(node.typ, noescape(dst)).Elem().SetZero()
	case typedAny:
		*(*any)(dst) = nil
	}
}

func growTypedSlice(node *typedNode, dst unsafe.Pointer, capacity int) {
	header := (*typedSliceHeader)(dst)
	currentHeader := *header
	next := reflect.MakeSlice(node.typ, currentHeader.len, capacity)
	if currentHeader.len != 0 {
		copyTypedSlice(node, currentHeader, next)
	}
	*header = typedSliceHeader{
		data: next.UnsafePointer(),
		len:  currentHeader.len,
		cap:  capacity,
	}
	runtime.KeepAlive(next)
}

func copyTypedSlice(node *typedNode, header typedSliceHeader, dst reflect.Value) {
	src := reflect.NewAt(node.typ, unsafe.Pointer(&header)).Elem()
	reflect.Copy(dst, src)
}

func nextTypedSliceCapacity(current, required int) int {
	capacity := current * 2
	if capacity < 4 {
		capacity = 4
	}
	if capacity < required {
		capacity = required
	}
	return capacity
}
