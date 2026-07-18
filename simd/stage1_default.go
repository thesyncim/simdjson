//go:build !goexperiment.simd || (!arm64 && !amd64)

package simd

// Stage1Enabled reports whether the scalar stage-1 engine is selected for
// production routing. Stage1Block remains fully available as a portable SWAR
// kernel even while routing stays disabled pending end-to-end crossover data.
func Stage1Enabled() bool { return false }

// Stage1Block classifies one full 64-byte block with the portable SWAR kernel.
func Stage1Block(block *[64]byte, masks *Stage1Masks) {
	stage1BlockPortable(block, masks)
}
