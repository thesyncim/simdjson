package simdjson

import (
	"encoding"
	"encoding/json"
	"reflect"
	"runtime"
	"unsafe"
)

// interfaceAt builds *T as an interface without letting dst escape: reflect
// only ever sees the laundered pointer, and the caller's frame keeps the
// memory alive for the duration of the call.
func interfaceAt(typ reflect.Type, p unsafe.Pointer) any {
	return reflect.NewAt(typ, noescape(p)).Interface()
}

// decodeViaUnmarshaler feeds the raw bytes of the next JSON value to the
// destination's UnmarshalJSON, exactly one validated value, null included.
func decodeViaUnmarshaler(cursor *decoderCursor, node *typedNode, dst unsafe.Pointer) error {
	start := cursor.i
	if err := cursor.Skip(); err != nil {
		return err
	}
	raw := cursor.src[start:cursor.i]

	// A pointer-kind receiver treats null as assignment like encoding/json.
	if node.typ.Kind() == reflect.Pointer && len(raw) > 0 && raw[0] == 'n' {
		*(*unsafe.Pointer)(dst) = nil
		return nil
	}
	unmarshaler, ok := receiverAt(node.typ, dst).(json.Unmarshaler)
	if !ok {
		return &DecodeError{Offset: start, Type: node.typ, Reason: "invalid compiled operation"}
	}
	if err := unmarshaler.UnmarshalJSON(raw); err != nil {
		return &DecodeError{Offset: start, Type: node.typ, Reason: err.Error()}
	}
	return nil
}

// interfaceAtPointer boxes an already-loaded pointer value of type typ.
func interfaceAtPointer(typ reflect.Type, pointer unsafe.Pointer) any {
	local := pointer
	return reflect.NewAt(typ, unsafe.Pointer(&local)).Elem().Interface()
}

// receiverAt boxes the un/marshaler receiver for the value at dst: pointer
// kinds are loaded and allocated on demand, other kinds pass their address.
func receiverAt(typ reflect.Type, dst unsafe.Pointer) any {
	if typ.Kind() != reflect.Pointer {
		return interfaceAt(typ, dst)
	}
	pointer := *(*unsafe.Pointer)(dst)
	if pointer == nil {
		value := reflect.New(typ.Elem())
		pointer = value.UnsafePointer()
		*(*unsafe.Pointer)(dst) = pointer
		runtime.KeepAlive(value)
	}
	return interfaceAtPointer(typ, pointer)
}

// decodeViaTextUnmarshaler decodes a JSON string through UnmarshalText.
// Null leaves non-pointer values untouched and nils pointers, like
// encoding/json.
func decodeViaTextUnmarshaler(cursor *decoderCursor, node *typedNode, dst unsafe.Pointer) error {
	null, err := cursor.TryNull()
	if err != nil {
		return err
	}
	if null {
		if node.typ.Kind() == reflect.Pointer {
			*(*unsafe.Pointer)(dst) = nil
		}
		return nil
	}
	start := cursor.i
	if start >= len(cursor.src) || cursor.src[start] != '"' {
		return &DecodeError{Offset: start, Type: node.typ, Reason: "expected string for TextUnmarshaler"}
	}
	text, err := cursor.stringToken()
	if err != nil {
		return err
	}

	unmarshaler, ok := receiverAt(node.typ, dst).(encoding.TextUnmarshaler)
	if !ok {
		return &DecodeError{Offset: start, Type: node.typ, Reason: "invalid compiled operation"}
	}
	if err := unmarshaler.UnmarshalText(text); err != nil {
		return &DecodeError{Offset: start, Type: node.typ, Reason: err.Error()}
	}
	return nil
}

// encodeMarshaler dispatches MarshalJSON or MarshalText, compacting and
// re-escaping custom JSON exactly like encoding/json.
func (e *encodeState) encodeMarshaler(node *typedNode, src unsafe.Pointer) error {
	var boxed any
	if node.typ.Kind() == reflect.Pointer {
		pointer := *(*unsafe.Pointer)(src)
		if pointer == nil {
			e.dst = append(e.dst, "null"...)
			return nil
		}
		boxed = interfaceAtPointer(node.typ, pointer)
	} else {
		boxed = interfaceAt(node.typ, src)
	}

	if node.encKind == typedMarshalerJSON {
		marshaler, ok := boxed.(json.Marshaler)
		if !ok {
			return &EncodeError{Reason: "invalid compiled operation"}
		}
		data, err := marshaler.MarshalJSON()
		if err != nil {
			return &EncodeError{Reason: "MarshalJSON: " + err.Error()}
		}
		compacted, err := AppendCompact(e.dst, data)
		if err != nil {
			return &EncodeError{Reason: "MarshalJSON produced invalid JSON: " + err.Error()}
		}
		if e.escapeHTML {
			compacted = escapeHTMLInPlaceTail(compacted, len(e.dst))
		}
		e.dst = compacted
		return nil
	}
	marshaler, ok := boxed.(encoding.TextMarshaler)
	if !ok {
		return &EncodeError{Reason: "invalid compiled operation"}
	}
	text, err := marshaler.MarshalText()
	if err != nil {
		return &EncodeError{Reason: "MarshalText: " + err.Error()}
	}
	e.dst = appendEncodedJSONString(e.dst, string(text), e.escapeHTML)
	return nil
}

// escapeHTMLInPlaceTail rewrites buf[from:] escaping <, >, &, U+2028, and
// U+2029, which in valid compact JSON only occur inside strings, matching
// encoding/json's compact(escape=true).
func escapeHTMLInPlaceTail(buf []byte, from int) []byte {
	tail := buf[from:]
	needsEscape := false
	for i := 0; i < len(tail); i++ {
		c := tail[i]
		if c == '<' || c == '>' || c == '&' {
			needsEscape = true
			break
		}
		if c == 0xE2 && i+2 < len(tail) && tail[i+1] == 0x80 && (tail[i+2] == 0xA8 || tail[i+2] == 0xA9) {
			needsEscape = true
			break
		}
	}
	if !needsEscape {
		return buf
	}
	escaped := make([]byte, 0, len(tail)+16)
	for i := 0; i < len(tail); i++ {
		c := tail[i]
		switch {
		case c == '<' || c == '>' || c == '&':
			escaped = append(escaped, '\\', 'u', '0', '0', encodeHexDigits[c>>4], encodeHexDigits[c&0xF])
		case c == 0xE2 && i+2 < len(tail) && tail[i+1] == 0x80 && (tail[i+2] == 0xA8 || tail[i+2] == 0xA9):
			escaped = append(escaped, '\\', 'u', '2', '0', '2', encodeHexDigits[tail[i+2]&0xF])
			i += 2
		default:
			escaped = append(escaped, c)
		}
	}
	return append(buf[:from], escaped...)
}
