//go:build !goexperiment.simd || (!arm64 && !amd64)

package simd

// Stage1Enabled reports whether this build provides the stage-1 kernel.
func Stage1Enabled() bool { return false }

// Stage1Masks holds the per-64-byte-block classification; unused in
// scalar builds.
type Stage1Masks struct {
	Whitespace uint64
	Structural uint64
	Quote      uint64
	Backslash  uint64
	Control    uint64
	NonASCII   bool
}

// Stage1Carry threads block-boundary state; unused in scalar builds.
type Stage1Carry struct {
	Escaped  uint64
	InString uint64
	Follows  uint64
}

// Stage1Block is unreachable in scalar builds.
func Stage1Block(p *[64]byte, m *Stage1Masks) { panic("simd: stage1 disabled") }

// Stage1Escaped is unreachable in scalar builds.
func Stage1Escaped(backslash uint64, carry *Stage1Carry) uint64 { panic("simd: stage1 disabled") }

// Stage1PrefixXOR is unreachable in scalar builds.
func Stage1PrefixXOR(quotes uint64, carry *Stage1Carry) uint64 { panic("simd: stage1 disabled") }
