package simdjson

import (
	"sort"
	"strings"
	"unicode/utf8"
)

// MarshalJSON implements json.Marshaler.
func (v Value) MarshalJSON() ([]byte, error) {
	return v.AppendJSON(nil), nil
}

// AppendJSON appends compact JSON for v to dst.
func (v Value) AppendJSON(dst []byte) []byte {
	switch v.kind {
	case Null:
		return append(dst, "null"...)
	case Bool:
		if v.b {
			return append(dst, "true"...)
		}
		return append(dst, "false"...)
	case Number:
		return append(dst, v.n...)
	case String:
		return appendJSONString(dst, v.s)
	case Array:
		dst = append(dst, '[')
		for i := range v.a {
			if i > 0 {
				dst = append(dst, ',')
			}
			dst = v.a[i].AppendJSON(dst)
		}
		return append(dst, ']')
	case Object:
		dst = append(dst, '{')
		for i, m := range v.o {
			if i > 0 {
				dst = append(dst, ',')
			}
			dst = appendJSONString(dst, m.Key)
			dst = append(dst, ':')
			dst = m.Value.AppendJSON(dst)
		}
		return append(dst, '}')
	default:
		return append(dst, "null"...)
	}
}

// Indent parses src and returns pretty JSON using prefix and indent.
func Indent(src []byte, prefix, indent string) ([]byte, error) {
	return AppendIndent(nil, src, prefix, indent)
}

// AppendIndent validates src and appends pretty JSON using prefix and indent.
// Like json.Indent, string and number tokens are copied from src verbatim, so
// escape spelling and number literals are preserved exactly; only structural
// whitespace is inserted.
func AppendIndent(dst, src []byte, prefix, indent string) ([]byte, error) {
	return appendIndentBytes(dst, src, prefix, indent, defaultMaxDepth)
}

// AppendIndent appends pretty JSON for v to dst.
func (v Value) AppendIndent(dst []byte, prefix, indent string) []byte {
	return appendIndentValue(dst, v, prefix, indent, 0)
}

func appendIndentValue(dst []byte, v Value, prefix, indent string, depth int) []byte {
	switch v.kind {
	case Array:
		if len(v.a) == 0 {
			return append(dst, "[]"...)
		}
		dst = append(dst, '[')
		for i := range v.a {
			if i > 0 {
				dst = append(dst, ',')
			}
			dst = append(dst, '\n')
			dst = append(dst, prefix...)
			dst = append(dst, strings.Repeat(indent, depth+1)...)
			dst = appendIndentValue(dst, v.a[i], prefix, indent, depth+1)
		}
		dst = append(dst, '\n')
		dst = append(dst, prefix...)
		dst = append(dst, strings.Repeat(indent, depth)...)
		return append(dst, ']')
	case Object:
		if len(v.o) == 0 {
			return append(dst, "{}"...)
		}
		dst = append(dst, '{')
		for i, m := range v.o {
			if i > 0 {
				dst = append(dst, ',')
			}
			dst = append(dst, '\n')
			dst = append(dst, prefix...)
			dst = append(dst, strings.Repeat(indent, depth+1)...)
			dst = appendJSONString(dst, m.Key)
			dst = append(dst, ": "...)
			dst = appendIndentValue(dst, m.Value, prefix, indent, depth+1)
		}
		dst = append(dst, '\n')
		dst = append(dst, prefix...)
		dst = append(dst, strings.Repeat(indent, depth)...)
		return append(dst, '}')
	default:
		return v.AppendJSON(dst)
	}
}

func appendJSONString(dst []byte, text string) []byte {
	const hex = "0123456789abcdef"
	dst = append(dst, '"')
	start := 0
	for i := 0; i < len(text); {
		c := text[i]
		if c >= utf8.RuneSelf {
			_, size := utf8.DecodeRuneInString(text[i:])
			if size != 1 {
				i += size
				continue
			}
			dst = append(dst, text[start:i]...)
			dst = append(dst, '\\', 'u', 'f', 'f', 'f', 'd')
			i++
			start = i
			continue
		}
		if c >= 0x20 && c != '"' && c != '\\' {
			i++
			continue
		}

		dst = append(dst, text[start:i]...)
		switch c {
		case '"', '\\':
			dst = append(dst, '\\', c)
		case '\b':
			dst = append(dst, '\\', 'b')
		case '\f':
			dst = append(dst, '\\', 'f')
		case '\n':
			dst = append(dst, '\\', 'n')
		case '\r':
			dst = append(dst, '\\', 'r')
		case '\t':
			dst = append(dst, '\\', 't')
		default:
			dst = append(dst, '\\', 'u', '0', '0', hex[c>>4], hex[c&0xF])
		}
		i++
		start = i
	}
	dst = append(dst, text[start:]...)
	return append(dst, '"')
}

// Canonicalize sorts object members recursively and emits compact JSON.
func Canonicalize(src []byte) ([]byte, error) {
	return AppendCanonicalize(nil, src)
}

// AppendCanonicalize sorts object members recursively and appends compact JSON.
func AppendCanonicalize(dst, src []byte) ([]byte, error) {
	v, err := ParseOptions(src, Options{ZeroCopy: true})
	if err != nil {
		return dst, err
	}
	return canonical(v).AppendJSON(dst), nil
}

func canonical(v Value) Value {
	switch v.kind {
	case Array:
		a := make([]Value, len(v.a))
		for i := range v.a {
			a[i] = canonical(v.a[i])
		}
		v.a = a
	case Object:
		o := make([]Member, len(v.o))
		for i := range v.o {
			o[i] = Member{Key: v.o[i].Key, Value: canonical(v.o[i].Value)}
		}
		sort.SliceStable(o, func(i, j int) bool {
			return o[i].Key < o[j].Key
		})
		v.o = o
	}
	return v
}
