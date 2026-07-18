package simd

import (
	"bytes"
	"encoding/binary"
	"math/bits"
	"unicode/utf8"
)

const (
	htmlQuoteAmpFold     = 0x0404040404040404
	htmlAngleBracketFold = 0x0202020202020202
)

func scanStringSpecialScalar(src []byte, i int) int {
	return scanStringSpecialScalarUntil(src, i, len(src))
}

func scanStringSpecialScalarUntil(src []byte, i, limit int) int {
	start := i
	for i+32 <= limit {
		m0 := stringSpecialMask(binary.LittleEndian.Uint64(src[i:]))
		m1 := stringSpecialMask(binary.LittleEndian.Uint64(src[i+8:]))
		m2 := stringSpecialMask(binary.LittleEndian.Uint64(src[i+16:]))
		m3 := stringSpecialMask(binary.LittleEndian.Uint64(src[i+24:]))
		if m0|m1|m2|m3 != 0 {
			if m0 != 0 {
				return i + bits.TrailingZeros64(m0)/8
			}
			if m1 != 0 {
				return i + 8 + bits.TrailingZeros64(m1)/8
			}
			if m2 != 0 {
				return i + 16 + bits.TrailingZeros64(m2)/8
			}
			return i + 24 + bits.TrailingZeros64(m3)/8
		}
		i += 32
	}
	for i+8 <= limit {
		m := stringSpecialMask(binary.LittleEndian.Uint64(src[i:]))
		if m != 0 {
			return i + bits.TrailingZeros64(m)/8
		}
		i += 8
	}
	if i == limit {
		return limit
	}
	if tail := limit - 8; tail >= start {
		// The overlapped bytes before i were already proved clean by a prior
		// word. A match in the final word is therefore necessarily at or after i.
		if m := stringSpecialMask(binary.LittleEndian.Uint64(src[tail:])); m != 0 {
			return tail + bits.TrailingZeros64(m)/8
		}
		return limit
	}
	for i < limit {
		c := src[i]
		if c == '"' || c == '\\' || c < 0x20 || c >= 0x80 {
			return i
		}
		i++
	}
	return limit
}

func scanStringSyntaxScalar(src []byte, i int) int {
	start := i
	limit := len(src)
	for i+32 <= limit {
		m0 := stringSyntaxMask(binary.LittleEndian.Uint64(src[i:]))
		m1 := stringSyntaxMask(binary.LittleEndian.Uint64(src[i+8:]))
		m2 := stringSyntaxMask(binary.LittleEndian.Uint64(src[i+16:]))
		m3 := stringSyntaxMask(binary.LittleEndian.Uint64(src[i+24:]))
		if m0|m1|m2|m3 != 0 {
			if m0 != 0 {
				return i + bits.TrailingZeros64(m0)/8
			}
			if m1 != 0 {
				return i + 8 + bits.TrailingZeros64(m1)/8
			}
			if m2 != 0 {
				return i + 16 + bits.TrailingZeros64(m2)/8
			}
			return i + 24 + bits.TrailingZeros64(m3)/8
		}
		i += 32
	}
	for i+8 <= limit {
		m := stringSyntaxMask(binary.LittleEndian.Uint64(src[i:]))
		if m != 0 {
			return i + bits.TrailingZeros64(m)/8
		}
		i += 8
	}
	if i == limit {
		return limit
	}
	if tail := limit - 8; tail >= start {
		if m := stringSyntaxMask(binary.LittleEndian.Uint64(src[tail:])); m != 0 {
			return tail + bits.TrailingZeros64(m)/8
		}
		return limit
	}
	for i < limit {
		c := src[i]
		if c == '"' || c == '\\' || c < 0x20 {
			return i
		}
		i++
	}
	return limit
}

func scanEncodedHTMLSyntaxScalar(src []byte, i int) int {
	start := i
	limit := len(src)
	for i+16 <= limit {
		x0 := binary.LittleEndian.Uint64(src[i:])
		x1 := binary.LittleEndian.Uint64(src[i+8:])
		m0 := byteEqMask(x0|htmlQuoteAmpFold, '&') |
			byteEqMask(x0, '\\') |
			((x0 - 0x2020202020202020) & ^x0 & 0x8080808080808080) |
			byteEqMask(x0|htmlAngleBracketFold, '>')
		m1 := byteEqMask(x1|htmlQuoteAmpFold, '&') |
			byteEqMask(x1, '\\') |
			((x1 - 0x2020202020202020) & ^x1 & 0x8080808080808080) |
			byteEqMask(x1|htmlAngleBracketFold, '>')
		if m0|m1 != 0 {
			if m0 != 0 {
				return i + bits.TrailingZeros64(m0)/8
			}
			return i + 8 + bits.TrailingZeros64(m1)/8
		}
		i += 16
	}
	for i+8 <= limit {
		x := binary.LittleEndian.Uint64(src[i:])
		m := byteEqMask(x|htmlQuoteAmpFold, '&') |
			byteEqMask(x, '\\') |
			((x - 0x2020202020202020) & ^x & 0x8080808080808080) |
			byteEqMask(x|htmlAngleBracketFold, '>')
		if m != 0 {
			return i + bits.TrailingZeros64(m)/8
		}
		i += 8
	}
	if i == limit {
		return limit
	}
	if tail := limit - 8; tail >= start {
		x := binary.LittleEndian.Uint64(src[tail:])
		m := byteEqMask(x|htmlQuoteAmpFold, '&') |
			byteEqMask(x, '\\') |
			((x - 0x2020202020202020) & ^x & 0x8080808080808080) |
			byteEqMask(x|htmlAngleBracketFold, '>')
		if m != 0 {
			return tail + bits.TrailingZeros64(m)/8
		}
		return limit
	}
	for i < limit {
		c := src[i]
		if c == '"' || c == '\\' || c == '<' || c == '>' || c == '&' || c < 0x20 {
			return i
		}
		i++
	}
	return limit
}

