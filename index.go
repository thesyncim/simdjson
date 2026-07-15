package simdjson

import (
	"errors"
	"math/bits"
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

// The info word packs a container's direct element count together with the
// entry's kind and flags, so an entry stays four uint32 words (16 bytes) with
// no padding. count occupies the low 27 bits; kind the next 3; flags the top 2.
// count is meaningful only for containers, where it holds the number of direct
// members; scalars leave it zero. Its 27-bit width caps a single container at
// infoMaxCount direct members. The builders reject any input that would exceed
// that (see ErrIndexTooLarge); reaching the cap needs a source larger than
// 256 MiB packed entirely into one container, so it never arises in practice.
const (
	infoCountBits          = 27
	infoKindBits           = 3
	infoCountMask   uint32 = 1<<infoCountBits - 1
	infoKindShift          = infoCountBits
	infoKindMask    uint32 = (1<<infoKindBits - 1) << infoKindShift
	infoFlagsShift         = infoCountBits + infoKindBits
	infoMaxCount    uint32 = infoCountMask
)

// IndexEntry is one compact structural entry in an Index. Its fields are private
// so callers can provide reusable storage without being coupled to the layout;
// kind, flags, and count share one packed word behind accessor methods, so the
// layout can change without touching every reader.
type IndexEntry struct {
	start uint32
	end   uint32
	next  uint32
	info  uint32
}

// Kind returns the entry's JSON kind.
func (e *IndexEntry) Kind() Kind {
	return Kind((e.info & infoKindMask) >> infoKindShift)
}

// Flags returns the entry's tape flags (escaped, key).
func (e *IndexEntry) Flags() uint8 {
	return uint8(e.info >> infoFlagsShift)
}

// Count returns a container's direct element count. It is meaningful only for
// arrays and objects; other kinds report zero.
func (e *IndexEntry) Count() uint32 {
	return e.info & infoCountMask
}

// packInfo composes an info word from its parts. The caller guarantees count
// fits in infoCountBits; the builders check this before an entry is written.
func packInfo(count uint32, kind Kind, flags uint8) uint32 {
	return count&infoCountMask | uint32(kind)<<infoKindShift | uint32(flags)<<infoFlagsShift
}

// setCount replaces the entry's element count, preserving kind and flags.
func (e *IndexEntry) setCount(count uint32) {
	e.info = e.info&^infoCountMask | count&infoCountMask
}

// bumpCount adds one to the entry's element count in place. count occupies the
// low bits of info, so an increment cannot disturb kind or flags unless it
// overflows the count field, which the builders prevent.
func (e *IndexEntry) bumpCount() {
	e.info++
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
				b.entries[entry] = IndexEntry{start: uint32(start), end: uint32(j + 1), next: 1, info: packInfo(0, String, flags)}
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
	b.entries[entry] = IndexEntry{start: uint32(start), end: uint32(end), next: 1, info: packInfo(0, String, flags)}
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
		b.entries[entry] = IndexEntry{start: uint32(start), info: packInfo(0, Object, 0)}
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
			if count > infoMaxCount {
				return tapeParseInvalid
			}
			b.i = i + 1
			finished := &b.entries[entry]
			finished.end = uint32(b.i)
			finished.setCount(count)
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
		b.entries[entry] = IndexEntry{start: uint32(start), info: packInfo(0, Array, 0)}
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
			if count > infoMaxCount {
				return tapeParseInvalid
			}
			b.i = i + 1
			finished := &b.entries[entry]
			finished.end = uint32(b.i)
			finished.setCount(count)
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
	b.entries[entry] = IndexEntry{start: uint32(start), end: uint32(b.i), next: 1, info: packInfo(0, kind, 0)}
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
			if frame.Count() == infoMaxCount {
				return ErrIndexTooLarge
			}
			frame.bumpCount()
			b.skipSpace()
			if b.i >= len(b.src) {
				if frame.Kind() == Array {
					return syntaxError(b.src, b.i, "unterminated array")
				}
				return syntaxError(b.src, b.i, "unterminated object")
			}
			if frame.Kind() == Array {
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
		entry, err := b.add(IndexEntry{start: uint32(start), info: packInfo(0, Array, 0)})
		return Array, entry, err
	case '{':
		b.i++
		entry, err := b.add(IndexEntry{start: uint32(start), info: packInfo(0, Object, 0)})
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
	entry, err := b.add(IndexEntry{start: uint32(start), end: uint32(end), next: 1, info: packInfo(0, kind, flags)})
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
	if _, err := b.add(IndexEntry{start: uint32(start), end: uint32(end), next: 1, info: packInfo(0, String, flags)}); err != nil {
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
