//go:build !goexperiment.simd || (!arm64 && !amd64)

package simd

// Stage1Enabled reports whether this build provides the stage-1 kernel.
func Stage1Enabled() bool { return false }

// Stage1Block is unreachable in scalar builds; the portable carry kernels
// Stage1Escaped and Stage1PrefixXOR remain available for callers that
// supply the block masks from another source.
func Stage1Block(p *[64]byte, m *Stage1Masks) { panic("simd: stage1 disabled") }
