package simdjson

import (
	"errors"
	"math/bits"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"
)

// ErrIndexFull means the caller-provided entry buffer has insufficient capacity.
var ErrIndexFull = errors.New("simdjson: index entry buffer is full")

// ErrIndexTooLarge means the source or entry count exceeds the index's 32-bit
// address space.
var ErrIndexTooLarge = errors.New("simdjson: indexed input exceeds 32-bit offsets")

const (
	tapeFlagEscaped = 1 << iota
	tapeFlagKey
)

// IndexEntry is one compact structural entry in an Index. Its fields are private
// so callers can provide reusable storage without being coupled to the layout.
type IndexEntry struct {
	start uint32
	end   uint32
	next  uint32
	count uint32
	kind  Kind
	flags uint8
}

// IndexOptions controls zero-copy structural indexing.
type IndexOptions struct {
	// MaxDepth has the same meaning as Options.MaxDepth.
	MaxDepth int
}

// Index is a validated, zero-copy structural index over its source JSON.
type Index struct {
	src     []byte
	entries []IndexEntry
}

// BuildIndex validates src and builds a navigable index in caller-owned storage.
// The returned Index aliases both src and storage. It performs no heap
// allocations for valid input when storage is sufficient.
func BuildIndex(src []byte, storage []IndexEntry) (Index, error) {
	return BuildIndexOptions(src, storage, IndexOptions{})
}

// BuildIndexOptions is BuildIndex with depth control.
func BuildIndexOptions(src []byte, storage []IndexEntry, opts IndexOptions) (Index, error) {
	if uint64(len(src)) > uint64(^uint32(0)) || uint64(cap(storage)) > uint64(^uint32(0)) {
		return Index{}, ErrIndexTooLarge
	}
	maxDepth := opts.MaxDepth
	if maxDepth <= 0 {
		maxDepth = defaultMaxDepth
	}
	b := tapeBuilder{
		src:      src,
		base:     unsafe.Pointer(unsafe.SliceData(src)),
		entries:  storage[:0],
		parent:   noTapeParent,
		maxDepth: maxDepth,
	}
	status := b.parseFast()
	switch status {
	case tapeParseOK:
	case tapeParseFull:
		return Index{}, ErrIndexFull
	default:
		b.entries = storage[:0]
		b.i = 0
		b.sp = 0
		b.parent = noTapeParent
		if err := b.parse(); err != nil {
			return Index{}, err
		}
	}
	return Index{src: src, entries: b.entries}, nil
}

// RequiredIndexEntries validates src and returns the exact storage length
// BuildIndex needs. Ordinary documents are counted without heap allocation.
func RequiredIndexEntries(src []byte) (int, error) {
	l, err := countLayout(src, defaultMaxDepth)
	if err != nil {
		return 0, err
	}
	return 1 + l.values + 2*l.members, nil
}

// Len returns the number of structural entries in the index.
func (t Index) Len() int {
	return len(t.entries)
}

// Root returns the document's top-level node.
func (t Index) Root() Node {
	if len(t.entries) == 0 {
		return Node{}
	}
	return Node{src: unsafe.SliceData(t.src), entry: unsafe.SliceData(t.entries)}
}

// Pointer returns a JSON Pointer target. CompilePointer plus PointerCompiled is
// preferable on hot paths because pointer compilation may allocate.
func (t Index) Pointer(pointer string) (Node, bool, error) {
	return t.Root().Pointer(pointer)
}

// PointerCompiled returns a precompiled JSON Pointer target without allocating.
func (t Index) PointerCompiled(pointer CompiledPointer) (Node, bool, error) {
	return t.Root().PointerCompiled(pointer)
}

