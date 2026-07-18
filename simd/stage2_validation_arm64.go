//go:build arm64 && !race

package simd

// Stage2Enabled reports whether this build provides a stage-2 grammar backend.
// Validation uses the Go-native packed-position machine.
func Stage2Enabled() bool { return true }

// Stage2NativeEnabled reports whether the legacy bitmap API is backed by a
// native machine. The removed assembly machine is not selected.
func Stage2NativeEnabled() bool { return false }

// Stage2Walk preserves the legacy bitmap API through the Go-native machine.
// Production validation continues to use Stage2PositionsTrusted.
func Stage2Walk(base *byte, emit []uint64, kinds *[Stage2KindsLen]byte, scalars []uint32, st *Stage2State) int {
	return Stage2WalkGo(base, emit, kinds, scalars, st)
}
