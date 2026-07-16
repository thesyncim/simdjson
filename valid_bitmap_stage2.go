package simdjson

import (
	"math/bits"
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

// stage2IndexPositionEnabled gates the Go-native forward index writer.
var stage2IndexPositionEnabled = simdkernels.Stage1StreamEnabled()

// The machine's depth limit must equal the walk's; both reject the open
// that would exceed it.
const _ = uint(simdkernels.Stage2MaxDepth-defaultMaxDepth) + uint(defaultMaxDepth-simdkernels.Stage2MaxDepth)

// validBitmapStreamChunkAsm is the number of blocks per batched kernel
// call once the whitespace sample has committed and the stage-2 machine
// is consuming the masks. The machine reloads and stores its state words
// per call, so unlike the Go walk it wants wide runs: at 4 blocks the
// call and state traffic cost about a third of a nanosecond per position
// on FHIR-shaped documents (1.35 vs 1.07 ns/pos at 16 blocks), while 16
// blocks holds every gate corpus at or under 1.1. Inside the sampling
// window the engine keeps validBitmapStreamChunk's 4-block cadence so
// the bailout decision — and therefore the decided verdict — is
// bit-identical to the Go engines'. Must be a multiple of
// validBitmapStreamChunk, divide validBitmapSampleBlocks' window
// alignment, and not exceed simdkernels.Stage1ChunkBlocks.
const validBitmapStreamChunkAsm = 16

// validBitmapStreamedAsm is validBitmapStreamed with the grammar walk on
// the stage-2 machine. The per-block checks — control bytes, UTF-8 run
// bracketing, escape targets, the whitespace sample — are shared logic
// and run identically; only the walk differs, and the Phase-A harness
// pins the walk pair to identical verdicts at identical chunk
// boundaries. The Go engine remains the fallback on builds without the
// machine and the reference in the differential tests.
func validBitmapStreamedAsm(src []byte) (valid, decided bool) {
	n := len(src)
	base := unsafe.Pointer(unsafe.SliceData(src))

	var st simdkernels.Stage1Stream
	var recs [simdkernels.Stage1ChunkBlocks]simdkernels.Stage1Rec
	var g simdkernels.Stage2State
	simdkernels.Stage2Reset(&g)
	var kinds [simdkernels.Stage2KindsLen]byte
	var scalars [validBitmapStreamChunkAsm * 64]uint32

	utf8RunStart, utf8RunEnd := -1, 0
	wsSample, emitSample, inStrSample, escSample := 0, 0, 0, 0
	skipEscape := -1

	fullBlocks := n / 64
	nBlocks := (n + 63) / 64
	for chunk := 0; chunk < nBlocks; {
		step := validBitmapStreamChunkAsm
		if chunk < validBitmapSampleBlocks {
			step = validBitmapStreamChunk
		}
		cnt := nBlocks - chunk
		if cnt > step {
			cnt = step
		}
		if chunk+cnt <= fullBlocks {
			simdkernels.Stage1BlocksGP((*byte)(unsafe.Add(base, chunk*64)), cnt, &st, &recs)
		} else {
			// The chunk contains the padded tail block. Space padding is
			// whitespace: it emits nothing and cannot invalidate the block.
			full := fullBlocks - chunk
			if full > 0 {
				simdkernels.Stage1BlocksGP((*byte)(unsafe.Add(base, chunk*64)), full, &st, &recs)
			}
			var tail [64]byte
			for i := range tail {
				tail[i] = ' '
			}
			copy(tail[:], src[fullBlocks*64:])
			var tailRecs [simdkernels.Stage1ChunkBlocks]simdkernels.Stage1Rec
			simdkernels.Stage1BlocksGP(&tail[0], 1, &st, &tailRecs)
			recs[full] = tailRecs[0]
		}

		var emits [validBitmapStreamChunkAsm]uint64
		for i := 0; i < cnt; i++ {
			block := chunk + i
			pos := block * 64
			rec := &recs[i]

			if rec.Bad {
				return false, true
			}
			// UTF-8 is checked per run of non-ASCII blocks while the bytes
			// are still cache-warm; see validBitmapPerBlock for the
			// coalescing rationale.
			if rec.NonASCII {
				if utf8RunStart >= 0 && block-utf8RunEnd > 8 {
					if !validUTF8Fast(src[utf8RunStart*64 : utf8RunEnd*64]) {
						return false, true
					}
					utf8RunStart = block
				} else if utf8RunStart < 0 {
					utf8RunStart = block
				}
				utf8RunEnd = block + 1
			}
			if esc := rec.EscInStr; esc != 0 {
				if validBitmapEscapes(src, n, pos, esc, &skipEscape) {
					return false, true
				}
			}

			emits[i] = rec.Emit
			if block < validBitmapSampleBlocks {
				wsSample += bits.OnesCount64(rec.WsOut)
				emitSample += bits.OnesCount64(rec.Emit)
				inStrSample += bits.OnesCount64(rec.InStr)
				escSample += bits.OnesCount64(rec.EscInStr)
				if block == validBitmapSampleBlocks-1 &&
					!validBitmapSampleCommit(wsSample, emitSample, inStrSample, escSample) {
					return false, false
				}
			}
		}
		if v, done := validBitmapWalkAsm(src, base, n, chunk*64, emits[:cnt], &g, &kinds, scalars[:]); done {
			return v, true
		}
		chunk += cnt
	}

	if st.Carry.InString != 0 || !simdkernels.Stage2Finish(&g) {
		return false, true
	}
	if utf8RunStart >= 0 && !validUTF8Fast(src[utf8RunStart*64:min(utf8RunEnd*64, n)]) {
		return false, true
	}
	return true, true
}

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
	if !validBitmapEmitsInBounds(n, pos, emits) {
		return false, true
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
		if !literalFalseTailAt(src, j) {
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
