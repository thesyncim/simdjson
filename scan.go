package simdjson

import simdkernels "github.com/thesyncim/simdjson/simd"

func scanStringSpecial(src []byte, i int) int {
	return simdkernels.IndexStringSpecial(src, i)
}

func scanStringSpecialLong(src []byte, i int) int {
	return simdkernels.IndexStringSpecialLong(src, i)
}

func scanStringSyntax(src []byte, i int) int {
	return simdkernels.IndexStringSyntax(src, i)
}

func scanEncodedHTMLSpecialFast(src []byte, i int) int {
	return simdkernels.IndexHTMLStringSpecial(src, i)
}

func scanEncodedHTMLSyntaxFast(src []byte, i int) int {
	return simdkernels.IndexHTMLStringSyntax(src, i)
}

func scanUnicodeEscapeRun(src []byte, i int) (int, bool) {
	return simdkernels.ScanUnicodeEscapeRun(src, i)
}

func validUTF8Fast(src []byte) bool {
	return simdkernels.ValidUTF8(src)
}

func validUTF8NoLineSeparatorFast(src []byte) bool {
	return simdkernels.ValidUTF8NoLineSeparator(src)
}

func hasJSONLineSeparatorFast(src []byte, start int) bool {
	return simdkernels.HasJSONLineSeparator(src, start)
}

func scanStringUnicodeRun(src []byte, i int) (next, bad int) {
	return simdkernels.ScanStringUnicodeRun(src, i)
}

func stringSpecialMask(word uint64) uint64 {
	return simdkernels.StringSpecialMask64(word)
}

func stringSyntaxMask(word uint64) uint64 {
	return simdkernels.StringSyntaxMask64(word)
}

func byteEqMask(word uint64, value byte) uint64 {
	return simdkernels.ByteEqualMask64(word, value)
}
