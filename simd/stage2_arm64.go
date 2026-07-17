//go:build arm64 && !race

package simd

import "unsafe"

// The arm64 stage-2 machine (stage2_arm64.s) is pure computation over
// caller-owned memory. Its safety argument, spelled out because the
// wrapper is //go:noescape:
//
//   - The assembly makes no calls, is NOSPLIT with a zero-size frame, and
//     stores no pointer anywhere: every pointer argument is dead at RET,
//     so nothing can retain or alias Go memory beyond the call.
//   - The caller's argument slots keep every underlying object live and
//     precisely scanned for the duration of the call; registers holding
//     derived interior pointers are covered by the runtime's conservative
//     scan if the goroutine is asynchronously preempted, and conservative
//     frames pin the stack against moves.
//   - Writes land only in caller-owned buffers at bounded offsets: one
//     uint32 per set emit bit into scalars (capacity enforced below,
//     fail-closed), kind bytes at depth&(Stage2KindsLen-1) into the slab,
//     and the four state words. Reads of src touch only emit-bit
//     positions, which the caller guarantees lie inside src.
//
// Race builds exclude this file and fall back to the
// portable walk (Stage2Enabled reports false there).

// Stage2Enabled reports that the hand-written grammar machine backs this
// build.
func Stage2Enabled() bool { return true }

// stage2Loop runs the register machine over nwords emit masks whose first
// mask covers the 64 source bytes at base. clsOff maps each source byte
// to its handler displacement (class*128); pt is the pair-legality table;
// kinds is the container-kind slab (Stage2KindsLen bytes); scalars
// receives one position (relative to base) per scalar-start bit. st is
// loaded on entry and stored back at exit.
//
//go:noescape
func stage2Loop(base *byte, emit *uint64, nwords int64, clsOff *uint64, pt *uint8, kinds *byte, scalars *uint32, st *Stage2State) (nscalars int64)

// Stage2Walk resumes the grammar machine over a run of consecutive
// blocks' emit masks. base points at the source byte covered by bit 0 of
// emit[0]; every set bit must name a real source byte (the caller rejects
// stray bits past the document before calling). Recorded scalar-start
// positions are relative to base; the return value is how many were
// written.
//
// len(scalars) must be at least 64*len(emit) — the emit-bit bound — so
// the machine's unconditional post-increment stores can never leave the
// buffer; the wrapper fails closed on a violation. kinds persists across
// the document's chunks and must start with byte 0 reading as array (a
// freshly zeroed slab); deeper slots are written before they are read.
func Stage2Walk(base *byte, emit []uint64, kinds *[Stage2KindsLen]byte, scalars []uint32, st *Stage2State) int {
	if len(emit) == 0 {
		return 0
	}
	if len(scalars) < 64*len(emit) {
		panic("simd: Stage2Walk scalars shorter than the emit-bit bound")
	}
	return int(stage2Loop(base, unsafe.SliceData(emit), int64(len(emit)),
		&stage2ClsOff[0], &stage2PairBad[0], &kinds[0], unsafe.SliceData(scalars), st))
}
