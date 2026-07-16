package benchmarks

// Production stage-2 machine study: the resumable grammar machine
// (simd/stage2_arm64.s) measured over real corpus masks, whole-document
// and chunked the way the Valid engine feeds it. The demonstration
// consumer (consumer_asm_arm64.s) ran 0.63-0.92 ns/pos with 16-byte entry
// writes and no suspend/resume; the production machine drops the entry
// writes, records scalar-start positions instead, and reloads/stores its
// four state words per chunk call. The chunked benchmarks price that
// suspend/resume traffic at the engine's granularity.

import (
	"testing"
	"unsafe"

	simdkernels "github.com/thesyncim/simdjson/simd"
)

// stage2CorpusScalars classifies a corpus's flattened positions: the
// scalar starts the machine must record, in order.
func stage2CorpusScalars(c gapCorpus) []uint32 {
	var out []uint32
	for _, p := range c.positions {
		switch c.src[p] {
		case '{', '[', '}', ']', ':', ',', '"':
		default:
			out = append(out, p)
		}
	}
	return out
}

// stage2RunChunked drives the machine over the corpus masks in runs of
// chunkWords, collecting scalar positions, and returns the final verdict.
func stage2RunChunked(src []byte, emit []uint64, chunkWords int, collect *[]uint32) bool {
	base := unsafe.Pointer(unsafe.SliceData(src))
	var st simdkernels.Stage2State
	simdkernels.Stage2Reset(&st)
	kinds := new([simdkernels.Stage2KindsLen]byte)
	scalars := make([]uint32, 64*chunkWords)
	for w := 0; w < len(emit); w += chunkWords {
		end := min(w+chunkWords, len(emit))
		ns := simdkernels.Stage2Walk((*byte)(unsafe.Add(base, w*64)), emit[w:end], kinds, scalars, &st)
		if collect != nil {
			for _, rel := range scalars[:ns] {
				*collect = append(*collect, uint32(w*64)+rel)
			}
		}
	}
	return simdkernels.Stage2Finish(&st)
}

// TestStage2MachineCorpora checks the production machine on every corpus:
// acceptance whole and chunked at the engine's granularities, scalar
// records equal to the classified positions, and — because Bad judges
// exactly the grammar the oracle walk judges — verdict agreement with
// consumerOracleWalk on mutated corpus prefixes.
func TestStage2MachineCorpora(t *testing.T) {
	if !simdkernels.Stage2Enabled() {
		t.Skip("stage-2 machine not available on this build")
	}
	for _, c := range loadGapCorpora(t) {
		wantScalars := stage2CorpusScalars(c)
		for _, chunkWords := range []int{4, 8, 16, len(c.emit)} {
			var got []uint32
			if !stage2RunChunked(c.src, c.emit, chunkWords, &got) {
				t.Fatalf("%s: machine rejected the corpus at chunk %d", c.label, chunkWords)
			}
			if len(got) != len(wantScalars) {
				t.Fatalf("%s (chunk %d): %d scalar starts, want %d", c.label, chunkWords, len(got), len(wantScalars))
			}
			for i := range got {
				if got[i] != wantScalars[i] {
					t.Fatalf("%s (chunk %d): scalar %d = %d, want %d", c.label, chunkWords, i, got[i], wantScalars[i])
				}
			}
		}
		// Grammar differential on structural mutations: flip one emitted
		// byte to another token class and require oracle agreement.
		hostile := []byte(`{}[]:,"5t`)
		for i := 0; i < 400; i++ {
			p := c.positions[(i*2654435761)%len(c.positions)]
			mutated := append([]byte(nil), c.src...)
			mutated[p] = hostile[i%len(hostile)]
			emit := stage1EmitMasks(mutated)
			want := consumerOracleWalk(mutated, emit)
			if got := stage2RunChunked(mutated, emit, 4, nil); got != want {
				t.Fatalf("%s: mutant at %d (%q): machine = %v, oracle = %v", c.label, p, mutated[p], got, want)
			}
		}
	}
}

func BenchmarkStage2Whole(b *testing.B) {
	if !simdkernels.Stage2Enabled() {
		b.Skip("stage-2 machine not available on this build")
	}
	for _, c := range loadGapCorpora(b) {
		b.Run(c.label, func(b *testing.B) {
			base := unsafe.SliceData(c.src)
			kinds := new([simdkernels.Stage2KindsLen]byte)
			scalars := make([]uint32, 64*len(c.emit))
			var st simdkernels.Stage2State
			b.SetBytes(int64(len(c.src)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				simdkernels.Stage2Reset(&st)
				intSink = simdkernels.Stage2Walk(base, c.emit, kinds, scalars, &st)
				boolSink = simdkernels.Stage2Finish(&st)
			}
			reportPerPosition(b, len(c.positions))
		})
	}
}

func benchmarkStage2Chunked(b *testing.B, chunkWords int) {
	if !simdkernels.Stage2Enabled() {
		b.Skip("stage-2 machine not available on this build")
	}
	for _, c := range loadGapCorpora(b) {
		b.Run(c.label, func(b *testing.B) {
			base := unsafe.Pointer(unsafe.SliceData(c.src))
			kinds := new([simdkernels.Stage2KindsLen]byte)
			scalars := make([]uint32, 64*chunkWords)
			var st simdkernels.Stage2State
			npos := 0
			b.SetBytes(int64(len(c.src)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				simdkernels.Stage2Reset(&st)
				npos = 0
				for w := 0; w < len(c.emit); w += chunkWords {
					end := min(w+chunkWords, len(c.emit))
					npos += simdkernels.Stage2Walk((*byte)(unsafe.Add(base, w*64)), c.emit[w:end], kinds, scalars, &st)
				}
				boolSink = simdkernels.Stage2Finish(&st)
				intSink = npos
			}
			reportPerPosition(b, len(c.positions))
		})
	}
}

func BenchmarkStage2Chunked4(b *testing.B)  { benchmarkStage2Chunked(b, 4) }
func BenchmarkStage2Chunked8(b *testing.B)  { benchmarkStage2Chunked(b, 8) }
func BenchmarkStage2Chunked16(b *testing.B) { benchmarkStage2Chunked(b, 16) }
