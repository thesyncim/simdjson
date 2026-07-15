package simdjson

import (
	"strconv"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"
)

// Node is a lightweight handle into an Index. Like the Index, it aliases
// the source document and the entry storage, and is valid only while both
// stay alive and unmodified.
type Node struct {
	src   *byte
	entry *IndexEntry
}

func (v Node) valid() bool {
	return v.entry != nil
}

// Kind returns the JSON kind of v.
func (v Node) Kind() Kind {
	if !v.valid() {
		return Invalid
	}
	return v.entry.kind
}

// Raw returns v's exact source range.
func (v Node) Raw() RawValue {
	if !v.valid() {
		return RawValue{}
	}
	e := v.entry
	return RawValue{src: tapeSourceBytes(v.src, e.start, e.end)}
}

// IsNull reports whether v is null.
func (v Node) IsNull() bool {
	return v.Kind() == Null
}

// Bool returns v as a boolean.
func (v Node) Bool() (bool, bool) {
	if v.Kind() != Bool {
		return false, false
	}
	return *(*byte)(unsafe.Add(unsafe.Pointer(v.src), uintptr(v.entry.start))) == 't', true
}

// NumberBytes returns the original number spelling without revalidating it.
func (v Node) NumberBytes() ([]byte, bool) {
	if v.Kind() != Number {
		return nil, false
	}
	e := v.entry
	return tapeSourceBytes(v.src, e.start, e.end), true
}

// NumberText returns an allocation-free string alias of the source number.
func (v Node) NumberText() (string, bool) {
	b, ok := v.NumberBytes()
	if !ok {
		return "", false
	}
	return ownedBytesString(b), true
}

// Int64 parses an integer value.
func (v Node) Int64() (int64, bool) {
	s, ok := v.NumberText()
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil
}

// Float64 parses a number value as float64.
func (v Node) Float64() (float64, bool) {
	s, ok := v.NumberText()
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseFloat(s, 64)
	return n, err == nil
}

// StringBytes returns an unescaped string as a source alias. Escaped strings
// return false; use AppendString for those.
func (v Node) StringBytes() ([]byte, bool) {
	if v.Kind() != String {
		return nil, false
	}
	e := v.entry
	if e.flags&tapeFlagEscaped != 0 {
		return nil, false
	}
	return tapeSourceBytes(v.src, e.start+1, e.end-1), true
}

// AppendString appends v's decoded string to dst.
func (v Node) AppendString(dst []byte) ([]byte, bool) {
	if v.Kind() != String {
		return dst, false
	}
	e := v.entry
	raw := tapeSourceBytes(v.src, e.start+1, e.end-1)
	if e.flags&tapeFlagEscaped == 0 {
		return append(dst, raw...), true
	}
	return appendDecodedJSONString(dst, raw), true
}

// ArrayLen returns the number of array elements.
func (v Node) ArrayLen() (int, bool) {
	if v.Kind() != Array {
		return 0, false
	}
	return int(v.entry.count), true
}

// ObjectLen returns the number of object members.
func (v Node) ObjectLen() (int, bool) {
	if v.Kind() != Object {
		return 0, false
	}
	return int(v.entry.count), true
}

// Index returns the ith array element.
func (v Node) Index(index int) (Node, bool) {
	count, ok := v.ArrayLen()
	if !ok || index < 0 || index >= count {
		return Node{}, false
	}
	entry := tapeEntryOffset(v.entry, 1)
	for range index {
		entry = tapeEntryOffset(entry, uintptr(entry.next))
	}
	return Node{src: v.src, entry: entry}, true
}

// Get returns the last object member with key.
func (v Node) Get(key string) (Node, bool) {
	count, ok := v.ObjectLen()
	if !ok || count == 0 {
		// The empty check also keeps the entry arithmetic below inside the
		// tape: an empty object can be its final entry.
		return Node{}, false
	}
	keyEntry := tapeEntryOffset(v.entry, 1)
	var found *IndexEntry
	for member := 0; member < count; member++ {
		valueEntry := tapeEntryOffset(keyEntry, 1)
		if tapeKeyEqual(tapeSourceBytes(v.src, keyEntry.start, keyEntry.end), keyEntry.flags, key) {
			found = valueEntry
		}
		if member+1 < count {
			keyEntry = tapeEntryOffset(valueEntry, uintptr(valueEntry.next))
		}
	}
	if found == nil {
		return Node{}, false
	}
	return Node{src: v.src, entry: found}, true
}

