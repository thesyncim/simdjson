//go:build goexperiment.simd && amd64

#include "textflag.h"

DATA scanStringSpecialTarget<>+0(SB)/8, $·scanStringSpecialScalar(SB)
GLOBL scanStringSpecialTarget<>(SB), NOPTR, $8
DATA scanStringSyntaxTarget<>+0(SB)/8, $·scanStringSyntaxScalar(SB)
GLOBL scanStringSyntaxTarget<>(SB), NOPTR, $8
DATA scanEncodedHTMLSpecialTarget<>+0(SB)/8, $·scanEncodedHTMLSpecialScalar(SB)
GLOBL scanEncodedHTMLSpecialTarget<>(SB), NOPTR, $8
DATA scanEncodedHTMLSyntaxTarget<>+0(SB)/8, $·scanEncodedHTMLSyntaxScalar(SB)
GLOBL scanEncodedHTMLSyntaxTarget<>(SB), NOPTR, $8

TEXT ·selectAVX2Scanner(SB), NOSPLIT|NOFRAME, $0-0
	MOVQ $·scanStringSpecialAVX2(SB), AX
	MOVQ AX, scanStringSpecialTarget<>(SB)
	MOVQ $·scanStringSyntaxAVX2(SB), AX
	MOVQ AX, scanStringSyntaxTarget<>(SB)
	MOVQ $·scanEncodedHTMLSpecialAVX2(SB), AX
	MOVQ AX, scanEncodedHTMLSpecialTarget<>(SB)
	MOVQ $·scanEncodedHTMLSyntaxAVX2(SB), AX
	MOVQ AX, scanEncodedHTMLSyntaxTarget<>(SB)
	RET

// Each entry point keeps the ABI0 argument frame intact and tail-jumps
// to a same-signature Go ABI wrapper. The selected target returns
// directly to the original Go caller.
TEXT ·scanStringSpecialRuntime(SB), NOSPLIT|NOFRAME, $0-40
	MOVQ scanStringSpecialTarget<>(SB), AX
	JMP AX

TEXT ·scanStringSyntaxRuntime(SB), NOSPLIT|NOFRAME, $0-40
	MOVQ scanStringSyntaxTarget<>(SB), AX
	JMP AX

TEXT ·scanEncodedHTMLSpecialRuntime(SB), NOSPLIT|NOFRAME, $0-40
	MOVQ scanEncodedHTMLSpecialTarget<>(SB), AX
	JMP AX

TEXT ·scanEncodedHTMLSyntaxRuntime(SB), NOSPLIT|NOFRAME, $0-40
	MOVQ scanEncodedHTMLSyntaxTarget<>(SB), AX
	JMP AX
