package simd

// Stage-2 index machine: the entry-writing sibling of the validation
// machine. It consumes the same stage-1 emit masks through the same
// pair-table grammar, and additionally writes one production tape entry
// per value or key token, patching container entries in place when their
// closers arrive — end offset, subtree span (next link), and direct
// member count. String and scalar entries are written with their start
// position and a placeholder body; the caller finishes their ends,
// kinds, and flags with the same byte scanners the fallback builder
// uses, so acceptance and every finished field are identical by
// construction. (When the stage-1 record grows a closing-quote mask,
// string ends can come from the masks instead of a byte scan; the
// finishing pass is the seam for that swap.)
//
// The machine only ever shortcuts the accepting path: any grammar
// violation, nesting past Stage2IndexMaxDepth, or entry-buffer
// exhaustion sets a flag and the caller falls back to the portable
// builder, which then decides the exact error or result. That keeps the
// machine's obligations narrow and testable: accept only documents the
// fallback accepts, and produce byte-identical entries when it does.

// Stage2IndexMaxDepth caps the machine's container nesting, mirroring
// the fallback builder's fast-walk cap (the root package asserts the two
// are equal at compile time). Deeper documents abort to the fallback,
// which diverts them exactly as it always has.
const Stage2IndexMaxDepth = 64

// Stage2IndexSlabLen sizes the per-depth scope slab (a power of two with
// headroom above the depth cap, so masked addressing stays in bounds
// while an abort is in flight). Each slot packs the open container's
// entry index, the parent's running member count, and the container kind
// into one word:
//
//	bits 32..63  entry index
//	bits  4..30  saved parent member count
//	bit   3      kind (8 = object), aligned with the machine's inObj8
//
// The slab is written at opens and read once at the matching close;
// nothing reloads a slot in between, so the patch stores cannot create
// store-to-load forwarding chains in the token loop.
const Stage2IndexSlabLen = 128

// Abort flags folded into Stage2IndexState.Bad alongside the grammar
// bits: entry storage exhausted, and nesting past the machine's cap.
const (
	Stage2IndexFull uint64 = 1 << 62
	Stage2IndexDeep uint64 = 1 << 61
)

// Stage2IndexState carries the index machine's registers between chunk
// calls. Field order is the assembly's load/store order. The zero value
// is NOT the document-start state; call Stage2IndexReset first.
type Stage2IndexState struct {
	// Bad is the sticky violation accumulator plus the abort flags;
	// nonzero means the caller must fall back to the portable builder.
	Bad uint64
	// Depth is the current container nesting depth.
	Depth int64
	// PrevRowIO is the refined previous class pre-shifted for the pair
	// table: row<<4 | inObj8.
	PrevRowIO uint64
	// KeyRow8 holds 1<<3 when the previous token opens a key context.
	KeyRow8 uint64
	// Count is the current container's member count so far (its commas).
	Count uint64
	// EntryOff is the entry cursor as a byte offset into the caller's
	// entry storage; divide by 16 for the entry count.
	EntryOff uint64
}

// Stage2IndexReset puts st in the document-start state. The caller's
// scope slab must be zeroed at document start: slot 0 is read when a
// root container closes and must present the array kind with a zero
// parent count.
func Stage2IndexReset(st *Stage2IndexState) {
	*st = Stage2IndexState{PrevRowIO: stage2RowStart << 4}
}

// Stage2IndexFinish folds the end-of-document rules into the verdict,
// exactly as Stage2Finish does for the validation machine.
func Stage2IndexFinish(st *Stage2IndexState) bool {
	bad := st.Bad | uint64(stage2EOFBad[st.PrevRowIO>>4&15])
	if st.Depth != 0 {
		bad |= 1
	}
	return bad == 0
}

// The machine writes production tape words directly, hardcoding the info
// packing (count | kind<<26 | flags<<29) for the facts it knows at token
// time. The root package asserts these against its private layout at
// compile time, so the coupling cannot drift silently. Scalars are
// written with a zero info word — the Invalid kind — as the placeholder
// the caller's finishing pass rewrites.
const (
	Stage2IndexInfoObject uint32 = 6 << 26 // Object kind; count patched at close
	Stage2IndexInfoArray  uint32 = 5 << 26 // Array kind; count patched at close
	Stage2IndexInfoString uint32 = 4 << 26 // String kind; end and escaped flag finished by the caller
	Stage2IndexKeyFlag    uint32 = 1 << 30 // key flag within a string's info word
)

// stage2IdxClsOff is the index machine's dispatch displacement table.
// Its handlers carry entry writes and close patches, so they occupy
// 256-byte slots (class*256) instead of the validation machine's 128.
var stage2IdxClsOff = func() (t [256]uint64) {
	for i := range t {
		t[i] = stage2ClsOff[i] * 2
	}
	return
}()
