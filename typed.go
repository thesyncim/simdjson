package simdjson

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"unsafe"
)

// TypedOptions controls decoding directly into caller-owned Go values.
type TypedOptions struct {
	// MaxDepth limits nested arrays and objects. Values <= 0 use the default.
	MaxDepth int

	// ZeroCopy aliases unescaped strings into src. Callers must not mutate src
	// while decoded strings are in use. When false, decoded strings are
	// independent of src; generated decoders may retain one private copy of the
	// input instead of allocating each string separately.
	ZeroCopy bool

	// DisallowUnknownFields rejects object keys absent from the compiled type.
	DisallowUnknownFields bool

	// CaseSensitive disables the encoding/json-compatible case-insensitive
	// fallback used after exact field-name matching.
	CaseSensitive bool
}

// TypedDecoder is an immutable decoder for one concrete Go type. Compile it
// once and reuse it concurrently; Decode keeps all mutable parser state local
// to the call.
type TypedDecoder[T any] struct {
	root      *typedNode
	rootSlice *typedNode
	options   TypedOptions
}

// CompileDecoder builds a decoder for T. Reflection is confined to this call
// and is never used for scalar or field dispatch in the token loop.
func CompileDecoder[T any](opts TypedOptions) (TypedDecoder[T], error) {
	typ := reflect.TypeFor[T]()
	compiler := typedCompiler{nodes: make(map[reflect.Type]*typedNode)}
	root, err := compiler.compile(typ, typ.String())
	if err != nil {
		return TypedDecoder[T]{}, err
	}
	return TypedDecoder[T]{
		root: root,
		rootSlice: &typedNode{
			kind: typedSlice,
			typ:  reflect.TypeFor[[]T](),
			elem: root,
		},
		options: opts,
	}, nil
}

// Decode replaces dst with one JSON value. Slice capacities already reachable
// through dst are retained where their fields are decoded again.
func (plan TypedDecoder[T]) Decode(src []byte, dst *T) error {
	if plan.root == nil {
		return fmt.Errorf("simdjson: zero TypedDecoder")
	}
	if dst == nil {
		return fmt.Errorf("simdjson: typed Decode destination is nil")
	}
	p := parser{
		src:      src,
		maxDepth: plan.options.MaxDepth,
		zeroCopy: plan.options.ZeroCopy,
	}
	if p.maxDepth <= 0 {
		p.maxDepth = defaultMaxDepth
	}
	p.skipSpace()
	if err := p.decodeTyped(plan.root, unsafe.Pointer(dst), 0, plan.options); err != nil {
		return err
	}
	p.skipSpace()
	if p.i != len(src) {
		return p.err(p.i, "unexpected data after top-level value")
	}
	return nil
}

