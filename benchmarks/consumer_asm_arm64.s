//go:build arm64

#include "textflag.h"

// consumerAsmLoop: the pair-table grammar + entry-write machine with
// direct-threaded per-class handlers.
//
// Layout: the first instruction jumps over the handler block; handlers
// start at symbol+128 and occupy one 128-byte slot per class code
// (O,A,C,B,L,M,Q,S). consumerClsOff maps a source byte to class*128, so
// dispatch is: load byte, load displacement, add to handler base, BR.
// Every handler ends with its own copy of the dispatch tail (classic
// direct threading): distinct indirect-branch sites give the predictor
// the current class as context, which is what keeps the less-regular
// corpora (FHIR, tweets) predictable. The word-exhausted test stays a
// separate conditional branch: an experiment that folded it into the
// indirect target stream (a sentinel slot selected by CLZ=64) ran 2x
// slower on the irregular corpora — the CBZ's own predictor is cheaper
// than widening the indirect branch's target set. Each handler must stay
// within its 128-byte slot (32 instructions); PCALIGN snaps every handler
// to its slot and the differential harness fails loudly if one overflows.
//
// Register map (live across the whole document):
//   R0  src base            R12 prevRowIO (row<<4 | inObj8)
//   R1  emit cursor         R13 keyRow8 (1<<3 when key context follows)
//   R2  emit end            R14 entries cursor
//   R3  pos (block byte)    R15 dzMask8 (256 while depth==0, else 0)
//   R4  m (mask word)       R19 kinds slab base
//   R5  j (position)        R20 hr = 10001-depth (0 => over limit)
//   R9  bad (sticky)        R21 handler slot base
//   R10 depth               R22 out pointer
//   R11 inObj8 (8=object)   R7,R8 dispatch/pair tables
//   R6,R16,R17,R23 scratch
//
// Handler contract: on entry R5=j and j's bit is already cleared from R4.
// Every handler ORs its pair-table byte into bad, stores its 16-byte
// entry via STP post-index, refreshes prevRowIO/keyRow8, then dispatches
// the next position itself.

#define DISPATCH \
	CBZ   R4, wordnext         \
	RBIT  R4, R16              \
	CLZ   R16, R16             \
	ADD   R16, R3, R5          \
	SUB   $1, R4, R17          \
	AND   R17, R4, R4          \
	MOVBU (R0)(R5), R6         \
	MOVD  (R7)(R6<<3), R6      \
	ADD   R6, R21, R16         \
	JMP   (R16)

