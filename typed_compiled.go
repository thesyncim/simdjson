package simdjson

import (
	"encoding/base64"
	"encoding/binary"
	"math/bits"
	"reflect"
	"runtime"
	"unsafe"
)

func decodeCompiled(cursor *decoderCursor, node *typedNode, dst unsafe.Pointer) error {
	var err error
	switch node.kind {
	case typedBool:
		err = cursor.Bool((*bool)(dst))
	case typedString:
		err = cursor.String((*string)(dst))
	case typedNumber:
		err = cursor.Number((*string)(dst))
	case typedInt:
		switch node.bits {
		case 8:
			err = cursor.Int((*int8)(dst))
		case 16:
			err = cursor.Int((*int16)(dst))
		case 32:
			err = cursor.Int((*int32)(dst))
		case 64:
			err = cursor.Int((*int64)(dst))
		}
	case typedUint:
		switch node.bits {
		case 8:
			err = cursor.Uint((*uint8)(dst))
		case 16:
			err = cursor.Uint((*uint16)(dst))
		case 32:
			err = cursor.Uint((*uint32)(dst))
		case 64:
			err = cursor.Uint((*uint64)(dst))
		}
	case typedFloat:
		if node.bits == 32 {
			err = cursor.Float((*float32)(dst))
		} else {
			err = cursor.Float((*float64)(dst))
		}
	case typedStruct:
		return decodeCompiledStruct(cursor, node, dst)
	case typedSlice:
		return decodeCompiledSlice(cursor, node, dst)
	case typedArray:
		return decodeCompiledArray(cursor, node, dst)
	case typedPointer:
		return decodeCompiledPointer(cursor, node, dst)
	case typedMap:
		return decodeCompiledMap(cursor, node, dst)
	case typedAny:
		return decodeCompiledAny(cursor, dst)
	case typedBytes:
		return decodeCompiledBytes(cursor, node, dst)
	default:
		return &DecodeError{Offset: cursor.i, Type: node.typ, Reason: "invalid compiled operation"}
	}
	if err == nil {
		return nil
	}
	return retagCompiledError(err, node.typ)
}