// DecodeArray decodes a top-level JSON array into dst, reusing its capacity.
// The returned slice is always the authoritative result.
func (plan TypedDecoder[T]) DecodeArray(src []byte, dst []T) ([]T, error) {
	if plan.rootSlice == nil {
		return dst[:0], fmt.Errorf("simdjson: zero TypedDecoder")
	}
	p := parser{
		src:      src,
		maxDepth: plan.options.MaxDepth,
		zeroCopy: plan.options.ZeroCopy,
	}
	if p.maxDepth <= 0 {
		p.maxDepth = defaultMaxDepth
	}
	p.skipSpace()
	if err := p.decodeTyped(plan.rootSlice, unsafe.Pointer(&dst), 0, plan.options); err != nil {
		return dst, err
	}
	p.skipSpace()
	if p.i != len(src) {
		return dst, p.err(p.i, "unexpected data after top-level value")
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

// TypedDecodeError reports valid JSON that cannot be stored in the requested
// Go type.
type TypedDecodeError struct {
	Offset   int
	Type     reflect.Type
	TypeName string
	Reason   string
}

func (e *TypedDecodeError) Error() string {
	typeName := e.TypeName
	if e.Type != nil {
		typeName = e.Type.String()
	}
	return fmt.Sprintf("simdjson: cannot decode JSON at byte %d into %s: %s", e.Offset, typeName, e.Reason)
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
)

type typedNode struct {
	kind   typedKind
	typ    reflect.Type
	size   uintptr
	bits   int
	length int
	elem   *typedNode
	fields []typedField
}

type typedField struct {
	name   string
	offset uintptr
	node   *typedNode
}

type typedCompiler struct {
	nodes map[reflect.Type]*typedNode
}

var jsonNumberReflectType = reflect.TypeFor[json.Number]()

func (c *typedCompiler) compile(typ reflect.Type, path string) (*typedNode, error) {
	if node := c.nodes[typ]; node != nil {
		return node, nil
	}
	node := &typedNode{typ: typ, size: typ.Size()}
	c.nodes[typ] = node

	if typ == jsonNumberReflectType {
		node.kind = typedNumber
		return node, nil
	}

	switch typ.Kind() {
	case reflect.Bool:
		node.kind = typedBool
	case reflect.String:
		node.kind = typedString
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		node.kind = typedInt
		node.bits = typ.Bits()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		node.kind = typedUint
		node.bits = typ.Bits()
	case reflect.Float32, reflect.Float64:
		node.kind = typedFloat
		node.bits = typ.Bits()
	case reflect.Pointer:
		node.kind = typedPointer
		elem, err := c.compile(typ.Elem(), path+"*")
		if err != nil {
			return nil, err
		}
		node.elem = elem
	case reflect.Slice:
		if typ.Elem().Kind() == reflect.Uint8 {
			return nil, c.unsupported(typ, path, "byte slices require base64 semantics")
		}
		node.kind = typedSlice
		elem, err := c.compile(typ.Elem(), path+"[]")
		if err != nil {
			return nil, err
		}
		node.elem = elem
	case reflect.Array:
		node.kind = typedArray
		node.length = typ.Len()
		elem, err := c.compile(typ.Elem(), path+"[]")
		if err != nil {
			return nil, err
		}
		node.elem = elem
	case reflect.Struct:
		node.kind = typedStruct
		seen := make(map[string]struct{}, typ.NumField())
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if !field.IsExported() {
				continue
			}
			tag, tagged := field.Tag.Lookup("json")
			name := field.Name
			if tagged {
				name, _, _ = strings.Cut(tag, ",")
				if name == "-" {
					continue
				}
				if name == "" {
					name = field.Name
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
			node.fields = append(node.fields, typedField{name: name, offset: field.Offset, node: fieldNode})
		}
	default:
		return nil, c.unsupported(typ, path, "kind "+typ.Kind().String()+" would require interface or reflective value dispatch")
	}
	return node, nil
}

func (c *typedCompiler) unsupported(typ reflect.Type, path, reason string) error {
	delete(c.nodes, typ)
	return &UnsupportedTypeError{Type: typ, Path: path, Reason: reason}
}

func (p *parser) decodeTyped(node *typedNode, dst unsafe.Pointer, depth int, opts TypedOptions) error {
	if depth > p.maxDepth {
		return p.err(p.i, "maximum nesting depth exceeded")
	}
	if p.i >= len(p.src) {
		return p.err(p.i, "expected value")
	}
	if p.src[p.i] == 'n' {
		if !matchStringAt(p.src, p.i, "null") {
			return p.err(p.i, "invalid literal")
		}
		p.i += 4
		resetTyped(node, dst)
		return nil
	}

	switch node.kind {
	case typedBool:
		return p.decodeTypedBool(node, dst)
	case typedString:
		return p.decodeTypedString(node, dst)
	case typedNumber:
		return p.decodeTypedNumber(node, dst)
	case typedInt:
		return p.decodeTypedInt(node, dst)
	case typedUint:
		return p.decodeTypedUint(node, dst)
	case typedFloat:
		return p.decodeTypedFloat(node, dst)
	case typedStruct:
		return p.decodeTypedStruct(node, dst, depth+1, opts)
	case typedSlice:
		return p.decodeTypedSlice(node, dst, depth+1, opts)
	case typedArray:
		return p.decodeTypedArray(node, dst, depth+1, opts)
	case typedPointer:
		return p.decodeTypedPointer(node, dst, depth, opts)
	default:
		return &TypedDecodeError{Offset: p.i, Type: node.typ, Reason: "invalid compiled operation"}
	}
}

func (p *parser) decodeTypedBool(node *typedNode, dst unsafe.Pointer) error {
	switch p.src[p.i] {
	case 't':
		if !matchStringAt(p.src, p.i, "true") {
			return p.err(p.i, "invalid literal")
		}
		*(*bool)(dst) = true
		p.i += 4
		return nil
	case 'f':
		if !matchStringAt(p.src, p.i, "false") {
			return p.err(p.i, "invalid literal")
		}
		*(*bool)(dst) = false
		p.i += 5
		return nil
	default:
		return p.typedMismatch(node, "expected boolean")
	}
}

func (p *parser) decodeTypedString(node *typedNode, dst unsafe.Pointer) error {
	if p.src[p.i] != '"' {
		return p.typedMismatch(node, "expected string")
	}
	text, err := p.parseString()
	if err != nil {
		return err
	}
	*(*string)(dst) = text
	return nil
}

func (p *parser) decodeTypedNumber(node *typedNode, dst unsafe.Pointer) error {
	if p.src[p.i] != '-' && !isDigit(p.src[p.i]) {
		return p.typedMismatch(node, "expected number")
	}
	start := p.i
	base := unsafe.Pointer(unsafe.SliceData(p.src))
	end, ok := scanNumberFast(base, len(p.src), start)
	if !ok {
		_, msg := scanNumber(p.src, start)
		return p.err(start, msg)
	}
	p.i = end
	text := unsafe.String((*byte)(unsafe.Add(base, start)), end-start)
	if !p.zeroCopy {
		text = string(p.src[start:end])
	}
	*(*string)(dst) = text
	return nil
}

func (p *parser) decodeTypedInt(node *typedNode, dst unsafe.Pointer) error {
	start, end, text, err := p.typedNumberToken(node)
	if err != nil {
		return err
	}
	if strings.IndexAny(text, ".eE") >= 0 {
		return &TypedDecodeError{Offset: start, Type: node.typ, Reason: "fractional number cannot be stored in an integer"}
	}
	value, parseErr := strconv.ParseInt(text, 10, node.bits)
	if parseErr != nil {
		return &TypedDecodeError{Offset: start, Type: node.typ, Reason: "integer overflow"}
	}
	p.i = end
	switch node.bits {
	case 8:
		*(*int8)(dst) = int8(value)
	case 16:
		*(*int16)(dst) = int16(value)
	case 32:
		*(*int32)(dst) = int32(value)
	case 64:
		*(*int64)(dst) = value
	}
	return nil
}

func (p *parser) decodeTypedUint(node *typedNode, dst unsafe.Pointer) error {
	start, end, text, err := p.typedNumberToken(node)
	if err != nil {
		return err
	}
	if strings.IndexAny(text, ".eE") >= 0 {
		return &TypedDecodeError{Offset: start, Type: node.typ, Reason: "fractional number cannot be stored in an unsigned integer"}
	}
	value, parseErr := strconv.ParseUint(text, 10, node.bits)
	if parseErr != nil {
		return &TypedDecodeError{Offset: start, Type: node.typ, Reason: "unsigned integer overflow"}
	}
	p.i = end
	switch node.bits {
	case 8:
		*(*uint8)(dst) = uint8(value)
	case 16:
		*(*uint16)(dst) = uint16(value)
	case 32:
		*(*uint32)(dst) = uint32(value)
	case 64:
		*(*uint64)(dst) = value
	}
	return nil
}

func (p *parser) decodeTypedFloat(node *typedNode, dst unsafe.Pointer) error {
	start, end, text, err := p.typedNumberToken(node)
	if err != nil {
		return err
	}
	var value float64
	if node.bits == 64 {
		base := unsafe.Pointer(unsafe.SliceData(p.src))
		if exact, ok := exactJSONFloat64(base, start, end); ok {
			value = exact
		} else {
			value, err = strconv.ParseFloat(text, 64)
		}
	} else {
		value, err = strconv.ParseFloat(text, 32)
	}
	if err != nil {
		return &TypedDecodeError{Offset: start, Type: node.typ, Reason: "number out of range"}
	}
	p.i = end
	if node.bits == 32 {
		*(*float32)(dst) = float32(value)
	} else {
		*(*float64)(dst) = value
	}
	return nil
}

func (p *parser) typedNumberToken(node *typedNode) (start, end int, text string, err error) {
	if p.src[p.i] != '-' && !isDigit(p.src[p.i]) {
		return p.i, p.i, "", p.typedMismatch(node, "expected number")
	}
	start = p.i
	base := unsafe.Pointer(unsafe.SliceData(p.src))
	end, ok := scanNumberFast(base, len(p.src), start)
	if !ok {
		_, msg := scanNumber(p.src, start)
		return start, end, "", p.err(start, msg)
	}
	return start, end, unsafe.String((*byte)(unsafe.Add(base, start)), end-start), nil
}

func (p *parser) decodeTypedPointer(node *typedNode, dst unsafe.Pointer, depth int, opts TypedOptions) error {
	pointer := *(*unsafe.Pointer)(dst)
	if pointer == nil {
		value := reflect.New(node.elem.typ)
		if value.Type() != node.typ {
			value = value.Convert(node.typ)
		}
		reflect.NewAt(node.typ, dst).Elem().Set(value)
		pointer = value.UnsafePointer()
	}
	return p.decodeTyped(node.elem, pointer, depth, opts)
}

func (p *parser) decodeTypedStruct(node *typedNode, dst unsafe.Pointer, depth int, opts TypedOptions) error {
	if p.src[p.i] != '{' {
		return p.typedMismatch(node, "expected object")
	}
	resetTyped(node, dst)
	p.i++
	p.skipSpace()
	if p.i < len(p.src) && p.src[p.i] == '}' {
		p.i++
		return nil
	}

	fieldPosition := 0
	for {
		p.skipSpace()
		if p.i >= len(p.src) || p.src[p.i] != '"' {
			return p.err(p.i, "expected object key string")
		}
		key, err := p.typedKey()
		if err != nil {
			return err
		}
		p.skipSpace()
		if p.i >= len(p.src) || p.src[p.i] != ':' {
			return p.err(p.i, "expected colon after object key")
		}
		p.i++
		p.skipSpace()
		field := node.findField(key, fieldPosition, !opts.CaseSensitive)
		if field == nil {
			if opts.DisallowUnknownFields {
				return &TypedDecodeError{Offset: p.i, Type: node.typ, Reason: "unknown field " + strconv.Quote(key)}
			}
			if err := p.skipTypedValue(depth); err != nil {
				return err
			}
		} else if err := p.decodeTyped(field.node, unsafe.Add(dst, field.offset), depth, opts); err != nil {
			return err
		}
		fieldPosition++
		p.skipSpace()
		if p.i >= len(p.src) {
			return p.err(p.i, "unterminated object")
		}
		switch p.src[p.i] {
		case ',':
			p.i++
		case '}':
			p.i++
			return nil
		default:
			return p.err(p.i, "expected comma or closing brace in object")
		}
	}
}

type typedSliceHeader struct {
	data unsafe.Pointer
	len  int
	cap  int
}

func (p *parser) decodeTypedSlice(node *typedNode, dst unsafe.Pointer, depth int, opts TypedOptions) error {
	if p.src[p.i] != '[' {
		return p.typedMismatch(node, "expected array")
	}
	header := (*typedSliceHeader)(dst)
	header.len = 0
	p.i++
	p.skipSpace()
	if p.i < len(p.src) && p.src[p.i] == ']' {
		p.i++
		if header.data == nil {
			reflect.NewAt(node.typ, dst).Elem().Set(reflect.MakeSlice(node.typ, 0, 0))
		}
		return nil
	}

	for index := 0; ; index++ {
		p.skipSpace()
		if index == header.cap {
			capacity := nextTypedSliceCapacity(header.cap, index+1)
			if header.cap == 0 && node.elem.kind == typedStruct && depth <= 3 {
				if estimate := (len(p.src) - p.i) / 128; estimate > capacity {
					capacity = estimate
					if capacity > 1024 {
						capacity = 1024
					}
				}
			}
			growTypedSlice(node, dst, capacity)
			header = (*typedSliceHeader)(dst)
		}
		header.len = index + 1
		element := unsafe.Add(header.data, uintptr(index)*node.elem.size)
		if err := p.decodeTyped(node.elem, element, depth, opts); err != nil {
			return err
		}
		p.skipSpace()
		if p.i >= len(p.src) {
			return p.err(p.i, "unterminated array")
		}
		switch p.src[p.i] {
		case ',':
			p.i++
		case ']':
			p.i++
			return nil
		default:
			return p.err(p.i, "expected comma or closing bracket in array")
		}
	}
}

func (p *parser) decodeTypedArray(node *typedNode, dst unsafe.Pointer, depth int, opts TypedOptions) error {
	if p.src[p.i] != '[' {
		return p.typedMismatch(node, "expected array")
	}
	resetTyped(node, dst)
	p.i++
	p.skipSpace()
	if p.i < len(p.src) && p.src[p.i] == ']' {
		p.i++
		return nil
	}

	for index := 0; ; index++ {
		p.skipSpace()
		if index < node.length {
			element := unsafe.Add(dst, uintptr(index)*node.elem.size)
			if err := p.decodeTyped(node.elem, element, depth, opts); err != nil {
				return err
			}
		} else if err := p.skipTypedValue(depth); err != nil {
			return err
		}
		p.skipSpace()
		if p.i >= len(p.src) {
			return p.err(p.i, "unterminated array")
		}
		switch p.src[p.i] {
		case ',':
			p.i++
		case ']':
			p.i++
			return nil
		default:
			return p.err(p.i, "expected comma or closing bracket in array")
		}
	}
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

func (p *parser) typedMismatch(node *typedNode, reason string) error {
	return &TypedDecodeError{Offset: p.i, Type: node.typ, Reason: reason}
}

func (node *typedNode) findField(key string, position int, fold bool) *typedField {
	if position < len(node.fields) && node.fields[position].name == key {
		return &node.fields[position]
	}
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
	case typedSlice:
		(*typedSliceHeader)(dst).len = 0
	case typedArray:
		for i := 0; i < node.length; i++ {
			resetTyped(node.elem, unsafe.Add(dst, uintptr(i)*node.elem.size))
		}
	case typedPointer:
		*(*unsafe.Pointer)(dst) = nil
	}
}

func growTypedSlice(node *typedNode, dst unsafe.Pointer, capacity int) {
	current := reflect.NewAt(node.typ, dst).Elem()
	next := reflect.MakeSlice(node.typ, current.Len(), capacity)
	reflect.Copy(next, current)
	current.Set(next)
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
