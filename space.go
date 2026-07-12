package simdjson

import "encoding/binary"

func skipSpace(src []byte, i int) int {
	for i < len(src) {
		c := src[i]
		if c > ' ' {
			return i
		}
		if c != ' ' && c != '\n' && c != '\r' && c != '\t' {
			return i
		}
		i++
		for i+8 <= len(src) && binary.LittleEndian.Uint64(src[i:]) == 0x2020202020202020 {
			i += 8
		}
	}
	return i
}

func matchStringAt(src []byte, i int, s string) bool {
	if len(src)-i < len(s) {
		return false
	}
	for j := 0; j < len(s); j++ {
		if src[i+j] != s[j] {
			return false
		}
	}
	return true
}
