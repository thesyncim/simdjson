package benchmarks

import (
	"testing"
	"unsafe"

	simdkernels "github.com/thesyncim/simdjson/simd"
)

func TestStage2PositionsCursorCorpora(t *testing.T) {
	for _, corpus := range loadPortableCorpora(t) {
		kindsA := new([simdkernels.Stage2KindsLen]byte)
		kindsB := new([simdkernels.Stage2KindsLen]byte)
		scalarsA := make([]uint32, len(corpus.positions))
		scalarsB := make([]uint32, len(corpus.positions))
		var stateA, stateB simdkernels.Stage2State
		simdkernels.Stage2Reset(&stateA)
		simdkernels.Stage2Reset(&stateB)
		base := unsafe.SliceData(corpus.src)
		nA := simdkernels.Stage2PositionsTrusted(base, corpus.positions, kindsA, scalarsA, &stateA)
		nB := simdkernels.Stage2PositionsCursorCandidate(base, corpus.positions, kindsB, scalarsB, &stateB)
		if nA != nB || stateA != stateB {
			t.Fatalf("%s: current n=%d state=%+v, cursor n=%d state=%+v", corpus.name, nA, stateA, nB, stateB)
		}
		for i := 0; i < nA; i++ {
			if scalarsA[i] != scalarsB[i] {
				t.Fatalf("%s: scalar %d current=%d cursor=%d", corpus.name, i, scalarsA[i], scalarsB[i])
			}
		}
	}
}

func BenchmarkStage2PositionsCursor(b *testing.B) {
	for _, corpus := range loadPortableCorpora(b) {
		b.Run(corpus.name+"/current", func(b *testing.B) {
			kinds := new([simdkernels.Stage2KindsLen]byte)
			scalars := make([]uint32, len(corpus.positions))
			var state simdkernels.Stage2State
			base := unsafe.SliceData(corpus.src)
			b.SetBytes(corpus.bytes)
			for i := 0; i < b.N; i++ {
				simdkernels.Stage2Reset(&state)
				intSink = simdkernels.Stage2PositionsTrusted(base, corpus.positions, kinds, scalars, &state)
			}
			reportPerPosition(b, len(corpus.positions))
		})
		b.Run(corpus.name+"/cursor", func(b *testing.B) {
			kinds := new([simdkernels.Stage2KindsLen]byte)
			scalars := make([]uint32, len(corpus.positions))
			var state simdkernels.Stage2State
			base := unsafe.SliceData(corpus.src)
			b.SetBytes(corpus.bytes)
			for i := 0; i < b.N; i++ {
				simdkernels.Stage2Reset(&state)
				intSink = simdkernels.Stage2PositionsCursorCandidate(base, corpus.positions, kinds, scalars, &state)
			}
			reportPerPosition(b, len(corpus.positions))
		})
	}
}
