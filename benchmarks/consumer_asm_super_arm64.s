//go:build arm64

#include "textflag.h"

// consumerAsmLoopSuper: an externally supplied variant of the
// demonstration consumer (consumer_asm_arm64.s), added verbatim for
// verification and measurement. Claimed deltas over the landed loop:
// fusedComma also consumes '}' inline so nested object closes chain
// without generic dispatch; fusedValue consumes scalar values after ':'
// (not just strings), reusing the loaded dispatch slot on a miss
// (dispatchOffsetKnown); every fused-guard miss dispatches off the
// already-loaded byte. The differential harness holds it to the same
// verdicts and entry stream as the original.
//
// Fenced negative from the same study: a larger ARRAY-specific fusion
// (guessing whole element runs inside arrays) was tested and rejected —
// the code growth produced inconsistent results and regressed the
// object and FHIR shapes. The fusions here are deliberately gated to
// object context (the TBNZ inObj guards); do not widen them to arrays
// without fresh evidence.

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

TEXT ·consumerAsmLoopSuper(SB), NOSPLIT, $0-64
    B    main

    PCALIGN $128
    // O: '{'
    MOVBU (R8)(R12), R16
    ORR   R16, R9, R9
    MOVD  $(1<<58), R23
    STP.P (R5, R23), 16(R14)
    ADD   $1, R10, R10
    SUBS  $1, R20, R20
    CSINC NE, R9, R9, R9
    AND   $16383, R10, R16
    MOVD  $8, R17
    MOVB  R17, (R19)(R16)
    MOVD  $8, R11
    MOVD  $8, R12
    MOVD  $8, R13
    MOVD  ZR, R15
    B     fusedKey

    PCALIGN $128
    // A: '['
    ADD   $1, R12, R16
    MOVBU (R8)(R16), R16
    ORR   R16, R9, R9
    MOVD  $(2<<58), R23
    STP.P (R5, R23), 16(R14)
    ADD   $1, R10, R10
    SUBS  $1, R20, R20
    CSINC NE, R9, R9, R9
    AND   $16383, R10, R16
    MOVB  ZR, (R19)(R16)
    MOVD  ZR, R11
    MOVD  $16, R12
    MOVD  ZR, R13
    MOVD  ZR, R15
    DISPATCH

    PCALIGN $128
    // C: '}'
    ADD   $2, R12, R16
    MOVBU (R8)(R16), R16
    ORR   R16, R9, R9
    MOVD  $(3<<58), R23
    STP.P (R5, R23), 16(R14)
    EOR   $8, R11, R16
    ORR   R16>>3, R9, R9
    SUB   $1, R10, R10
    ORR   R10>>63, R9, R9
    ADD   $1, R20, R20
    AND   $16383, R10, R16
    MOVBU (R19)(R16), R11
    MOVD  $256, R16
    CMP   $0, R10
    CSEL  EQ, R16, ZR, R15
    ORR   $32, R11, R12
    MOVD  ZR, R13
    TBNZ  $3, R11, fusedComma
    DISPATCH

    PCALIGN $128
    // B: ']'
    ADD   $3, R12, R16
    MOVBU (R8)(R16), R16
    ORR   R16, R9, R9
    MOVD  $(4<<58), R23
    STP.P (R5, R23), 16(R14)
    ORR   R11>>3, R9, R9
    SUB   $1, R10, R10
    ORR   R10>>63, R9, R9
    ADD   $1, R20, R20
    AND   $16383, R10, R16
    MOVBU (R19)(R16), R11
    MOVD  $256, R16
    CMP   $0, R10
    CSEL  EQ, R16, ZR, R15
    ORR   $48, R11, R12
    MOVD  ZR, R13
    TBNZ  $3, R11, fusedComma
    DISPATCH

    PCALIGN $128
    // L: ':'
    ADD   $4, R12, R16
    MOVBU (R8)(R16), R16
    ORR   R16, R9, R9
    MOVD  $(5<<58), R23
    STP.P (R5, R23), 16(R14)
    ORR   $64, R11, R12
    MOVD  ZR, R13
    DISPATCH

    PCALIGN $128
    // M: ','
    ADD   $5, R12, R16
    MOVBU (R8)(R16), R16
    ORR   R16, R9, R9
    ORR   R15, R9, R9
    MOVD  $(6<<58), R23
    STP.P (R5, R23), 16(R14)
    ADD   $80, R11, R12
    MOVD  R11, R13
    TBNZ  $3, R11, fusedKey
    DISPATCH

    PCALIGN $128
    // Q: '"'
    ADD   $6, R12, R16
    MOVBU (R8)(R16), R16
    ORR   R16, R9, R9
    MOVD  $(7<<58), R23
    STP.P (R5, R23), 16(R14)
    ADD   R13<<4, R11, R16
    ADD   $96, R16, R12
    TBNZ  $3, R13, fusedL
    MOVD  ZR, R13
    TBNZ  $3, R11, fusedComma
    DISPATCH

    PCALIGN $128
    // S: scalar start
    ADD   $7, R12, R16
    MOVBU (R8)(R16), R16
    ORR   R16, R9, R9
    MOVD  $(8<<58), R23
    STP.P (R5, R23), 16(R14)
    ORR   $112, R11, R12
    MOVD  ZR, R13
    TBNZ  $3, R11, fusedComma
    DISPATCH

