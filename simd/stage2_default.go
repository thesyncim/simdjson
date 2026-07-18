//go:build !arm64 || race

package simd

// Stage2Enabled reports whether this build provides a stage-2 grammar backend.
// Portable builds use the Go-native machine.
func Stage2Enabled() bool { return true }

// Stage2NativeEnabled reports whether Stage2Walk is backed by a native bitmap
// machine. Portable builds return false so production routing prefers the
// packed-position Go machine.
func Stage2NativeEnabled() bool { return false }

// Stage2Walk provides the legacy bitmap API through the Go-native machine.
func Stage2Walk(base *byte, emit []uint64, kinds *[Stage2KindsLen]byte, scalars []uint32, st *Stage2State) int {
	return Stage2WalkGo(base, emit, kinds, scalars, st)
}