TEXT ·consumerAsmLoop(SB), NOSPLIT, $0-64
	B    main

	// ---- handler slots: symbol+128 + class*128 ----

	PCALIGN $128
	// O: '{'  push object
	MOVBU (R8)(R12), R16       // pair table, column 0
	ORR   R16, R9, R9
	MOVD  $(1<<58), R23
	STP.P (R5, R23), 16(R14)   // entry {start=j, info=kind<<58}
	ADD   $1, R10, R10
	SUBS  $1, R20, R20
	CSINC NE, R9, R9, R9       // bad grows when depth passes the limit
	AND   $16383, R10, R16
	MOVD  $8, R17
	MOVB  R17, (R19)(R16)      // kinds[depth] = object
	MOVD  $8, R11              // inObj8
	MOVD  $8, R12              // prevRowIO = rowO<<4 | inObj8
	MOVD  $8, R13              // keyRow8: '{' opens a key context
	MOVD  ZR, R15              // depth > 0
	B     fusedKey             // a key quote nearly always follows '{'

	PCALIGN $128
	// A: '['  push array
	ADD   $1, R12, R16
	MOVBU (R8)(R16), R16
	ORR   R16, R9, R9
	MOVD  $(2<<58), R23
	STP.P (R5, R23), 16(R14)
	ADD   $1, R10, R10
	SUBS  $1, R20, R20
	CSINC NE, R9, R9, R9
	AND   $16383, R10, R16
	MOVB  ZR, (R19)(R16)       // kinds[depth] = array
	MOVD  ZR, R11
	MOVD  $16, R12             // rowA<<4
	MOVD  ZR, R13
	MOVD  ZR, R15
	DISPATCH

	PCALIGN $128
	// C: '}'  pop, top must be an object
	ADD   $2, R12, R16
	MOVBU (R8)(R16), R16
	ORR   R16, R9, R9
	MOVD  $(3<<58), R23
	STP.P (R5, R23), 16(R14)
	EOR   $8, R11, R16
	ORR   R16>>3, R9, R9       // kind mismatch
	SUB   $1, R10, R10
	ORR   R10>>63, R9, R9      // underflow
	ADD   $1, R20, R20
	AND   $16383, R10, R16
	MOVBU (R19)(R16), R11      // enclosing kind
	MOVD  $256, R16
	CMP   $0, R10
	CSEL  EQ, R16, ZR, R15
	ORR   $32, R11, R12        // rowC<<4 | inObj8
	MOVD  ZR, R13
	DISPATCH

	PCALIGN $128
	// B: ']'  pop, top must be an array
	ADD   $3, R12, R16
	MOVBU (R8)(R16), R16
	ORR   R16, R9, R9
	MOVD  $(4<<58), R23
	STP.P (R5, R23), 16(R14)
	ORR   R11>>3, R9, R9       // kind mismatch (object on top)
	SUB   $1, R10, R10
	ORR   R10>>63, R9, R9
	ADD   $1, R20, R20
	AND   $16383, R10, R16
	MOVBU (R19)(R16), R11
	MOVD  $256, R16
	CMP   $0, R10
	CSEL  EQ, R16, ZR, R15
	ORR   $48, R11, R12        // rowB<<4 | inObj8
	MOVD  ZR, R13
	DISPATCH

	PCALIGN $128
	// L: ':'
	ADD   $4, R12, R16
	MOVBU (R8)(R16), R16
	ORR   R16, R9, R9
	MOVD  $(5<<58), R23
	STP.P (R5, R23), 16(R14)
	ORR   $64, R11, R12        // rowL<<4 | inObj8
	MOVD  ZR, R13
	DISPATCH

	PCALIGN $128
	// M: ','
	ADD   $5, R12, R16
	MOVBU (R8)(R16), R16
	ORR   R16, R9, R9
	ORR   R15, R9, R9          // comma at depth 0 (dzMask8 sentinel)
	MOVD  $(6<<58), R23
	STP.P (R5, R23), 16(R14)
	ADD   $80, R11, R12        // rowM<<4 | inObj8
	MOVD  R11, R13             // keyRow8 = inObj8: a key follows in objects
	TBNZ  $3, R11, fusedKey    // in an object, only a key quote can follow
	DISPATCH

	PCALIGN $128
	// Q: '"'
	ADD   $6, R12, R16
	MOVBU (R8)(R16), R16
	ORR   R16, R9, R9
	MOVD  $(7<<58), R23
	STP.P (R5, R23), 16(R14)
	ADD   R13<<4, R11, R16     // (key ? 8 : 0)<<4 + inObj8
	ADD   $96, R16, R12        // (rowQ | keybit)<<4 | inObj8
	TBNZ  $3, R13, fusedL      // a key quote is always followed by ':'
	MOVD  ZR, R13
	TBNZ  $3, R11, fusedComma  // object value: ',' then another member, usually
	DISPATCH

	PCALIGN $128
	// S: scalar start
	ADD   $7, R12, R16
	MOVBU (R8)(R16), R16
	ORR   R16, R9, R9
	MOVD  $(8<<58), R23
	STP.P (R5, R23), 16(R14)
	ORR   $112, R11, R12       // rowS<<4 | inObj8
	MOVD  ZR, R13
	TBNZ  $3, R11, fusedComma  // object value: ',' then another member, usually
	DISPATCH

	// ---- fused forced transitions (branched to from the M and Q slots,
	// living outside the 128-byte slot grid) ----
	//
	// fusedKey: after ','-in-object the only legal token is a key quote.
	// Peek the next position in this word; on an exact '"' match consume
	// it here — entry write included — and fall into fusedL for its ':'.
	// Any mismatch or a word boundary bails to the normal dispatcher,
	// where the pair table judges the (rowM,inObj=1) or (rowQk) pair as
	// usual, so fusion can never change a verdict.
	// fusedComma: after a completed value inside an object, ',' is the
	// most likely next token; on a hit, consume it (depth-0 sentinel
	// included) and chain straight into the key-quote peek. The guard is
	// object-only: short arrays (canada's coordinate pairs) mispredict a
	// comma guess half the time, objects almost never.
fusedComma:
	CBZ   R4, fusedCommaNext
	RBIT  R4, R16
	CLZ   R16, R16
	ADD   R16, R3, R5
	MOVBU (R0)(R5), R6
	CMP   $44, R6              // ','
	BNE   dispatchKnown
	SUB   $1, R4, R16
	AND   R16, R4, R4
	MOVD  $(6<<58), R23
	STP.P (R5, R23), 16(R14)
	ORR   R15, R9, R9          // comma at depth 0
	ADD   $80, R11, R12        // rowM<<4 | inObj8
	MOVD  R11, R13

