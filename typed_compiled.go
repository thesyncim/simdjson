package simdjson

import (
	"encoding"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"math/bits"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"unsafe"
)

func (cursor *decoderCursor) decodeCompiled(node *typedNode, dst unsafe.Pointer) error {
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
		return cursor.decodeCompiledStruct(node, dst)
	case typedSlice:
		return cursor.decodeCompiledSlice(node, dst)
	case typedArray:
		return cursor.decodeCompiledArray(node, dst)
	case typedPointer:
		return cursor.decodeCompiledPointer(node, dst)
	case typedMap:
		return cursor.decodeCompiledMap(node, dst)
	case typedAny:
		return cursor.decodeCompiledAny(dst)
	case typedBytes:
		return cursor.decodeCompiledBytes(node, dst)
	case typedUnmarshalerJSON:
		return cursor.decodeViaUnmarshaler(node, dst)
	case typedUnmarshalerText:
		return cursor.decodeViaTextUnmarshaler(node, dst)
	case typedIface:
		return cursor.decodeCompiledIface(node, dst)
	default:
		return &DecodeError{Offset: cursor.i, Type: node.typ, Reason: "invalid compiled operation"}
	}
	if err == nil {
		return nil
	}
	return retagCompiledError(err, node.typ)
}

func (cursor *decoderCursor) decodeCompiledStruct(node *typedNode, dst unsafe.Pointer) error {
	if i := cursor.i; i < len(cursor.src) && cursor.src[i] == '{' && cursor.depth < cursor.maxDepth {
		cursor.depth++
		cursor.i = i + 1
	} else {
		null := false
		if !cursor.notNullFast() {
			var err error
			null, err = cursor.TryNull()
			if err != nil {
				return err
			}
		}
		if null {
			// encoding/json treats null on a struct as a no-op.
			if cursor.flags&decoderReplace != 0 {
				resetTyped(node, dst)
			}
			return nil
		}
		if err := cursor.BeginObject(node.name); err != nil {
			return err
		}
	}
	var seen uint64
	if cursor.flags&decoderReplace != 0 && (node.inlineMap != nil || (node.allSet == 0 && len(node.fields) > 0)) {
		// The catch-all breaks the allSet shortcut: even when every declared
		// field is overwritten, stale unknown members must be cleared, so an
		// inline map forces the reset.
		resetTyped(node, dst)
	}
	position, first := 0, true
	// inlineDec stays nil until the first unknown member of a struct that
	// declares a catch-all, so structs without one add only this word.
	var inlineDec *inlineDecoder
	for {
		var field *typedField
		var key string
		var ok, matched bool
		var err error
		if position < len(node.fields) {
			field = &node.fields[position]
			if cursor.flags&decoderExpectedSlow == 0 && cursor.matchObjectFieldExpected(first, field) {
				matched, ok = true, true
			} else {
				cursor.flags |= decoderExpectedSlow
				key, matched, ok, err = cursor.nextObjectFieldExpectedSlow(first, field)
			}
		} else {
			key, ok, err = cursor.NextObjectField(first)
		}
		if err != nil {
			return err
		}
		if !ok {
			if cursor.flags&decoderReplace != 0 {
				resetMissingTypedFields(node, dst, seen)
			}
			return nil
		}
		first = false
		if matched {
			position++
		} else {
			field = node.findFieldSlow(key, !cursor.CaseSensitive())
			if field == nil {
				if node.inlineMap != nil {
					// The catch-all consumes the member, so it is never
					// "unknown" and DisallowUnknownFields does not apply.
					if inlineDec == nil {
						inlineDec = newInlineDecoder(node.inlineMap)
					}
					if err := inlineDec.decodeEntry(cursor, node.inlineMap, dst, key); err != nil {
						return prependDecodePathField(err, key)
					}
					continue
				}
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
			fieldErr = cursor.decodeCompiledStruct(fieldNode, fieldDst)
		case typedOpSlice:
			fieldErr = cursor.decodeCompiledSlice(fieldNode, fieldDst)
		case typedOpArray:
			fieldErr = cursor.decodeCompiledArray(fieldNode, fieldDst)
		case typedOpPointer:
			fieldErr = cursor.decodeCompiledPointer(fieldNode, fieldDst)
		case typedOpMap:
			fieldErr = cursor.decodeCompiledMap(fieldNode, fieldDst)
		case typedOpAny:
			fieldErr = cursor.decodeCompiledAny(fieldDst)
		case typedOpBytes:
			fieldErr = cursor.decodeCompiledBytes(fieldNode, fieldDst)
		case typedOpQuoted:
			fieldErr = cursor.decodeQuotedField(fieldNode, fieldDst)
		case typedOpUnmarshaler:
			if fieldNode.kind == typedUnmarshalerJSON {
				fieldErr = cursor.decodeViaUnmarshaler(fieldNode, fieldDst)
			} else {
				fieldErr = cursor.decodeViaTextUnmarshaler(fieldNode, fieldDst)
			}
		case typedOpIface:
			fieldErr = cursor.decodeCompiledIface(fieldNode, fieldDst)
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

func (cursor *decoderCursor) matchObjectFieldExpected(first bool, expected *typedField) bool {
	src := cursor.src
	i := cursor.i
	n := len(src)
	if i >= n || expected.keyMask == 0 {
		return false
	}
	base := unsafe.Pointer(unsafe.SliceData(src))
	if first {
		if fastByteAt(base, i) != '"' {
			return false
		}
	} else {
		if fastByteAt(base, i) != ',' {
			return false
		}
		i++
		if i >= n || fastByteAt(base, i) != '"' {
			return false
		}
	}

	keyStart := i + 1
	if keyStart+8 > n {
		return false
	}
	word := loadUint64LE(unsafe.Add(base, keyStart))
	if (word^expected.key)&expected.keyMask != 0 {
		return false
	}
	keyEnd := keyStart + int(expected.keyLen)
	if expected.keyLen <= 6 {
		cursor.i = keyEnd + 2
		if cursor.i < n && fastByteAt(base, cursor.i) <= ' ' {
			cursor.skipSpace()
		}
		return true
	}
	if keyEnd+2 > n || loadUint16LE(unsafe.Add(base, keyEnd)) != quoteColonLE {
		return false
	}
	if expected.keyLen > 7 && !matchStringAt(src, keyStart+8, expected.name[8:]) {
		return false
	}
	cursor.i = keyEnd + 2
	if cursor.i < n && fastByteAt(base, cursor.i) <= ' ' {
		cursor.skipSpace()
	}
	return true
}

//go:noinline
func (cursor *decoderCursor) nextObjectFieldExpectedSlow(first bool, expected *typedField) (key string, matched, ok bool, err error) {
	i := cursor.i
	if i < len(cursor.src) && cursor.src[i] <= ' ' {
		i = skipSpaceIndent(cursor.src, i)
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
				i = skipSpaceIndent(cursor.src, i)
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
		foldedHead := diff != 0
		if diff != 0 && diff&^expected.keyFold == 0 && cursor.flags&decoderCaseSensitive == 0 {
			diff = 0
		}
		if diff == 0 {
			keyEnd := keyStart + int(expected.keyLen)
			if expected.keyLen <= 6 {
				cursor.i = keyEnd + 2
				if cursor.i < len(cursor.src) && cursor.src[cursor.i] <= ' ' {
					cursor.skipSpace()
				}
				return "", true, true, nil
			}
			matchedName := expected.keyLen <= 7
			if !matchedName && keyEnd+1 < len(cursor.src) && cursor.src[keyEnd] == '"' {
				matchedName = !foldedHead && matchStringAt(cursor.src, keyStart+8, expected.name[8:])
				if !matchedName && cursor.flags&decoderCaseSensitive == 0 {
					actual := unsafe.String(unsafe.SliceData(cursor.src[keyStart:keyEnd]), keyEnd-keyStart)
					matchedName = strings.EqualFold(actual, expected.name)
				}
			}
			if matchedName && keyEnd+1 < len(cursor.src) && cursor.src[keyEnd+1] == ':' {
				cursor.i = keyEnd + 2
				if cursor.i < len(cursor.src) && cursor.src[cursor.i] <= ' ' {
					cursor.skipSpace()
				}
				return "", true, true, nil
			}
		}
	}
	special := stringSpecialMask(word)
	keyEnd := keyStart
	if special != 0 {
		keyEnd += bits.TrailingZeros64(special) / 8
	} else {
		keyEnd = scanStringSpecial(cursor.src, keyStart+8)
	}
	if keyEnd >= len(cursor.src) || cursor.src[keyEnd] != '"' || keyEnd+1 >= len(cursor.src) || cursor.src[keyEnd+1] != ':' {
		key, ok, err = cursor.NextObjectField(first)
		return key, false, ok, err
	}
	cursor.i = keyEnd + 2
	if cursor.i < len(cursor.src) && cursor.src[cursor.i] <= ' ' {
		cursor.skipSpace()
	}
	keyLen := keyEnd - keyStart
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

func (cursor *decoderCursor) decodeCompiledSlice(node *typedNode, dst unsafe.Pointer) error {
	if i := cursor.i; i < len(cursor.src) && cursor.src[i] == '[' && cursor.depth < cursor.maxDepth {
		cursor.depth++
		cursor.i = i + 1
	} else {
		null := false
		if !cursor.notNullFast() {
			var err error
			null, err = cursor.TryNull()
			if err != nil {
				return err
			}
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
	// Homogeneous scalar slices ([]int64, []float64, ...) run a fused loop that
	// hoists the element-kind dispatch out of the per-element path and parses
	// each number straight into the backing array, replacing the generic
	// double-switch below. The element kind is loop-invariant, so the choice is
	// made once here; each branch matches the exact 64-bit width so a named
	// alias type is written through storage of the right size.
	switch elem := node.elem; elem.kind {
	case typedInt:
		if elem.bits == 64 && elem.size == 8 {
			return decodeCompiledInt64Slice(cursor, node, dst)
		}
	case typedUint:
		if elem.bits == 64 && elem.size == 8 {
			return decodeCompiledUint64Slice(cursor, node, dst)
		}
	case typedFloat:
		if elem.bits == 64 && elem.size == 8 {
			return decodeCompiledFloat64Slice(cursor, node, dst)
		}
	}
	for index, first := 0, true; ; index, first = index+1, false {
		more, err := cursor.NextArrayElement(first)
		if err != nil {
			return err
		}
		if !more {
			if index == 0 {
				// An empty array yields a fresh empty slice, dropping any
				// reused backing, exactly like encoding/json's MakeSlice(T,0,0).
				// Keeping the old array would leak stale elements when the same
				// destination later decodes a longer array into the reused cap.
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
			elementErr = cursor.decodeCompiledStruct(node.elem, element)
		case typedSlice:
			elementErr = cursor.decodeCompiledSlice(node.elem, element)
		case typedArray:
			elementErr = cursor.decodeCompiledArray(node.elem, element)
		case typedPointer:
			elementErr = cursor.decodeCompiledPointer(node.elem, element)
		case typedMap:
			elementErr = cursor.decodeCompiledMap(node.elem, element)
		default:
			elementErr = cursor.decodeCompiled(node.elem, element)
		}
		if elementErr != nil {
			return prependDecodePathIndex(elementErr, index)
		}
	}
}

// The fused scalar-slice decoders below (int64 / uint64 / float64) each replace
// the generic loop in decodeCompiledSlice for a homogeneous slice of that 64-bit
// scalar. The header has already been opened and its length zeroed by the
// caller; grow, empty-array, and error semantics are identical to the generic
// path. The gain is structural: the loop-invariant element kind is resolved
// once, so every element parses straight through the number method without the
// generic double-switch, and the common "value then comma" delimiter step is
// consumed inline so a full array of numbers never re-enters NextArrayElement.
//
// The delimiter-and-grow prologue is intentionally duplicated across the three
// rather than factored behind a helper that returns the element pointer: such a
// helper would trace the returned pointer back to dst and leak the whole
// destination to the heap, defeating the on-stack decode the generic path
// preserves. Keeping the element address a local of each loop matches the
// generic loop's escape profile exactly.

func decodeCompiledInt64Slice(cursor *decoderCursor, node *typedNode, dst unsafe.Pointer) error {
	header := (*typedSliceHeader)(dst)
	for index, first := 0, true; ; index, first = index+1, false {
		if !scalarSliceAdvance(cursor, first) {
			more, err := cursor.NextArrayElement(first)
			if err != nil {
				return err
			}
			if !more {
				if index == 0 {
					setTypedEmptySlice(node, dst)
				}
				return nil
			}
		}
		if index == header.cap {
			growTypedSlice(node, dst, nextTypedSliceCapacity(header.cap, index+1))
			header = (*typedSliceHeader)(dst)
		}
		header.len = index + 1
		element := (*int64)(unsafe.Add(header.data, uintptr(index)*node.elem.size))
		if err := cursor.Int(element); err != nil {
			return prependDecodePathIndex(retagCompiledError(err, node.elem.typ), index)
		}
	}
}

func decodeCompiledUint64Slice(cursor *decoderCursor, node *typedNode, dst unsafe.Pointer) error {
	header := (*typedSliceHeader)(dst)
	for index, first := 0, true; ; index, first = index+1, false {
		if !scalarSliceAdvance(cursor, first) {
			more, err := cursor.NextArrayElement(first)
			if err != nil {
				return err
			}
			if !more {
				if index == 0 {
					setTypedEmptySlice(node, dst)
				}
				return nil
			}
		}
		if index == header.cap {
			growTypedSlice(node, dst, nextTypedSliceCapacity(header.cap, index+1))
			header = (*typedSliceHeader)(dst)
		}
		header.len = index + 1
		element := (*uint64)(unsafe.Add(header.data, uintptr(index)*node.elem.size))
		if err := cursor.Uint(element); err != nil {
			return prependDecodePathIndex(retagCompiledError(err, node.elem.typ), index)
		}
	}
}

func decodeCompiledFloat64Slice(cursor *decoderCursor, node *typedNode, dst unsafe.Pointer) error {
	header := (*typedSliceHeader)(dst)
	for index, first := 0, true; ; index, first = index+1, false {
		if !scalarSliceAdvance(cursor, first) {
			more, err := cursor.NextArrayElement(first)
			if err != nil {
				return err
			}
			if !more {
				if index == 0 {
					setTypedEmptySlice(node, dst)
				}
				return nil
			}
		}
		if index == header.cap {
			growTypedSlice(node, dst, nextTypedSliceCapacity(header.cap, index+1))
			header = (*typedSliceHeader)(dst)
		}
		header.len = index + 1
		element := (*float64)(unsafe.Add(header.data, uintptr(index)*node.elem.size))
		if err := cursor.Float(element); err != nil {
			return prependDecodePathIndex(retagCompiledError(err, node.elem.typ), index)
		}
	}
}

// scalarSliceAdvance consumes the inline delimiter between two scalar elements:
// after the first element a comma advances to the next value. It reports whether
// it recognized and consumed a fast-path delimiter; the first element, a closing
// bracket, whitespace, and every malformed case return false so the caller
// re-enters NextArrayElement, which owns the slow scan and every error message.
// It takes no pointer into the destination, so it adds nothing to the decode's
// escape profile.
func scalarSliceAdvance(cursor *decoderCursor, first bool) bool {
	if first {
		return false
	}
	i := cursor.i
	if i >= len(cursor.src) || cursor.src[i] != ',' {
		return false
	}
	cursor.i = i + 1
	if cursor.i < len(cursor.src) && cursor.src[cursor.i] <= ' ' {
		cursor.skipSpace()
	}
	return true
}

func (cursor *decoderCursor) decodeCompiledArray(node *typedNode, dst unsafe.Pointer) error {
	replace := cursor.flags&decoderReplace != 0
	if i := cursor.i; i < len(cursor.src) && cursor.src[i] == '[' && cursor.depth < cursor.maxDepth {
		cursor.depth++
		cursor.i = i + 1
		if replace {
			resetTyped(node, dst)
		}
	} else {
		null := false
		if !cursor.notNullFast() {
			var err error
			null, err = cursor.TryNull()
			if err != nil {
				return err
			}
		}
		if null {
			// encoding/json treats null on an array as a no-op.
			if replace {
				resetTyped(node, dst)
			}
			return nil
		}
		if replace {
			resetTyped(node, dst)
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
			if !replace {
				zeroTypedArrayTail(node, dst, index)
			}
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
				elementErr = cursor.decodeCompiledStruct(node.elem, element)
			case typedSlice:
				elementErr = cursor.decodeCompiledSlice(node.elem, element)
			case typedArray:
				elementErr = cursor.decodeCompiledArray(node.elem, element)
			case typedPointer:
				elementErr = cursor.decodeCompiledPointer(node.elem, element)
			case typedMap:
				elementErr = cursor.decodeCompiledMap(node.elem, element)
			case typedAny:
				elementErr = cursor.decodeCompiledAny(element)
			case typedBytes:
				elementErr = cursor.decodeCompiledBytes(node.elem, element)
			case typedUnmarshalerJSON:
				elementErr = cursor.decodeViaUnmarshaler(node.elem, element)
			case typedUnmarshalerText:
				elementErr = cursor.decodeViaTextUnmarshaler(node.elem, element)
			case typedIface:
				elementErr = cursor.decodeCompiledIface(node.elem, element)
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
	replace := cursor.flags&decoderReplace != 0
	src := cursor.src
	// Straight-line coordinate-pair path: once elements are known to run
	// long, a compact [f,f] parses without the per-element loop machinery.
	if node.length == 2 && unsafe.Sizeof(T(0)) == 8 && cursor.floatLong {
		base := unsafe.Pointer(unsafe.SliceData(src))
		if i := cursor.i; i < len(src) && (src[i] == '-' || isDigit(src[i])) {
			end0, v0, exact0, ok0 := scanTypedFloat64(base, len(src), i)
			if ok0 && exact0 && end0 < len(src) && src[end0] == ',' &&
				end0+1 < len(src) && (src[end0+1] == '-' || isDigit(src[end0+1])) {
				end1, v1, exact1, ok1 := scanTypedFloat64(base, len(src), end0+1)
				if ok1 && exact1 && end1 < len(src) && src[end1] == ']' {
					*(*T)(dst) = T(v0)
					*(*T)(unsafe.Add(dst, node.elem.size)) = T(v1)
					cursor.i = end1 + 1
					cursor.depth--
					return nil
				}
			}
		}
	}
	for index, first := 0, true; ; index, first = index+1, false {
		// Fused fast path: consume the delimiter and a short float without the
		// general element iterator. cursor.i stays untouched until the whole
		// element is accepted, so every partial match falls through cleanly.
		i := cursor.i
		if i < len(src) && src[i] <= ' ' {
			i = skipSpaceIndent(src, i)
		}
		if i < len(src) && index < node.length {
			c := src[i]
			if c == ']' {
				cursor.i = i + 1
				cursor.depth--
				if !replace {
					zeroTypedArrayTail(node, dst, index)
				}
				return nil
			}
			if !first {
				if c != ',' {
					goto general
				}
				i++
				if i < len(src) && src[i] <= ' ' {
					i = skipSpaceIndent(src, i)
				}
			}
			if i < len(src) && (src[i] == '-' || isDigit(src[i])) {
				if !cursor.floatLong {
					if value, end, ok := shortTypedFloatAt(src, i); ok {
						*(*T)(unsafe.Add(dst, uintptr(index)*node.elem.size)) = T(value)
						cursor.i = end
						continue
					}
					// Short probes fail on every element of uniformly long
					// arrays; stop paying for them until a short element
					// reappears.
					cursor.floatLong = true
				}
				if unsafe.Sizeof(T(0)) == 8 {
					base := unsafe.Pointer(unsafe.SliceData(src))
					end, value, exact, ok := scanTypedFloat64(base, len(src), i)
					if ok && exact {
						*(*T)(unsafe.Add(dst, uintptr(index)*node.elem.size)) = T(value)
						cursor.i = end
						cursor.floatLong = end-i >= 6
						continue
					}
				}
			}
		}
	general:
		more, err := cursor.NextArrayElement(first)
		if err != nil {
			return err
		}
		if !more {
			if !replace {
				zeroTypedArrayTail(node, dst, index)
			}
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

// zeroTypedArrayTail zeroes fixed-array elements past the document's length,
// matching encoding/json's array padding in merge mode.
func zeroTypedArrayTail(node *typedNode, dst unsafe.Pointer, from int) {
	for index := from; index < node.length; index++ {
		resetTyped(node.elem, unsafe.Add(dst, uintptr(index)*node.elem.size))
	}
}

func (cursor *decoderCursor) decodeCompiledPointer(node *typedNode, dst unsafe.Pointer) error {
	null := false
	if !cursor.notNullFast() {
		var err error
		null, err = cursor.TryNull()
		if err != nil {
			return err
		}
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
		return cursor.decodeCompiledStruct(node.elem, pointer)
	case typedSlice:
		return cursor.decodeCompiledSlice(node.elem, pointer)
	case typedArray:
		return cursor.decodeCompiledArray(node.elem, pointer)
	case typedPointer:
		return cursor.decodeCompiledPointer(node.elem, pointer)
	case typedMap:
		return cursor.decodeCompiledMap(node.elem, pointer)
	default:
		return cursor.decodeCompiled(node.elem, pointer)
	}
}

// inlineDecoder decodes a run of unknown members into one struct's ",inline"
// catch-all map. It mirrors decodeCompiledMap: a single element and a single
// key value are allocated once and reused for every member, so a document with
// N unknown members costs one element allocation rather than N.
//
// The reused values are heap backed (reflect.New), never pointers into the
// destination struct, so this decoder is safe to keep on the heap while the
// destination lives on a stack that may move. The map header is re-derived from
// structPtr on each call as a stack local for the same reason.
type inlineDecoder struct {
	keyValue     reflect.Value
	elementValue reflect.Value
	elementPtr   unsafe.Pointer
}

// newInlineDecoder builds the reusable state for a catch-all map. It is called
// at most once per struct decode, on the first unknown member.
func newInlineDecoder(inline *typedInlineMap) *inlineDecoder {
	element := reflect.New(inline.elem.typ)
	return &inlineDecoder{
		keyValue:     reflect.New(inline.mapType.Key()).Elem(),
		elementValue: element.Elem(),
		elementPtr:   element.UnsafePointer(),
	}
}

// decodeEntry decodes one member into the catch-all, allocating the map on
// first use. The member name becomes the key, cloned in owned mode so the
// retained key does not alias the source; the value follows the cursor's
// ownership rules like any other decode. SetMapIndex copies both key and value
// into the map, so reusing the source values across members is safe.
func (d *inlineDecoder) decodeEntry(cursor *decoderCursor, inline *typedInlineMap, structPtr unsafe.Pointer, key string) error {
	mapValue := reflect.NewAt(inline.mapType, noescape(unsafe.Add(structPtr, inline.offset))).Elem()
	if mapValue.IsNil() {
		mapValue.Set(reflect.MakeMap(inline.mapType))
	}
	d.elementValue.SetZero()
	var err error
	switch inline.elem.kind {
	case typedStruct:
		err = cursor.decodeCompiledStruct(inline.elem, d.elementPtr)
	case typedSlice:
		err = cursor.decodeCompiledSlice(inline.elem, d.elementPtr)
	case typedArray:
		err = cursor.decodeCompiledArray(inline.elem, d.elementPtr)
	case typedPointer:
		err = cursor.decodeCompiledPointer(inline.elem, d.elementPtr)
	case typedMap:
		err = cursor.decodeCompiledMap(inline.elem, d.elementPtr)
	default:
		err = cursor.decodeCompiled(inline.elem, d.elementPtr)
	}
	if err != nil {
		return err
	}
	if cursor.flags&decoderZeroCopy == 0 {
		key = strings.Clone(key)
	}
	d.keyValue.SetString(key)
	mapValue.SetMapIndex(d.keyValue, d.elementValue)
	return nil
}

// decodeCompiledMap decodes a JSON object into a map with string keys. Like
// encoding/json it allocates a map only when dst holds nil and otherwise
// merges into the existing entries. Entries decode through one reusable
// element that is zeroed between entries, so nested slice capacity is never
// shared between values.
func (cursor *decoderCursor) decodeCompiledMap(node *typedNode, dst unsafe.Pointer) error {
	null := false
	if !cursor.notNullFast() {
		var err error
		null, err = cursor.TryNull()
		if err != nil {
			return err
		}
	}
	if null {
		reflect.NewAt(node.typ, noescape(dst)).Elem().SetZero()
		return nil
	}
	if err := cursor.BeginObject(node.name); err != nil {
		return err
	}
	// Map keys are retained by the result, so switch owned decodes to the
	// private input copy before the first key string is sliced.
	cursor.ownSource()
	mapValue := reflect.NewAt(node.typ, noescape(dst)).Elem()
	if cursor.flags&decoderReplace != 0 && !mapValue.IsNil() {
		// Replace decodes as if into a fresh destination, so a reused map drops
		// keys the document does not set instead of merging into them.
		mapValue.Clear()
	}
	if mapValue.IsNil() {
		mapValue.Set(reflect.MakeMap(node.typ))
	}
	keyType := node.typ.Key()
	element := reflect.New(node.elem.typ)
	elementPtr := element.UnsafePointer()
	elementValue := element.Elem()
	// One reusable key box serves every entry: SetMapIndex copies the key into
	// the map, so the box is reset per entry instead of allocating one each
	// time. The text unmarshaler, when present, is bound to the box once.
	keyValue := reflect.New(keyType).Elem()
	var keyUnmarshaler encoding.TextUnmarshaler
	if node.mapKeyTextDecode {
		keyUnmarshaler = keyValue.Addr().Interface().(encoding.TextUnmarshaler)
	}
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
			entryErr = cursor.decodeCompiledStruct(node.elem, elementPtr)
		case typedSlice:
			entryErr = cursor.decodeCompiledSlice(node.elem, elementPtr)
		case typedArray:
			entryErr = cursor.decodeCompiledArray(node.elem, elementPtr)
		case typedPointer:
			entryErr = cursor.decodeCompiledPointer(node.elem, elementPtr)
		case typedMap:
			entryErr = cursor.decodeCompiledMap(node.elem, elementPtr)
		default:
			entryErr = cursor.decodeCompiled(node.elem, elementPtr)
		}
		if entryErr != nil {
			return prependDecodePathField(entryErr, key)
		}
		if keyErr := setMapKeyValue(keyValue, keyUnmarshaler, node, keyType, key); keyErr != nil {
			return prependDecodePathField(&DecodeError{Offset: cursor.i, Type: keyType, Reason: keyErr.Error()}, key)
		}
		mapValue.SetMapIndex(keyValue, elementValue)
	}
}

// setMapKeyValue decodes a member name into the reused key box in place,
// following encoding/json: text unmarshalers first, then string kinds, then
// base-10 integers with range checks. The box is copied into the map by
// SetMapIndex, so it may be reused across entries; the text path zeroes it
// first to match a freshly allocated key.
func setMapKeyValue(keyValue reflect.Value, unmarshaler encoding.TextUnmarshaler, node *typedNode, keyType reflect.Type, key string) error {
	if node.mapKeyTextDecode {
		keyValue.SetZero()
		return unmarshaler.UnmarshalText([]byte(key))
	}
	switch node.mapKeyKind {
	case mapKeyString:
		keyValue.SetString(key)
		return nil
	case mapKeyInt:
		parsed, err := strconv.ParseInt(key, 10, 64)
		if err != nil || keyValue.OverflowInt(parsed) {
			return errors.New("cannot parse map key as " + keyType.String())
		}
		keyValue.SetInt(parsed)
		return nil
	case mapKeyUint:
		parsed, err := strconv.ParseUint(key, 10, 64)
		if err != nil || keyValue.OverflowUint(parsed) {
			return errors.New("cannot parse map key as " + keyType.String())
		}
		keyValue.SetUint(parsed)
		return nil
	default:
		return errors.New("map key type " + keyType.String() + " cannot be decoded")
	}
}

// decodeCompiledAny decodes one JSON value into an empty interface using the
// standard dynamic shapes: map[string]any, []any, string, float64, bool, and
// nil. Like encoding/json, an interface already holding a non-nil pointer is
// decoded into rather than replaced; anything else is replaced, and null
// clears the interface.
func (cursor *decoderCursor) decodeCompiledAny(dst unsafe.Pointer) error {
	if existing := *(*any)(dst); existing != nil {
		null := false
		if !cursor.notNullFast() {
			var err error
			null, err = cursor.TryNull()
			if err != nil {
				return err
			}
		}
		if null {
			*(*any)(dst) = nil
			return nil
		}
		existingValue := reflect.ValueOf(existing)
		if existingValue.Kind() == reflect.Pointer && !existingValue.IsNil() {
			inner, err := dynamicDecodeNode(existingValue.Type().Elem())
			if err != nil {
				return &DecodeError{Offset: cursor.i, Type: existingValue.Type(), Reason: err.Error()}
			}
			return cursor.decodeCompiled(inner, existingValue.UnsafePointer())
		}
	}
	// Dynamic strings are retained by the result, so owned decodes switch to
	// the private input copy first.
	cursor.ownSource()
	p := cursor.slowParser()
	p.skipSpace()
	value, err := p.parseAnyValue(cursor.depth, false)
	cursor.i = p.i
	// The dynamic tree retains any escaped strings it materialized in the
	// arena; advancing the arena keeps later escaped strings from
	// overwriting them.
	cursor.adoptStringArena(p.strings)
	if err != nil {
		return err
	}
	*(*any)(dst) = value
	return nil
}

// dynamicDecodeNodes caches one compiled decode plan per concrete type found
// inside an interface value.
var dynamicDecodeNodes sync.Map

type dynamicDecodeEntry struct {
	node *typedNode
	err  error
}

func dynamicDecodeNode(typ reflect.Type) (*typedNode, error) {
	if entry, ok := dynamicDecodeNodes.Load(typ); ok {
		cached := entry.(*dynamicDecodeEntry)
		return cached.node, cached.err
	}
	compiler := typedCompiler{nodes: make(map[reflect.Type]*typedNode)}
	node, err := compiler.compile(typ, typ.String())
	if err == nil {
		prepareTypedResets(node, make(map[*typedNode]bool))
	}
	entry, _ := dynamicDecodeNodes.LoadOrStore(typ, &dynamicDecodeEntry{node: node, err: err})
	cached := entry.(*dynamicDecodeEntry)
	return cached.node, cached.err
}

// decodeCompiledIface decodes into a non-empty interface: null clears it,
// and a held non-nil pointer is decoded into like encoding/json; any other
// state cannot be decoded.
func (cursor *decoderCursor) decodeCompiledIface(node *typedNode, dst unsafe.Pointer) error {
	null := false
	if !cursor.notNullFast() {
		var err error
		null, err = cursor.TryNull()
		if err != nil {
			return err
		}
	}
	if null {
		reflect.NewAt(node.typ, noescape(dst)).Elem().SetZero()
		return nil
	}
	value := reflect.NewAt(node.typ, noescape(dst)).Elem()
	if !value.IsNil() {
		concrete := value.Elem()
		if concrete.Kind() == reflect.Pointer && !concrete.IsNil() {
			inner, innerErr := dynamicDecodeNode(concrete.Type().Elem())
			if innerErr != nil {
				return &DecodeError{Offset: cursor.i, Type: concrete.Type(), Reason: innerErr.Error()}
			}
			return cursor.decodeCompiled(inner, concrete.UnsafePointer())
		}
	}
	return &DecodeError{Offset: cursor.i, Type: node.typ, Reason: "cannot decode into a non-empty interface"}
}

// decodeQuotedField decodes a scalar tagged with the string option: the JSON
// value is a string whose contents are one scalar, parsed with encoding/json's
// semantics. A bare null clears pointer fields and resets values only in
// replace mode; anything but a string or null is rejected.
func (cursor *decoderCursor) decodeQuotedField(node *typedNode, dst unsafe.Pointer) error {
	null := false
	if !cursor.notNullFast() {
		var err error
		null, err = cursor.TryNull()
		if err != nil {
			return err
		}
	}
	if null {
		if node.baseKind == typedPointer || cursor.flags&decoderReplace != 0 {
			resetTyped(node, dst)
		}
		return nil
	}
	i := cursor.i
	if i >= len(cursor.src) || cursor.src[i] != '"' {
		return &DecodeError{Offset: i, Type: node.typ, Reason: "expected quoted value for string-tagged field"}
	}
	inner, err := cursor.stringToken()
	if err != nil {
		return err
	}
	// The inner scalar may alias a temporary unescape buffer, so decoded
	// strings must never alias it.
	flags := cursor.flags &^ (decoderZeroCopy | decoderSourceOwned)
	sub := decoderCursor{src: inner, maxDepth: cursor.maxDepth, flags: flags}
	scalar := node
	if scalar.kind == typedPointer {
		scalar = scalar.elem
	}
	switch scalar.kind {
	case typedInt, typedUint, typedFloat:
		return decodeQuotedNumber(node, scalar, dst, inner, i)
	}
	if scalar.kind == typedString {
		// The contents must themselves be a JSON string.
		if len(inner) == 0 || inner[0] != '"' {
			return &DecodeError{Offset: i, Type: node.typ, Reason: "string-tagged field does not contain a JSON string"}
		}
	}
	if err := sub.decodeCompiled(node, dst); err != nil {
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

// decodeQuotedNumber stores a string-tagged number with encoding/json's
// semantics: the quoted contents are handed to strconv verbatim, which
// accepts spellings strict JSON does not (leading zeros, an explicit plus,
// and strconv's float forms).
func decodeQuotedNumber(node, scalar *typedNode, dst unsafe.Pointer, inner []byte, offset int) error {
	text := unsafe.String(unsafe.SliceData(inner), len(inner))
	if text == "null" {
		// encoding/json treats a quoted null like the bare literal: value
		// fields are left untouched and pointer fields are cleared.
		if node.kind == typedPointer {
			*(*unsafe.Pointer)(dst) = nil
		}
		return nil
	}
	scalarDst := dst
	if node.kind == typedPointer {
		pointer := *(*unsafe.Pointer)(dst)
		if pointer == nil {
			pointer = allocateTypedPointer(node, dst)
		}
		scalarDst = pointer
	}
	switch scalar.kind {
	case typedInt:
		value, err := strconv.ParseInt(text, 10, int(scalar.bits))
		if err != nil {
			return &DecodeError{Offset: offset, Type: node.typ, Reason: "cannot parse string-tagged integer " + strconv.Quote(text)}
		}
		switch scalar.bits {
		case 8:
			*(*int8)(scalarDst) = int8(value)
		case 16:
			*(*int16)(scalarDst) = int16(value)
		case 32:
			*(*int32)(scalarDst) = int32(value)
		default:
			*(*int64)(scalarDst) = value
		}
	case typedUint:
		value, err := strconv.ParseUint(text, 10, int(scalar.bits))
		if err != nil {
			return &DecodeError{Offset: offset, Type: node.typ, Reason: "cannot parse string-tagged integer " + strconv.Quote(text)}
		}
		switch scalar.bits {
		case 8:
			*(*uint8)(scalarDst) = uint8(value)
		case 16:
			*(*uint16)(scalarDst) = uint16(value)
		case 32:
			*(*uint32)(scalarDst) = uint32(value)
		default:
			*(*uint64)(scalarDst) = value
		}
	default:
		value, err := strconv.ParseFloat(text, int(scalar.bits))
		if err != nil {
			return &DecodeError{Offset: offset, Type: node.typ, Reason: "cannot parse string-tagged number " + strconv.Quote(text)}
		}
		if scalar.bits == 32 {
			*(*float32)(scalarDst) = float32(value)
		} else {
			*(*float64)(scalarDst) = value
		}
	}
	return nil
}

// decodeCompiledBytes decodes a base64 JSON string into a byte slice,
// reusing destination capacity when possible.
func (cursor *decoderCursor) decodeCompiledBytes(node *typedNode, dst unsafe.Pointer) error {
	null := false
	if !cursor.notNullFast() {
		var err error
		null, err = cursor.TryNull()
		if err != nil {
			return err
		}
	}
	header := (*typedSliceHeader)(dst)
	if null {
		*header = typedSliceHeader{}
		return nil
	}
	i := cursor.i
	if i < len(cursor.src) && cursor.src[i] == '[' {
		// encoding/json also decodes a byte slice from an array of
		// integers, one element per byte.
		return cursor.decodeBytesArray(node, dst)
	}
	if i >= len(cursor.src) || cursor.src[i] != '"' {
		return &DecodeError{Offset: i, Type: node.typ, Reason: "expected base64 string"}
	}
	encoded, err := cursor.stringToken()
	if err != nil {
		return err
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

// decodeBytesArray decodes the array form of []byte accepted by
// encoding/json: a JSON array of integers, one per byte, reusing destination
// capacity like every other slice decode.
func (cursor *decoderCursor) decodeBytesArray(node *typedNode, dst unsafe.Pointer) error {
	if err := cursor.BeginArray(node.name); err != nil {
		return err
	}
	header := (*typedSliceHeader)(dst)
	var buf []byte
	if header.data != nil {
		buf = unsafe.Slice((*byte)(header.data), header.cap)[:0]
	}
	for first := true; ; first = false {
		more, err := cursor.NextArrayElement(first)
		if err != nil {
			return err
		}
		if !more {
			break
		}
		var element uint8
		if err := cursor.Uint(&element); err != nil {
			return retagCompiledError(err, node.typ)
		}
		buf = append(buf, element)
	}
	if buf == nil {
		buf = make([]byte, 0)
	}
	header.data = unsafe.Pointer(unsafe.SliceData(buf))
	header.len = len(buf)
	header.cap = cap(buf)
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
			// Only raw pointers were stored; pin the allocation until the
			// slot write is visible to the collector.
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
