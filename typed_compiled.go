package simdjson

import (
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
	default:
		return &TypedDecodeError{Offset: cursor.i, Type: node.typ, Reason: "invalid compiled operation"}
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
	for position, first := 0, true; ; position, first = position+1, false {
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
		if !matched {
			field = node.findFieldSlow(key, !cursor.CaseSensitive())
		}
		if field == nil {
			if err := cursor.Unknown(node.name, key); err != nil {
				return err
			}
			continue
		}
		seen |= field.seen
		fieldNode := field.node
		fieldDst := unsafe.Add(dst, field.offset)
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
		default:
			fieldErr = &TypedDecodeError{Offset: cursor.i, Type: fieldNode.typ, Reason: "invalid compiled operation"}
		}
		if fieldErr != nil {
			if field.op > typedOpInvalid && field.op < typedOpStruct {
				return retagCompiledError(fieldErr, fieldNode.typ)
			}
			return fieldErr
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
	if mask := expected.keyMask; mask != 0 && (word^expected.key)&mask == 0 {
		// The masked compare covers the key bytes and the closing quote.
		keyEnd := keyStart + int(expected.keyLen)
		if keyEnd+1 < len(cursor.src) && cursor.src[keyEnd+1] == ':' {
			cursor.i = keyEnd + 2
			if cursor.i < len(cursor.src) && cursor.src[cursor.i] <= ' ' {
				cursor.skipSpace()
			}
			return "", true, true, nil
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
		if missing&field.seen != 0 {
			resetTyped(field.node, unsafe.Add(dst, field.offset))
		}
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
		default:
			elementErr = decodeCompiled(cursor, node.elem, element)
		}
		if elementErr != nil {
			return elementErr
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
			default:
				elementErr = &TypedDecodeError{Offset: cursor.i, Type: node.elem.typ, Reason: "invalid compiled operation"}
			}
			if elementErr != nil {
				if node.elem.kind <= typedFloat {
					return retagCompiledError(elementErr, node.elem.typ)
				}
				return elementErr
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
				return err
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
	default:
		return decodeCompiled(cursor, node.elem, pointer)
	}
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
	if typed, ok := err.(*TypedDecodeError); ok {
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
	case typedSlice:
		return append(ops, typedResetOp{offset: offset, kind: typedResetSlice})
	case typedPointer:
		return append(ops, typedResetOp{offset: offset, kind: typedResetPointer})
	case typedStruct:
		for i := range node.fields {
			field := &node.fields[i]
			ops = appendTypedReset(ops, field.node, offset+field.offset)
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
		}
	}
}
