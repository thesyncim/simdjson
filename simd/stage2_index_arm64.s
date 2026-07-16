//go:build arm64 && !race && !simdjson_safehooks

#include "textflag.h"

// stage2IndexLoop: the pair-table grammar machine with production tape
// entry writes, resumable across chunk calls. The dispatch discipline is
// the validation machine's (stage2_arm64.s): per-class handlers reached
// by an indirect branch off the source byte, each ending in its own copy
// of the dispatch tail, the word-exhausted test on a separate CBZ.
// Handlers carry entry writes and close patches, so they occupy 256-byte
// slots (64 instructions) dispatched through stage2IdxClsOff.
//
// Register map (live across the call):
//   R0  src base (absolute)   R13 keyRow8 (1<<3 when key context follows)
//   R1  emit cursor           R14 entry cursor (STP.P writes)
//   R2  emit end              R15 scalar-entry cursor (uint32 stores)
//   R3  pos (absolute)        R19 scope slab base
//   R4  m (mask word)         R20 hr = maxDepth+1-depth (0 => too deep)
//   R5  j (position)          R21 handler slot base
//   R9  bad (sticky)          R22 state pointer
//   R10 depth                 R24 entry storage end
//   R11 inObj8 (8=object)     R25 entry storage base
//   R12 prevRowIO             R26 current container's member count
//   R6,R16,R17,R23 scratch    R7,R8 dispatch/pair tables
//
// Entry writes: every value or key token stores one 16-byte production
// entry via STP.P — word0 = start | end<<32, word1 = next | info<<32 —
// against a bounds register (R24); exhaustion aborts with the Full flag
// and the caller falls back, so a store can never land outside the
// caller's storage. Containers push {entry index, saved parent count,
// kind} onto the scope slab and patch their own entry at the close:
// end, subtree span (next), and the member count, which lives in a
// register and is bumped at commas. The slab is written at opens and
// read once at the matching close — the patch stores go to entry memory
// nothing reloads, so no store-to-load forwarding chain forms.
//
// Aborts (storage full, depth past the cap, depth underflow) set a Bad
// flag and return; the machine's only contract is to shortcut accepting
// documents, so an abort simply hands the input to the portable builder.
// Underflow must abort rather than run on: a close patches the entry
// named by the slab, and only the abort guarantees that name is one this
// document's opens wrote.

#define IDISPATCH \
	CBZ   R4, iwordnext        \
	RBIT  R4, R16              \
	CLZ   R16, R16             \
	ADD   R16, R3, R5          \
	SUB   $1, R4, R17          \
	AND   R17, R4, R4          \
	MOVBU (R0)(R5), R6         \
	MOVD  (R7)(R6<<3), R6      \
	ADD   R6, R21, R16         \
	JMP   (R16)

