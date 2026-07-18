//go:build !goexperiment.simd || !arm64

package simd

import "unsafe"

// Stage1StreamEnabled reports whether production routing selects the batched
// kernel. The portable implementation remains callable while routing stays
// disabled pending end-to-end crossover benchmarks.
func Stage1StreamEnabled() bool { return false }

// Stage1BlocksGP is the portable equivalent of the batched SIMD classifier.
// It preserves carry state across blocks and emits the same Stage1Rec records.
func Stage1BlocksGP(p *byte, nblocks int, st *Stage1Stream, out *[Stage1ChunkBlocks]Stage1Rec) {
	if nblocks <= 0 || nblocks > Stage1ChunkBlocks {
		panic("simd: Stage1BlocksGP block count outside [1, Stage1ChunkBlocks]")
	}
	base := unsafe.Pointer(p)
	recs := out[:nblocks]
	for i := range recs {
		var masks Stage1Masks
		stage1BlockPortable((*[64]byte)(unsafe.Add(base, i*64)), &masks)
		Stage1RecFromMasks(&masks, st, &recs[i])
	}
}

// Stage1IndexBlocks is unreachable on builds without the packed portable
// producer. Callers must gate on Stage1StreamEnabled.
func Stage1IndexBlocks(p *byte, nblocks int, base uint32, st *Stage1IndexStream, out []uint32) int {
	panic("simd: stage1 index kernel disabled")
}

// Stage1IndexBlocksMeta is unreachable on builds without the packed portable
// producer. Callers must gate on Stage1StreamEnabled.
func Stage1IndexBlocksMeta(p *byte, nblocks int, base uint32, st *Stage1IndexStream, out []uint32, meta *Stage1IndexMeta) int {
	panic("simd: stage1 index kernel disabled")
}

// Stage1CursorBlocks is unreachable on builds without the packed portable
// producer. Callers must gate on Stage1StreamEnabled.
func Stage1CursorBlocks(p *byte, nblocks int, base uint32, st *Stage1IndexStream, out []uint32) int {
	panic("simd: stage1 cursor kernel disabled")
}

// Stage1ValidBlocks is unreachable on builds without the packed portable
// producer. Callers must gate on Stage1StreamEnabled.
func Stage1ValidBlocks(p *byte, nblocks int, base uint32, st *Stage1IndexStream, out []uint32, meta *Stage1ValidMeta) int {
	panic("simd: stage1 validation kernel disabled")
}

// Stage1ValidBlocksCoarse is unreachable on builds without the packed portable
// producer. Callers must gate on Stage1StreamEnabled.
func Stage1ValidBlocksCoarse(p *byte, nblocks int, base uint32, st *Stage1IndexStream, out []uint32, meta *Stage1ValidMeta) int {
	panic("simd: stage1 validation kernel disabled")
}
