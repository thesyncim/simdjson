//go:build arm64 && !race && !simdjson_safehooks

package simd

import "unsafe"

// The index machine shares the validation machine's safety argument
// (stage2_arm64.go): NOSPLIT pure computation, no calls out, no pointer
// stores, every pointer dead at RET. Its writes land in caller-owned
// memory at bounded offsets — 16-byte entries against an explicit end
// pointer (exhaustion aborts before the store), scope words at
// depth&(Stage2IndexSlabLen-1), patch stores at entry indexes the slab
// received from this document's own opens (underflow aborts before a
// patch can use a stale index), and the six state words.

// stage2IndexLoop runs the entry-writing machine over nwords emit masks
// whose first mask covers the 64 source bytes at base+pos. Entries are
// 16-byte production tape records appended at ent+st.EntryOff; entCap is
// the storage's total capacity in entries. st is loaded on entry and
// stored back at exit.
//
//go:noescape
func stage2IndexLoop(base *byte, pos int64, emit *uint64, nwords int64, clsOff *uint64, pt *uint8, slab *uint64, ent *byte, entCap int64, st *Stage2IndexState)

// Stage2IndexWalk resumes the index machine over a run of consecutive
// blocks' emit masks. base is the document start; pos is the absolute
// byte offset of bit 0 of emit[0], and every set bit must name a real
// source byte. ent and entCap describe the caller's entry storage; the
// machine appends at st.EntryOff and aborts with Stage2IndexFull rather
// than write past entCap entries. slab persists across the document's
// chunks and must start zeroed.
func Stage2IndexWalk(base *byte, pos int, emit []uint64, slab *[Stage2IndexSlabLen]uint64, ent *byte, entCap int, st *Stage2IndexState) {
	if len(emit) == 0 {
		return
	}
	stage2IndexLoop(base, int64(pos), unsafe.SliceData(emit), int64(len(emit)),
		&stage2IdxClsOff[0], &stage2PairBad[0], &slab[0], ent, int64(entCap), st)
}
