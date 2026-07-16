//go:build arm64 && !race && !simdjson_safehooks

package simd

// Stage2Enabled reports false now that validation is implemented by the
// Go-native packed-position machine.
func Stage2Enabled() bool { return false }

// Stage2Walk is retained only for source compatibility with the experimental
// bitmap-machine API. Production validation uses Stage2PositionsTrusted.
func Stage2Walk(base *byte, emit []uint64, kinds *[Stage2KindsLen]byte, scalars []uint32, st *Stage2State) int {
	panic("simd: bitmap stage-2 machine removed; use the packed-position API")
}