TEXT ·stage2IndexLoop(SB), NOSPLIT, $0-96
	B    imain

	// ---- handler slots: symbol+256 + class*256 ----

	PCALIGN $256
	// O: '{'  push object, write its entry, save the parent scope
	MOVBU (R8)(R12), R16       // pair table, column 0
	ORR   R16, R9, R9
	CMP   R24, R14
	BHS   idxfull
	ADD   $1, R10, R10
	SUBS  $1, R20, R20
	BEQ   idxdeep
	SUB   R25, R14, R16        // this entry's byte offset
	LSL   $28, R16, R16        // entry index << 32
	ORR   R26<<4, R16, R16     // | parent count << 4
	ORR   $8, R16, R16         // | object kind
	AND   $127, R10, R17
	MOVD  R16, (R19)(R17<<3)   // slab[depth] = scope word
	MOVD  $(6<<58), R23        // word1: next 0, info Object<<26
	STP.P (R5, R23), 16(R14)   // entry {start=j, end=0}
	MOVD  ZR, R26              // fresh member count
	MOVD  $8, R11              // inObj8
	MOVD  $8, R12              // prevRowIO = rowO<<4 | inObj8
	MOVD  $8, R13              // keyRow8: '{' opens a key context
	B     ifusedKey            // a key quote nearly always follows '{'

	PCALIGN $256
	// A: '['  push array, write its entry, save the parent scope
	ADD   $1, R12, R16
	MOVBU (R8)(R16), R16
	ORR   R16, R9, R9
	CMP   R24, R14
	BHS   idxfull
	ADD   $1, R10, R10
	SUBS  $1, R20, R20
	BEQ   idxdeep
	SUB   R25, R14, R16
	LSL   $28, R16, R16
	ORR   R26<<4, R16, R16     // array kind: bit 3 stays clear
	AND   $127, R10, R17
	MOVD  R16, (R19)(R17<<3)
	MOVD  $(5<<58), R23        // word1: next 0, info Array<<26
	STP.P (R5, R23), 16(R14)
	MOVD  ZR, R26
	MOVD  ZR, R11
	MOVD  $16, R12             // rowA<<4
	MOVD  ZR, R13
	IDISPATCH

	PCALIGN $256
	// C: '}'  fix a preceding string's end, pop, patch the object
	ADD   $2, R12, R16
	MOVBU (R8)(R16), R16
	ORR   R16, R9, R9
	UBFX  $4, R12, $3, R16     // previous refined class
	CMP   $6, R16
	BNE   cfxdone         // only string rows (Q, Qk) leave an open end
	CMP   R25, R14
	BLS   cfxdone         // no entry to patch (resumed misuse; fail closed)
	SUB   $1, R5, R16
cfxscan:
	MOVBU (R0)(R16), R17
	CMP   $32, R17             // whitespace is <= 0x20; the close is not
	BHI   cfxhit
	SUB   $1, R16, R16
	B     cfxscan
cfxhit:
	ADD   $1, R16, R17
	MOVW  R17, -12(R14)        // the string entry's end = close + 1
cfxdone:
	SUB   $1, R10, R10
	TBNZ  $63, R10, idxunder   // underflow aborts before any patch
	ADD   $1, R20, R20
	ADD   $1, R10, R16
	AND   $127, R16, R16
	MOVD  (R19)(R16<<3), R17   // this container's scope word
	AND   $8, R17, R16
	EOR   $8, R16, R16
	ORR   R16>>3, R9, R9       // kind mismatch (array on top)
	CMP   $8, R12              // empty iff prev token was this '{'
	CSINC EQ, R26, R26, R6     // members = count, +1 when non-empty
	UBFX  $4, R17, $26, R26    // parent count, saved here at the open
	LSR   $32, R17, R16        // entry index
	ADD   R16<<4, R25, R23     // entry address
	ADD   $1, R5, R12          // end = j+1 (prev row consumed above)
	MOVW  R12, 4(R23)          // entry.end
	SUB   R25, R14, R12
	LSR   $4, R12, R12
	SUB   R16, R12, R12        // next = current index - entry index
	MOVD  $(6<<26), R16
	ADD   R16, R6, R6          // info = members | Object<<26
	ORR   R6<<32, R12, R12
	MOVD  R12, 8(R23)          // entry.{next,info}
	AND   $127, R10, R16
	MOVD  (R19)(R16<<3), R17   // parent scope word
	AND   $8, R17, R11         // enclosing kind
	ORR   $32, R11, R12        // rowC<<4 | inObj8
	MOVD  ZR, R13
	TBNZ  $3, R11, ifusedComma // in an object: ',' or another '}' next
	IDISPATCH

	PCALIGN $256
	// B: ']'  fix a preceding string's end, pop, patch the array
	ADD   $3, R12, R16
	MOVBU (R8)(R16), R16
	ORR   R16, R9, R9
	UBFX  $4, R12, $3, R16     // previous refined class
	CMP   $6, R16
	BNE   bfxdone         // only string rows (Q, Qk) leave an open end
	CMP   R25, R14
	BLS   bfxdone         // no entry to patch (resumed misuse; fail closed)
	SUB   $1, R5, R16
bfxscan:
	MOVBU (R0)(R16), R17
	CMP   $32, R17             // whitespace is <= 0x20; the close is not
	BHI   bfxhit
	SUB   $1, R16, R16
	B     bfxscan
