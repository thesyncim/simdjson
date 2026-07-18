package simd

import (
	"encoding/binary"
	"math/bits"
)

// The stage-1 masks and carry kernels are architecture-neutral. SIMD builds
// provide a vector Stage1Block; scalar builds use the SWAR classifier below.
// Routing may still leave the full stage-1 engine disabled when the scalar
// classifier does not beat the ordinary parser for a workload.

// Stage1Masks holds the per-64-byte-block classification. Bit i of each
// mask describes byte i of the block.
type Stage1Masks struct {
	Whitespace uint64 // space, tab, line feed, carriage return
	Structural uint64 // { } [ ] : ,
	Quote      uint64 // every raw quote; escaped quotes are removed by stage 1
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

const (
	stage1EvenBits      = uint64(0x5555555555555555)
	stage1ByteLow7      = uint64(0x7f7f7f7f7f7f7f7f)
	stage1ByteHigh      = uint64(0x8080808080808080)
	stage1ByteBit5      = uint64(0x2020202020202020)
	stage1ByteBit2      = uint64(0x0404040404040404)
	stage1CompressBytes = uint64(0x0002040810204081)
)

// stage1BlockPortable classifies one 64-byte block using eight independent
// SWAR words. The exact-zero primitive masks each lane to seven bits before
// adding, so carries cannot leak between bytes; unlike the usual has-zero
// trick, every returned bit is an exact lane result and is safe to materialize
// as a stage-1 mask.
func stage1BlockPortable(block *[64]byte, out *Stage1Masks) {
	var whitespace, structural, quote, backslash, control uint64
	var high uint64
	for word := 0; word < 8; word++ {
		x := binary.LittleEndian.Uint64(block[word*8:])

		q := stage1ByteEqExact(x, '"')
		slash := stage1ByteEqExact(x, '\\')
		ws := stage1ByteEqExact(x|stage1ByteBit2, '\r') |
			stage1ByteEqExact(x, '\n') |
			stage1ByteEqExact(x, ' ')
		structure := stage1ByteEqExact(x|stage1ByteBit5, '{') |
			stage1ByteEqExact(x|stage1ByteBit5, '}') |
			stage1ByteEqExact(x, ':') |
			stage1ByteEqExact(x, ',')
		ctrl := stage1ZeroByteMaskExact(x & 0xe0e0e0e0e0e0e0e0)

		shift := uint(word * 8)
		whitespace |= stage1CompressHighBytes(ws) << shift
		structural |= stage1CompressHighBytes(structure) << shift
		quote |= stage1CompressHighBytes(q) << shift
		backslash |= stage1CompressHighBytes(slash) << shift
		control |= stage1CompressHighBytes(ctrl) << shift
		high |= x & stage1ByteHigh
	}
	*out = Stage1Masks{
		Whitespace: whitespace,
		Structural: structural,
		Quote:      quote,
		Backslash:  backslash,
		Control:    control,
		NonASCII:   high != 0,
	}
}

func stage1ByteEqExact(x uint64, value byte) uint64 {
	return stage1ZeroByteMaskExact(x ^ uint64(value)*0x0101010101010101)
}

func stage1ZeroByteMaskExact(x uint64) uint64 {
	return ^(((x&stage1ByteLow7)+stage1ByteLow7)|x|stage1ByteLow7) & stage1ByteHigh
}

func stage1CompressHighBytes(x uint64) uint64 {
	return x * stage1CompressBytes >> 56
}

// Stage1Escaped resolves the bytes escaped by backslash runs and updates the
// block-boundary carry. The common paths avoid the full odd-run arithmetic:
// blocks without backslashes only forward a pending carry, while isolated
// backslashes directly produce their shifted target mask.
func Stage1Escaped(backslash uint64, carry *Stage1Carry) uint64 {
	carryEscaped := carry.Escaped
	if backslash == 0 {
		carry.Escaped = 0
		return carryEscaped
	}

	// A carry consumes a backslash in lane zero. Remove it before looking for
	// adjacent active backslashes; otherwise a boundary escape can make an
	// isolated run appear dense and can incorrectly start another escape.
	backslash &^= carryEscaped
	followsEscape := backslash<<1 | carryEscaped
	if backslash&followsEscape == 0 {
		carry.Escaped = backslash >> 63
		return followsEscape
	}

	// General odd-length backslash-run resolution from simdjson. Adding each
	// odd-positioned run start to the run mask propagates through that run;
	// the shifted sum then selects exactly the escaped target bytes.
	oddSequenceStarts := backslash & ^(stage1EvenBits | followsEscape)
	sequencesStartingOnEven, overflow := bits.Add64(oddSequenceStarts, backslash, 0)
	carry.Escaped = overflow
	return (stage1EvenBits ^ sequencesStartingOnEven<<1) & followsEscape
}

// Stage1PrefixXOR computes for each bit the parity of all bits at or below it;
// with the unescaped-quote mask as input the result marks string interiors
// from each opening quote through the byte before its closing quote. The carry
// flips the whole block when it starts inside a string.
func Stage1PrefixXOR(quotes uint64, carry *Stage1Carry) uint64 {
	// Quote-free blocks are common in long strings, whitespace, and numeric
	// arrays. Their output is exactly the incoming string state and the carry
	// cannot change, avoiding the six-instruction dependency chain below.
	if quotes == 0 {
		return carry.InString
	}

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