func scanEncodedHTMLSpecialScalar(src []byte, i int) int {
	start := i
	limit := len(src)
	for i+16 <= limit {
		x0 := binary.LittleEndian.Uint64(src[i:])
		x1 := binary.LittleEndian.Uint64(src[i+8:])
		m0 := htmlStringSpecialMask(x0)
		m1 := htmlStringSpecialMask(x1)
		if m0|m1 != 0 {
			if m0 != 0 {
				return i + bits.TrailingZeros64(m0)/8
			}
			return i + 8 + bits.TrailingZeros64(m1)/8
		}
		i += 16
	}
	for i+8 <= limit {
		x := binary.LittleEndian.Uint64(src[i:])
		if m := htmlStringSpecialMask(x); m != 0 {
			return i + bits.TrailingZeros64(m)/8
		}
		i += 8
	}
	if i == limit {
		return limit
	}
	if tail := limit - 8; tail >= start {
		if m := htmlStringSpecialMask(binary.LittleEndian.Uint64(src[tail:])); m != 0 {
			return tail + bits.TrailingZeros64(m)/8
		}
		return limit
	}
	for i < limit {
		c := src[i]
		if c == '"' || c == '\\' || c == '<' || c == '>' || c == '&' || c < 0x20 || c >= 0x80 {
			return i
		}
		i++
	}
	return limit
}

func hasJSONLineSeparatorScalar(src []byte, start int) bool {
	src = src[start:]
	offset := bytes.IndexByte(src, 0xe2)
	if offset < 0 {
		return false
	}
	i := offset
	if i+2 < len(src) && src[i+1] == 0x80 && (src[i+2] == 0xa8 || src[i+2] == 0xa9) {
		return true
	}
	i++

	for i+10 <= len(src) {
		window := src[i : i+10]
		candidates := byteEqMask(binary.LittleEndian.Uint64(window), 0xe2)
		for candidates != 0 {
			offset := bits.TrailingZeros64(candidates) / 8
			if offset >= 8 {
				break
			}
			if window[offset+1] == 0x80 && (window[offset+2] == 0xa8 || window[offset+2] == 0xa9) {
				return true
			}
			candidates &= candidates - 1
		}
		i += 8
	}
	for i+2 < len(src) {
		if src[i] == 0xe2 && src[i+1] == 0x80 && (src[i+2] == 0xa8 || src[i+2] == 0xa9) {
			return true
		}
		i++
	}
	return false
}

func invalidUTF8Index(src []byte, start, end int) int {
	if validUTF8Fast(src[start:end]) {
		return -1
	}
	for start < end {
		r, n := utf8.DecodeRune(src[start:end])
		if r == utf8.RuneError && n == 1 {
			return start
		}
		start += n
	}
	return end
}

func scanStringUnicodeRun(src []byte, i int) (next, bad int) {
	if len(src)-i < 32 {
		r, n := utf8.DecodeRune(src[i:])
		if r == utf8.RuneError && n == 1 {
			return i, i
		}
		return i + n, -1
	}
	next = scanStringSyntax(src, i)
	return next, invalidUTF8Index(src, i, next)
}

func stringSpecialMask(x uint64) uint64 {
	const highBits = 0x8080808080808080
	return byteEqMask(x, '"') |
		byteEqMask(x, '\\') |
		((x - 0x2020202020202020) & ^x & highBits) |
		(x & highBits)
}

func stringSyntaxMask(x uint64) uint64 {
	const highBits = 0x8080808080808080
	return byteEqMask(x, '"') |
		byteEqMask(x, '\\') |
		((x - 0x2020202020202020) & ^x & highBits)
}

func htmlStringSpecialMask(x uint64) uint64 {
	const highBits = 0x8080808080808080
	return byteEqMask(x|htmlQuoteAmpFold, '&') |
		byteEqMask(x, '\\') |
		((x - 0x2020202020202020) & ^x & highBits) |
		(x & highBits) |
		byteEqMask(x|htmlAngleBracketFold, '>')
}

func byteEqMask(x uint64, b byte) uint64 {
	const highBits = 0x8080808080808080
	y := x ^ (uint64(b) * 0x0101010101010101)
	return (y - 0x0101010101010101) & ^y & highBits
}