bfxhit:
	ADD   $1, R16, R17
	MOVW  R17, -12(R14)        // the string entry's end = close + 1
bfxdone:
	SUB   $1, R10, R10
	TBNZ  $63, R10, idxunder
	ADD   $1, R20, R20
	ADD   $1, R10, R16
	AND   $127, R16, R16
	MOVD  (R19)(R16<<3), R17
	AND   $8, R17, R16
	ORR   R16>>3, R9, R9       // kind mismatch (object on top)
	CMP   $16, R12             // empty iff prev token was this '['
	CSINC EQ, R26, R26, R6
	UBFX  $4, R17, $26, R26    // parent count, saved here at the open
	LSR   $32, R17, R16
	ADD   R16<<4, R25, R23
	ADD   $1, R5, R12
	MOVW  R12, 4(R23)
	SUB   R25, R14, R12
	LSR   $4, R12, R12
	SUB   R16, R12, R12
	MOVD  $(5<<26), R16
	ADD   R16, R6, R6          // info = members | Array<<26
	ORR   R6<<32, R12, R12
	MOVD  R12, 8(R23)
	AND   $127, R10, R16
	MOVD  (R19)(R16<<3), R17
	AND   $8, R17, R11
	ORR   $48, R11, R12        // rowB<<4 | inObj8
	MOVD  ZR, R13
	TBNZ  $3, R11, ifusedComma // in an object: ',' or another '}' next
	IDISPATCH

	PCALIGN $256
	// L: ':'  fix the preceding key string's end
	ADD   $4, R12, R16
	MOVBU (R8)(R16), R16
	ORR   R16, R9, R9
	UBFX  $4, R12, $3, R16     // previous refined class
	CMP   $6, R16
	BNE   lfxdone         // only string rows (Q, Qk) leave an open end
	CMP   R25, R14
	BLS   lfxdone         // no entry to patch (resumed misuse; fail closed)
	SUB   $1, R5, R16
lfxscan:
	MOVBU (R0)(R16), R17
	CMP   $32, R17             // whitespace is <= 0x20; the close is not
	BHI   lfxhit
	SUB   $1, R16, R16
	B     lfxscan
lfxhit:
	ADD   $1, R16, R17
	MOVW  R17, -12(R14)        // the string entry's end = close + 1
lfxdone:
	ORR   $64, R11, R12        // rowL<<4 | inObj8
	MOVD  ZR, R13
	IDISPATCH

	PCALIGN $256
	// M: ','  fix a preceding string's end, bump the member count
	ADD   $5, R12, R16
	MOVBU (R8)(R16), R16
	ORR   R16, R9, R9
	CMP   $0, R10
	CSINC NE, R9, R9, R9       // comma at depth 0
	// String-end fixup: when the previous token was a string, its close
	// is the first non-whitespace byte behind this token (the grammar
	// admits nothing else between them), and its entry is the last one
	// written.
	UBFX  $4, R12, $3, R16     // previous refined class
	CMP   $6, R16
	BNE   mfxdone         // only string rows (Q, Qk) leave an open end
	CMP   R25, R14
	BLS   mfxdone         // no entry to patch (resumed misuse; fail closed)
	SUB   $1, R5, R16
mfxscan:
	MOVBU (R0)(R16), R17
	CMP   $32, R17             // whitespace is <= 0x20; the close is not
	BHI   mfxhit
	SUB   $1, R16, R16
	B     mfxscan
mfxhit:
	ADD   $1, R16, R17
	MOVW  R17, -12(R14)        // the string entry's end = close + 1
