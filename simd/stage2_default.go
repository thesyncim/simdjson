//go:build !arm64 || race || simdjson_safehooks

package simd

// Stage2Enabled reports whether this build provides the hand-written
// grammar machine. Builds without it — non-arm64, -race, and
// simdjson_safehooks — keep the portable walk.
func Stage2Enabled() bool { return false }

// Stage2Walk is unreachable on builds without the machine; callers must
// gate on Stage2Enabled.
func Stage2Walk(base *byte, emit []uint64, kinds *[Stage2KindsLen]byte, scalars []uint32, st *Stage2State) int {
	panic("simd: stage-2 machine not available on this build")
}
