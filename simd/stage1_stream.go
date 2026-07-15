package simd

// Streamed stage-1: the batched kernel classifies a run of 64-byte blocks
// per call and emits one Stage1Rec per block, keeping the carry state
// internal to the call. The kernel is Stage1BlocksGP: classification in
// NEON, per-mask movemask to general-purpose registers, escape chain and
// prefix-XOR in scalar code. This is the C++ simdjson shape
// (json_scanner::next), batched so the vector constants load once per
// chunk instead of once per block.
//
// The record carries exactly the masks the bitmap validator consumes;
// everything else (whitespace skipping, string interiors) dies inside
// the kernel.

// Stage1ChunkBlocks is the maximum number of blocks per kernel call. It
// matches the validator's sampling window so the first chunk decides
// engine commitment, and 32 blocks of records stay comfortably inside
// L1.
const Stage1ChunkBlocks = 32

// Stage1Rec is the per-block output of the batched kernel. Bit i of each
// mask describes byte i of the block.
type Stage1Rec struct {
	Emit     uint64 // structural bytes outside strings, opening quotes, scalar starts
	EscInStr uint64 // escape-target bytes inside strings (byte after a backslash)
	Bad      uint64 // control-byte violations (raw controls in strings, non-ws controls outside)
	WsOut    uint64 // whitespace outside strings (density sampling)
	NonASCII bool   // any byte at or above 0x80 in this block (drives per-run UTF-8 checking)
}

// Stage1Stream threads carry state between chunks. The zero value is the
// document-start state.
type Stage1Stream struct {
	Carry   Stage1Carry
	Follows uint64 // bit 0: last byte of the previous block was a scalar candidate
}

// Stage1RecFromMasks derives one record from a block's classification
// masks using the portable carry kernels. It is the scalar reference
// for the batched kernel and works on every build.
func Stage1RecFromMasks(m *Stage1Masks, st *Stage1Stream, r *Stage1Rec) {
	escaped := Stage1Escaped(m.Backslash, &st.Carry)
	quotes := m.Quote &^ escaped
	inStr := Stage1PrefixXOR(quotes, &st.Carry)
	closers := quotes &^ inStr
	openers := quotes & inStr
	outside := ^(inStr | closers)
	cand := ^(m.Whitespace | m.Structural | m.Quote | inStr)
	starts := cand &^ (cand<<1 | st.Follows)
	st.Follows = cand >> 63
	r.Emit = m.Structural&outside | openers | starts&outside
	r.EscInStr = escaped & inStr
	r.Bad = m.Control&inStr | m.Control&outside&^m.Whitespace
	r.WsOut = m.Whitespace & outside
	r.NonASCII = m.NonASCII
}