mfxdone:
	ADD   $1, R26, R26
	ADD   $80, R11, R12        // rowM<<4 | inObj8
	MOVD  R11, R13             // keyRow8 = inObj8: a key follows in objects
	TBNZ  $3, R11, ifusedKey   // in an object, only a key quote can follow
	IDISPATCH

	PCALIGN $256
	// Q: '"'  write the string entry; the caller finishes end and flags
	ADD   $6, R12, R16
	MOVBU (R8)(R16), R16
	ORR   R16, R9, R9
	CMP   R24, R14
	BHS   idxfull
	MOVD  $(4<<58 + 1), R23    // word1: next 1, info String<<26
	ORR   R13<<59, R23, R23    // key flag (keyRow8 bit 3 lands at info bit 30)
	STP.P (R5, R23), 16(R14)   // entry {start=j, end pending}
	ADD   R13<<4, R11, R16     // (key ? 8 : 0)<<4 + inObj8
	ADD   $96, R16, R12        // (rowQ | keybit)<<4 | inObj8
	TBNZ  $3, R13, ifusedL     // a key quote is always followed by ':'
	MOVD  ZR, R13
	TBNZ  $3, R11, ifusedCommaQ // object value: ',' plus this string's end fix
	IDISPATCH

	PCALIGN $256
	// S: scalar start — placeholder entry, body finished by the caller
	ADD   $7, R12, R16
	MOVBU (R8)(R16), R16
	ORR   R16, R9, R9
	CMP   R24, R14
	BHS   idxfull
	SUB   R25, R14, R16
	LSR   $4, R16, R16
	MOVW.P R16, 4(R15)         // record the scalar entry index
	MOVD  $1, R23              // word1: next 1, info 0 (placeholder kind)
	STP.P (R5, R23), 16(R14)   // entry {start=j, end pending}
	ORR   $112, R11, R12       // rowS<<4 | inObj8
	MOVD  ZR, R13
	TBNZ  $3, R11, ifusedComma
	IDISPATCH

	// ---- fused forced transitions (see stage2_arm64.s; identical
	// prediction structure, with the entry writes the tokens carry) ----
ifusedCommaQ:
	CBZ   R4, ifusedCommaQNext
	RBIT  R4, R16
	CLZ   R16, R16
	ADD   R16, R3, R5
	// The peeked position anchors the string's close whatever the token
	// turns out to be; a bail re-fixes idempotently in its handler.
	CMP   R25, R14
	BLS   fcqdone         // no entry to patch (resumed misuse; fail closed)
	SUB   $1, R5, R16
fcqscan:
	MOVBU (R0)(R16), R17
	CMP   $32, R17             // whitespace is <= 0x20; the close is not
	BHI   fcqhit
	SUB   $1, R16, R16
	B     fcqscan
fcqhit:
	ADD   $1, R16, R17
	MOVW  R17, -12(R14)        // the string entry's end = close + 1
fcqdone:
	MOVBU (R0)(R5), R6
	CMP   $44, R6              // ','
	BEQ   ifusedCommaHit
	CMP   $125, R6             // '}'
	BEQ   ifusedClose
	B     idispatchKnown

ifusedComma:
	CBZ   R4, ifusedCommaNext
	RBIT  R4, R16
	CLZ   R16, R16
	ADD   R16, R3, R5
	MOVBU (R0)(R5), R6
	CMP   $44, R6              // ','
	BEQ   ifusedCommaHit
	CMP   $125, R6             // '}': close inline and chain upward
	BNE   idispatchKnown

	// ifusedClose: the '}' following a completed object value. Legality
	// and the kind match are guaranteed by the entry guards (a completed
	// value inside an object), the preceding string's end was fixed at
	// the peek that got us here, and a value just completed, so the
	// member count is count+1 unconditionally — the empty-object case
	// can only reach the generic C handler.
ifusedClose:
	SUB   $1, R4, R16
	AND   R16, R4, R4
	SUB   $1, R10, R10
	TBNZ  $63, R10, idxunder   // underflow aborts before any patch
	ADD   $1, R20, R20
	ADD   $1, R10, R16
	AND   $127, R16, R16
	MOVD  (R19)(R16<<3), R17   // this container's scope word
	ADD   $1, R26, R6          // members = count + 1
	UBFX  $4, R17, $26, R26    // parent count, saved here at the open
	LSR   $32, R17, R16        // entry index
	ADD   R16<<4, R25, R23     // entry address
	ADD   $1, R5, R12          // end = j+1
	MOVW  R12, 4(R23)          // entry.end
	SUB   R25, R14, R12
	LSR   $4, R12, R12
	SUB   R16, R12, R12        // next = current index - entry index
	MOVD  $(6<<26), R16
	ADD   R16, R6, R6          // info = members | Object<<26
	ORR   R6<<32, R12, R12
	MOVD  R12, 8(R23)          // entry.{next,info}
	AND   $127, R10, R16
	MOVD  (R19)(R16<<3), R17   // parent scope word
	AND   $8, R17, R11         // enclosing kind
	ORR   $32, R11, R12        // rowC<<4 | inObj8
	MOVD  ZR, R13
	TBNZ  $3, R11, ifusedComma // nested closes chain until an array
	IDISPATCH

