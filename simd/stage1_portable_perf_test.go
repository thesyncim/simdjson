package simd

import (
	"math/bits"
	"testing"
)

var stage1PortableBenchRecSink Stage1Rec
var stage1PortableBenchMaskSink uint64

func stage1EscapedBaseline(backslash uint64, carry *Stage1Carry) uint64 {
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

func stage1PrefixXORBaseline(quotes uint64, carry *Stage1Carry) uint64 {
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

func stage1RecBaseline(m *Stage1Masks, st *Stage1Stream, r *Stage1Rec) {
	escaped := stage1EscapedBaseline(m.Backslash, &st.Carry)
	quotes := m.Quote &^ escaped
	inStr := stage1PrefixXORBaseline(quotes, &st.Carry)
	closers := quotes &^ inStr
	openers := quotes & inStr
	outside := ^(inStr | closers)
	cand := ^(m.Whitespace | m.Structural | m.Quote | inStr)
	starts := cand &^ (cand<<1 | st.Follows)
	st.Follows = cand >> 63
	r.Emit = m.Structural&outside | openers | starts&outside
	r.Scalar = cand & outside
	r.EscInStr = escaped & inStr
	r.Bad = m.Control&inStr|m.Control&outside&^m.Whitespace != 0
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
	var escapedBaseline, escapedFinal Stage1Carry
	var prefixBaseline, prefixFinal Stage1Carry
	var streamBaseline, streamFinal Stage1Stream
	for round := 0; round < 1_000_000; round++ {
		state ^= state << 13
		state ^= state >> 7
		state ^= state << 17
		backslash := state
		if round&7 != 0 {
			backslash &= state >> 17 & state >> 41
		}
		baselineEscaped := stage1EscapedBaseline(backslash, &escapedBaseline)
		finalEscaped := Stage1Escaped(backslash, &escapedFinal)
		if baselineEscaped != finalEscaped || escapedBaseline != escapedFinal {
			t.Fatalf("escaped round %d: baseline=(%#x,%+v) final=(%#x,%+v)",
				round, baselineEscaped, escapedBaseline, finalEscaped, escapedFinal)
		}

		quotes := bits.RotateLeft64(state, 23) & (state >> 13)
		if round%5 == 0 {
			quotes = 0
		}
		baselinePrefix := stage1PrefixXORBaseline(quotes, &prefixBaseline)
		finalPrefix := Stage1PrefixXOR(quotes, &prefixFinal)
		if baselinePrefix != finalPrefix || prefixBaseline != prefixFinal {
			t.Fatalf("prefix round %d: baseline=(%#x,%+v) final=(%#x,%+v)",
				round, baselinePrefix, prefixBaseline, finalPrefix, prefixFinal)
		}

		m := Stage1Masks{
			Whitespace: state & (state >> 5),
			Structural: bits.RotateLeft64(state, 11) & (state >> 7),
			Quote:      quotes,
			Backslash:  backslash,
			Control:    bits.RotateLeft64(state, 37) & (state >> 3),
			NonASCII:   state>>63 != 0,
		}
		var baselineRec, finalRec Stage1Rec
		stage1RecBaseline(&m, &streamBaseline, &baselineRec)
		Stage1RecFromMasks(&m, &streamFinal, &finalRec)
		if baselineRec != finalRec || streamBaseline != streamFinal {
			t.Fatalf("record round %d diverged\nbaseline=%+v state=%+v\nfinal=%+v state=%+v",
				round, baselineRec, streamBaseline, finalRec, streamFinal)
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
		b.Run(workload.name+"/baseline", func(b *testing.B) {
			var carry Stage1Carry
			var sink uint64
			b.SetBytes(64)
			for i := 0; i < b.N; i++ {
				sink ^= stage1EscapedBaseline(workload.mask, &carry)
			}
			stage1PortableBenchMaskSink = sink ^ carry.Escaped
		})
		b.Run(workload.name+"/final", func(b *testing.B) {
			var carry Stage1Carry
			var sink uint64
			b.SetBytes(64)
			for i := 0; i < b.N; i++ {
				sink ^= Stage1Escaped(workload.mask, &carry)
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
		b.Run(workload.name+"/baseline", func(b *testing.B) {
			var carry Stage1Carry
			var sink uint64
			b.SetBytes(64)
			for i := 0; i < b.N; i++ {
				sink ^= stage1PrefixXORBaseline(workload.mask, &carry)
			}
			stage1PortableBenchMaskSink = sink ^ carry.InString
		})
		b.Run(workload.name+"/final", func(b *testing.B) {
			var carry Stage1Carry
			var sink uint64
			b.SetBytes(64)
			for i := 0; i < b.N; i++ {
				sink ^= Stage1PrefixXOR(workload.mask, &carry)
			}
			stage1PortableBenchMaskSink = sink ^ carry.InString
		})
	}
}

func BenchmarkStage1RecPortable(b *testing.B) {
	workloads := []struct {
		name     string
		doc      []byte
		inString uint64
	}{
		{"normal", []byte("    {\"key\":\"value with words\",\"n\":12345,\"ok\":true,\"items\":[1,2,3]},\n"), 0},
		{"numbers", []byte("[1,2,3,4,5,6,7,8,9,10,123456789,0.12345,true,false,null]"), 0},
		{"escaped", []byte("{\"a\":\"value with \\\\n and \\\\t and \\\\u1234 and isolated escapes\",\"b\":\"x\"}"), 0},
		{"inside-string", []byte("plain string bytes without quotes or backslashes plain string bytes"), ^uint64(0)},
	}
	for _, workload := range workloads {
		masks := stage1PortableMasks(workload.doc)
		b.Run(workload.name+"/baseline", func(b *testing.B) {
			st := Stage1Stream{Carry: Stage1Carry{InString: workload.inString}}
			var rec Stage1Rec
			b.SetBytes(32 * 64)
			for n := 0; n < b.N; n++ {
				for i := range masks {
					stage1RecBaseline(&masks[i], &st, &rec)
				}
			}
			stage1PortableBenchRecSink = rec
		})
		b.Run(workload.name+"/final", func(b *testing.B) {
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
	}
}