fusedKey:
	CBZ   R4, fusedKeyNext
	RBIT  R4, R16
	CLZ   R16, R16
	ADD   R16, R3, R5
	MOVBU (R0)(R5), R6
	CMP   $34, R6              // '"'
	BNE   dispatchKnown
	SUB   $1, R4, R16
	AND   R16, R4, R4
	MOVD  $(7<<58), R23
	STP.P (R5, R23), 16(R14)
	ADD   $224, R11, R12       // rowQk<<4 | inObj8

	// fusedL: after a key quote the only legal token is ':'.
fusedL:
	CBZ   R4, fusedLNext
	RBIT  R4, R16
	CLZ   R16, R16
	ADD   R16, R3, R5
	MOVBU (R0)(R5), R6
	CMP   $58, R6              // ':'
	BNE   dispatchKnown
	SUB   $1, R4, R16
	AND   R16, R4, R4
	MOVD  $(5<<58), R23
	STP.P (R5, R23), 16(R14)
	ORR   $64, R11, R12        // rowL<<4 | inObj8
	MOVD  ZR, R13

	// fusedValQ: after the fused ':', guess a string value — the common
	// case in string-heavy documents — and chain back into fusedComma to
	// consume whole `"key":"value",` members per dispatch.
fusedValQ:
	CBZ   R4, fusedValQNext
	RBIT  R4, R16
	CLZ   R16, R16
	ADD   R16, R3, R5
	MOVBU (R0)(R5), R6
	CMP   $34, R6              // '"'
	BNE   dispatchKnown
	SUB   $1, R4, R16
	AND   R16, R4, R4
	MOVD  $(7<<58), R23
	STP.P (R5, R23), 16(R14)
	ADD   $96, R11, R12        // rowQv<<4 | inObj8
	B     fusedComma

	// dispatchKnown: the shared bail target for failed peeks. The peek
	// already computed j (R5) and loaded the byte (R6), so bail consumes
	// the bit and dispatches straight off the loaded byte; the pair table
	// rules on whatever actually follows.
dispatchKnown:
	SUB   $1, R4, R16
	AND   R16, R4, R4
	MOVD  (R7)(R6<<3), R6
	ADD   R6, R21, R16
	JMP   (R16)

	// Word-advance stubs: indented documents break fused chains at
	// 64-byte boundaries every few positions, so each fused peek gets a
	// retry stub that walks to the next non-empty word and resumes the
	// same peek instead of falling back to a full dispatch.
fusedCommaNext:
	ADD  $64, R3, R3
	CMP  R1, R2
	BEQ  done
	MOVD.P 8(R1), R4
	B    fusedComma

fusedKeyNext:
	ADD  $64, R3, R3
	CMP  R1, R2
	BEQ  done
	MOVD.P 8(R1), R4
	B    fusedKey

fusedLNext:
	ADD  $64, R3, R3
	CMP  R1, R2
	BEQ  done
	MOVD.P 8(R1), R4
	B    fusedL

fusedValQNext:
	ADD  $64, R3, R3
	CMP  R1, R2
	BEQ  done
	MOVD.P 8(R1), R4
	B    fusedValQ

	// ---- main ----
main:
	MOVD base+0(FP), R0
	MOVD emit+8(FP), R1
	MOVD nwords+16(FP), R2
	ADD  R2<<3, R1, R2         // emit end
	MOVD clsOff+24(FP), R7
	MOVD pt+32(FP), R8
	MOVD kinds+40(FP), R19
	MOVD entries+48(FP), R14
	MOVD out+56(FP), R22
	MOVD $·consumerAsmLoop(SB), R21
	ADD  $128, R21             // handler slot base
	MOVD ZR, R3
	MOVD ZR, R9
	MOVD ZR, R10
	MOVD ZR, R11
	MOVD ZR, R13
	MOVD $128, R12             // START row (8<<4)
	MOVD $256, R15             // depth == 0
	MOVD $10001, R20           // headroom to just past the depth limit

wordloop:
	MOVD.P 8(R1), R4
	DISPATCH

wordnext:
	ADD  $64, R3, R3
	CMP  R1, R2
	BNE  wordloop

done:
	MOVD R9, 0(R22)            // bad
	MOVD R10, 8(R22)           // depth
	MOVD R12, 16(R22)          // prevRowIO
	MOVD R14, 24(R22)          // ep
	RET
