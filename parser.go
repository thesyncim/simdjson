package simdjson

import (
	"bytes"
	"sync"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"
)

const defaultMaxDepth = 10000

// Options configures parser limits.
type Options struct {
	// MaxDepth limits nested arrays and objects. Values <= 0 use the default.
	MaxDepth int

	// ZeroCopy reuses src storage for unescaped strings and numbers.
	// Callers must not mutate src for as long as the returned Value is used.
	// When false, results are independent of src: decoded strings alias at
	// most one private copy of the input, so retaining any decoded string
	// retains that copy.
	ZeroCopy bool

}

// Parse parses src into an ordered JSON AST.
func Parse(src []byte) (Value, error) {
	return ParseOptions(src, Options{})
}

// parseTapePool recycles tape storage between ParseOptions calls; the tape
// is consumed before the call returns and never escapes.
var parseTapePool = sync.Pool{
	New: func() any {
		storage := make([]IndexEntry, 0, 1024)
		return &storage
	},
}

// ParseOptions parses src using opts. It builds the structural tape first,
// so containers allocate exactly once from shared arenas sized by the tape.
func ParseOptions(src []byte, opts Options) (Value, error) {
	maxDepth := opts.MaxDepth
	if maxDepth <= 0 {
		maxDepth = defaultMaxDepth
	}
	pooled := parseTapePool.Get().(*[]IndexEntry)
	storage := (*pooled)[:cap(*pooled)]
	estimate := len(src)/8 + 8
	var entries []IndexEntry
	for {
		if cap(storage) < estimate {
			storage = make([]IndexEntry, estimate)
		}
		index, err := BuildIndexOptions(src, storage[:cap(storage)], IndexOptions{MaxDepth: maxDepth})
		if err == ErrIndexFull {
			estimate = cap(storage) * 2
			continue
		}
		if err != nil {
			*pooled = storage[:0]
			parseTapePool.Put(pooled)
			return Value{}, err
		}
		entries = index.entries
		break
	}
	defer func() {
		*pooled = storage[:0]
		parseTapePool.Put(pooled)
	}()
	if len(entries) == 0 {
		return Value{}, syntaxError(src, 0, "expected value")
	}
	builder := astBuilder{src: src, entries: entries, zeroCopy: opts.ZeroCopy}
	values, members := 0, 0
	for i := range entries {
		if entries[i].flags&tapeFlagKey != 0 {
			members++
		} else {
			values++
		}
	}
	// Object member values live inside Member entries, so the value arena
	// only holds array elements: every non-key entry except the root and
	// the object member values (one per key).
	if arrayValues := values - members - 1; arrayValues > 0 {
		builder.valueArena = make([]Value, arrayValues)
	}
	if members > 0 {
		builder.memberArena = make([]Member, members)
	}
	value, _ := builder.build(0)
	return value, nil
}

// astBuilder turns a validated structural tape into the ordered Value tree.
type astBuilder struct {
	src         []byte
	entries     []IndexEntry
	valueArena  []Value
	valuePos    int
	memberArena []Member
	memberPos   int
	zeroCopy    bool
	ownedSrc    []byte
}

func (b *astBuilder) build(idx int) (Value, int) {
	entry := &b.entries[idx]
	switch entry.kind {
	case Object:
		count := int(entry.count)
		var members []Member
		if count > 0 {
			members = b.memberArena[b.memberPos : b.memberPos+count]
			b.memberPos += count
		}
		i := idx + 1
		for m := 0; m < count; m++ {
			key := b.stringAt(&b.entries[i])
			var value Value
			value, i = b.build(i + 1)
			members[m] = Member{Key: key, Value: value}
		}
		return Value{kind: Object, o: members}, i
	case Array:
		count := int(entry.count)
		var values []Value
		if count > 0 {
			values = b.valueArena[b.valuePos : b.valuePos+count]
			b.valuePos += count
		}
		i := idx + 1
		for m := 0; m < count; m++ {
			values[m], i = b.build(i)
		}
		return Value{kind: Array, a: values}, i
	case String:
		return Value{kind: String, s: b.stringAt(entry)}, idx + 1
	case Number:
		return Value{kind: Number, n: b.text(entry.start, entry.end)}, idx + 1
	case Bool:
		return Value{kind: Bool, b: b.src[entry.start] == 't'}, idx + 1
	default:
		return Value{kind: Null}, idx + 1
	}
}

// stringAt decodes a string entry: escaped strings decode into owned storage,
// clean strings follow the parser's ownership rules.
func (b *astBuilder) stringAt(entry *IndexEntry) string {
	if entry.flags&tapeFlagEscaped != 0 {
		return ownedBytesString(appendDecodedJSONString(nil, b.src[entry.start+1:entry.end-1]))
	}
	return b.text(entry.start+1, entry.end-1)
}

// text returns src[start:end] under the parser's ownership rules: zero copy
// aliases src, owned mode aliases one lazily made private copy of the input.
func (b *astBuilder) text(start, end uint32) string {
	if start == end {
		return ""
	}
	if b.zeroCopy {
		return unsafe.String(unsafe.SliceData(b.src[start:end]), int(end-start))
	}
	if b.ownedSrc == nil {
		b.ownedSrc = append([]byte(nil), b.src...)
	}
	return unsafe.String(&b.ownedSrc[start], int(end-start))
}