func decodeCompiledStruct(cursor *decoderCursor, node *typedNode, dst unsafe.Pointer) error {
	if i := cursor.i; i < len(cursor.src) && cursor.src[i] == '{' && cursor.depth < cursor.maxDepth {
		cursor.depth++
		cursor.i = i + 1
	} else {
		null, err := cursor.TryNull()
		if err != nil {
			return err
		}
		if null {
			resetTyped(node, dst)
			return nil
		}
		if err := cursor.BeginObject(node.name); err != nil {
			return err
		}
	}
	var seen uint64
	if node.allSet == 0 && len(node.fields) > 0 {
		resetTyped(node, dst)
	}
	position, first := 0, true
	for {
		var field *typedField
		var key string
		var ok, matched bool
		var err error
		if position < len(node.fields) {
			field = &node.fields[position]
			key, matched, ok, err = cursor.nextObjectFieldExpected(first, field)
		} else {
			key, ok, err = cursor.NextObjectField(first)
		}
		if err != nil {
			return err
		}
		if !ok {
			resetMissingTypedFields(node, dst, seen)
			return nil
		}
		first = false
		if matched {
			position++
		} else {
			field = node.findFieldSlow(key, !cursor.CaseSensitive())
			if field == nil {
				if err := cursor.Unknown(node.name, key); err != nil {
					return err
				}
				continue
			}
			// Resume expected-key matching after the matched field, so
			// documents whose member order is shifted from the struct order
			// (extra members, rotations) recover the fast path.
			position = int(field.pos) + 1
		}
		seen |= field.seen
		fieldNode := field.node
		fieldBase := dst
		if field.hop >= 0 {
			resolved, hopErr := resolveDecodeHops(dst, node.fieldHops[field.hop], cursor.i)
			if hopErr != nil {
				return prependDecodePathField(hopErr, field.name)
			}
			fieldBase = resolved
		}
		fieldDst := unsafe.Add(fieldBase, field.offset)
		var fieldErr error
		switch field.op {
		case typedOpBool:
			fieldErr = cursor.Bool((*bool)(fieldDst))
		case typedOpString:
			fieldErr = cursor.String((*string)(fieldDst))
		case typedOpNumber:
			fieldErr = cursor.Number((*string)(fieldDst))
		case typedOpInt8:
			fieldErr = cursor.Int((*int8)(fieldDst))
		case typedOpInt16:
			fieldErr = cursor.Int((*int16)(fieldDst))
		case typedOpInt32:
			fieldErr = cursor.Int((*int32)(fieldDst))
		case typedOpInt64:
			fieldErr = cursor.Int((*int64)(fieldDst))
		case typedOpUint8:
			fieldErr = cursor.Uint((*uint8)(fieldDst))
		case typedOpUint16:
			fieldErr = cursor.Uint((*uint16)(fieldDst))
		case typedOpUint32:
			fieldErr = cursor.Uint((*uint32)(fieldDst))
		case typedOpUint64:
			fieldErr = cursor.Uint((*uint64)(fieldDst))
		case typedOpFloat32:
			fieldErr = cursor.Float((*float32)(fieldDst))
		case typedOpFloat64:
			fieldErr = cursor.Float((*float64)(fieldDst))
		case typedOpStruct:
			fieldErr = decodeCompiledStruct(cursor, fieldNode, fieldDst)
		case typedOpSlice:
			fieldErr = decodeCompiledSlice(cursor, fieldNode, fieldDst)
		case typedOpArray:
			fieldErr = decodeCompiledArray(cursor, fieldNode, fieldDst)
		case typedOpPointer:
			fieldErr = decodeCompiledPointer(cursor, fieldNode, fieldDst)
		case typedOpMap:
			fieldErr = decodeCompiledMap(cursor, fieldNode, fieldDst)
		case typedOpAny:
			fieldErr = decodeCompiledAny(cursor, fieldDst)
		case typedOpBytes:
			fieldErr = decodeCompiledBytes(cursor, fieldNode, fieldDst)
		case typedOpQuoted:
			fieldErr = decodeQuotedField(cursor, fieldNode, fieldDst)
		default:
			fieldErr = &DecodeError{Offset: cursor.i, Type: fieldNode.typ, Reason: "invalid compiled operation"}
		}
		if fieldErr != nil {
			if field.op > typedOpInvalid && field.op < typedOpStruct {
				fieldErr = retagCompiledError(fieldErr, fieldNode.typ)
			}
			return prependDecodePathField(fieldErr, field.name)
		}
	}
}

