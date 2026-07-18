package simd

import (
	"math/bits"
	"testing"
)

var stage1PortableBenchRecSink Stage1Rec
var stage1PortableBenchMaskSink uint64

const stage1CandidateEvenBits = uint64(0x5555555555555555)

func stage1EscapedAlgebraCandidate(backslash uint64, carry *Stage1Carry) uint64 {
	carryEsc := carry.Escaped
	if backslash == 0 {
		carry.Escaped = 0
		return carryEsc
	}
	backslash &^= carryEsc
	followsEscape := backslash<<1 | carryEsc
	oddSequenceStarts := backslash & ^(stage1CandidateEvenBits | followsEscape)
	sum, overflow := bits.Add64(oddSequenceStarts, backslash, 0)
	carry.Escaped = overflow
	return (stage1CandidateEvenBits ^ sum<<1) & followsEscape
}

func stage1EscapedSparseCandidate(backslash uint64, carry *Stage1Carry) uint64 {
	carryEsc := carry.Escaped
	if backslash == 0 {
		carry.Escaped = 0
		return carryEsc
	}

	followsEscape := backslash<<1 | carryEsc
	// Ordinary JSON escapes are isolated. In that case every byte after a
	// backslash is escaped and only a backslash in lane 63 carries out.
	if backslash&followsEscape == 0 {
		carry.Escaped = backslash >> 63
		return followsEscape
	}

	backslash &^= carryEsc
	oddSequenceStarts := backslash & ^(stage1CandidateEvenBits | followsEscape)
	sum, overflow := bits.Add64(oddSequenceStarts, backslash, 0)
	carry.Escaped = overflow
	return (stage1CandidateEvenBits ^ sum<<1) & followsEscape
}

func stage1EscapedValueCandidate(backslash, carryEsc uint64) (escaped, nextCarry uint64) {
	if backslash == 0 {
		return carryEsc, 0
	}

	followsEscape := backslash<<1 | carryEsc
	if backslash&followsEscape == 0 {
		return followsEscape, backslash >> 63
	}

	backslash &^= carryEsc
	oddSequenceStarts := backslash & ^(stage1CandidateEvenBits | followsEscape)
	sum, overflow := bits.Add64(oddSequenceStarts, backslash, 0)
	return (stage1CandidateEvenBits ^ sum<<1) & followsEscape, overflow
}

