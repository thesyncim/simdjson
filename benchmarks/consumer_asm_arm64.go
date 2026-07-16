//go:build arm64

package benchmarks

import "unsafe"

// The hand-written arm64 consumer: the same pair-table grammar machine as
// the Go variants, dispatched through a computed indirect branch into
// per-class handlers spaced 128 bytes apart. Each handler carries only its
// class's work — constant pair-table column, constant entry info word,
// container stack traffic only in the four container handlers — and the
// whole machine state (depth, kind, row context, sticky bad) lives in
// registers across the document. This is the measurement the fence calls
// for: what does the grammar + entry-write consumer cost when Go codegen
// overhead (rematerialized table bases, register shuffles, split hot
// blocks) is taken out of the picture.

const consumerAsmEnabled = true

// consumerAsmOut receives the machine's final registers.
type consumerAsmOut struct {
	bad       uint64
	depth     int64
	prevRowIO uint64
	ep        uint64
}

// consumerAsmLoop runs the register machine over full 64-byte-block emit
// masks. clsOff maps each source byte to its handler displacement
// (class*128); pt is the pair-legality table; kinds is the container-kind
// slab (consumerKindsLen bytes, kinds[0]==0); entries receives one
// 16-byte entry per position.
//
//go:noescape
func consumerAsmLoop(base *byte, emit *uint64, nwords int64, clsOff *uint64, pt *uint8, kinds *byte, entries *uint64, out *consumerAsmOut)

// consumerClsOff is the dispatch displacement table: handler slot base is
// the loop symbol + 128, and each class handler occupies one 128-byte slot
// in class-code order. The upper half (byte index + 256, selected when the
// mask word is exhausted) routes every byte to the word-advance handler in
// slot 8.
var consumerClsOff = func() (t [512]uint64) {
	for i := 0; i < 256; i++ {
		t[i] = (consumerClassTab[i] & 7) * 128
	}
	for i := 256; i < 512; i++ {
		t[i] = 8 * 128
	}
	return
}()

// consumerAsmLoopSuper is the externally supplied variant
// (consumer_asm_super_arm64.s): fusedComma also consumes '}' inline so
// nested object closes chain without generic dispatch, fusedValue
// consumes scalar values after ':' reusing the loaded dispatch slot on
// a miss, and fused-guard misses dispatch off the already-loaded byte.
//
//go:noescape
func consumerAsmLoopSuper(base *byte, emit *uint64, nwords int64, clsOff *uint64, pt *uint8, kinds *byte, entries *uint64, out *consumerAsmOut)

// consumerPairEntriesAsm wraps the register machine with the same
// contract as consumerPairEntriesGolf: grammar verdict plus one 16-byte
// entry per position.
func consumerPairEntriesAsm(src []byte, emit []uint64, kinds []byte, entries []uint64) (ok bool, n int) {
	if len(emit) == 0 {
		return false, 0
	}
	var out consumerAsmOut
	consumerAsmLoop(
		unsafe.SliceData(src),
		unsafe.SliceData(emit),
		int64(len(emit)),
		&consumerClsOff[0],
		&consumerPairBad[0],
		unsafe.SliceData(kinds),
		unsafe.SliceData(entries),
		&out,
	)
	bad := out.bad
	bad |= uint64(consumerEOFBad[out.prevRowIO>>4&15])
	if out.depth != 0 {
		bad |= 1
	}
	n = int(out.ep-uint64(uintptr(unsafe.Pointer(unsafe.SliceData(entries))))) / 16
	return bad == 0, n
}

// consumerPairEntriesAsmSuper is consumerPairEntriesAsm on the supplied
// variant; the harness holds the two to identical verdicts and entries.
func consumerPairEntriesAsmSuper(src []byte, emit []uint64, kinds []byte, entries []uint64) (ok bool, n int) {
	if len(emit) == 0 {
		return false, 0
	}
	var out consumerAsmOut
	consumerAsmLoopSuper(
		unsafe.SliceData(src),
		unsafe.SliceData(emit),
		int64(len(emit)),
		&consumerClsOff[0],
		&consumerPairBad[0],
		unsafe.SliceData(kinds),
		unsafe.SliceData(entries),
		&out,
	)
	bad := out.bad
	bad |= uint64(consumerEOFBad[out.prevRowIO>>4&15])
	if out.depth != 0 {
		bad |= 1
	}
	n = int(out.ep-uint64(uintptr(unsafe.Pointer(unsafe.SliceData(entries))))) / 16
	return bad == 0, n
}