func (cursor *decoderCursor) nextObjectFieldExpected(first bool, expected *typedField) (key string, matched, ok bool, err error) {
	i := cursor.i
	if i < len(cursor.src) && cursor.src[i] <= ' ' {
		i = skipSpace(cursor.src, i)
	}
	if i >= len(cursor.src) {
		key, ok, err = cursor.NextObjectField(first)
		return key, false, ok, err
	}
	if first {
		if cursor.src[i] == '}' {
			cursor.i = i + 1
			cursor.depth--
			return "", false, false, nil
		}
		if cursor.src[i] != '"' {
			key, ok, err = cursor.NextObjectField(first)
			return key, false, ok, err
		}
	} else {
		switch cursor.src[i] {
		case '}':
			cursor.i = i + 1
			cursor.depth--
			return "", false, false, nil
		case ',':
			i++
			if i < len(cursor.src) && cursor.src[i] <= ' ' {
				i = skipSpace(cursor.src, i)
			}
			if i >= len(cursor.src) || cursor.src[i] != '"' {
				key, ok, err = cursor.NextObjectField(first)
				return key, false, ok, err
			}
		default:
			key, ok, err = cursor.NextObjectField(first)
			return key, false, ok, err
		}
	}

	keyStart := i + 1
	if keyStart+8 > len(cursor.src) {
		key, ok, err = cursor.NextObjectField(first)
		return key, false, ok, err
	}
	word := binary.LittleEndian.Uint64(cursor.src[keyStart:])
	if mask := expected.keyMask; mask != 0 {
		// The masked compare covers the key bytes and the closing quote. When
		// the exact compare misses and folding applies, retry with the ASCII
		// case bits of the key's letters masked out.
		diff := (word ^ expected.key) & mask
		if diff != 0 && diff&^expected.keyFold == 0 && cursor.flags&decoderCaseSensitive == 0 {
			diff = 0
		}
		if diff == 0 {
			keyEnd := keyStart + int(expected.keyLen)
			if keyEnd+1 < len(cursor.src) && cursor.src[keyEnd+1] == ':' {
				cursor.i = keyEnd + 2
				if cursor.i < len(cursor.src) && cursor.src[cursor.i] <= ' ' {
					cursor.skipSpace()
				}
				return "", true, true, nil
			}
		}
	}
	special := stringSpecialMask(word)
	if special == 0 {
		key, ok, err = cursor.NextObjectField(first)
		return key, false, ok, err
	}
	keyLen := bits.TrailingZeros64(special) / 8
	keyEnd := keyStart + keyLen
	if cursor.src[keyEnd] != '"' || keyEnd+1 >= len(cursor.src) || cursor.src[keyEnd+1] != ':' {
		key, ok, err = cursor.NextObjectField(first)
		return key, false, ok, err
	}
	cursor.i = keyEnd + 2
	if cursor.i < len(cursor.src) && cursor.src[cursor.i] <= ' ' {
		cursor.skipSpace()
	}
	key = unsafe.String(unsafe.SliceData(cursor.src[keyStart:keyEnd]), keyLen)
	return key, false, true, nil
}

func resetMissingTypedFields(node *typedNode, dst unsafe.Pointer, seen uint64) {
	if seen == node.allSet || node.allSet == 0 {
		return
	}
	missing := node.allSet &^ seen
	for i := range node.fields {
		field := &node.fields[i]
		if missing&field.seen == 0 {
			continue
		}
		target := dst
		if field.hop >= 0 {
			target = resolveResetHops(dst, node.fieldHops[field.hop])
			if target == nil {
				continue
			}
		}
		resetTyped(field.node, unsafe.Add(target, field.offset))
	}
}

