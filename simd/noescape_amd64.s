//go:build goexperiment.simd && amd64

#include "textflag.h"

// noescapeBytes copies a slice header unchanged. The Go declaration carries
// //go:noescape, so passing the returned header through a selected function
// value does not make the original parser state escape.
TEXT ·noescapeBytes(SB), NOSPLIT, $0-48
	MOVQ src_base+0(FP), AX
	MOVQ src_len+8(FP), BX
	MOVQ src_cap+16(FP), CX
	MOVQ AX, ret_base+24(FP)
	MOVQ BX, ret_len+32(FP)
	MOVQ CX, ret_cap+40(FP)
	RET
