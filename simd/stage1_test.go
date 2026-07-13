//go:build goexperiment.simd && (arm64 || amd64)

package simd

import (
	"testing"
)

func stage1RefMasks(block *[64]byte) Stage1Masks {
	var m Stage1Masks
	for i, c := range block {
		bit := uint64(1) << i
		switch c {
		case ' ', '\t', '\n', '\r':
			m.Whitespace |= bit
		case '{', '}', '[', ']', ':', ',':
			m.Structural |= bit
		case '"':
			m.Quote |= bit
		case '\\':
			m.Backslash |= bit
		}
		if c < 0x20 {
			m.Control |= bit
		}
		if c >= 0x80 {
			m.NonASCII = true
		}
	}
	return m
}

func TestStage1BlockAllByteValues(t *testing.T) {
	for b := 0; b <= 0xff; b++ {
		var block [64]byte
		for i := range block {
			block[i] = 'a'
		}
		// Place the probe byte at several lane positions, including both
		// halves of every vector.
		for _, at := range []int{0, 7, 8, 15, 16, 31, 32, 47, 48, 63} {
			block[at] = byte(b)
		}
		var got Stage1Masks
		Stage1Block(&block, &got)
		want := stage1RefMasks(&block)
		if got != want {
			t.Fatalf("Stage1Block(byte=0x%02x) = %+v, want %+v", b, got, want)
		}
		for _, at := range []int{0, 7, 8, 15, 16, 31, 32, 47, 48, 63} {
			block[at] = 'a'
		}
	}
}

func TestStage1BlockRandom(t *testing.T) {
	state := uint64(0x9e3779b97f4a7c15)
	interesting := []byte{'"', '\\', '{', '}', '[', ']', ':', ',', ' ', '\t', '\n', '\r', 0x00, 0x1f, 0x7f, 0x80, 0xff, 'a', '0'}
	var block [64]byte
	for round := 0; round < 20000; round++ {
		for i := range block {
			state ^= state << 13
			state ^= state >> 7
			state ^= state << 17
			block[i] = interesting[state%uint64(len(interesting))]
		}
		var got Stage1Masks
		Stage1Block(&block, &got)
		if want := stage1RefMasks(&block); got != want {
			t.Fatalf("Stage1Block(%q) = %+v, want %+v", block, got, want)
		}
	}
}

// stage1RefEscaped walks bits with an explicit escape flag.
func stage1RefEscaped(backslash uint64, escaped bool) (uint64, bool) {
	var out uint64
	for i := 0; i < 64; i++ {
		bit := uint64(1) << i
		if escaped {
			out |= bit
			escaped = false
			continue
		}
		if backslash&bit != 0 {
			escaped = true
		}
	}
	return out, escaped
}

func TestStage1EscapedMatchesReference(t *testing.T) {
	state := uint64(0x243f6a8885a308d3)
	var carry Stage1Carry
	refEscaped := false
	for round := 0; round < 200000; round++ {
		state ^= state << 13
		state ^= state >> 7
		state ^= state << 17
		bs := state
		switch round % 4 {
		case 1:
			bs = 0
		case 2:
			bs = ^uint64(0)
		case 3:
			bs &= 0xff00ff00ff00ff0f
		}
		got := Stage1Escaped(bs, &carry)
		want, wantEscaped := stage1RefEscaped(bs, refEscaped)
		refEscaped = wantEscaped
		if got != want {
			t.Fatalf("round %d: Stage1Escaped(%#x) = %#x, want %#x", round, bs, got, want)
		}
		if (carry.Escaped != 0) != refEscaped {
			t.Fatalf("round %d: carry = %#x, want escaped=%v", round, carry.Escaped, refEscaped)
		}
	}
}

func stage1RefPrefixXOR(quotes uint64, in bool) (uint64, bool) {
	var out uint64
	for i := 0; i < 64; i++ {
		bit := uint64(1) << i
		if quotes&bit != 0 {
			in = !in
		}
		if in {
			out |= bit
		}
	}
	return out, in
}

func TestStage1PrefixXORMatchesReference(t *testing.T) {
	state := uint64(0x13198a2e03707344)
	var carry Stage1Carry
	refIn := false
	for round := 0; round < 200000; round++ {
		state ^= state << 13
		state ^= state >> 7
		state ^= state << 17
		q := state & state >> 3 & state >> 11
		got := Stage1PrefixXOR(q, &carry)
		want, wantIn := stage1RefPrefixXOR(q, refIn)
		refIn = wantIn
		if got != want {
			t.Fatalf("round %d: Stage1PrefixXOR(%#x) = %#x, want %#x", round, q, got, want)
		}
		if (carry.InString != 0) != refIn {
			t.Fatalf("round %d: carry = %#x, want in=%v", round, carry.InString, refIn)
		}
	}
}
