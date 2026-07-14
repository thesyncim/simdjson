package simdjson

import (
	"unicode/utf16"
	"unicode/utf8"
)

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
