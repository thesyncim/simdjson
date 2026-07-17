package simdjson

import (
	"runtime"
	"testing"
)

func TestDecoderStructuralTapeDropsOversizedBacking(t *testing.T) {
	tape := decoderStructuralTape{
		positions: make([]uint32, 1, decoderStructuralTapeRetentionPositions+1),
		index:     1,
		bad:       true,
		nonASCII:  true,
		escaped:   true,
	}

	tape.resetForPool()
	runtime.GC()
	if tape.positions != nil {
		t.Fatalf("oversized tape retained capacity %d after release", cap(tape.positions))
	}
	if tape.index != 0 || tape.bad || tape.nonASCII || tape.escaped {
		t.Fatalf("released tape retained state: %+v", tape)
	}
}

func TestDecoderStructuralTapeRetainsBoundedBacking(t *testing.T) {
	tape := decoderStructuralTape{positions: make([]uint32, 1, 1024)}
	warmCap := cap(tape.positions)
	tape.resetForPool()
	if cap(tape.positions) != warmCap || len(tape.positions) != 0 {
		t.Fatalf("bounded tape release cap=%d len=%d, want cap=%d len=0", cap(tape.positions), len(tape.positions), warmCap)
	}
}
