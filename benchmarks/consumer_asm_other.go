//go:build !arm64

package benchmarks

// The hand-written consumer exists only on arm64.
const consumerAsmEnabled = false

func consumerPairEntriesAsm(src []byte, emit []uint64, kinds []byte, entries []uint64) (ok bool, n int) {
	panic("consumerPairEntriesAsm: arm64 only")
}

func consumerPairEntriesAsmSuper(src []byte, emit []uint64, kinds []byte, entries []uint64) (ok bool, n int) {
	panic("consumerPairEntriesAsmSuper: arm64 only")
}
