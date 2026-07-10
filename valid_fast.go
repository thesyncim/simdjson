package simdjson

import (
	"encoding/binary"
	"math/bits"
	"unsafe"
)

// validFast is the bool-only validation path: a recursive descent machine
// with an inline word-at-a-time fast path for short clean strings. Depth is
// bounded like Validate.
func validFast(src []byte) bool {
	n := len(src)
	base := unsafe.Pointer(unsafe.SliceData(src))
	i, c := nextSignificantFast(base, n, 0)
	if i >= n {
		return false
	}
	i, ok := validValueFast(src, base, n, i, c, 0)
	if !ok {
		return false
	}
	return skipSpaceFast(base, n, i) == n
}

// validStringFast consumes a string starting at the opening quote, taking one
// SWAR word inline for the short clean case before the general scanner.
func validStringFast(src []byte, base unsafe.Pointer, n, i int) (int, bool) {
	start := i + 1
	if start+8 <= n {
		if m := stringSpecialMask(binary.LittleEndian.Uint64(src[start:])); m != 0 {
			j := start + bits.TrailingZeros64(m)/8
			if fastByteAt(base, j) == '"' {
				return j + 1, true
			}
		}
	}
	end, _, ok := scanJSONStringFastLong(src, base, i)
	return end, ok
}

func validValueFast(src []byte, base unsafe.Pointer, n, i int, c byte, depth int) (int, bool) {
	switch c {
	case '{':
		if depth >= defaultMaxDepth {
			return i, false
		}
		i++
		i, c = nextSignificantFast(base, n, i)
		if i >= n {
			return i, false
		}
		if c == '}' {
			return i + 1, true
		}
		for {
			if c != '"' {
				return i, false
			}
			var ok bool
			i, ok = validStringFast(src, base, n, i)
			if !ok {
				return i, false
			}
			i, c = nextSignificantFast(base, n, i)
			if i >= n || c != ':' {
				return i, false
			}
			i, c = nextSignificantFast(base, n, i+1)
			if i >= n {
				return i, false
			}
			i, ok = validValueFast(src, base, n, i, c, depth+1)
			if !ok {
				return i, false
			}
			i, c = nextSignificantFast(base, n, i)
			if i >= n {
				return i, false
			}
			if c == ',' {
				i, c = nextSignificantFast(base, n, i+1)
				if i >= n {
					return i, false
				}
				continue
			}
			if c == '}' {
				return i + 1, true
			}
			return i, false
		}
	case '[':
		if depth >= defaultMaxDepth {
			return i, false
		}
		i++
		i, c = nextSignificantFast(base, n, i)
		if i >= n {
			return i, false
		}
		if c == ']' {
			return i + 1, true
		}
		for {
			var ok bool
			i, ok = validValueFast(src, base, n, i, c, depth+1)
			if !ok {
				return i, false
			}
			i, c = nextSignificantFast(base, n, i)
			if i >= n {
				return i, false
			}
			if c == ',' {
				i, c = nextSignificantFast(base, n, i+1)
				if i >= n {
					return i, false
				}
				continue
			}
			if c == ']' {
				return i + 1, true
			}
			return i, false
		}
	case '"':
		return validStringFast(src, base, n, i)
	case 't':
		if i+4 > n || fastByteAt(base, i+1) != 'r' || fastByteAt(base, i+2) != 'u' || fastByteAt(base, i+3) != 'e' {
			return i, false
		}
		return i + 4, true
	case 'f':
		if i+5 > n || fastByteAt(base, i+1) != 'a' || fastByteAt(base, i+2) != 'l' || fastByteAt(base, i+3) != 's' || fastByteAt(base, i+4) != 'e' {
			return i, false
		}
		return i + 5, true
	case 'n':
		if i+4 > n || fastByteAt(base, i+1) != 'u' || fastByteAt(base, i+2) != 'l' || fastByteAt(base, i+3) != 'l' {
			return i, false
		}
		return i + 4, true
	default:
		if c != '-' && !isDigit(c) {
			return i, false
		}
		return scanNumberFast(base, n, i)
	}
}