func decodeCompiledSlice(cursor *decoderCursor, node *typedNode, dst unsafe.Pointer) error {
	if i := cursor.i; i < len(cursor.src) && cursor.src[i] == '[' && cursor.depth < cursor.maxDepth {
		cursor.depth++
		cursor.i = i + 1
	} else {
		null, err := cursor.TryNull()
		if err != nil {
			return err
		}
		if null {
			*(*typedSliceHeader)(dst) = typedSliceHeader{}
			return nil
		}
		if err := cursor.BeginArray(node.name); err != nil {
			return err
		}
	}
	header := (*typedSliceHeader)(dst)
	header.len = 0
	for index, first := 0, true; ; index, first = index+1, false {
		more, err := cursor.NextArrayElement(first)
		if err != nil {
			return err
		}
		if !more {
			if index == 0 && header.data == nil {
				setTypedEmptySlice(node, dst)
			}
			return nil
		}
		if index == header.cap {
			capacity := nextTypedSliceCapacity(header.cap, index+1)
			if header.cap == 0 && node.elem.kind == typedStruct && cursor.depth <= 3 {
				if estimate := (len(cursor.src) - cursor.i) / 128; estimate > capacity {
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
		var elementErr error
		switch node.elem.kind {
		case typedStruct:
			elementErr = decodeCompiledStruct(cursor, node.elem, element)
		case typedSlice:
			elementErr = decodeCompiledSlice(cursor, node.elem, element)
		case typedArray:
			elementErr = decodeCompiledArray(cursor, node.elem, element)
		case typedPointer:
			elementErr = decodeCompiledPointer(cursor, node.elem, element)
		case typedMap:
			elementErr = decodeCompiledMap(cursor, node.elem, element)
		default:
			elementErr = decodeCompiled(cursor, node.elem, element)
		}
		if elementErr != nil {
			return prependDecodePathIndex(elementErr, index)
		}
	}
}

func decodeCompiledArray(cursor *decoderCursor, node *typedNode, dst unsafe.Pointer) error {
	if i := cursor.i; i < len(cursor.src) && cursor.src[i] == '[' && cursor.depth < cursor.maxDepth {
		cursor.depth++
		cursor.i = i + 1
		resetTyped(node, dst)
	} else {
		null, err := cursor.TryNull()
		if err != nil {
			return err
		}
		resetTyped(node, dst)
		if null {
			return nil
		}
		if err := cursor.BeginArray(node.name); err != nil {
			return err
		}
	}
	var err error
	if node.elem.kind == typedFloat {
		if node.elem.bits == 32 {
			err = decodeCompiledFloatArray[float32](cursor, node, dst)
		} else {
			err = decodeCompiledFloatArray[float64](cursor, node, dst)
		}
		if err != nil {
			return retagCompiledError(err, node.elem.typ)
		}
		return nil
	}
	for index, first := 0, true; ; index, first = index+1, false {
		more, err := cursor.NextArrayElement(first)
		if err != nil {
			return err
		}
		if !more {
			return nil
		}
		if index < node.length {
			element := unsafe.Add(dst, uintptr(index)*node.elem.size)
			var elementErr error
			switch node.elem.kind {
			case typedBool:
				elementErr = cursor.Bool((*bool)(element))
			case typedString:
				elementErr = cursor.String((*string)(element))
			case typedNumber:
				elementErr = cursor.Number((*string)(element))
			case typedInt:
				switch node.elem.bits {
				case 8:
					elementErr = cursor.Int((*int8)(element))
				case 16:
					elementErr = cursor.Int((*int16)(element))
				case 32:
					elementErr = cursor.Int((*int32)(element))
				case 64:
					elementErr = cursor.Int((*int64)(element))
				}
			case typedUint:
				switch node.elem.bits {
				case 8:
					elementErr = cursor.Uint((*uint8)(element))
				case 16:
					elementErr = cursor.Uint((*uint16)(element))
				case 32:
					elementErr = cursor.Uint((*uint32)(element))
				case 64:
					elementErr = cursor.Uint((*uint64)(element))
				}
			case typedFloat:
				if node.elem.bits == 32 {
					elementErr = cursor.Float((*float32)(element))
				} else {
					elementErr = cursor.Float((*float64)(element))
				}
			case typedStruct:
				elementErr = decodeCompiledStruct(cursor, node.elem, element)
			case typedSlice:
				elementErr = decodeCompiledSlice(cursor, node.elem, element)
			case typedArray:
				elementErr = decodeCompiledArray(cursor, node.elem, element)
			case typedPointer:
				elementErr = decodeCompiledPointer(cursor, node.elem, element)
			case typedMap:
				elementErr = decodeCompiledMap(cursor, node.elem, element)
			case typedAny:
				elementErr = decodeCompiledAny(cursor, element)
			case typedBytes:
				elementErr = decodeCompiledBytes(cursor, node.elem, element)
			default:
				elementErr = &DecodeError{Offset: cursor.i, Type: node.elem.typ, Reason: "invalid compiled operation"}
			}
			if elementErr != nil {
				if node.elem.kind <= typedFloat {
					elementErr = retagCompiledError(elementErr, node.elem.typ)
				}
				return prependDecodePathIndex(elementErr, index)
			}
		} else if err := cursor.Skip(); err != nil {
			return err
		}
	}
}

func decodeCompiledFloatArray[T floatValue](cursor *decoderCursor, node *typedNode, dst unsafe.Pointer) error {
	src := cursor.src
	for index, first := 0, true; ; index, first = index+1, false {
		// Fused fast path: consume the delimiter and a short float without the
		// general element iterator. cursor.i stays untouched until the whole
		// element is accepted, so every partial match falls through cleanly.
		i := cursor.i
		if i < len(src) && src[i] <= ' ' {
			i = skipSpace(src, i)
		}
		if i < len(src) && index < node.length {
			c := src[i]
			if c == ']' {
				cursor.i = i + 1
				cursor.depth--
				return nil
			}
			if !first {
				if c != ',' {
					goto general
				}
				i++
				if i < len(src) && src[i] <= ' ' {
					i = skipSpace(src, i)
				}
			}
			if i < len(src) && (src[i] == '-' || isDigit(src[i])) {
				if value, end, ok := shortTypedFloatAt(src, i); ok {
					*(*T)(unsafe.Add(dst, uintptr(index)*node.elem.size)) = T(value)
					cursor.i = end
					continue
				}
			}
		}
	general:
		more, err := cursor.NextArrayElement(first)
		if err != nil {
			return err
		}
		if !more {
			return nil
		}
		if index < node.length {
			element := (*T)(unsafe.Add(dst, uintptr(index)*node.elem.size))
			if err := cursor.Float(element); err != nil {
				return prependDecodePathIndex(err, index)
			}
		} else if err := cursor.Skip(); err != nil {
			return err
		}
	}
}

func decodeCompiledPointer(cursor *decoderCursor, node *typedNode, dst unsafe.Pointer) error {
	null, err := cursor.TryNull()
	if err != nil {
		return err
	}
	if null {
		*(*unsafe.Pointer)(dst) = nil
		return nil
	}
	pointer := *(*unsafe.Pointer)(dst)
	if pointer == nil {
		pointer = allocateTypedPointer(node, dst)
	}
	switch node.elem.kind {
	case typedStruct:
		return decodeCompiledStruct(cursor, node.elem, pointer)
	case typedSlice:
		return decodeCompiledSlice(cursor, node.elem, pointer)
	case typedArray:
		return decodeCompiledArray(cursor, node.elem, pointer)
	case typedPointer:
		return decodeCompiledPointer(cursor, node.elem, pointer)
	case typedMap:
		return decodeCompiledMap(cursor, node.elem, pointer)
	default:
		return decodeCompiled(cursor, node.elem, pointer)
	}
}

// decodeCompiledMap decodes a JSON object into a map with string keys. Like
// encoding/json it allocates a map only when dst holds nil and otherwise
// merges into the existing entries. Entries decode through one reusable
// element that is zeroed between entries, so nested slice capacity is never
// shared between values.
func decodeCompiledMap(cursor *decoderCursor, node *typedNode, dst unsafe.Pointer) error {
	null, err := cursor.TryNull()
	if err != nil {
		return err
	}
	if null {
		*(*unsafe.Pointer)(dst) = nil
		return nil
	}
	if err := cursor.BeginObject(node.name); err != nil {
		return err
	}
	// Map keys are retained by the result, so switch owned decodes to the
	// private input copy before the first key string is sliced.
	cursor.ownSource()
	// dst itself must stay out of reflect: reflect.NewAt would mark every
	// Decode destination as escaping. A map value is one pointer word, so
	// reflect works on a local copy of it and the created map is written
	// back through a pointer store, which carries a write barrier.
	existing := *(*unsafe.Pointer)(dst)
	var mapValue reflect.Value
	if existing == nil {
		mapValue = reflect.MakeMap(node.typ)
		*(*unsafe.Pointer)(dst) = mapValue.UnsafePointer()
	} else {
		mapValue = reflect.NewAt(node.typ, unsafe.Pointer(&existing)).Elem()
	}
	keyType := node.typ.Key()
	element := reflect.New(node.elem.typ)
	elementPtr := element.UnsafePointer()
	elementValue := element.Elem()
	for first := true; ; first = false {
		key, ok, err := cursor.NextObjectField(first)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		elementValue.SetZero()
		var entryErr error
		switch node.elem.kind {
		case typedStruct:
			entryErr = decodeCompiledStruct(cursor, node.elem, elementPtr)
		case typedSlice:
			entryErr = decodeCompiledSlice(cursor, node.elem, elementPtr)
		case typedArray:
			entryErr = decodeCompiledArray(cursor, node.elem, elementPtr)
		case typedPointer:
			entryErr = decodeCompiledPointer(cursor, node.elem, elementPtr)
		case typedMap:
			entryErr = decodeCompiledMap(cursor, node.elem, elementPtr)
		default:
			entryErr = decodeCompiled(cursor, node.elem, elementPtr)
		}
		if entryErr != nil {
			return prependDecodePathField(entryErr, key)
		}
		keyValue := reflect.ValueOf(key)
		if keyType.Kind() == reflect.String && keyType != keyValue.Type() {
			keyValue = keyValue.Convert(keyType)
		}
		mapValue.SetMapIndex(keyValue, elementValue)
	}
}

// decodeCompiledAny decodes one JSON value into an empty interface using the
// standard dynamic shapes: map[string]any, []any, string, float64, bool, and
// nil. The destination is always replaced; unlike encoding/json, a pointer
// already stored in the interface is not decoded into.
func decodeCompiledAny(cursor *decoderCursor, dst unsafe.Pointer) error {
	// Dynamic strings are retained by the result, so owned decodes switch to
	// the private input copy first.
	cursor.ownSource()
	p := cursor.slowParser()
	p.skipSpace()
	value, err := p.parseAnyValue(cursor.depth, false)
	cursor.i = p.i
	if err != nil {
		return err
	}
	*(*any)(dst) = value
	return nil
}

// decodeQuotedField decodes a scalar tagged with the string option: the JSON
// value is a string whose contents are one JSON scalar. Bare null resets the
// field like encoding/json; anything but a string is rejected.
func decodeQuotedField(cursor *decoderCursor, node *typedNode, dst unsafe.Pointer) error {
	null, err := cursor.TryNull()
	if err != nil {
		return err
	}
	if null {
		resetTyped(node, dst)
		return nil
	}
	i := cursor.i
	if i >= len(cursor.src) || cursor.src[i] != '"' {
		return &DecodeError{Offset: i, Type: node.typ, Reason: "expected quoted value for string-tagged field"}
	}
	var inner []byte
	start := i + 1
	end := scanStringSpecial(cursor.src, start)
	if end < len(cursor.src) && cursor.src[end] == '"' {
		inner = cursor.src[start:end]
		cursor.i = end + 1
	} else {
		p := cursor.slowParser()
		text, err := p.parseString()
		cursor.i = p.i
		if err != nil {
			return err
		}
		inner = unsafe.Slice(unsafe.StringData(text), len(text))
	}
	// The inner scalar may alias a temporary unescape buffer, so decoded
	// strings must never alias it.
	flags := cursor.flags &^ (decoderZeroCopy | decoderSourceOwned)
	sub := decoderCursor{src: inner, maxDepth: cursor.maxDepth, flags: flags}
	scalar := node
	if scalar.kind == typedPointer {
		scalar = scalar.elem
	}
	if scalar.kind == typedString {
		// The contents must themselves be a JSON string.
		if len(inner) == 0 || inner[0] != '"' {
			return &DecodeError{Offset: i, Type: node.typ, Reason: "string-tagged field does not contain a JSON string"}
		}
	}
	if err := decodeCompiled(&sub, node, dst); err != nil {
		if typed, ok := err.(*DecodeError); ok {
			typed.Offset = i
		}
		return err
	}
	if sub.i != len(inner) {
		return &DecodeError{Offset: i, Type: node.typ, Reason: "string-tagged field contains trailing data"}
	}
	return nil
}

// decodeCompiledBytes decodes a base64 JSON string into a byte slice,
// reusing destination capacity when possible.
func decodeCompiledBytes(cursor *decoderCursor, node *typedNode, dst unsafe.Pointer) error {
	null, err := cursor.TryNull()
	if err != nil {
		return err
	}
	header := (*typedSliceHeader)(dst)
	if null {
		*header = typedSliceHeader{}
		return nil
	}
	i := cursor.i
	if i >= len(cursor.src) || cursor.src[i] != '"' {
		return &DecodeError{Offset: i, Type: node.typ, Reason: "expected base64 string"}
	}
	start := i + 1
	end := scanStringSpecial(cursor.src, start)
	var encoded []byte
	if end < len(cursor.src) && cursor.src[end] == '"' {
		encoded = cursor.src[start:end]
		cursor.i = end + 1
	} else {
		p := cursor.slowParser()
		text, err := p.parseString()
		cursor.i = p.i
		if err != nil {
			return err
		}
		encoded = unsafe.Slice(unsafe.StringData(text), len(text))
	}
	decodedLen := base64.StdEncoding.DecodedLen(len(encoded))
	if header.data == nil || header.cap < decodedLen {
		buffer := make([]byte, decodedLen)
		header.data = unsafe.Pointer(unsafe.SliceData(buffer))
		header.cap = decodedLen
	}
	target := unsafe.Slice((*byte)(header.data), header.cap)
	n, err := base64.StdEncoding.Decode(target, encoded)
	if err != nil {
		return &DecodeError{Offset: i, Type: node.typ, Reason: "invalid base64: " + err.Error()}
	}
	header.len = n
	return nil
}

// resolveDecodeHops walks embedded pointer hops toward a flattened field,
// allocating nil intermediates like encoding/json, which also only rejects
// unexported embedded pointers at the moment an allocation is required.
func resolveDecodeHops(dst unsafe.Pointer, hops []typedFieldHop, offset int) (unsafe.Pointer, error) {
	for i := range hops {
		hop := &hops[i]
		slot := (*unsafe.Pointer)(unsafe.Add(dst, hop.offset))
		pointer := *slot
		if pointer == nil {
			if hop.unexported {
				return nil, &DecodeError{Offset: offset, TypeName: hop.pointee.String(), Reason: "cannot set embedded pointer to unexported struct type"}
			}
			value := reflect.New(hop.pointee)
			pointer = value.UnsafePointer()
			*slot = pointer
			runtime.KeepAlive(value)
		}
		dst = pointer
	}
	return dst, nil
}

// resolveResetHops walks hops without allocating; a nil link means the field
// is already zero.
func resolveResetHops(dst unsafe.Pointer, hops []typedFieldHop) unsafe.Pointer {
	for i := range hops {
		pointer := *(*unsafe.Pointer)(unsafe.Add(dst, hops[i].offset))
		if pointer == nil {
			return nil
		}
		dst = pointer
	}
	return dst
}

func allocateTypedPointer(node *typedNode, dst unsafe.Pointer) unsafe.Pointer {
	value := reflect.New(node.elem.typ)
	pointer := value.UnsafePointer()
	*(*unsafe.Pointer)(dst) = pointer
	runtime.KeepAlive(value)
	return pointer
}

func setTypedEmptySlice(node *typedNode, dst unsafe.Pointer) {
	empty := reflect.MakeSlice(node.typ, 0, 0)
	*(*typedSliceHeader)(dst) = typedSliceHeader{data: empty.UnsafePointer()}
	runtime.KeepAlive(empty)
}

func retagCompiledError(err error, typ reflect.Type) error {
	if typed, ok := err.(*DecodeError); ok {
		typed.Type = typ
		typed.TypeName = ""
	}
	return err
}

type typedResetKind uint8

const (
	typedResetByte typedResetKind = iota
	typedResetUint16
	typedResetUint32
	typedResetUint64
	typedResetBytes
	typedResetString
	typedResetSlice
	typedResetPointer
	typedResetInterface
)

type typedResetOp struct {
	offset uintptr
	size   uintptr
	kind   typedResetKind
}

func prepareTypedResets(node *typedNode, seen map[*typedNode]bool) {
	if node == nil || seen[node] {
		return
	}
	seen[node] = true
	if node.kind == typedStruct || node.kind == typedArray {
		node.reset = appendTypedReset(node.reset, node, 0)
		node.ready = true
	}
	prepareTypedResets(node.elem, seen)
	for i := range node.fields {
		prepareTypedResets(node.fields[i].node, seen)
	}
}

func appendTypedReset(ops []typedResetOp, node *typedNode, offset uintptr) []typedResetOp {
	switch node.kind {
	case typedBool, typedInt, typedUint, typedFloat:
		return appendTypedClear(ops, offset, node.size)
	case typedString, typedNumber:
		return append(ops, typedResetOp{offset: offset, kind: typedResetString})
	case typedSlice, typedBytes:
		return append(ops, typedResetOp{offset: offset, kind: typedResetSlice})
	case typedPointer, typedMap:
		return append(ops, typedResetOp{offset: offset, kind: typedResetPointer})
	case typedAny:
		return append(ops, typedResetOp{offset: offset, kind: typedResetInterface})
	case typedStruct:
		for i := range node.fields {
			field := &node.fields[i]
			if field.hop >= 0 {
				continue
			}
			ops = appendTypedReset(ops, field.node, offset+field.offset)
		}
		for _, hopOffset := range node.hopResets {
			ops = append(ops, typedResetOp{offset: offset + hopOffset, kind: typedResetPointer})
		}
		return ops
	case typedArray:
		if typedRawClearable(node.elem) {
			return appendTypedClear(ops, offset, node.size)
		}
		for i := 0; i < node.length; i++ {
			ops = appendTypedReset(ops, node.elem, offset+uintptr(i)*node.elem.size)
		}
	}
	return ops
}

func typedRawClearable(node *typedNode) bool {
	switch node.kind {
	case typedBool, typedInt, typedUint, typedFloat:
		return true
	case typedArray:
		return typedRawClearable(node.elem)
	default:
		return false
	}
}

func appendTypedClear(ops []typedResetOp, offset, size uintptr) []typedResetOp {
	kind := typedResetBytes
	switch size {
	case 1:
		kind = typedResetByte
	case 2:
		kind = typedResetUint16
	case 4:
		kind = typedResetUint32
	case 8:
		kind = typedResetUint64
	}
	return append(ops, typedResetOp{offset: offset, size: size, kind: kind})
}

func applyTypedReset(ops []typedResetOp, dst unsafe.Pointer) {
	for i := range ops {
		op := &ops[i]
		field := unsafe.Add(dst, op.offset)
		switch op.kind {
		case typedResetByte:
			*(*uint8)(field) = 0
		case typedResetUint16:
			*(*uint16)(field) = 0
		case typedResetUint32:
			*(*uint32)(field) = 0
		case typedResetUint64:
			*(*uint64)(field) = 0
		case typedResetBytes:
			clear(unsafe.Slice((*byte)(field), int(op.size)))
		case typedResetString:
			*(*string)(field) = ""
		case typedResetSlice:
			(*typedSliceHeader)(field).len = 0
		case typedResetPointer:
			*(*unsafe.Pointer)(field) = nil
		case typedResetInterface:
			*(*any)(field) = nil
		}
	}
}