ifusedCommaHit:
	SUB   $1, R4, R16
	AND   R16, R4, R4
	CMP   $0, R10
	CSINC NE, R9, R9, R9       // comma at depth 0
	ADD   $1, R26, R26
	ADD   $80, R11, R12        // rowM<<4 | inObj8
	MOVD  R11, R13

ifusedKey:
	CBZ   R4, ifusedKeyNext
	RBIT  R4, R16
	CLZ   R16, R16
	ADD   R16, R3, R5
	MOVBU (R0)(R5), R6
	CMP   $34, R6              // '"'
	BNE   idispatchKnown
	SUB   $1, R4, R16
	AND   R16, R4, R4
	CMP   R24, R14
	BHS   idxfull
	MOVD  $(4<<58 + 1), R23
	ORR   $(1<<62), R23, R23   // key flag
	STP.P (R5, R23), 16(R14)
	ADD   $224, R11, R12       // rowQk<<4 | inObj8

	// ifusedL: after a key quote the only legal token is ':'.
ifusedL:
	CBZ   R4, ifusedLNext
	RBIT  R4, R16
	CLZ   R16, R16
	ADD   R16, R3, R5
	CMP   R25, R14
	BLS   flxdone         // no entry to patch (resumed misuse; fail closed)
	SUB   $1, R5, R16
flxscan:
	MOVBU (R0)(R16), R17
	CMP   $32, R17             // whitespace is <= 0x20; the close is not
	BHI   flxhit
	SUB   $1, R16, R16
	B     flxscan
flxhit:
	ADD   $1, R16, R17
	MOVW  R17, -12(R14)        // the string entry's end = close + 1
flxdone:
	MOVBU (R0)(R5), R6
	CMP   $58, R6              // ':'
	BNE   idispatchKnown
	SUB   $1, R4, R16
	AND   R16, R4, R4
	ORR   $64, R11, R12        // rowL<<4 | inObj8
	MOVD  ZR, R13

	// ifusedValue: after the fused ':', consume a string or scalar value —
	// together the dominant object-value classes. The scalar leg writes
	// its placeholder entry exactly as the S handler would and rejoins the
	// fixup-free comma peek; the string leg rejoins the fixing one. Any
	// other class bails on the dispatch slot already loaded for the test.
	// (A wider array-specific fusion was tried in the consumer study and
	// rejected: code growth regressed the object and FHIR shapes.)
ifusedValue:
	CBZ   R4, ifusedValueNext
	RBIT  R4, R16
	CLZ   R16, R16
	ADD   R16, R3, R5
	MOVBU (R0)(R5), R6
	CMP   $34, R6              // '"'
	BEQ   ifusedValueQ
	MOVD  (R7)(R6<<3), R17
	CMP   $(7*256), R17        // scalar-start class slot
	BNE   idispatchOffsetKnown
	SUB   $1, R4, R16
	AND   R16, R4, R4
	CMP   R24, R14
	BHS   idxfull
	SUB   R25, R14, R16
	LSR   $4, R16, R16
	MOVW.P R16, 4(R15)
	MOVD  $1, R23              // word1: next 1, info 0 (placeholder kind)
	STP.P (R5, R23), 16(R14)
	ORR   $112, R11, R12       // rowS<<4 | inObj8
	MOVD  ZR, R13
	B     ifusedComma

