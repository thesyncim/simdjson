package simdjson

import (
	"unsafe"

	simdkernels "github.com/thesyncim/simdjson/simd"
)

// The stage-2 machine (simd/stage2_arm64.s) replaces validBitmapWalk's
// role on builds that have it: the grammar — pair legality, container
// kinds, depth, comma/closer placement — runs in a direct-threaded
// register machine over the emit masks, and the machine records each
// scalar-start position for the Go-side body checks below. The portable
// walk remains the engine on every other build and the reference the
// machine is differentially tested against; both consume identical masks
// and must produce identical verdicts at identical chunk boundaries.

// stage2MachineEnabled gates the asm grammar machine: it needs the
// machine itself and the batched stage-1 kernel that feeds it.
var stage2MachineEnabled = simdkernels.Stage2Enabled() && simdkernels.Stage1StreamEnabled()

// The machine's depth limit must equal the walk's; both reject the open
// that would exceed it.
const _ = uint(simdkernels.Stage2MaxDepth-defaultMaxDepth) + uint(defaultMaxDepth-simdkernels.Stage2MaxDepth)

// validBitmapWalkAsm is validBitmapWalk over the stage-2 machine: it
// feeds a run of consecutive blocks' emit masks to the machine, then
// validates the recorded scalar starts. pos is the byte offset of the
// first mask; scalars needs 64*len(emits) capacity (the emit-bit bound);
// kinds persists across the document's chunks and must start zeroed.
// done reports that validation has concluded (valid always carries a
// rejection — acceptance is decided by simdkernels.Stage2Finish after
// the last chunk); otherwise the caller proceeds to the next run.
func validBitmapWalkAsm(src []byte, base unsafe.Pointer, n, pos int, emits []uint64,
	st *simdkernels.Stage2State, kinds *[simdkernels.Stage2KindsLen]byte, scalars []uint32) (valid, done bool) {
	// Emit bits at or past len(src) reject exactly like the walk's j >= n
	// guard. They cannot arise from the space-padded tail block, so the
	// scan only ever runs on the document's final chunk and fails closed
	// on masks that violate the framing contract.
	if pos+len(emits)*64 > n {
		for i := len(emits) - 1; i >= 0; i-- {
			wordBase := pos + i*64
			if wordBase >= n {
				if emits[i] != 0 {
					return false, true
				}
				continue
			}
			if rel := uint(n - wordBase); rel < 64 && emits[i]>>rel != 0 {
				return false, true
			}
			break
		}
	}

	ns := simdkernels.Stage2Walk((*byte)(unsafe.Add(base, pos)), emits, kinds, scalars, st)
	if st.Bad != 0 {
		return false, true
	}
	// Scalar bodies: the machine judged the token's placement; the byte
	// content — strict number syntax, exact literals, and the terminator
	// rule — is per-byte work validated here, immediately after the
	// machine while the source bytes are cache-warm.
	for _, rel := range scalars[:ns] {
		if !validScalarTokenAt(src, base, n, pos+int(rel)) {
			return false, true
		}
	}
	return false, false
}

// validScalarTokenAt mirrors the walk's scalar case: a strict number or
// literal starting at j, which must end at whitespace, a structural byte,
// or the document's end.
func validScalarTokenAt(src []byte, base unsafe.Pointer, n, j int) bool {
	var end int
	switch c := fastByteAt(base, j); {
	case c == '-' || '0' <= c && c <= '9':
		var msg string
		end, msg = scanNumber(src, j)
		if msg != "" {
			return false
		}
	case c == 't':
		if !literalTrueAt(src, j) {
			return false
		}
		end = j + 4
	case c == 'f':
		if !literalFalseAt(src, j) {
			return false
		}
		end = j + 5
	case c == 'n':
		if !literalNullAt(src, j) {
			return false
		}
		end = j + 4
	default:
		return false
	}
	if end < n {
		if c := fastByteAt(base, end); !isJSONSpaceOrStructural(c) {
			return false
		}
	}
	return true
}