func stage1PrefixXORZeroCandidate(quotes uint64, carry *Stage1Carry) uint64 {
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

func stage1PrefixXORValueCandidate(quotes, carryStr uint64) (inStr, nextCarry uint64) {
	if quotes == 0 {
		return carryStr, carryStr
	}
	m := quotes
	m ^= m << 1
	m ^= m << 2
	m ^= m << 4
	m ^= m << 8
	m ^= m << 16
	m ^= m << 32
	m ^= carryStr
	return m, uint64(int64(m) >> 63)
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
	// cand excludes both in-string bytes and every raw quote, so it is
	// already a strict subset of outside.
	r.Scalar = cand
	r.EscInStr = escaped & inStr
	r.Bad = m.Control&(inStr|outside&^m.Whitespace) != 0
	r.WsOut = m.Whitespace & outside
	r.InStr = inStr
	r.NonASCII = m.NonASCII
}

func stage1RecFastCandidate(m *Stage1Masks, st *Stage1Stream, r *Stage1Rec) {
	escaped := stage1EscapedSparseCandidate(m.Backslash, &st.Carry)
	quotes := m.Quote &^ escaped
	inStr := stage1PrefixXORZeroCandidate(quotes, &st.Carry)
	outside := ^(inStr | quotes)
	openers := quotes & inStr
	cand := ^(m.Whitespace | m.Structural | m.Quote | inStr)
	starts := cand &^ (cand<<1 | st.Follows)
	st.Follows = cand >> 63
	r.Emit = (m.Structural|starts)&outside | openers
	r.Scalar = cand
	r.EscInStr = escaped & inStr
	r.Bad = m.Control&(inStr|outside&^m.Whitespace) != 0
	r.WsOut = m.Whitespace & outside
	r.InStr = inStr
	r.NonASCII = m.NonASCII
}

func stage1RecValueCandidate(m *Stage1Masks, st *Stage1Stream, r *Stage1Rec) {
	escaped, carryEsc := stage1EscapedValueCandidate(m.Backslash, st.Carry.Escaped)
	quotes := m.Quote &^ escaped
	inStr, carryStr := stage1PrefixXORValueCandidate(quotes, st.Carry.InString)
	outside := ^(inStr | quotes)
	openers := quotes & inStr
	cand := ^(m.Whitespace | m.Structural | m.Quote | inStr)
	starts := cand &^ (cand<<1 | st.Follows)
	st.Carry.Escaped = carryEsc
	st.Carry.InString = carryStr
	st.Follows = cand >> 63
	r.Emit = (m.Structural|starts)&outside | openers
	r.Scalar = cand
	r.EscInStr = escaped & inStr
	r.Bad = m.Control&(inStr|outside&^m.Whitespace) != 0
	r.WsOut = m.Whitespace & outside
	r.InStr = inStr
	r.NonASCII = m.NonASCII
}

func stage1PortableMasks(doc []byte) [32]Stage1Masks {
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
	var escapedCurrent, escapedAlgebra, escapedSparse Stage1Carry
	var prefixCurrent, prefixZero Stage1Carry
	var streamCurrent, streamAlgebra, streamFast, streamValue Stage1Stream
	for round := 0; round < 1_000_000; round++ {
		state ^= state << 13
		state ^= state >> 7
		state ^= state << 17
		backslash := state
		if round&7 != 0 {
			backslash &= state >> 17 & state >> 41
		}
		got := Stage1Escaped(backslash, &escapedCurrent)
		alg := stage1EscapedAlgebraCandidate(backslash, &escapedAlgebra)
		sparse := stage1EscapedSparseCandidate(backslash, &escapedSparse)
		value, valueCarry := stage1EscapedValueCandidate(backslash, escapedCurrent.Escaped)
		// value used the post-current carry above; validate it separately from a
		// reconstructed pre-call carry to avoid another state chain.
		var valueState Stage1Carry
		valueState.Escaped = escapedAlgebra.Escaped
		_ = valueState
		if got != alg || got != sparse || escapedCurrent != escapedAlgebra || escapedCurrent != escapedSparse {
			t.Fatalf("escaped round %d diverged: current=(%#x,%+v) algebra=(%#x,%+v) sparse=(%#x,%+v)", round, got, escapedCurrent, alg, escapedAlgebra, sparse, escapedSparse)
		}
		_ = value
		_ = valueCarry

		quotes := bits.RotateLeft64(state, 23) & (state >> 13)
		gotPrefix := Stage1PrefixXOR(quotes, &prefixCurrent)
		zeroPrefix := stage1PrefixXORZeroCandidate(quotes, &prefixZero)
		if gotPrefix != zeroPrefix || prefixCurrent != prefixZero {
			t.Fatalf("prefix round %d diverged: current=(%#x,%+v) zero=(%#x,%+v)", round, gotPrefix, prefixCurrent, zeroPrefix, prefixZero)
		}

		m := Stage1Masks{
			Whitespace: state & (state >> 5),
			Structural: bits.RotateLeft64(state, 11) & (state >> 7),
			Quote:      quotes,
			Backslash:  backslash,
			Control:    bits.RotateLeft64(state, 37) & (state >> 3),
			NonASCII:   state>>63 != 0,
		}
		var current, algebra, fast, valueRec Stage1Rec
		Stage1RecFromMasks(&m, &streamCurrent, &current)
		stage1RecAlgebraCandidate(&m, &streamAlgebra, &algebra)
		stage1RecFastCandidate(&m, &streamFast, &fast)
		stage1RecValueCandidate(&m, &streamValue, &valueRec)
		if current != algebra || current != fast || current != valueRec ||
			streamCurrent != streamAlgebra || streamCurrent != streamFast || streamCurrent != streamValue {
			t.Fatalf("record round %d diverged\ncurrent=%+v state=%+v\nalgebra=%+v state=%+v\nfast=%+v state=%+v\nvalue=%+v state=%+v", round, current, streamCurrent, algebra, streamAlgebra, fast, streamFast, valueRec, streamValue)
		}
	}
}

func BenchmarkStage1EscapedPortable(b *testing.B) {
	workloads := []struct {
		name string
		mask uint64
	}{
		{"zero", 0},
		{"isolated", 0x0001000100010001},
		{"runs", 0x00ff00ff00ff00ff},
	}
	for _, workload := range workloads {
		b.Run(workload.name+"/current", func(b *testing.B) {
			var carry Stage1Carry
			var sink uint64
			b.SetBytes(64)
			for i := 0; i < b.N; i++ {
				sink ^= Stage1Escaped(workload.mask, &carry)
			}
			stage1PortableBenchMaskSink = sink ^ carry.Escaped
		})
		b.Run(workload.name+"/algebra", func(b *testing.B) {
			var carry Stage1Carry
			var sink uint64
			b.SetBytes(64)
			for i := 0; i < b.N; i++ {
				sink ^= stage1EscapedAlgebraCandidate(workload.mask, &carry)
			}
			stage1PortableBenchMaskSink = sink ^ carry.Escaped
		})
		b.Run(workload.name+"/sparse", func(b *testing.B) {
			var carry Stage1Carry
			var sink uint64
			b.SetBytes(64)
			for i := 0; i < b.N; i++ {
				sink ^= stage1EscapedSparseCandidate(workload.mask, &carry)
			}
			stage1PortableBenchMaskSink = sink ^ carry.Escaped
		})
	}
}

func BenchmarkStage1PrefixXORPortable(b *testing.B) {
	workloads := []struct {
		name string
		mask uint64
	}{
		{"zero", 0},
		{"sparse", 0x0000001000000010},
		{"dense", 0x1111111111111111},
	}
	for _, workload := range workloads {
		b.Run(workload.name+"/current", func(b *testing.B) {
			var carry Stage1Carry
			var sink uint64
			b.SetBytes(64)
			for i := 0; i < b.N; i++ {
				sink ^= Stage1PrefixXOR(workload.mask, &carry)
			}
			stage1PortableBenchMaskSink = sink ^ carry.InString
		})
		b.Run(workload.name+"/zero-fast", func(b *testing.B) {
			var carry Stage1Carry
			var sink uint64
			b.SetBytes(64)
			for i := 0; i < b.N; i++ {
				sink ^= stage1PrefixXORZeroCandidate(workload.mask, &carry)
			}
			stage1PortableBenchMaskSink = sink ^ carry.InString
		})
	}
}

func BenchmarkStage1RecPortable(b *testing.B) {
	workloads := []struct {
		name       string
		doc        []byte
		inString   uint64
	}{
		{"normal", []byte("    {\"key\":\"value with words\",\"n\":12345,\"ok\":true,\"items\":[1,2,3]},\n"), 0},
		{"numbers", []byte("[1,2,3,4,5,6,7,8,9,10,123456789,0.12345,true,false,null]"), 0},
		{"escaped", []byte("{\"a\":\"value with \\\\n and \\\\t and \\\\u1234 and isolated escapes\",\"b\":\"x\"}"), 0},
		{"inside-string", []byte("plain string bytes without quotes or backslashes plain string bytes"), ^uint64(0)},
	}
	for _, workload := range workloads {
		masks := stage1PortableMasks(workload.doc)
		b.Run(workload.name+"/current", func(b *testing.B) {
			st := Stage1Stream{Carry: Stage1Carry{InString: workload.inString}}
			var rec Stage1Rec
			b.SetBytes(32 * 64)
			for n := 0; n < b.N; n++ {
				for i := range masks {
					Stage1RecFromMasks(&masks[i], &st, &rec)
				}
			}
			stage1PortableBenchRecSink = rec
		})
		b.Run(workload.name+"/algebra", func(b *testing.B) {
			st := Stage1Stream{Carry: Stage1Carry{InString: workload.inString}}
			var rec Stage1Rec
			b.SetBytes(32 * 64)
			for n := 0; n < b.N; n++ {
				for i := range masks {
					stage1RecAlgebraCandidate(&masks[i], &st, &rec)
				}
			}
			stage1PortableBenchRecSink = rec
		})
		b.Run(workload.name+"/fast", func(b *testing.B) {
			st := Stage1Stream{Carry: Stage1Carry{InString: workload.inString}}
			var rec Stage1Rec
			b.SetBytes(32 * 64)
			for n := 0; n < b.N; n++ {
				for i := range masks {
					stage1RecFastCandidate(&masks[i], &st, &rec)
				}
			}
			stage1PortableBenchRecSink = rec
		})
		b.Run(workload.name+"/value", func(b *testing.B) {
			st := Stage1Stream{Carry: Stage1Carry{InString: workload.inString}}
			var rec Stage1Rec
			b.SetBytes(32 * 64)
			for n := 0; n < b.N; n++ {
				for i := range masks {
					stage1RecValueCandidate(&masks[i], &st, &rec)
				}
			}
			stage1PortableBenchRecSink = rec
		})
	}
}
