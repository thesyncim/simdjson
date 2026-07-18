package simd

import (
	"math/bits"
	"testing"
)

var stage1PortableBenchRecSink Stage1Rec
var stage1PortableBenchMaskSink uint64

func stage1EscapedCandidate(backslash uint64, carry *Stage1Carry) uint64 {
	carryEsc := carry.Escaped
	if backslash == 0 {
		carry.Escaped = 0
		return carryEsc
	}
	backslash &^= carryEsc
	followsEscape := backslash<<1 | carryEsc
	const evenBits = uint64(0x5555555555555555)
	oddSequenceStarts := backslash & ^(evenBits | followsEscape)
	sum, overflow := bits.Add64(oddSequenceStarts, backslash, 0)
	carry.Escaped = overflow
	return (evenBits ^ sum<<1) & followsEscape
}

func stage1RecAlgebraCandidate(m *Stage1Masks, st *Stage1Stream, r *Stage1Rec) {
	escaped := Stage1Escaped(m.Backslash, &st.Carry)
	quotes := m.Quote &^ escaped
	inStr := Stage1PrefixXOR(quotes, &st.Carry)
	outside := ^(inStr | quotes)
	openers := quotes & inStr
	cand := ^(m.Whitespace | m.Structural | m.Quote | inStr)
	starts := cand &^ (cand<<1 | st.Follows)
	st.Follows = cand >> 63
	r.Emit = (m.Structural|starts)&outside | openers
	r.Scalar = cand & outside
	r.EscInStr = escaped & inStr
	r.Bad = m.Control&(inStr|outside&^m.Whitespace) != 0
	r.WsOut = m.Whitespace & outside
	r.InStr = inStr
	r.NonASCII = m.NonASCII
}

func stage1RecFusedCandidate(m *Stage1Masks, st *Stage1Stream, r *Stage1Rec) {
	carryEsc := st.Carry.Escaped
	backslash := m.Backslash
	var escaped uint64
	if backslash == 0 {
		escaped = carryEsc
		carryEsc = 0
	} else {
		backslash &^= carryEsc
		followsEscape := backslash<<1 | carryEsc
		const evenBits = uint64(0x5555555555555555)
		oddSequenceStarts := backslash & ^evenBits & ^followsEscape
		sum, overflow := bits.Add64(oddSequenceStarts, backslash, 0)
		carryEsc = overflow
		escaped = (evenBits ^ sum<<1) & followsEscape
	}

	quotes := m.Quote &^ escaped
	inStr := quotes
	inStr ^= inStr << 1
	inStr ^= inStr << 2
	inStr ^= inStr << 4
	inStr ^= inStr << 8
	inStr ^= inStr << 16
	inStr ^= inStr << 32
	inStr ^= st.Carry.InString
	carryStr := uint64(int64(inStr) >> 63)

	outside := ^(inStr | quotes)
	openers := quotes & inStr
	cand := ^(m.Whitespace | m.Structural | m.Quote | inStr)
	starts := cand &^ (cand<<1 | st.Follows)
	st.Carry.Escaped = carryEsc
	st.Carry.InString = carryStr
	st.Follows = cand >> 63
	r.Emit = (m.Structural|starts)&outside | openers
	r.Scalar = cand & outside
	r.EscInStr = escaped & inStr
	r.Bad = m.Control&(inStr|outside&^m.Whitespace) != 0
	r.WsOut = m.Whitespace & outside
	r.InStr = inStr
	r.NonASCII = m.NonASCII
}

