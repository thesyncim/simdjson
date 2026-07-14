package simd

import "math/bits"

// The stage-1 carry resolution is pure uint64 bit arithmetic and works on
// every build, so the two carry kernels and the block-classification and
// boundary-carry types live here without a build tag. Only Stage1Block and
// Stage1Enabled, which need the SIMD byte classifier, remain per-arch; on a
// scalar build Stage1Enabled reports false while Escaped and PrefixXOR stay
// available for callers that thread the masks in from another source.

// Stage1Masks holds the per-64-byte-block classification. Bit i of each
// mask describes byte i of the block.
type Stage1Masks struct {
	Whitespace uint64 // space, tab, line feed, carriage return
	Structural uint64 // { } [ ] : ,
	Quote      uint64 // unescaped quotes only
	Backslash  uint64 // every backslash
	Control    uint64 // bytes below 0x20 (whitespace included)
	NonASCII   bool   // any byte at or above 0x80 in the block
}

// Stage1Carry threads block-boundary state between consecutive blocks.
// The zero value is the document-start state.
type Stage1Carry struct {
	Escaped  uint64 // bit 0: first byte of next block is escaped
	InString uint64 // all-ones when the next block starts inside a string
}

// Stage1Escaped resolves which characters are escaped by backslash
// sequences, updating the carry. This is the branchless odd-length
// backslash-run algorithm from simdjson.
func Stage1Escaped(backslash uint64, carry *Stage1Carry) uint64 {
	if backslash == 0 {
		escaped := carry.Escaped
		carry.Escaped = 0
		return escaped
	}
	backslash &^= carry.Escaped
	followsEscape := backslash<<1 | carry.Escaped
	const evenBits = uint64(0x5555555555555555)
	oddSequenceStarts := backslash & ^evenBits & ^followsEscape
	sequencesStartingOnEven, overflow := bits.Add64(oddSequenceStarts, backslash, 0)
	carry.Escaped = overflow
	invert := sequencesStartingOnEven << 1
	return (evenBits ^ invert) & followsEscape
}

// Stage1PrefixXOR computes for each bit the parity of all bits at or
// below it; with the unescaped-quote mask as input the result marks
// string interiors from each opening quote through the byte before its
// closing quote. The carry flips the whole block when it starts inside a
// string.
func Stage1PrefixXOR(quotes uint64, carry *Stage1Carry) uint64 {
	m := quotes
	m ^= m << 1
	m ^= m << 2
	m ^= m << 4
	m ^= m << 8
	m ^= m << 16
	m ^= m << 32
	m ^= carry.InString
	carry.InString = uint64(int64(m) >> 63)
	return m
}
