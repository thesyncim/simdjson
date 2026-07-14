package simd

import (
	"encoding/binary"
	"math/bits"
	"unicode/utf8"
)

func scanStringSpecialScalar(src []byte, i int) int {
	return scanStringSpecialScalarUntil(src, i, len(src))
}

func scanStringSpecialScalarUntil(src []byte, i, limit int) int {
	for i+16 <= limit {
		x := binary.LittleEndian.Uint64(src[i:])
		if m := stringSpecialMask(x); m != 0 {
			return i + bits.TrailingZeros64(m)/8
		}
		x = binary.LittleEndian.Uint64(src[i+8:])
		if m := stringSpecialMask(x); m != 0 {
			return i + 8 + bits.TrailingZeros64(m)/8
		}
		i += 16
	}
	if i+8 <= limit {
		x := binary.LittleEndian.Uint64(src[i:])
		if m := stringSpecialMask(x); m != 0 {
			return i + bits.TrailingZeros64(m)/8
		}
		i += 8
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
	limit := len(src)
	for i+16 <= limit {
		x := binary.LittleEndian.Uint64(src[i:])
		if m := stringSyntaxMask(x); m != 0 {
			return i + bits.TrailingZeros64(m)/8
		}
		x = binary.LittleEndian.Uint64(src[i+8:])
		if m := stringSyntaxMask(x); m != 0 {
			return i + 8 + bits.TrailingZeros64(m)/8
		}
		i += 16
	}
	if i+8 <= limit {
		x := binary.LittleEndian.Uint64(src[i:])
		if m := stringSyntaxMask(x); m != 0 {
			return i + bits.TrailingZeros64(m)/8
		}
		i += 8
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
	for i+8 <= len(src) {
		x := binary.LittleEndian.Uint64(src[i:])
		m := stringSyntaxMask(x) | byteEqMask(x, '<') | byteEqMask(x, '>') | byteEqMask(x, '&')
		if m != 0 {
			return i + bits.TrailingZeros64(m)/8
		}
		i += 8
	}
	for i < len(src) {
		c := src[i]
		if c == '"' || c == '\\' || c == '<' || c == '>' || c == '&' || c < 0x20 {
			return i
		}
		i++
	}
	return len(src)
}

func scanEncodedHTMLSpecialScalar(src []byte, i int) int {
	for i+8 <= len(src) {
		x := binary.LittleEndian.Uint64(src[i:])
		m := stringSpecialMask(x) | byteEqMask(x, '<') | byteEqMask(x, '>') | byteEqMask(x, '&')
		if m != 0 {
			return i + bits.TrailingZeros64(m)/8
		}
		i += 8
	}
	for i < len(src) {
		c := src[i]
		if c == '"' || c == '\\' || c == '<' || c == '>' || c == '&' || c < 0x20 || c >= 0x80 {
			return i
		}
		i++
	}
	return len(src)
}

func hasJSONLineSeparatorScalar(src []byte, start int) bool {
	for i := start; i+2 < len(src); i++ {
		if src[i] == 0xe2 && src[i+1] == 0x80 && (src[i+2] == 0xa8 || src[i+2] == 0xa9) {
			return true
		}
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

func stringChunkHasSpecial(x uint64) bool {
	return stringSpecialMask(x) != 0
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
	// Setting bit 1 folds '<' (0x3C) onto '>' (0x3E), so one equality probe
	// covers both angle brackets; no other byte maps onto 0x3E that way.
	return stringSpecialMask(x) |
		byteEqMask(x, '&') |
		byteEqMask(x|0x0202020202020202, '>')
}

func hasByte(x uint64, b byte) bool {
	return byteEqMask(x, b) != 0
}

func byteEqMask(x uint64, b byte) uint64 {
	const highBits = 0x8080808080808080
	y := x ^ (uint64(b) * 0x0101010101010101)
	return (y - 0x0101010101010101) & ^y & highBits
}
