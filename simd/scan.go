package simd

import "unsafe"

// Unchecked exposes scanner entry points for hot callers that have already
// proved 0 <= start <= len(src). Violating that precondition may panic or cause
// an out-of-bounds vector access. The ordinary package functions below clamp
// start and are the safe default.
var Unchecked UncheckedScans

// UncheckedScans is the precondition-based scanner surface exposed through
// Unchecked. Its methods omit public-boundary offset normalization.
type UncheckedScans struct{}

// IndexStringSpecial is the unchecked form of the package function.
func (UncheckedScans) IndexStringSpecial(src []byte, start int) int {
	return scanStringSpecial(src, start)
}

// IndexStringSpecialScalarUntil scans [start, limit) with the portable
// word-at-a-time kernel. It requires 0 <= start <= limit <= len(src).
func (UncheckedScans) IndexStringSpecialScalarUntil(src []byte, start, limit int) int {
	return scanStringSpecialScalarUntil(src, start, limit)
}

// IndexStringSpecialLong is the unchecked form of the package function.
func (UncheckedScans) IndexStringSpecialLong(src []byte, start int) int {
	return scanStringSpecialLong(src, start)
}

// IndexStringSyntax is the unchecked form of the package function.
func (UncheckedScans) IndexStringSyntax(src []byte, start int) int {
	return scanStringSyntax(src, start)
}

// IndexHTMLStringSpecial is the unchecked form of the package function.
func (UncheckedScans) IndexHTMLStringSpecial(src []byte, start int) int {
	return scanEncodedHTMLSpecialFast(src, start)
}

// IndexHTMLStringSyntax is the unchecked form of the package function.
func (UncheckedScans) IndexHTMLStringSyntax(src []byte, start int) int {
	return scanEncodedHTMLSyntaxFast(src, start)
}

// ScanUnicodeEscapeRun is the unchecked form of the package function.
func (UncheckedScans) ScanUnicodeEscapeRun(src []byte, start int) (end int, ok bool) {
	return scanUnicodeEscapeRun(src, start)
}

// HasJSONLineSeparator is the unchecked form of the package function.
func (UncheckedScans) HasJSONLineSeparator(src []byte, start int) bool {
	return hasJSONLineSeparatorFast(src, start)
}

// ScanStringUnicodeRun is the unchecked form of the package function.
func (UncheckedScans) ScanStringUnicodeRun(src []byte, start int) (next, bad int) {
	return scanStringUnicodeRun(src, start)
}

// IndexStringSpecial returns the first byte at or after start that is a quote,
// backslash, control byte, or non-ASCII byte. It returns len(src) when none is
// present. Start is clamped to the bounds of src.
func IndexStringSpecial(src []byte, start int) int {
	return scanStringSpecial(src, normalizeStart(src, start))
}

// IndexStringSpecialLong bypasses the short-input probe and enters the selected
// long scanner directly. Start is clamped to the bounds of src.
func IndexStringSpecialLong(src []byte, start int) int {
	return scanStringSpecialLong(src, normalizeStart(src, start))
}

// IndexStringSyntax returns the first quote, backslash, or control byte at or
// after start. Non-ASCII bytes are allowed. Start is clamped to the bounds of
// src.
func IndexStringSyntax(src []byte, start int) int {
	return scanStringSyntax(src, normalizeStart(src, start))
}

// IndexHTMLStringSpecial is IndexStringSpecial with '<', '>', and '&' added to
// the stop set used by HTML-safe JSON encoders. Start is clamped to the bounds
// of src.
func IndexHTMLStringSpecial(src []byte, start int) int {
	return scanEncodedHTMLSpecialFast(src, normalizeStart(src, start))
}

// IndexHTMLStringSyntax is IndexStringSyntax with '<', '>', and '&' added to
// the stop set used by HTML-safe JSON encoders. Start is clamped to the bounds
// of src.
func IndexHTMLStringSyntax(src []byte, start int) int {
	return scanEncodedHTMLSyntaxFast(src, normalizeStart(src, start))
}

// ScanUnicodeEscapeRun validates complete vector-sized groups of contiguous
// JSON \uXXXX escapes. It returns start when a scalar decision is required.
// Start is clamped to the bounds of src.
func ScanUnicodeEscapeRun(src []byte, start int) (end int, ok bool) {
	return scanUnicodeEscapeRun(src, normalizeStart(src, start))
}

// ValidUTF8 reports whether src consists entirely of valid UTF-8.
func ValidUTF8(src []byte) bool {
	return validUTF8Fast(src)
}

// ValidUTF8NoLineSeparator reports whether src is valid UTF-8 and contains
// neither U+2028 nor U+2029.
func ValidUTF8NoLineSeparator(src []byte) bool {
	return validUTF8NoLineSeparatorFast(src)
}

// HasJSONLineSeparator reports whether U+2028 or U+2029 occurs at or after
// start. Start is clamped to the bounds of src.
func HasJSONLineSeparator(src []byte, start int) bool {
	return hasJSONLineSeparatorFast(src, normalizeStart(src, start))
}

// StringSpecialMask64 returns a high-bit byte mask for quote, backslash,
// control, and non-ASCII bytes in a little-endian eight-byte word.
func StringSpecialMask64(word uint64) uint64 {
	return stringSpecialMask(word)
}

// StringSyntaxMask64 returns a high-bit byte mask for quote, backslash, and
// control bytes in a little-endian eight-byte word.
func StringSyntaxMask64(word uint64) uint64 {
	return stringSyntaxMask(word)
}

// HTMLStringSpecialMask64 is StringSpecialMask64 with '<', '>', and '&' added
// to the flagged set used by HTML-safe JSON encoders.
func HTMLStringSpecialMask64(word uint64) uint64 {
	return htmlStringSpecialMask(word)
}

// ByteEqualMask64 returns a high-bit byte mask for bytes equal to value in a
// little-endian eight-byte word.
func ByteEqualMask64(word uint64, value byte) uint64 {
	return byteEqMask(word, value)
}

// ScanStringUnicodeRun scans a non-ASCII string run and returns the next byte
// to inspect and the first malformed UTF-8 byte, or -1 when the run is valid.
// Start is clamped to the bounds of src.
func ScanStringUnicodeRun(src []byte, start int) (next, bad int) {
	return scanStringUnicodeRun(src, normalizeStart(src, start))
}

func normalizeStart(src []byte, start int) int {
	if uint(start) <= uint(len(src)) {
		return start
	}
	if start < 0 {
		return 0
	}
	return len(src)
}

// CopyStringPrefix copies bytes that do not require JSON string escaping and
// returns the index of the first quote, backslash, control, or non-ASCII byte.
// It returns len(src) when the complete string was copied and -1 when dst is
// too short or the slices overlap.
func CopyStringPrefix(dst, src []byte) int {
	if len(dst) < len(src) || slicesOverlap(dst[:len(src)], src) {
		return -1
	}
	return copyStringPrefix(dst[:len(src)], src)
}

// CopyHTMLStringPrefix is CopyStringPrefix with '<', '>', and '&' included in
// the escape set.
func CopyHTMLStringPrefix(dst, src []byte) int {
	if len(dst) < len(src) || slicesOverlap(dst[:len(src)], src) {
		return -1
	}
	return copyHTMLStringPrefix(dst[:len(src)], src)
}

func slicesOverlap(a, b []byte) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	a0 := uintptr(unsafe.Pointer(unsafe.SliceData(a)))
	b0 := uintptr(unsafe.Pointer(unsafe.SliceData(b)))
	return a0 < b0+uintptr(len(b)) && b0 < a0+uintptr(len(a))
}