// Node is a lightweight handle into an Index.
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
	if !ok {
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

// Pointer returns a JSON Pointer target relative to v.
func (v Node) Pointer(pointer string) (Node, bool, error) {
	compiled, err := CompilePointer(pointer)
	if err != nil {
		return Node{}, false, err
	}
	return v.PointerCompiled(compiled)
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

// ArrayIter iterates array values without allocating.
type ArrayIter struct {
	src       *byte
	entry     *IndexEntry
	remaining uint32
}

// ArrayIter returns an iterator over v's array elements.
func (v Node) ArrayIter() (ArrayIter, bool) {
	count, ok := v.ArrayLen()
	if !ok {
		return ArrayIter{}, false
	}
	entry := (*IndexEntry)(nil)
	if count != 0 {
		entry = tapeEntryOffset(v.entry, 1)
	}
	return ArrayIter{src: v.src, entry: entry, remaining: uint32(count)}, true
}

// Valid reports whether the cursor points at an array element.
func (it ArrayIter) Valid() bool {
	return it.remaining != 0
}

// Current returns the array element at the cursor without advancing it.
func (it ArrayIter) Current() Node {
	if it.remaining == 0 {
		return Node{}
	}
	return Node{src: it.src, entry: it.entry}
}

// CurrentKind returns the kind at the cursor without advancing it.
func (it ArrayIter) CurrentKind() Kind {
	if it.remaining == 0 {
		return Invalid
	}
	return it.entry.kind
}

// CurrentRaw returns the exact source slice at the cursor without advancing it.
func (it ArrayIter) CurrentRaw() RawValue {
	if it.remaining == 0 {
		return RawValue{}
	}
	entry := it.entry
	return RawValue{src: tapeSourceBytes(it.src, entry.start, entry.end)}
}

// Advance returns a cursor positioned at the next array element. Assign the
// result back to the cursor to keep iteration state register-resident.
func (it ArrayIter) Advance() ArrayIter {
	if it.remaining == 0 {
		return it
	}
	entry := it.entry
	it.remaining--
	if it.remaining != 0 {
		it.entry = tapeEntryOffset(entry, uintptr(entry.next))
	} else {
		it.entry = nil
	}
	return it
}

// Next returns the next array element.
func (it *ArrayIter) Next() (Node, bool) {
	if it.remaining == 0 {
		return Node{}, false
	}
	entry := it.entry
	it.remaining--
	if it.remaining != 0 {
		it.entry = tapeEntryOffset(entry, uintptr(entry.next))
	} else {
		it.entry = nil
	}
	return Node{src: it.src, entry: entry}, true
}

// NextKind advances the iterator and returns only the next value's kind.
func (it *ArrayIter) NextKind() (Kind, bool) {
	if it.remaining == 0 {
		return Invalid, false
	}
	entry := it.entry
	it.remaining--
	if it.remaining != 0 {
		it.entry = tapeEntryOffset(entry, uintptr(entry.next))
	} else {
		it.entry = nil
	}
	return entry.kind, true
}

// NextRaw advances the iterator and returns the next exact source slice.
func (it *ArrayIter) NextRaw() (RawValue, bool) {
	if it.remaining == 0 {
		return RawValue{}, false
	}
	entry := it.entry
	it.remaining--
	if it.remaining != 0 {
		it.entry = tapeEntryOffset(entry, uintptr(entry.next))
	} else {
		it.entry = nil
	}
	return RawValue{src: tapeSourceBytes(it.src, entry.start, entry.end)}, true
}

// FlatArrayIter iterates an array whose direct elements each occupy one index
// entry. Its fixed entry stride is faster than the general subtree cursor.
type FlatArrayIter struct {
	src       *byte
	entry     *IndexEntry
	remaining uint32
}

// FlatArrayIter returns a fixed-stride iterator when every direct array element
// is scalar or an empty container.
func (v Node) FlatArrayIter() (FlatArrayIter, bool) {
	if !v.valid() || v.entry.kind != Array || v.entry.next != v.entry.count+1 {
		return FlatArrayIter{}, false
	}
	entry := (*IndexEntry)(nil)
	if v.entry.count != 0 {
		entry = tapeEntryOffset(v.entry, 1)
	}
	return FlatArrayIter{src: v.src, entry: entry, remaining: v.entry.count}, true
}

// Valid reports whether the cursor points at an array element.
func (it FlatArrayIter) Valid() bool {
	return it.remaining != 0
}

// Current returns the array element at the cursor without advancing it.
func (it FlatArrayIter) Current() Node {
	if it.remaining == 0 {
		return Node{}
	}
	return Node{src: it.src, entry: it.entry}
}

// CurrentKind returns the kind at the cursor without advancing it.
func (it FlatArrayIter) CurrentKind() Kind {
	if it.remaining == 0 {
		return Invalid
	}
	return it.entry.kind
}

// CurrentRaw returns the exact source slice at the cursor without advancing it.
func (it FlatArrayIter) CurrentRaw() RawValue {
	if it.remaining == 0 {
		return RawValue{}
	}
	entry := it.entry
	return RawValue{src: tapeSourceBytes(it.src, entry.start, entry.end)}
}

// Advance returns a cursor positioned at the next array element. Assign the
// result back to the cursor to keep iteration state register-resident.
func (it FlatArrayIter) Advance() FlatArrayIter {
	if it.remaining == 0 {
		return it
	}
	it.remaining--
	if it.remaining != 0 {
		it.entry = tapeEntryOffset(it.entry, 1)
	} else {
		it.entry = nil
	}
	return it
}

// Next returns the next flat array value.
func (it *FlatArrayIter) Next() (Node, bool) {
	if it.remaining == 0 {
		return Node{}, false
	}
	entry := it.entry
	it.remaining--
	if it.remaining != 0 {
		it.entry = tapeEntryOffset(entry, 1)
	} else {
		it.entry = nil
	}
	return Node{src: it.src, entry: entry}, true
}

// NextKind advances the iterator and returns the next value's kind.
func (it *FlatArrayIter) NextKind() (Kind, bool) {
	if it.remaining == 0 {
		return Invalid, false
	}
	entry := it.entry
	it.remaining--
	if it.remaining != 0 {
		it.entry = tapeEntryOffset(entry, 1)
	} else {
		it.entry = nil
	}
	return entry.kind, true
}

// NextRaw advances the iterator and returns the next exact source slice.
func (it *FlatArrayIter) NextRaw() (RawValue, bool) {
	if it.remaining == 0 {
		return RawValue{}, false
	}
	entry := it.entry
	it.remaining--
	if it.remaining != 0 {
		it.entry = tapeEntryOffset(entry, 1)
	} else {
		it.entry = nil
	}
	return RawValue{src: tapeSourceBytes(it.src, entry.start, entry.end)}, true
}

// ObjectIter iterates ordered object key/value pairs without allocating.
type ObjectIter struct {
	src       *byte
	entry     *IndexEntry
	remaining uint32
}

// ObjectIter returns an iterator over v's object members.
func (v Node) ObjectIter() (ObjectIter, bool) {
	count, ok := v.ObjectLen()
	if !ok {
		return ObjectIter{}, false
	}
	entry := (*IndexEntry)(nil)
	if count != 0 {
		entry = tapeEntryOffset(v.entry, 1)
	}
	return ObjectIter{src: v.src, entry: entry, remaining: uint32(count)}, true
}

// Valid reports whether the cursor points at an object member.
func (it ObjectIter) Valid() bool {
	return it.remaining != 0
}

// Current returns the key and value at the cursor without advancing it.
func (it ObjectIter) Current() (key, value Node) {
	if it.remaining == 0 {
		return Node{}, Node{}
	}
	keyEntry := it.entry
	return Node{src: it.src, entry: keyEntry}, Node{src: it.src, entry: tapeEntryOffset(keyEntry, 1)}
}

// CurrentRaw returns the exact key and value source slices at the cursor. The
// key includes its JSON quotes and escapes.
func (it ObjectIter) CurrentRaw() (key, value RawValue) {
	if it.remaining == 0 {
		return RawValue{}, RawValue{}
	}
	keyEntry := it.entry
	valueEntry := tapeEntryOffset(keyEntry, 1)
	return RawValue{src: tapeSourceBytes(it.src, keyEntry.start, keyEntry.end)},
		RawValue{src: tapeSourceBytes(it.src, valueEntry.start, valueEntry.end)}
}

// Advance returns a cursor positioned at the next object member. Assign the
// result back to the cursor to keep iteration state register-resident.
func (it ObjectIter) Advance() ObjectIter {
	if it.remaining == 0 {
		return it
	}
	valueEntry := tapeEntryOffset(it.entry, 1)
	it.remaining--
	if it.remaining != 0 {
		it.entry = tapeEntryOffset(valueEntry, uintptr(valueEntry.next))
	} else {
		it.entry = nil
	}
	return it
}

// Next returns the next ordered key/value pair. The key is a String Node.
func (it *ObjectIter) Next() (key, value Node, ok bool) {
	if it.remaining == 0 {
		return Node{}, Node{}, false
	}
	keyEntry := it.entry
	valueEntry := tapeEntryOffset(keyEntry, 1)
	key = Node{src: it.src, entry: keyEntry}
	value = Node{src: it.src, entry: valueEntry}
	it.remaining--
	if it.remaining != 0 {
		it.entry = tapeEntryOffset(valueEntry, uintptr(valueEntry.next))
	} else {
		it.entry = nil
	}
	return key, value, true
}

// NextRaw advances the iterator and returns the next exact key and value
// source slices. The key includes its JSON quotes and escapes.
func (it *ObjectIter) NextRaw() (key, value RawValue, ok bool) {
	if it.remaining == 0 {
		return RawValue{}, RawValue{}, false
	}
	keyEntry := it.entry
	valueEntry := tapeEntryOffset(keyEntry, 1)
	key = RawValue{src: tapeSourceBytes(it.src, keyEntry.start, keyEntry.end)}
	value = RawValue{src: tapeSourceBytes(it.src, valueEntry.start, valueEntry.end)}
	it.remaining--
	if it.remaining != 0 {
		it.entry = tapeEntryOffset(valueEntry, uintptr(valueEntry.next))
	} else {
		it.entry = nil
	}
	return key, value, true
}

// FlatObjectIter iterates an object whose direct values each occupy one index
// entry.
type FlatObjectIter struct {
	src       *byte
	entry     *IndexEntry
	remaining uint32
}

// FlatObjectIter returns a fixed-stride iterator when every direct object value
// is scalar or an empty container.
func (v Node) FlatObjectIter() (FlatObjectIter, bool) {
	if !v.valid() || v.entry.kind != Object || v.entry.next != 2*v.entry.count+1 {
		return FlatObjectIter{}, false
	}
	entry := (*IndexEntry)(nil)
	if v.entry.count != 0 {
		entry = tapeEntryOffset(v.entry, 1)
	}
	return FlatObjectIter{src: v.src, entry: entry, remaining: v.entry.count}, true
}

// Valid reports whether the cursor points at an object member.
func (it FlatObjectIter) Valid() bool {
	return it.remaining != 0
}

// Current returns the key and value at the cursor without advancing it.
func (it FlatObjectIter) Current() (key, value Node) {
	if it.remaining == 0 {
		return Node{}, Node{}
	}
	keyEntry := it.entry
	return Node{src: it.src, entry: keyEntry}, Node{src: it.src, entry: tapeEntryOffset(keyEntry, 1)}
}

// CurrentRaw returns the exact key and value source slices at the cursor. The
// key includes its JSON quotes and escapes.
func (it FlatObjectIter) CurrentRaw() (key, value RawValue) {
	if it.remaining == 0 {
		return RawValue{}, RawValue{}
	}
	keyEntry := it.entry
	valueEntry := tapeEntryOffset(keyEntry, 1)
	return RawValue{src: tapeSourceBytes(it.src, keyEntry.start, keyEntry.end)},
		RawValue{src: tapeSourceBytes(it.src, valueEntry.start, valueEntry.end)}
}

// Advance returns a cursor positioned at the next object member. Assign the
// result back to the cursor to keep iteration state register-resident.
func (it FlatObjectIter) Advance() FlatObjectIter {
	if it.remaining == 0 {
		return it
	}
	it.remaining--
	if it.remaining != 0 {
		it.entry = tapeEntryOffset(it.entry, 2)
	} else {
		it.entry = nil
	}
	return it
}

// Next returns the next flat object key and value.
func (it *FlatObjectIter) Next() (key, value Node, ok bool) {
	if it.remaining == 0 {
		return Node{}, Node{}, false
	}
	keyEntry := it.entry
	valueEntry := tapeEntryOffset(keyEntry, 1)
	key = Node{src: it.src, entry: keyEntry}
	value = Node{src: it.src, entry: valueEntry}
	it.remaining--
	if it.remaining != 0 {
		it.entry = tapeEntryOffset(keyEntry, 2)
	} else {
		it.entry = nil
	}
	return key, value, true
}

// NextRaw returns the next exact object key and value source slices.
func (it *FlatObjectIter) NextRaw() (key, value RawValue, ok bool) {
	if it.remaining == 0 {
		return RawValue{}, RawValue{}, false
	}
	keyEntry := it.entry
	valueEntry := tapeEntryOffset(keyEntry, 1)
	key = RawValue{src: tapeSourceBytes(it.src, keyEntry.start, keyEntry.end)}
	value = RawValue{src: tapeSourceBytes(it.src, valueEntry.start, valueEntry.end)}
	it.remaining--
	if it.remaining != 0 {
		it.entry = tapeEntryOffset(keyEntry, 2)
	} else {
		it.entry = nil
	}
	return key, value, true
}

func tapeEntryOffset(entry *IndexEntry, offset uintptr) *IndexEntry {
	return (*IndexEntry)(unsafe.Add(unsafe.Pointer(entry), offset*unsafe.Sizeof(IndexEntry{})))
}

func tapeSourceBytes(src *byte, start, end uint32) []byte {
	return unsafe.Slice((*byte)(unsafe.Add(unsafe.Pointer(src), uintptr(start))), int(end-start))
}

type tapeBuilder struct {
	src      []byte
	base     unsafe.Pointer
	entries  []IndexEntry
	parent   uint32
	i        int
	sp       int
	maxDepth int
}

const noTapeParent uint32 = ^uint32(0)

type tapeParseStatus uint8

const (
	tapeParseOK tapeParseStatus = iota
	tapeParseInvalid
	tapeParseFull
)

// parseFast is the happy-path tape builder: a recursive descent walk with an
// inline one-word fast path for short clean strings. It reports full or
// invalid input so BuildIndex can fall back to the diagnostic parser.
func (b *tapeBuilder) parseFast() tapeParseStatus {
	b.skipSpace()
	if b.i >= len(b.src) {
		return tapeParseInvalid
	}
	if status := b.valueFast(0); status != tapeParseOK {
		return status
	}
	b.skipSpace()
	if b.i != len(b.src) {
		return tapeParseInvalid
	}
	return tapeParseOK
}

// stringFast records one string entry starting at the opening quote.
func (b *tapeBuilder) stringFast(start int, flags uint8) tapeParseStatus {
	scanStart := start + 1
	if start+9 <= len(b.src) {
		if m := stringSpecialMask(loadUint64LE(unsafe.Add(b.base, start+1))); m != 0 {
			j := start + 1 + bits.TrailingZeros64(m)/8
			if b.src[j] == '"' {
				if len(b.entries) == cap(b.entries) {
					return tapeParseFull
				}
				entry := len(b.entries)
				b.entries = b.entries[:entry+1]
				b.entries[entry] = IndexEntry{start: uint32(start), end: uint32(j + 1), next: 1, kind: String, flags: flags}
				b.i = j + 1
				return tapeParseOK
			}
			scanStart = j
		} else {
			scanStart += 8
		}
	}
	end, escaped, ok := scanJSONStringFastFrom(b.src, b.base, scanStart)
	if !ok {
		return tapeParseInvalid
	}
	if escaped {
		flags |= tapeFlagEscaped
	}
	if len(b.entries) == cap(b.entries) {
		return tapeParseFull
	}
	entry := len(b.entries)
	b.entries = b.entries[:entry+1]
	b.entries[entry] = IndexEntry{start: uint32(start), end: uint32(end), next: 1, kind: String, flags: flags}
	b.i = end
	return tapeParseOK
}

func (b *tapeBuilder) valueFast(depth int) tapeParseStatus {
	n := len(b.src)
	base := b.base
	start := b.i
	switch fastByteAt(base, start) {
	case '{':
		if depth >= b.maxDepth {
			return tapeParseInvalid
		}
		if len(b.entries) == cap(b.entries) {
			return tapeParseFull
		}
		entry := len(b.entries)
		b.entries = b.entries[:entry+1]
		b.entries[entry] = IndexEntry{start: uint32(start), kind: Object}
		i, c := nextSignificantFast(base, n, start+1)
		if i >= n {
			return tapeParseInvalid
		}
		if c == '}' {
			b.i = i + 1
			finished := &b.entries[entry]
			finished.end = uint32(b.i)
			finished.next = uint32(len(b.entries) - entry)
			return tapeParseOK
		}
		count := uint32(0)
		for {
			if c != '"' {
				return tapeParseInvalid
			}
			if status := b.stringFast(i, tapeFlagKey); status != tapeParseOK {
				return status
			}
			i, c = nextSignificantFast(base, n, b.i)
			if i >= n || c != ':' {
				return tapeParseInvalid
			}
			i, _ = nextSignificantFast(base, n, i+1)
			if i >= n {
				return tapeParseInvalid
			}
			b.i = i
			if status := b.valueFast(depth + 1); status != tapeParseOK {
				return status
			}
			count++
			i, c = nextSignificantFast(base, n, b.i)
			if i >= n {
				return tapeParseInvalid
			}
			if c == ',' {
				i, c = nextSignificantFast(base, n, i+1)
				if i >= n {
					return tapeParseInvalid
				}
				continue
			}
			if c != '}' {
				return tapeParseInvalid
			}
			b.i = i + 1
			finished := &b.entries[entry]
			finished.end = uint32(b.i)
			finished.count = count
			finished.next = uint32(len(b.entries) - entry)
			return tapeParseOK
		}
	case '[':
		if depth >= b.maxDepth {
			return tapeParseInvalid
		}
		if len(b.entries) == cap(b.entries) {
			return tapeParseFull
		}
		entry := len(b.entries)
		b.entries = b.entries[:entry+1]
		b.entries[entry] = IndexEntry{start: uint32(start), kind: Array}
		i, c := nextSignificantFast(base, n, start+1)
		if i >= n {
			return tapeParseInvalid
		}
		if c == ']' {
			b.i = i + 1
			finished := &b.entries[entry]
			finished.end = uint32(b.i)
			finished.next = uint32(len(b.entries) - entry)
			return tapeParseOK
		}
		count := uint32(0)
		for {
			b.i = i
			if status := b.valueFast(depth + 1); status != tapeParseOK {
				return status
			}
			count++
			i, c = nextSignificantFast(base, n, b.i)
			if i >= n {
				return tapeParseInvalid
			}
			if c == ',' {
				i, _ = nextSignificantFast(base, n, i+1)
				if i >= n {
					return tapeParseInvalid
				}
				continue
			}
			if c != ']' {
				return tapeParseInvalid
			}
			b.i = i + 1
			finished := &b.entries[entry]
			finished.end = uint32(b.i)
			finished.count = count
			finished.next = uint32(len(b.entries) - entry)
			return tapeParseOK
		}
	case '"':
		return b.stringFast(start, 0)
	case 't':
		if start+4 > n || loadUint32LE(unsafe.Add(base, start)) != wordTrueLE {
			return tapeParseInvalid
		}
		b.i = start + 4
		return b.emitScalar(start, Bool)
	case 'f':
		if start+5 > n || loadUint32LE(unsafe.Add(base, start+1)) != wordAlseLE {
			return tapeParseInvalid
		}
		b.i = start + 5
		return b.emitScalar(start, Bool)
	case 'n':
		if start+4 > n || loadUint32LE(unsafe.Add(base, start)) != wordNullLE {
			return tapeParseInvalid
		}
		b.i = start + 4
		return b.emitScalar(start, Null)
	default:
		c := fastByteAt(base, start)
		if c != '-' && !isDigit(c) {
			return tapeParseInvalid
		}
		end, ok := scanNumberFast(base, n, start)
		if !ok {
			return tapeParseInvalid
		}
		b.i = end
		return b.emitScalar(start, Number)
	}
}

func (b *tapeBuilder) emitScalar(start int, kind Kind) tapeParseStatus {
	if len(b.entries) == cap(b.entries) {
		return tapeParseFull
	}
	entry := len(b.entries)
	b.entries = b.entries[:entry+1]
	b.entries[entry] = IndexEntry{start: uint32(start), end: uint32(b.i), next: 1, kind: kind}
	return tapeParseOK
}

func (b *tapeBuilder) parse() error {
	b.skipSpace()
	completed := false
	for {
		if !completed {
			kind, entry, err := b.value()
			if err != nil {
				return err
			}
			if kind != Array && kind != Object {
				completed = true
			} else {
				if b.sp >= b.maxDepth {
					return syntaxError(b.src, b.i-1, "maximum nesting depth exceeded")
				}
				b.pushContainer(entry)
				b.skipSpace()
				close := byte(']')
				if kind == Object {
					close = '}'
				}
				if b.i < len(b.src) && b.src[b.i] == close {
					b.i++
					b.finishContainer()
					completed = true
				} else {
					if kind == Object {
						if err := b.objectKey(); err != nil {
							return err
						}
					}
					continue
				}
			}
		}

		for completed {
			if b.sp == 0 {
				b.skipSpace()
				if b.i != len(b.src) {
					return syntaxError(b.src, b.i, "unexpected data after top-level value")
				}
				return nil
			}
			frame := &b.entries[b.parent]
			frame.count++
			b.skipSpace()
			if b.i >= len(b.src) {
				if frame.kind == Array {
					return syntaxError(b.src, b.i, "unterminated array")
				}
				return syntaxError(b.src, b.i, "unterminated object")
			}
			if frame.kind == Array {
				switch b.src[b.i] {
				case ',':
					b.i++
					completed = false
				case ']':
					b.i++
					b.finishContainer()
				default:
					return syntaxError(b.src, b.i, "expected comma or closing bracket in array")
				}
			} else {
				switch b.src[b.i] {
				case ',':
					b.i++
					if err := b.objectKey(); err != nil {
						return err
					}
					completed = false
				case '}':
					b.i++
					b.finishContainer()
				default:
					return syntaxError(b.src, b.i, "expected comma or closing brace in object")
				}
			}
		}
	}
}

func (b *tapeBuilder) value() (Kind, int, error) {
	b.skipSpace()
	if b.i >= len(b.src) {
		return Invalid, 0, syntaxError(b.src, b.i, "expected value")
	}
	start := b.i
	switch b.src[b.i] {
	case 'n':
		if !matchStringAt(b.src, b.i, "null") {
			return Invalid, 0, syntaxError(b.src, b.i, "invalid literal")
		}
		b.i += 4
		return b.scalar(Null, start, 0)
	case 't':
		if !matchStringAt(b.src, b.i, "true") {
			return Invalid, 0, syntaxError(b.src, b.i, "invalid literal")
		}
		b.i += 4
		return b.scalar(Bool, start, 0)
	case 'f':
		if !matchStringAt(b.src, b.i, "false") {
			return Invalid, 0, syntaxError(b.src, b.i, "invalid literal")
		}
		b.i += 5
		return b.scalar(Bool, start, 0)
	case '"':
		end, escaped, err := b.string()
		if err != nil {
			return Invalid, 0, err
		}
		flags := uint8(0)
		if escaped {
			flags |= tapeFlagEscaped
		}
		return b.scalarAt(String, start, end, flags)
	case '[':
		b.i++
		entry, err := b.add(IndexEntry{start: uint32(start), kind: Array})
		return Array, entry, err
	case '{':
		b.i++
		entry, err := b.add(IndexEntry{start: uint32(start), kind: Object})
		return Object, entry, err
	default:
		if fastByteAt(b.base, b.i) != '-' && !isDigit(fastByteAt(b.base, b.i)) {
			return Invalid, 0, syntaxError(b.src, b.i, "unexpected byte while parsing value")
		}
		end, ok := scanNumberFast(b.base, len(b.src), b.i)
		if !ok {
			_, msg := scanNumber(b.src, b.i)
			return Invalid, 0, syntaxError(b.src, start, msg)
		}
		b.i = end
		return b.scalar(Number, start, 0)
	}
}

func (b *tapeBuilder) scalar(kind Kind, start int, flags uint8) (Kind, int, error) {
	return b.scalarAt(kind, start, b.i, flags)
}

func (b *tapeBuilder) scalarAt(kind Kind, start, end int, flags uint8) (Kind, int, error) {
	entry, err := b.add(IndexEntry{start: uint32(start), end: uint32(end), next: 1, kind: kind, flags: flags})
	return kind, entry, err
}

func (b *tapeBuilder) objectKey() error {
	b.skipSpace()
	if b.i >= len(b.src) || b.src[b.i] != '"' {
		return syntaxError(b.src, b.i, "expected object key string")
	}
	start := b.i
	end, escaped, err := b.string()
	if err != nil {
		return err
	}
	flags := uint8(tapeFlagKey)
	if escaped {
		flags |= tapeFlagEscaped
	}
	if _, err := b.add(IndexEntry{start: uint32(start), end: uint32(end), next: 1, kind: String, flags: flags}); err != nil {
		return err
	}
	b.skipSpace()
	if b.i >= len(b.src) || b.src[b.i] != ':' {
		return syntaxError(b.src, b.i, "expected colon after object key")
	}
	b.i++
	return nil
}

func (b *tapeBuilder) string() (end int, escaped bool, err error) {
	end, escaped, ok := scanJSONStringFast(b.src, b.base, b.i, len(b.src) <= 64)
	if ok {
		b.i = end
		return end, escaped, nil
	}
	s := rawSeeker{src: b.src, i: b.i, maxDepth: b.maxDepth}
	_, _, escaped, err = s.parseStringRaw()
	if err != nil {
		return 0, false, err
	}
	b.i = s.i
	return b.i, escaped, nil
}

func (b *tapeBuilder) add(entry IndexEntry) (int, error) {
	if len(b.entries) == cap(b.entries) {
		return 0, ErrIndexFull
	}
	index := len(b.entries)
	b.entries = b.entries[:index+1]
	b.entries[index] = entry
	return index, nil
}

func (b *tapeBuilder) finishContainer() {
	entry := b.parent
	e := &b.entries[entry]
	b.parent = e.next
	b.sp--
	e.end = uint32(b.i)
	e.next = uint32(len(b.entries)) - entry
}

func (b *tapeBuilder) pushContainer(entry int) {
	b.entries[entry].next = b.parent
	b.parent = uint32(entry)
	b.sp++
}

func (b *tapeBuilder) skipSpace() {
	b.i = skipSpaceFast(b.base, len(b.src), b.i)
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

func appendDecodedJSONString(dst, raw []byte) []byte {
	for i := 0; i < len(raw); {
		if raw[i] != '\\' {
			start := i
			for i < len(raw) && raw[i] != '\\' {
				i++
			}
			dst = append(dst, raw[start:i]...)
			continue
		}
		i++
		if raw[i] != 'u' {
			dst = append(dst, decodedSimpleEscape(raw[i]))
			i++
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
		dst = utf8.AppendRune(dst, r)
	}
	return dst
}

func decodedSimpleEscape(c byte) byte {
	switch c {
	case 'b':
		return '\b'
	case 'f':
		return '\f'
	case 'n':
		return '\n'
	case 'r':
		return '\r'
	case 't':
		return '\t'
	default:
		return c
	}
}
