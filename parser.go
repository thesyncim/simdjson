package simdjson

import (
	"fmt"
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
	ZeroCopy bool

	// Preallocate validates once up front and allocates shared backing storage
	// for arrays and objects, reducing one allocation per non-empty container.
	Preallocate bool
}

// Parse parses src into an ordered JSON AST.
func Parse(src []byte) (Value, error) {
	return ParseOptions(src, Options{})
}

// ParseString parses s into an ordered JSON AST.
func ParseString(s string) (Value, error) {
	return Parse([]byte(s))
}

// ParseZeroCopy parses src and aliases unescaped strings and numbers into src.
// Callers must not mutate src for as long as the returned Value is used.
func ParseZeroCopy(src []byte) (Value, error) {
	return ParseOptions(src, Options{ZeroCopy: true})
}

// ParseMinAlloc parses src with zero-copy strings and shared container backing.
// Callers must not mutate src for as long as the returned Value is used.
func ParseMinAlloc(src []byte) (Value, error) {
	return ParseOptions(src, Options{ZeroCopy: true, Preallocate: true})
}

// ParseOptions parses src using opts.
func ParseOptions(src []byte, opts Options) (Value, error) {
	p := parser{src: src, maxDepth: opts.MaxDepth, zeroCopy: opts.ZeroCopy}
	if p.maxDepth <= 0 {
		p.maxDepth = defaultMaxDepth
	}
	if opts.Preallocate {
		layout, err := countLayout(src, p.maxDepth)
		if err != nil {
			return Value{}, err
		}
		p.layout = layout
		p.prealloc = true
		if layout.values > 0 {
			p.valueArena = make([]Value, layout.values)
		}
		if layout.members > 0 {
			p.memberArena = make([]Member, layout.members)
		}
	}
	p.skipSpace()
	v, err := p.parseValue(0)
	if err != nil {
		return Value{}, err
	}
	p.skipSpace()
	if p.i != len(src) {
		return Value{}, p.err(p.i, "unexpected data after top-level value")
	}
	return v, nil
}

type parser struct {
	src      []byte
	i        int
	maxDepth int
	zeroCopy bool

	layout       layout
	prealloc     bool
	containerPos int
	valueArena   []Value
	valuePos     int
	memberArena  []Member
	memberPos    int
	anyArena     *anyValueArena
}

func (p *parser) err(off int, msg string) error {
	return syntaxError(p.src, off, msg)
}

func (p *parser) skipSpace() {
	p.i = skipSpace(p.src, p.i)
}

func (p *parser) parseValue(depth int) (Value, error) {
	if depth > p.maxDepth {
		return Value{}, p.err(p.i, "maximum nesting depth exceeded")
	}
	if p.i >= len(p.src) {
		return Value{}, p.err(p.i, "expected value")
	}
	switch p.src[p.i] {
	case 'n':
		return p.parseLiteral("null", Value{kind: Null})
	case 't':
		return p.parseLiteral("true", Value{kind: Bool, b: true})
	case 'f':
		return p.parseLiteral("false", Value{kind: Bool})
	case '"':
		s, err := p.parseString()
		if err != nil {
			return Value{}, err
		}
		return Value{kind: String, s: s}, nil
	case '[':
		return p.parseArray(depth + 1)
	case '{':
		return p.parseObject(depth + 1)
	default:
		if p.src[p.i] == '-' || isDigit(p.src[p.i]) {
			return p.parseNumber()
		}
		return Value{}, p.err(p.i, fmt.Sprintf("unexpected byte %q while parsing value", p.src[p.i]))
	}
}

func (p *parser) parseLiteral(lit string, v Value) (Value, error) {
	if !matchStringAt(p.src, p.i, lit) {
		return Value{}, p.err(p.i, "invalid literal")
	}
	p.i += len(lit)
	return v, nil
}