// Pointer returns a JSON Pointer target relative to v. It walks pointer tokens
// in place, so a slash-free pointer resolves without allocating a compiled
// token list, matching Value.Pointer.
func (v Node) Pointer(pointer string) (Node, bool, error) {
	if pointer == "" {
		return v, v.valid(), nil
	}
	if pointer[0] != '/' {
		return Node{}, false, &PointerError{Pointer: pointer, Message: "pointer must be empty or start with slash"}
	}
	cur := v
	for i := 1; i <= len(pointer); {
		j := i
		for j < len(pointer) && pointer[j] != '/' {
			j++
		}
		token, err := unescapePointerToken(pointer[i:j])
		if err != nil {
			return Node{}, false, err
		}
		switch cur.Kind() {
		case Object:
			next, ok := cur.Get(token)
			if !ok {
				return Node{}, false, nil
			}
			cur = next
		case Array:
			index, ok, err := parsePointerIndex(token)
			if err != nil || !ok {
				return Node{}, ok, err
			}
			next, ok := cur.Index(index)
			if !ok {
				return Node{}, false, nil
			}
			cur = next
		default:
			return Node{}, false, nil
		}
		i = j + 1
	}
	return cur, cur.valid(), nil
}

// PointerCompiled resolves pointer from v without allocating.
func (v Node) PointerCompiled(pointer CompiledPointer) (Node, bool, error) {
	cur := v
	for i := range pointer.tokens {
		token := pointer.tokens[i]
		switch cur.Kind() {
		case Object:
			next, ok := cur.Get(token.text)
			if !ok {
				return Node{}, false, nil
			}
			cur = next
		case Array:
			index, ok, err := token.arrayIndex()
			if err != nil || !ok {
				return Node{}, ok, err
			}
			next, ok := cur.Index(index)
			if !ok {
				return Node{}, false, nil
			}
			cur = next
		default:
			return Node{}, false, nil
		}
	}
	return cur, cur.valid(), nil
}

// tapeEntryOffset steps offset entries forward within one tape. Callers
// must stay inside the entries built for this document: entry counts come
// from the tape itself (count, next), so the arithmetic never leaves the
// allocation as long as those fields are trusted and empty containers are
// checked before stepping past their header.
func tapeEntryOffset(entry *IndexEntry, offset uintptr) *IndexEntry {
	return (*IndexEntry)(unsafe.Add(unsafe.Pointer(entry), offset*unsafe.Sizeof(IndexEntry{})))
}

// tapeSourceBytes reslices the document by tape coordinates. start and end
// were produced by the builder from real token positions, so they are always
// within the source that entry describes.
func tapeSourceBytes(src *byte, start, end uint32) []byte {
	return unsafe.Slice((*byte)(unsafe.Add(unsafe.Pointer(src), uintptr(start))), int(end-start))
}

func tapeKeyEqual(raw []byte, flags uint8, key string) bool {
	if flags&tapeFlagEscaped == 0 {
		return bytesEqualString(raw[1:len(raw)-1], key)
	}
	raw = raw[1 : len(raw)-1]
	ki := 0
	for i := 0; i < len(raw); {
		if raw[i] != '\\' {
			if ki >= len(key) || raw[i] != key[ki] {
				return false
			}
			i++
			ki++
			continue
		}
		i++
		if raw[i] != 'u' {
			c := decodedSimpleEscape(raw[i])
			if ki >= len(key) || key[ki] != c {
				return false
			}
			i++
			ki++
			continue
		}
		u, _ := hex4(raw, i+1)
		i += 5
		r := rune(u)
		if 0xD800 <= r && r <= 0xDBFF {
			lo, _ := hex4(raw, i+2)
			r = utf16.DecodeRune(r, rune(lo))
			i += 6
		}
		var encoded [utf8.UTFMax]byte
		n := utf8.EncodeRune(encoded[:], r)
		if ki+n > len(key) || !bytesEqualString(encoded[:n], key[ki:ki+n]) {
			return false
		}
		ki += n
	}
	return ki == len(key)
}
