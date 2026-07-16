package simdjson

import (
	"math/bits"
	"unsafe"

	simdkernels "github.com/thesyncim/simdjson/simd"
)

// validPositionsStreamed validates through a packed, forward-only structural
// stream. Stage1ValidBlocks writes only grammar events; Stage2PositionsGo
// consumes them immediately and compacts scalar starts in place, so storage is
// fixed per chunk and the path allocates nothing.
func validPositionsStreamed(src []byte) (valid, decided bool) {
	if commit, invalid, numberMode := validPositionsSample(src); invalid {
		return false, true
	} else if !commit {
		return false, false
	} else {
		return validPositionsCommitted(src, numberMode), true
	}
}

func validPositionsSample(src []byte) (commit, invalid bool, numberMode uint8) {
	base := unsafe.Pointer(unsafe.SliceData(src))
	var stream simdkernels.Stage1Stream
	var recs [simdkernels.Stage1ChunkBlocks]simdkernels.Stage1Rec
	simdkernels.Stage1BlocksGP((*byte)(base), validBitmapSampleBlocks, &stream, &recs)

	ws, emit, inStr, esc := 0, 0, 0, 0
	for i := 0; i < validBitmapSampleBlocks; i++ {
		rec := &recs[i]
		if rec.Bad {
			return false, true, 0
		}
		ws += bits.OnesCount64(rec.WsOut)
		emit += bits.OnesCount64(rec.Emit)
		inStr += bits.OnesCount64(rec.InStr)
		esc += bits.OnesCount64(rec.EscInStr)
	}
	return validBitmapSampleCommit(ws, emit, inStr, esc), false, validBitmapNumberMode(inStr)
}

func validPositionsCommitted(src []byte, numberMode uint8) bool {
	n := len(src)
	base := unsafe.Pointer(unsafe.SliceData(src))
	fullBlocks := n / 64
	var stream simdkernels.Stage1IndexStream
	var grammar simdkernels.Stage2State
	simdkernels.Stage2Reset(&grammar)
	var kinds [simdkernels.Stage2KindsLen]byte
	var positions [simdkernels.Stage1ChunkBlocks*64 + 64]uint32
	var meta simdkernels.Stage1ValidMeta
	utf8RunStart, utf8RunEnd := -1, 0
	skipEscape := -1

	consume := func(written int) bool {
		if written == 0 {
			return true
		}
		// The output aliases the input intentionally. The machine reads a
		// position before writing a scalar at an index no greater than the
		// consumed input index, so unread positions are never overwritten.
		nscalars := simdkernels.Stage2PositionsTrusted(unsafe.SliceData(src), positions[:written], &kinds, positions[:], &grammar)
		if grammar.Bad != 0 {
			return false
		}
		for _, scalar := range positions[:nscalars] {
			if !validScalarTokenAtMode(src, base, n, int(scalar), numberMode) {
				return false
			}
		}
		return true
	}

	for block := 0; block < fullBlocks; block += simdkernels.Stage1ChunkBlocks {
		count := min(simdkernels.Stage1ChunkBlocks, fullBlocks-block)
		written := simdkernels.Stage1ValidBlocks(
			(*byte)(unsafe.Add(base, block*64)), count, uint32(block*64), &stream, positions[:], &meta,
		)
		for i := 0; i < count; i++ {
			current := block + i
			if meta.NonASCII&(1<<i) != 0 {
				if utf8RunStart >= 0 && current-utf8RunEnd > 8 {
					if !validUTF8Fast(src[utf8RunStart*64 : utf8RunEnd*64]) {
						return false
					}
					utf8RunStart = current
				} else if utf8RunStart < 0 {
					utf8RunStart = current
				}
				utf8RunEnd = current + 1
			}
			if esc := meta.EscInStr[i]; esc != 0 && validBitmapEscapes(src, n, current*64, esc, &skipEscape) {
				return false
			}
		}
		if !consume(written) {
			return false
		}
	}
	if fullBlocks*64 != n {
		var tail [64]byte
		for i := range tail {
			tail[i] = ' '
		}
		copy(tail[:], src[fullBlocks*64:])
		written := simdkernels.Stage1ValidBlocks(
			&tail[0], 1, uint32(fullBlocks*64), &stream, positions[:], &meta,
		)
		if meta.NonASCII&1 != 0 {
			if utf8RunStart >= 0 && fullBlocks-utf8RunEnd > 8 {
				if !validUTF8Fast(src[utf8RunStart*64 : utf8RunEnd*64]) {
					return false
				}
				utf8RunStart = fullBlocks
			} else if utf8RunStart < 0 {
				utf8RunStart = fullBlocks
			}
			utf8RunEnd = fullBlocks + 1
		}
		if esc := meta.EscInStr[0]; esc != 0 && validBitmapEscapes(src, n, fullBlocks*64, esc, &skipEscape) {
			return false
		}
		if !consume(written) {
			return false
		}
	}

	if stream.Bad || stream.Carry.InString != 0 || !simdkernels.Stage2Finish(&grammar) {
		return false
	}
	if utf8RunStart >= 0 && !validUTF8Fast(src[utf8RunStart*64:min(utf8RunEnd*64, n)]) {
		return false
	}
	return true
}

func validScalarTokenAtMode(src []byte, base unsafe.Pointer, n, j int, numberMode uint8) bool {
	c := fastByteAt(base, j)
	if c != '-' && '0' <= c && c <= '9' {
		if numberMode == vbNumberShort && j+4 <= n {
			if invalid := nonDigitMask4(loadUint32LE(unsafe.Add(base, j))); invalid != 0 {
				width := bits.TrailingZeros32(invalid) / 8
				if width != 0 && (c != '0' || width == 1) {
					end := j + width
					if isJSONSpaceOrStructural(fastByteAt(base, end)) {
						return true
					}
				}
			}
		}
		if numberMode == vbNumberNine && c != '0' && j+9 <= n &&
			nonDigitMask8(loadUint64LE(unsafe.Add(base, j))) == 0 &&
			isDigit(fastByteAt(base, j+8)) &&
			(j+9 == n || isJSONSpaceOrStructural(fastByteAt(base, j+9))) {
			return true
		}
	}
	return validScalarTokenAt(src, base, n, j)
}
