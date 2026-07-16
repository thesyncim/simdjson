package simdjson

import "encoding/binary"

func isJSONWhitespace(c byte) bool {
	return c == ' ' || c == '\n' || c == '\r' || c == '\t'
}

// skipSpace returns the index of the first significant byte at or after i,
// consuming runs of eight spaces word-at-a-time so indented documents skip
// quickly. It must stay inlineable into every parser loop: the inlining
// budget is 80 and one call to a non-inlineable function costs 57 by
// itself, so almost any addition here de-inlines every call site.
func skipSpace(src []byte, i int) int {
	// The unsigned form of the guard lets the prover drop the bounds check
	// on src[i]: i is never negative here, and if it ever were the loop
	// would simply exit as it does today.
	for uint(i) < uint(len(src)) {
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

// skipSpaceIndent is skipSpace with one extra four-space step after a line
// feed, covering indentation shallower than the eight-space word run. The
// extra nodes make it too costly to inline everywhere skipSpace goes, so it
// serves only call sites that already branched on seeing whitespace —
// pretty-printed member and element boundaries — where the run is long
// enough to amortize it.
func skipSpaceIndent(src []byte, i int) int {
	for uint(i) < uint(len(src)) {
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
		if c == '\n' && i+4 <= len(src) && binary.LittleEndian.Uint32(src[i:]) == 0x20202020 {
			i += 4
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