ifusedValueQ:
	SUB   $1, R4, R16
	AND   R16, R4, R4
	CMP   R24, R14
	BHS   idxfull
	MOVD  $(4<<58 + 1), R23
	STP.P (R5, R23), 16(R14)
	ADD   $96, R11, R12        // rowQv<<4 | inObj8
	MOVD  ZR, R13
	B     ifusedCommaQ

	// idispatchOffsetKnown: bail with the dispatch slot already in hand.
idispatchOffsetKnown:
	SUB   $1, R4, R16
	AND   R16, R4, R4
	ADD   R17, R21, R16
	JMP   (R16)

	// idispatchKnown: shared bail target for failed peeks; the byte is
	// already loaded, so consume the bit and dispatch off it.
idispatchKnown:
	SUB   $1, R4, R16
	AND   R16, R4, R4
	MOVD  (R7)(R6<<3), R6
	ADD   R6, R21, R16
	JMP   (R16)

	// Word-advance stubs for the fused peeks.
ifusedCommaQNext:
	ADD  $64, R3, R3
	CMP  R1, R2
	BEQ  idone
	MOVD.P 8(R1), R4
	B    ifusedCommaQ

ifusedCommaNext:
	ADD  $64, R3, R3
	CMP  R1, R2
	BEQ  idone
	MOVD.P 8(R1), R4
	B    ifusedComma

ifusedKeyNext:
	ADD  $64, R3, R3
	CMP  R1, R2
	BEQ  idone
	MOVD.P 8(R1), R4
	B    ifusedKey

ifusedLNext:
	ADD  $64, R3, R3
	CMP  R1, R2
	BEQ  idone
	MOVD.P 8(R1), R4
	B    ifusedL

ifusedValueNext:
	ADD  $64, R3, R3
	CMP  R1, R2
	BEQ  idone
	MOVD.P 8(R1), R4
	B    ifusedValue

	// Aborts: flag the state and suspend; the caller falls back to the
	// portable builder, so nothing after the flag matters.
idxfull:
	ORR  $(1<<62), R9, R9
	B    idone

idxdeep:
	ORR  $(1<<61), R9, R9
	B    idone

idxunder:
	ORR  $1, R9, R9
	B    idone

	// ---- main ----
imain:
	MOVD base+0(FP), R0
	MOVD pos+8(FP), R3
	MOVD emit+16(FP), R1
	MOVD nwords+24(FP), R2
	ADD  R2<<3, R1, R2         // emit end
	MOVD clsOff+32(FP), R7
	MOVD pt+40(FP), R8
	MOVD slab+48(FP), R19
	MOVD ent+56(FP), R25
	MOVD entCap+64(FP), R24
	ADD  R24<<4, R25, R24      // entry storage end
	MOVD scalars+72(FP), R15
	MOVD st+80(FP), R22
	MOVD $·stage2IndexLoop(SB), R21
	ADD  $256, R21             // handler slot base

	// Resume: carried words load from the state struct; the derived
	// registers rebuild from them (see stage2_arm64.s).
	MOVD 0(R22), R9            // bad
	MOVD 8(R22), R10           // depth
	MOVD 16(R22), R12          // prevRowIO
	MOVD 24(R22), R13          // keyRow8
	MOVD 32(R22), R26          // member count
	MOVD 40(R22), R14
	ADD  R25, R14, R14         // entry cursor = base + offset
	AND  $8, R12, R11          // inObj8
	MOVD $65, R20              // Stage2IndexMaxDepth + 1
	SUB  R10, R20, R20         // headroom

iwordloop:
	MOVD.P 8(R1), R4
	IDISPATCH

iwordnext:
	ADD  $64, R3, R3
	CMP  R1, R2
	BNE  iwordloop

idone:
	MOVD R9, 0(R22)            // bad
	MOVD R10, 8(R22)           // depth
	MOVD R12, 16(R22)          // prevRowIO
	MOVD R13, 24(R22)          // keyRow8
	MOVD R26, 32(R22)          // member count
	SUB  R25, R14, R16
	MOVD R16, 40(R22)          // entry cursor offset
	MOVD scalars+72(FP), R16
	SUB  R16, R15, R16
	LSR  $2, R16, R16
	MOVD R16, nscalars+88(FP)
	RET
