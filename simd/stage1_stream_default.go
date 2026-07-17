//go:build !goexperiment.simd || !arm64

package simd

// Stage1StreamEnabled reports whether this build provides the batched
// stage-1 kernel.
func Stage1StreamEnabled() bool { return false }

// Stage1BlocksGP is unreachable on builds without the batched kernel.
func Stage1BlocksGP(p *byte, nblocks int, st *Stage1Stream, out *[Stage1ChunkBlocks]Stage1Rec) {
	panic("simd: stage1 stream kernel disabled")
}

// Stage1IndexBlocks is unreachable on builds without the batched index
// producer.
func Stage1IndexBlocks(p *byte, nblocks int, base uint32, st *Stage1IndexStream, out []uint32) int {
	panic("simd: stage1 index kernel disabled")
}

// Stage1IndexBlocksMeta is unreachable on builds without the batched index
// producer.
func Stage1IndexBlocksMeta(p *byte, nblocks int, base uint32, st *Stage1IndexStream, out []uint32, meta *Stage1IndexMeta) int {
	panic("simd: stage1 index kernel disabled")
}

// Stage1CursorBlocks is unreachable on builds without the compact batched
// index producer.
func Stage1CursorBlocks(p *byte, nblocks int, base uint32, st *Stage1IndexStream, out []uint32) int {
	panic("simd: stage1 cursor kernel disabled")
}

// Stage1ValidBlocks is unreachable on builds without the batched validation
// producer.
func Stage1ValidBlocks(p *byte, nblocks int, base uint32, st *Stage1IndexStream, out []uint32, meta *Stage1ValidMeta) int {
	panic("simd: stage1 validation kernel disabled")
}

// Stage1ValidBlocksCoarse is unreachable without the batched validation
// producer.
func Stage1ValidBlocksCoarse(p *byte, nblocks int, base uint32, st *Stage1IndexStream, out []uint32, meta *Stage1ValidMeta) int {
	panic("simd: stage1 validation kernel disabled")
}
