//go:build goexperiment.simd && (arm64 || amd64)

package simd

import "math/bits"

// Stage-1 structural scanning in the style of the simdjson paper: each
// 64-byte block is classified into one-bit-per-byte masks from which the
// caller derives string extents and structural positions.
//
// These kernels are verified groundwork without a production consumer. A
// position-driven validator was built on them and measured about twice as
// slow as the recursive-descent scanner: profiling put only a small share
// of the time in the masks, with the rest in position extraction and
// cursor dispatch, so that walker was removed. The economics flip only
// with a consumer near one nanosecond per emitted position.

// Stage1Enabled reports whether this build provides the stage-1 kernel.
func Stage1Enabled() bool { return true }

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
	Follows  uint64 // bit 63 was significant (scalar token byte)
}

// stage1ClassLo and stage1ClassHi classify bytes by nibble lookup. A byte
// with low nibble l and high nibble h has class bits lo[l] & hi[h]. The
// bit products are exact: each bit's low-set x high-set cross product
// contains only the intended characters.
//
// bit 0: space (0x20)      bit 1: tab, LF, CR
// bit 2: comma             bit 3: colon
// bit 4: [ and {           bit 5: ] and }
var stage1ClassLo = [16]uint8{
	1 << 0, 0, 0, 0, 0, 0, 0, 0,
	0, 1 << 1, 1<<1 | 1<<3, 1 << 4, 1 << 2, 1<<1 | 1<<5, 0, 0,
}

var stage1ClassHi = [16]uint8{
	1 << 1, 0, 1<<0 | 1<<2, 1 << 3, 0, 1<<4 | 1<<5, 0, 1<<4 | 1<<5,
	0, 0, 0, 0, 0, 0, 0, 0,
}

const (
	stage1WhitespaceBits = 1<<0 | 1<<1
	stage1StructuralBits = 1<<2 | 1<<3 | 1<<4 | 1<<5
)

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