// After a completed object value, the complete valid follower set is ',' or
// '}'. Handle both here. This removes the generic dispatch for every object
// close and lets nested closes chain until the enclosing container is an array.
fusedComma:
    CBZ   R4, fusedCommaNext
    RBIT  R4, R16
    CLZ   R16, R16
    ADD   R16, R3, R5
    MOVBU (R0)(R5), R6
    CMP   $44, R6
    BEQ   fusedCommaHit
    CMP   $125, R6
    BNE   dispatchKnown
    SUB   $1, R4, R16
    AND   R16, R4, R4
    MOVD  $(3<<58), R23
    STP.P (R5, R23), 16(R14)
    SUB   $1, R10, R10
    ORR   R10>>63, R9, R9
    ADD   $1, R20, R20
    AND   $16383, R10, R16
    MOVBU (R19)(R16), R11
    MOVD  $256, R16
    CMP   $0, R10
    CSEL  EQ, R16, ZR, R15
    ORR   $32, R11, R12
    MOVD  ZR, R13
    TBNZ  $3, R11, fusedComma
    DISPATCH

fusedCommaHit:
    SUB   $1, R4, R16
    AND   R16, R4, R4
    MOVD  $(6<<58), R23
    STP.P (R5, R23), 16(R14)
    ORR   R15, R9, R9
    ADD   $80, R11, R12
    MOVD  R11, R13

fusedKey:
    CBZ   R4, fusedKeyNext
    RBIT  R4, R16
    CLZ   R16, R16
    ADD   R16, R3, R5
    MOVBU (R0)(R5), R6
    CMP   $34, R6
    BNE   dispatchKnown
    SUB   $1, R4, R16
    AND   R16, R4, R4
    MOVD  $(7<<58), R23
    STP.P (R5, R23), 16(R14)
    ADD   $224, R11, R12

fusedL:
    CBZ   R4, fusedLNext
    RBIT  R4, R16
    CLZ   R16, R16
    ADD   R16, R3, R5
    MOVBU (R0)(R5), R6
    CMP   $58, R6
    BNE   dispatchKnown
    SUB   $1, R4, R16
    AND   R16, R4, R4
    MOVD  $(5<<58), R23
    STP.P (R5, R23), 16(R14)
    ORR   $64, R11, R12
    MOVD  ZR, R13

// The original code only guessed a string after ':'. A scalar is the other
// dominant object-value class. Load the already-hot byte->slot table once,
// consume scalar values directly, and reuse the loaded slot on every miss.
fusedValue:
    CBZ   R4, fusedValueNext
    RBIT  R4, R16
    CLZ   R16, R16
    ADD   R16, R3, R5
    MOVBU (R0)(R5), R6
    CMP   $34, R6
    BEQ   fusedValueQ
    MOVD  (R7)(R6<<3), R17
    CMP   $(7*128), R17
    BNE   dispatchOffsetKnown
    SUB   $1, R4, R16
    AND   R16, R4, R4
    MOVD  $(8<<58), R23
    STP.P (R5, R23), 16(R14)
    ORR   $112, R11, R12
    MOVD  ZR, R13
    B     fusedComma

fusedValueQ:
    SUB   $1, R4, R16
    AND   R16, R4, R4
    MOVD  $(7<<58), R23
    STP.P (R5, R23), 16(R14)
    ADD   $96, R11, R12
    MOVD  ZR, R13
    B     fusedComma

dispatchOffsetKnown:
    SUB   $1, R4, R16
    AND   R16, R4, R4
    ADD   R17, R21, R16
    JMP   (R16)

dispatchKnown:
    SUB   $1, R4, R16
    AND   R16, R4, R4
    MOVD  (R7)(R6<<3), R6
    ADD   R6, R21, R16
    JMP   (R16)

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

fusedValueNext:
    ADD  $64, R3, R3
    CMP  R1, R2
    BEQ  done
    MOVD.P 8(R1), R4
    B    fusedValue

main:
    MOVD base+0(FP), R0
    MOVD emit+8(FP), R1
    MOVD nwords+16(FP), R2
    ADD  R2<<3, R1, R2
    MOVD clsOff+24(FP), R7
    MOVD pt+32(FP), R8
    MOVD kinds+40(FP), R19
    MOVD entries+48(FP), R14
    MOVD out+56(FP), R22
    MOVD $·consumerAsmLoopSuper(SB), R21
    ADD  $128, R21
    MOVD ZR, R3
    MOVD ZR, R9
    MOVD ZR, R10
    MOVD ZR, R11
    MOVD ZR, R13
    MOVD $128, R12
    MOVD $256, R15
    MOVD $10001, R20

wordloop:
    MOVD.P 8(R1), R4
    DISPATCH

wordnext:
    ADD  $64, R3, R3
    CMP  R1, R2
    BNE  wordloop

done:
    MOVD R9, 0(R22)
    MOVD R10, 8(R22)
    MOVD R12, 16(R22)
    MOVD R14, 24(R22)
    RET