func stage1PortableBenchMasks() [32]Stage1Masks {
	doc := []byte("    {\"key\":\"value with words and escaped \\\\ slash\",\"n\":12345,\"ok\":true,\"items\":[1,2,3]},\n")
	var masks [32]Stage1Masks
	for block := range masks {
		var m Stage1Masks
		for lane := 0; lane < 64; lane++ {
			c := doc[(block*64+lane)%len(doc)]
			bit := uint64(1) << lane
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
		masks[block] = m
	}
	return masks
}

func TestStage1PortableCandidates(t *testing.T) {
	state := uint64(0x243f6a8885a308d3)
	var escapedCurrent, escapedCandidate Stage1Carry
	var streamCurrent, streamAlgebra, streamFused Stage1Stream
	for round := 0; round < 1_000_000; round++ {
		state ^= state << 13
		state ^= state >> 7
		state ^= state << 17
		backslash := state
		if round&7 != 0 {
			backslash &= state >> 17 & state >> 41
		}
		got := Stage1Escaped(backslash, &escapedCurrent)
		want := stage1EscapedCandidate(backslash, &escapedCandidate)
		if got != want || escapedCurrent != escapedCandidate {
			t.Fatalf("escaped round %d: current=(%#x,%+v) candidate=(%#x,%+v)", round, got, escapedCurrent, want, escapedCandidate)
		}

		m := Stage1Masks{
			Whitespace: state & (state >> 5),
			Structural: bits.RotateLeft64(state, 11) & (state >> 7),
			Quote:      bits.RotateLeft64(state, 23) & (state >> 13),
			Backslash:  backslash,
			Control:    bits.RotateLeft64(state, 37) & (state >> 3),
			NonASCII:   state>>63 != 0,
		}
		var current, algebra, fused Stage1Rec
		Stage1RecFromMasks(&m, &streamCurrent, &current)
		stage1RecAlgebraCandidate(&m, &streamAlgebra, &algebra)
		stage1RecFusedCandidate(&m, &streamFused, &fused)
		if current != algebra || current != fused || streamCurrent != streamAlgebra || streamCurrent != streamFused {
			t.Fatalf("record round %d diverged\ncurrent=%+v state=%+v\nalgebra=%+v state=%+v\nfused=%+v state=%+v", round, current, streamCurrent, algebra, streamAlgebra, fused, streamFused)
		}
	}
}

func BenchmarkStage1EscapedPortable(b *testing.B) {
	masks := stage1PortableBenchMasks()
	b.Run("current", func(b *testing.B) {
		var carry Stage1Carry
		var sink uint64
		b.SetBytes(64)
		for i := 0; i < b.N; i++ {
			sink ^= Stage1Escaped(masks[i&31].Backslash, &carry)
		}
		stage1PortableBenchMaskSink = sink ^ carry.Escaped
	})
	b.Run("candidate", func(b *testing.B) {
		var carry Stage1Carry
		var sink uint64
		b.SetBytes(64)
		for i := 0; i < b.N; i++ {
			sink ^= stage1EscapedCandidate(masks[i&31].Backslash, &carry)
		}
		stage1PortableBenchMaskSink = sink ^ carry.Escaped
	})
}

func BenchmarkStage1RecPortable(b *testing.B) {
	masks := stage1PortableBenchMasks()
	b.Run("current", func(b *testing.B) {
		var st Stage1Stream
		var rec Stage1Rec
		b.SetBytes(32 * 64)
		for n := 0; n < b.N; n++ {
			for i := range masks {
				Stage1RecFromMasks(&masks[i], &st, &rec)
			}
		}
		stage1PortableBenchRecSink = rec
	})
	b.Run("algebra", func(b *testing.B) {
		var st Stage1Stream
		var rec Stage1Rec
		b.SetBytes(32 * 64)
		for n := 0; n < b.N; n++ {
			for i := range masks {
				stage1RecAlgebraCandidate(&masks[i], &st, &rec)
			}
		}
		stage1PortableBenchRecSink = rec
	})
	b.Run("fused", func(b *testing.B) {
		var st Stage1Stream
		var rec Stage1Rec
		b.SetBytes(32 * 64)
		for n := 0; n < b.N; n++ {
			for i := range masks {
				stage1RecFusedCandidate(&masks[i], &st, &rec)
			}
		}
		stage1PortableBenchRecSink = rec
	})
}