func (p *parser) parseArray(depth int) (Value, error) {
	if depth > p.maxDepth {
		return Value{}, p.err(p.i, "maximum nesting depth exceeded")
	}
	p.i++
	var a []Value
	if p.prealloc {
		a = p.allocValues(p.nextContainerCount())
	}
	p.skipSpace()
	if p.i < len(p.src) && p.src[p.i] == ']' {
		p.i++
		return Value{kind: Array, a: a}, nil
	}

	if !p.prealloc {
		a = make([]Value, 0, 4)
	}
	idx := 0
	for {
		p.skipSpace()
		v, err := p.parseValue(depth)
		if err != nil {
			return Value{}, err
		}
		if !p.prealloc {
			a = append(a, v)
		} else {
			a[idx] = v
			idx++
		}
		p.skipSpace()
		if p.i >= len(p.src) {
			return Value{}, p.err(p.i, "unterminated array")
		}
		switch p.src[p.i] {
		case ',':
			p.i++
		case ']':
			p.i++
			return Value{kind: Array, a: a}, nil
		default:
			return Value{}, p.err(p.i, "expected comma or closing bracket in array")
		}
	}
}

func (p *parser) parseObject(depth int) (Value, error) {
	if depth > p.maxDepth {
		return Value{}, p.err(p.i, "maximum nesting depth exceeded")
	}
	p.i++
	var o []Member
	if p.prealloc {
		o = p.allocMembers(p.nextContainerCount())
	}
	p.skipSpace()
	if p.i < len(p.src) && p.src[p.i] == '}' {
		p.i++
		return Value{kind: Object, o: o}, nil
	}

	if !p.prealloc {
		o = make([]Member, 0, 4)
	}
	idx := 0
	for {
		p.skipSpace()
		if p.i >= len(p.src) || p.src[p.i] != '"' {
			return Value{}, p.err(p.i, "expected object key string")
		}
		key, err := p.parseString()
		if err != nil {
			return Value{}, err
		}
		p.skipSpace()
		if p.i >= len(p.src) || p.src[p.i] != ':' {
			return Value{}, p.err(p.i, "expected colon after object key")
		}
		p.i++
		p.skipSpace()
		v, err := p.parseValue(depth)
		if err != nil {
			return Value{}, err
		}
		if !p.prealloc {
			o = append(o, Member{Key: key, Value: v})
		} else {
			o[idx] = Member{Key: key, Value: v}
			idx++
		}
		p.skipSpace()
		if p.i >= len(p.src) {
			return Value{}, p.err(p.i, "unterminated object")
		}
		switch p.src[p.i] {
		case ',':
			p.i++
		case '}':
			p.i++
			return Value{kind: Object, o: o}, nil
		default:
			return Value{}, p.err(p.i, "expected comma or closing brace in object")
		}
	}
}

func (p *parser) parseNumber() (Value, error) {
	start := p.i
	end, msg := scanNumber(p.src, p.i)
	if msg != "" {
		return Value{}, p.err(start, msg)
	}
	p.i = end
	return Value{kind: Number, n: p.string(p.src[start:p.i])}, nil
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

func (p *parser) string(b []byte) string {
	if !p.zeroCopy || len(b) == 0 {
		return string(b)
	}
	return unsafe.String(unsafe.SliceData(b), len(b))
}

func ownedBytesString(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return unsafe.String(unsafe.SliceData(b), len(b))
}

func (p *parser) nextContainerCount() int {
	n := p.layout.container(p.containerPos)
	p.containerPos++
	return n
}

func (p *parser) allocValues(n int) []Value {
	if n == 0 {
		return nil
	}
	start := p.valuePos
	p.valuePos += n
	return p.valueArena[start:p.valuePos]
}

func (p *parser) allocMembers(n int) []Member {
	if n == 0 {
		return nil
	}
	start := p.memberPos
	p.memberPos += n
	return p.memberArena[start:p.memberPos]
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