// parser holds shared low-level scanning state for the dynamic decoding
// paths and the typed decoder's slow paths.
type parser struct {
	src      []byte
	i        int
	maxDepth int
	zeroCopy bool
	ownedSrc []byte
	anyArena *anyValueArena
}

func (p *parser) err(off int, msg string) error {
	return syntaxError(p.src, off, msg)
}

func (p *parser) skipSpace() {
	p.i = skipSpace(p.src, p.i)
}

func (p *parser) parseString() (string, error) {
	p.i++
	start := p.i
	chunkStart := start
	var out []byte

	for {
		j := scanStringSpecial(p.src, p.i)
		if j >= len(p.src) {
			return "", p.err(len(p.src), "unterminated string")
		}
		p.i = j
		c := p.src[p.i]
		switch {
		case c == '"':
			if out == nil {
				s := p.string(p.src[start:p.i])
				p.i++
				return s, nil
			}
			out = append(out, p.src[chunkStart:p.i]...)
			p.i++
			return ownedBytesString(out), nil
		case c == '\\':
			if out == nil {
				out = make([]byte, 0, len(p.src[start:p.i]))
			}
			out = append(out, p.src[chunkStart:p.i]...)
			p.i++
			if p.i >= len(p.src) {
				return "", p.err(p.i, "unterminated escape sequence")
			}
			if err := p.appendEscape(&out); err != nil {
				return "", err
			}
			chunkStart = p.i
		case c < 0x20:
			return "", p.err(p.i, "unescaped control byte in string")
		default:
			next, bad := scanStringUnicodeRun(p.src, p.i)
			if bad >= 0 {
				return "", p.err(bad, "invalid UTF-8 in string")
			}
			p.i = next
		}
	}
}

// string converts a subslice of p.src into a result string. Zero-copy results
// alias p.src directly. Owned results alias one lazily made private copy of
// the input, so a document's strings cost one allocation in total rather than
// one allocation each; retaining any decoded string retains that copy.
func (p *parser) string(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	if p.zeroCopy {
		return unsafe.String(unsafe.SliceData(b), len(b))
	}
	if p.ownedSrc == nil {
		p.ownedSrc = bytes.Clone(p.src)
	}
	offset := uintptr(unsafe.Pointer(unsafe.SliceData(b))) - uintptr(unsafe.Pointer(unsafe.SliceData(p.src)))
	return unsafe.String(&p.ownedSrc[offset], len(b))
}

func ownedBytesString(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return unsafe.String(unsafe.SliceData(b), len(b))
}

func (p *parser) appendEscape(out *[]byte) error {
	switch p.src[p.i] {
	case '"', '\\', '/':
		*out = append(*out, p.src[p.i])
		p.i++
		return nil
	case 'b':
		*out = append(*out, '\b')
		p.i++
		return nil
	case 'f':
		*out = append(*out, '\f')
		p.i++
		return nil
	case 'n':
		*out = append(*out, '\n')
		p.i++
		return nil
	case 'r':
		*out = append(*out, '\r')
		p.i++
		return nil
	case 't':
		*out = append(*out, '\t')
		p.i++
		return nil
	case 'u':
		r, err := p.parseUnicodeEscape()
		if err != nil {
			return err
		}
		*out = utf8.AppendRune(*out, r)
		return nil
	default:
		return p.err(p.i-1, "invalid escape sequence")
	}
}

func (p *parser) parseUnicodeEscape() (rune, error) {
	start := p.i - 1
	p.i++
	u, ok := hex4(p.src, p.i)
	if !ok {
		return 0, p.err(start, "invalid unicode escape")
	}
	p.i += 4
	r := rune(u)
	if 0xD800 <= r && r <= 0xDBFF {
		if p.i+6 > len(p.src) || p.src[p.i] != '\\' || p.src[p.i+1] != 'u' {
			return 0, p.err(start, "missing low surrogate")
		}
		p.i += 2
		lo, ok := hex4(p.src, p.i)
		if !ok {
			return 0, p.err(start, "invalid low surrogate")
		}
		p.i += 4
		lor := rune(lo)
		if lor < 0xDC00 || lor > 0xDFFF {
			return 0, p.err(start, "invalid low surrogate")
		}
		return utf16.DecodeRune(r, lor), nil
	}
	if 0xDC00 <= r && r <= 0xDFFF {
		return 0, p.err(start, "unexpected low surrogate")
	}
	return r, nil
}

func hex4(src []byte, i int) (uint16, bool) {
	if i+4 > len(src) {
		return 0, false
	}
	var v uint16
	for j := 0; j < 4; j++ {
		h, ok := fromHex(src[i+j])
		if !ok {
			return 0, false
		}
		v = v<<4 | uint16(h)
	}
	return v, true
}

func fromHex(c byte) (byte, bool) {
	switch {
	case '0' <= c && c <= '9':
		return c - '0', true
	case 'a' <= c && c <= 'f':
		return c - 'a' + 10, true
	case 'A' <= c && c <= 'F':
		return c - 'A' + 10, true
	default:
		return 0, false
	}
}

func isDigit(c byte) bool {
	return '0' <= c && c <= '9'
}

func isOneNine(c byte) bool {
	return '1' <= c && c <= '9'
}