func scanNumberFast(base unsafe.Pointer, n, i int) (int, bool) {
	if fastByteAt(base, i) == '-' {
		i++
		if i >= n {
			return i, false
		}
	}
	if fastByteAt(base, i) == '0' {
		i++
	} else if isOneNine(fastByteAt(base, i)) {
		for i++; i < n && isDigit(fastByteAt(base, i)); i++ {
		}
	} else {
		return i, false
	}
	if i < n && fastByteAt(base, i) == '.' {
		i++
		if i >= n || !isDigit(fastByteAt(base, i)) {
			return i, false
		}
		for i++; i < n && isDigit(fastByteAt(base, i)); i++ {
		}
	}
	if i < n && (fastByteAt(base, i) == 'e' || fastByteAt(base, i) == 'E') {
		i++
		if i < n && (fastByteAt(base, i) == '+' || fastByteAt(base, i) == '-') {
			i++
		}
		if i >= n || !isDigit(fastByteAt(base, i)) {
			return i, false
		}
		for i++; i < n && isDigit(fastByteAt(base, i)); i++ {
		}
	}
	return i, true
}

func scanJSONStringFast(src []byte, base unsafe.Pointer, i int, short bool) (int, bool, bool) {
	if short {
		if end, ok := scanShortJSONString(base, len(src), i); ok {
			return end, false, true
		}
	}
	return scanJSONStringFastLong(src, base, i)
}

func scanJSONStringFastLong(src []byte, base unsafe.Pointer, i int) (int, bool, bool) {
	escaped := false
	i++
	for {
		j := scanStringSpecial(src, i)
		if j >= len(src) {
			return len(src), escaped, false
		}
		switch c := fastByteAt(base, j); {
		case c == '"':
			return j + 1, escaped, true
		case c == '\\':
			escaped = true
			j++
			if j >= len(src) {
				return j, escaped, false
			}
			switch fastByteAt(base, j) {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
				i = j + 1
			case 'u':
				u, ok := hex4(src, j+1)
				if !ok {
					return j, escaped, false
				}
				i = j + 5
				switch {
				case 0xD800 <= u && u <= 0xDBFF:
					if i+6 > len(src) || fastByteAt(base, i) != '\\' || fastByteAt(base, i+1) != 'u' {
						return i, escaped, false
					}
					lo, ok := hex4(src, i+2)
					if !ok || lo < 0xDC00 || lo > 0xDFFF {
						return i, escaped, false
					}
					i += 6
				case 0xDC00 <= u && u <= 0xDFFF:
					return i, escaped, false
				}
			default:
				return j, escaped, false
			}
		case c < 0x20:
			return j, escaped, false
		default:
			next, bad := scanStringUnicodeRun(src, j)
			if bad >= 0 {
				return bad, escaped, false
			}
			i = next
		}
	}
}

func scanShortJSONString(base unsafe.Pointer, n, quote int) (int, bool) {
	limit := quote + 9
	if limit > n {
		limit = n
	}
	for i := quote + 1; i < limit; i++ {
		c := fastByteAt(base, i)
		if c == '"' {
			return i + 1, true
		}
		if c == '\\' || c < 0x20 || c >= 0x80 {
			return 0, false
		}
	}
	return 0, false
}

// nextSignificantFast skips insignificant whitespace and returns the first
// significant byte, saving the reload every caller performed afterwards.
func nextSignificantFast(base unsafe.Pointer, n, i int) (int, byte) {
	for i < n {
		c := fastByteAt(base, i)
		if c > ' ' || (c != ' ' && c != '\n' && c != '\r' && c != '\t') {
			return i, c
		}
		i++
	}
	return i, 0
}

func skipSpaceFast(base unsafe.Pointer, n, i int) int {
	for i < n {
		c := fastByteAt(base, i)
		if c > ' ' || (c != ' ' && c != '\n' && c != '\r' && c != '\t') {
			return i
		}
		i++
	}
	return i
}

func fastByteAt(base unsafe.Pointer, index int) byte {
	return *(*byte)(unsafe.Add(base, uintptr(index)))
}

func fastStateAt(base unsafe.Pointer, index int) uint8 {
	return *(*uint8)(unsafe.Add(base, uintptr(index)))
}

func fastStateSet(base unsafe.Pointer, index int, state uint8) {
	*(*uint8)(unsafe.Add(base, uintptr(index))) = state
}
