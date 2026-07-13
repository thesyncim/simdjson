package simdjson

import (
	"encoding"
	"encoding/json"
	"reflect"
	"runtime"
	"sync"
	"time"
	"unsafe"
)

type encoderMarshalerScratch struct {
	value reflect.Value
	boxed any
}

type encoderScratch struct {
	marshalers []encoderMarshalerScratch
	// mapEntries and mapKeyArena are reused by encodeMap so sorted map
	// encoding does not allocate per map. Ownership transfers to one
	// encodeMap call at a time; nested maps fall back to fresh slices.
	mapEntries  []mapEncodeEntry
	mapKeyArena []byte
}

func (s *encoderScratch) reset() {
	for i := range s.marshalers {
		s.marshalers[i].value.SetZero()
	}
	// Entries hold key strings and reflect values from the encoded maps;
	// drop those references before the scratch returns to the pool.
	clear(s.mapEntries[:cap(s.mapEntries)])
}

func newEncoderScratchPool(types []reflect.Type, hasMap bool) *sync.Pool {
	if len(types) == 0 && !hasMap {
		return nil
	}
	types = append([]reflect.Type(nil), types...)
	return &sync.Pool{New: func() any {
		scratch := &encoderScratch{marshalers: make([]encoderMarshalerScratch, len(types))}
		for i, typ := range types {
			value := reflect.New(typ)
			scratch.marshalers[i] = encoderMarshalerScratch{value: value.Elem(), boxed: value.Interface()}
		}
		return scratch
	}}
}

// valueInterfaceAt copies T into an interface. Calling a value-receiver method
// on that interface cannot expose p to user code.
func valueInterfaceAt(typ reflect.Type, p unsafe.Pointer) any {
	return reflect.NewAt(typ, noescape(p)).Elem().Interface()
}

// copiedPointerReceiverAt creates a shallow, heap-backed *T for a call into
// user code. Pointer-receiver methods may legally retain their receiver;
// passing p through noescape would leave them holding a stale stack pointer
// after stack growth.
func copiedPointerReceiverAt(typ reflect.Type, p unsafe.Pointer) (any, reflect.Value) {
	shadow := reflect.New(typ)
	shadow.Elem().Set(reflect.NewAt(typ, noescape(p)).Elem())
	return shadow.Interface(), shadow
}

func copiedLoadedPointerReceiverAt(typ reflect.Type, p unsafe.Pointer) (any, reflect.Value) {
	shadow := reflect.New(typ.Elem())
	shadow.Elem().Set(reflect.NewAt(typ.Elem(), noescape(p)).Elem())
	return shadow.Interface(), shadow
}

func copyMethodReceiverBack(typ reflect.Type, dst unsafe.Pointer, shadow reflect.Value) {
	if !shadow.IsValid() {
		return
	}
	if typ.Kind() == reflect.Pointer {
		pointer := *(*unsafe.Pointer)(dst)
		if pointer != nil {
			reflect.NewAt(typ.Elem(), noescape(pointer)).Elem().Set(shadow.Elem())
		}
		return
	}
	reflect.NewAt(typ, noescape(dst)).Elem().Set(shadow.Elem())
}

// decodeViaUnmarshaler feeds the raw bytes of the next JSON value to the
// destination's UnmarshalJSON, exactly one validated value, null included.
func decodeViaUnmarshaler(cursor *decoderCursor, node *typedNode, dst unsafe.Pointer) error {
	start := cursor.i
	if err := cursor.Skip(); err != nil {
		return err
	}
	raw := cursor.src[start:cursor.i]
	if node.typ == timeReflectType {
		if err := (*time.Time)(dst).UnmarshalJSON(raw); err != nil {
			return &DecodeError{Offset: start, Type: node.typ, Reason: err.Error()}
		}
		return nil
	}

	// A pointer-kind receiver treats null as assignment like encoding/json.
	if node.typ.Kind() == reflect.Pointer && len(raw) > 0 && raw[0] == 'n' {
		*(*unsafe.Pointer)(dst) = nil
		return nil
	}
	receiver, shadow := receiverAt(node.typ, dst)
	unmarshaler, ok := receiver.(json.Unmarshaler)
	if !ok {
		return &DecodeError{Offset: start, Type: node.typ, Reason: "invalid compiled operation"}
	}
	err := unmarshaler.UnmarshalJSON(raw)
	copyMethodReceiverBack(node.typ, dst, shadow)
	if err != nil {
		return &DecodeError{Offset: start, Type: node.typ, Reason: err.Error()}
	}
	return nil
}

// receiverAt boxes the un/marshaler receiver for the value at dst: pointer
// kinds are loaded and allocated on demand, other kinds pass their address.
func receiverAt(typ reflect.Type, dst unsafe.Pointer) (any, reflect.Value) {
	if typ.Kind() != reflect.Pointer {
		return copiedPointerReceiverAt(typ, dst)
	}
	pointer := *(*unsafe.Pointer)(dst)
	if pointer == nil {
		value := reflect.New(typ.Elem())
		pointer = value.UnsafePointer()
		*(*unsafe.Pointer)(dst) = pointer
		boxed, shadow := copiedLoadedPointerReceiverAt(typ, pointer)
		runtime.KeepAlive(value)
		return boxed, shadow
	}
	return copiedLoadedPointerReceiverAt(typ, pointer)
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

	receiver, shadow := receiverAt(node.typ, dst)
	unmarshaler, ok := receiver.(encoding.TextUnmarshaler)
	if !ok {
		return &DecodeError{Offset: start, Type: node.typ, Reason: "invalid compiled operation"}
	}
	err = unmarshaler.UnmarshalText(text)
	copyMethodReceiverBack(node.typ, dst, shadow)
	if err != nil {
		return &DecodeError{Offset: start, Type: node.typ, Reason: err.Error()}
	}
	return nil
}

// encodeMarshaler dispatches MarshalJSON or MarshalText, compacting and
// re-escaping custom JSON exactly like encoding/json.
func (e *encodeState) encodeMarshaler(node *typedNode, src unsafe.Pointer) error {
	if node.encKind == typedTime {
		return e.encodeTime(src)
	}
	return e.encodeMarshalerKind(node, src, node.encKind)
}

func (e *encodeState) encodeMarshalerKind(node *typedNode, src unsafe.Pointer, kind typedKind) error {
	var boxed any
	var shadow reflect.Value
	if node.typ.Kind() == reflect.Pointer {
		pointer := *(*unsafe.Pointer)(src)
		if pointer == nil {
			e.dst = append(e.dst, "null"...)
			return nil
		}
		boxed, shadow = copiedLoadedPointerReceiverAt(node.typ, pointer)
	} else if (kind == typedMarshalerJSON && node.typ.Implements(jsonMarshalerReflectType)) ||
		(kind == typedMarshalerText && node.typ.Implements(textMarshalerReflectType)) {
		if e.scratch == nil || node.encScratch < 0 {
			boxed = valueInterfaceAt(node.typ, src)
		} else {
			slot := &e.scratch.marshalers[node.encScratch]
			slot.value.Set(reflect.NewAt(node.typ, noescape(src)).Elem())
			boxed = slot.boxed
		}
	} else {
		boxed, shadow = copiedPointerReceiverAt(node.typ, src)
	}

	if kind == typedMarshalerJSON {
		marshaler, ok := boxed.(json.Marshaler)
		if !ok {
			return &EncodeError{Reason: "invalid compiled operation"}
		}
		data, err := marshaler.MarshalJSON()
		copyMethodReceiverBack(node.typ, src, shadow)
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
	copyMethodReceiverBack(node.typ, src, shadow)
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
