package simdjson

import (
	"encoding/json"
	"fmt"
	"reflect"
	"runtime"
	"strconv"
	"strings"
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
			kind: typedSlice,
			typ:  reflect.TypeFor[[]T](),
			name: reflect.TypeFor[[]T]().String(),
			elem: root,
		},
		options: opts,
	}, nil
}

// Decode replaces dst with one JSON value. Slice capacities already reachable
// through dst are retained where their fields are decoded again.
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
)

type typedNode struct {
	kind   typedKind
	op     typedOp
	typ    reflect.Type
	name   string
	size   uintptr
	bits   int
	length int
	elem   *typedNode
	fields []typedField
	reset  []typedResetOp
	ready  bool
	allSet uint64
}

type typedField struct {
	name         string
	encName      string
	encNamePlain string
	offset       uintptr
	node         *typedNode
	seen         uint64
	key          uint64
	keyMask      uint64
	keyFold      uint64
	pos          int32
	keyLen       uint8
	op           typedOp
	omitEmpty    bool
	quoted       bool
}

type typedCompiler struct {
	nodes map[reflect.Type]*typedNode
}

var jsonNumberReflectType = reflect.TypeFor[json.Number]()

func (c *typedCompiler) compile(typ reflect.Type, path string) (*typedNode, error) {
	if node := c.nodes[typ]; node != nil {
		return node, nil
	}
	node := &typedNode{typ: typ, name: typ.String(), size: typ.Size()}
	c.nodes[typ] = node

	if typ == jsonNumberReflectType {
		node.kind = typedNumber
		node.op = typedOpNumber
		return node, nil
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
			return nil, err
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
			return nil, err
		}
		node.elem = elem
	case reflect.Array:
		node.kind = typedArray
		node.op = typedOpArray
		node.length = typ.Len()
		elem, err := c.compile(typ.Elem(), path+"[]")
		if err != nil {
			return nil, err
		}
		node.elem = elem
	case reflect.Interface:
		if typ.NumMethod() != 0 {
			return nil, c.unsupported(typ, path, "non-empty interfaces would require dynamic dispatch")
		}
		node.kind = typedAny
		node.op = typedOpAny
	case reflect.Map:
		if typ.Key().Kind() != reflect.String {
			return nil, c.unsupported(typ, path, "map keys must have a string kind")
		}
		node.kind = typedMap
		node.op = typedOpMap
		elem, err := c.compile(typ.Elem(), path+"[key]")
		if err != nil {
			return nil, err
		}
		node.elem = elem
	case reflect.Struct:
		node.kind = typedStruct
		node.op = typedOpStruct
		seen := make(map[string]struct{}, typ.NumField())
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if !field.IsExported() {
				continue
			}
			tag, tagged := field.Tag.Lookup("json")
			name := field.Name
			omitEmpty := false
			quoted := false
			if tagged {
				if tag == "-" {
					continue
				}
				var options string
				name, options, _ = strings.Cut(tag, ",")
				if !validTypedTag(name) {
					name = field.Name
				}
				for options != "" {
					var option string
					option, options, _ = strings.Cut(options, ",")
					switch option {
					case "omitempty":
						omitEmpty = true
					case "string":
						quoted = true
					}
				}
			}
			if field.Anonymous && !tagged {
				return nil, c.unsupported(typ, path+"."+field.Name, "untagged anonymous fields are not yet flattened")
			}
			if _, ok := seen[name]; ok {
				return nil, c.unsupported(typ, path+"."+field.Name, "duplicate JSON field name "+strconv.Quote(name))
			}
			seen[name] = struct{}{}
			fieldNode, err := c.compile(field.Type, path+"."+field.Name)
			if err != nil {
				return nil, err
			}
			fieldIndex := len(node.fields)
			compiledField := typedField{
				name:         name,
				encName:      string(appendEncodedJSONString(nil, name, true)) + ":",
				encNamePlain: string(appendEncodedJSONString(nil, name, false)) + ":",
				omitEmpty:    omitEmpty,
				offset:       field.Offset,
				node:         fieldNode,
				op:           fieldNode.op,
				pos:          int32(fieldIndex),
			}
			if quoted {
				// Like encoding/json, the option looks through one unnamed
				// pointer level when deciding eligibility.
				quotedNode := fieldNode
				if quotedNode.kind == typedPointer && field.Type.Name() == "" {
					quotedNode = quotedNode.elem
				}
				switch quotedNode.kind {
				case typedBool, typedString, typedNumber, typedInt, typedUint, typedFloat:
					compiledField.quoted = true
					compiledField.op = typedOpQuoted
				}
			}
			if fieldIndex < 64 {
				compiledField.seen = uint64(1) << fieldIndex
			}
			if len(name) <= 7 {
				for byteIndex := range len(name) {
					c := name[byteIndex]
					compiledField.key |= uint64(c) << (byteIndex * 8)
					if lower := c | 0x20; 'a' <= lower && lower <= 'z' {
						compiledField.keyFold |= 0x20 << (byteIndex * 8)
					}
				}
				compiledField.key |= uint64('"') << (len(name) * 8)
				compiledField.keyMask = ^uint64(0) >> ((7 - len(name)) * 8)
				compiledField.keyLen = uint8(len(name))
			}
			node.fields = append(node.fields, compiledField)
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
	default:
		return nil, c.unsupported(typ, path, "kind "+typ.Kind().String()+" would require interface or reflective value dispatch")
	}
	return node, nil
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
	for i := range node.fields {
		if node.fields[i].name == key {
			return &node.fields[i]
		}
	}
	if fold {
		for i := range node.fields {
			if strings.EqualFold(node.fields[i].name, key) {
				return &node.fields[i]
			}
		}
	}
	return nil
}

func resetTyped(node *typedNode, dst unsafe.Pointer) {
	if node.ready {
		applyTypedReset(node.reset, dst)
		return
	}
	switch node.kind {
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
			resetTyped(field.node, unsafe.Add(dst, field.offset))
		}
	case typedSlice, typedBytes:
		(*typedSliceHeader)(dst).len = 0
	case typedArray:
		for i := 0; i < node.length; i++ {
			resetTyped(node.elem, unsafe.Add(dst, uintptr(i)*node.elem.size))
		}
	case typedPointer, typedMap:
		*(*unsafe.Pointer)(dst) = nil
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
