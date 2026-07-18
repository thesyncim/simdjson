package benchmarks

import (
	"math/bits"
	"strings"
	"testing"

	simdkernels "github.com/thesyncim/simdjson/simd"
	stdlibcorpus "github.com/thesyncim/simdjson/tests/stdlib"
)

const portableEvenBits = uint64(0x5555555555555555)

type portableCorpus struct {
	name  string
	bytes int64
	masks []simdkernels.Stage1Masks
}

func loadPortableCorpora(tb testing.TB) []portableCorpus {
	tb.Helper()
	if !simdkernels.Stage1Enabled() {
		tb.Skip("stage-1 classifier unavailable")
	}
	corpora := make([]portableCorpus, 0, len(stdlibcorpus.Names))
	for _, name := range stdlibcorpus.Names {
		src, err := stdlibcorpus.Read(name)
		if err != nil {
			tb.Fatal(err)
		}
		nblocks := (len(src) + 63) / 64
		masks := make([]simdkernels.Stage1Masks, nblocks)
		for block := 0; block < nblocks; block++ {
			var bytes [64]byte
			for i := range bytes {
				bytes[i] = ' '
			}
			start := block * 64
			copy(bytes[:], src[start:min(start+64, len(src))])
			simdkernels.Stage1Block(&bytes, &masks[block])
		}
		corpora = append(corpora, portableCorpus{
			name:  strings.TrimSuffix(name, ".json.zst"),
			bytes: int64(len(src)),
			masks: masks,
		})
	}
	return corpora
}

func portableEscapedBaseline(backslash uint64, carry *simdkernels.Stage1Carry) uint64 {
	if backslash == 0 {
		escaped := carry.Escaped
		carry.Escaped = 0
		return escaped
	}
	backslash &^= carry.Escaped
	followsEscape := backslash<<1 | carry.Escaped
	oddSequenceStarts := backslash & ^portableEvenBits & ^followsEscape
	sum, overflow := bits.Add64(oddSequenceStarts, backslash, 0)
	carry.Escaped = overflow
	return (portableEvenBits ^ sum<<1) & followsEscape
}

func portablePrefixBaseline(quotes uint64, carry *simdkernels.Stage1Carry) uint64 {
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

func portableRecBaseline(m *simdkernels.Stage1Masks, st *simdkernels.Stage1Stream, r *simdkernels.Stage1Rec) {
	escaped := portableEscapedBaseline(m.Backslash, &st.Carry)
	quotes := m.Quote &^ escaped
	inStr := portablePrefixBaseline(quotes, &st.Carry)
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

func TestPortableStage1CorpusStats(t *testing.T) {
	for _, corpus := range loadPortableCorpora(t) {
		var carry simdkernels.Stage1Carry
		quoteZero, slashZero, slashIsolated := 0, 0, 0
		for i := range corpus.masks {
			m := &corpus.masks[i]
			if m.Quote == 0 {
				quoteZero++
			}
			if m.Backslash == 0 {
				slashZero++
			} else {
				backslash := m.Backslash &^ carry.Escaped
				if backslash&(backslash<<1|carry.Escaped) == 0 {
					slashIsolated++
				}
			}
			simdkernels.Stage1Escaped(m.Backslash, &carry)
		}
		t.Logf("%s blocks=%d quote-zero=%.2f%% slash-zero=%.2f%% slash-isolated-of-nonzero=%.2f%%",
			corpus.name, len(corpus.masks),
			100*float64(quoteZero)/float64(len(corpus.masks)),
			100*float64(slashZero)/float64(len(corpus.masks)),
			100*float64(slashIsolated)/float64(max(1, len(corpus.masks)-slashZero)))
	}
}

func BenchmarkPortableStage1Corpus(b *testing.B) {
	for _, corpus := range loadPortableCorpora(b) {
		b.Run(corpus.name+"/baseline", func(b *testing.B) {
			var st simdkernels.Stage1Stream
			var rec simdkernels.Stage1Rec
			b.SetBytes(corpus.bytes)
			for n := 0; n < b.N; n++ {
				for i := range corpus.masks {
					portableRecBaseline(&corpus.masks[i], &st, &rec)
				}
			}
			stage1PortableBenchSink = rec.Emit ^ rec.Scalar ^ rec.InStr
		})
		b.Run(corpus.name+"/final", func(b *testing.B) {
			var st simdkernels.Stage1Stream
			var rec simdkernels.Stage1Rec
			b.SetBytes(corpus.bytes)
			for n := 0; n < b.N; n++ {
				for i := range corpus.masks {
					simdkernels.Stage1RecFromMasks(&corpus.masks[i], &st, &rec)
				}
			}
			stage1PortableBenchSink = rec.Emit ^ rec.Scalar ^ rec.InStr
		})
	}
}

var stage1PortableBenchSink uint64
