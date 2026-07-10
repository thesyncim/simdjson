package simdjson

import "unsafe"

// validFast is the bool-only validation path. It keeps common nesting state
// inline and leaves detailed diagnostics and extreme depth to Validate. Small
// inputs first try the short-string scanner before the general one.
func validFast(src []byte) bool {
	const inlineDepth = 16
	var containers [inlineDepth]uint8
	n := len(src)
	short := n <= 64
	base := unsafe.Pointer(unsafe.SliceData(src))
	containerBase := unsafe.Pointer(&containers[0])
	i := skipSpaceFast(base, n, 0)
	depth := 0
	completed := false
	needObjectKey := false

	for {
		if !completed {
			if needObjectKey {
				i = skipSpaceFast(base, n, i)
				if i >= n || fastByteAt(base, i) != '"' {
					return false
				}
				var ok bool
				i, _, ok = scanJSONStringFast(src, base, i, short)
				if !ok {
					return false
				}
				i = skipSpaceFast(base, n, i)
				if i >= n || fastByteAt(base, i) != ':' {
					return false
				}
				i++
				needObjectKey = false
			}

			i = skipSpaceFast(base, n, i)
			if i >= n {
				return false
			}
			kind := uint8(0)
			switch fastByteAt(base, i) {
			case 'n':
				if i+4 > n || fastByteAt(base, i+1) != 'u' || fastByteAt(base, i+2) != 'l' || fastByteAt(base, i+3) != 'l' {
					return false
				}
				i += 4
			case 't':
				if i+4 > n || fastByteAt(base, i+1) != 'r' || fastByteAt(base, i+2) != 'u' || fastByteAt(base, i+3) != 'e' {
					return false
				}
				i += 4
			case 'f':
				if i+5 > n || fastByteAt(base, i+1) != 'a' || fastByteAt(base, i+2) != 'l' || fastByteAt(base, i+3) != 's' || fastByteAt(base, i+4) != 'e' {
					return false
				}
				i += 5
			case '"':
				var ok bool
				i, _, ok = scanJSONStringFast(src, base, i, short)
				if !ok {
					return false
				}
			case '[':
				kind = uint8(Array)
				i++
			case '{':
				kind = uint8(Object)
				i++
			default:
				if c := fastByteAt(base, i); c != '-' && !isDigit(c) {
					return false
				}
				var ok bool
				i, ok = scanNumberFast(base, n, i)
				if !ok {
					return false
				}
			}

			if kind == 0 {
				completed = true
			} else {
				if depth == inlineDepth {
					return Validate(src) == nil
				}
				fastStateSet(containerBase, depth, kind)
				depth++
				i = skipSpaceFast(base, n, i)
				close := byte(']')
				if kind == uint8(Object) {
					close = '}'
				}
				if i < n && fastByteAt(base, i) == close {
					i++
					depth--
					completed = true
				} else {
					needObjectKey = kind == uint8(Object)
					continue
				}
			}
		}

		for completed {
			if depth == 0 {
				return skipSpaceFast(base, n, i) == n
			}
			kind := fastStateAt(containerBase, depth-1)
			i = skipSpaceFast(base, n, i)
			if i >= n {
				return false
			}
			if kind == uint8(Array) {
				switch fastByteAt(base, i) {
				case ',':
					i++
					completed = false
				case ']':
					i++
					depth--
				default:
					return false
				}
			} else {
				switch fastByteAt(base, i) {
				case ',':
					i++
					needObjectKey = true
					completed = false
				case '}':
					i++
					depth--
				default:
					return false
				}
			}
		}
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
