package simdjson

import (
	"math/bits"
	"unsafe"

	simdkernels "github.com/thesyncim/simdjson/simd"
)

// The bitmap index engine is BuildIndex on the stage-1 masks: the
// batched kernel classifies blocks, the stage-2 index machine
// (simd/stage2_index_arm64.s) writes production tape entries straight
// from the emit masks — containers patched in place at their closers —
// and a Go finishing pass completes scalar bodies with the fallback
// builder's exact scanners. Strings never rescan their interiors at
// all: the machine pins each string's closing quote while dispatching
// its following token (the grammar admits nothing but whitespace
// between the two, so the close is the first non-whitespace byte
// walking back — one or two bytes on real documents), and the escaped
// flag is driven by the block escape masks, costing nothing on chunks
// without escapes. Interior validity (escape forms, raw controls,
// UTF-8) comes from the same per-block checks the Valid engine runs.
// Whitespace, dispatch, and string bodies — the fallback's per-byte
// costs — die in the masks.
//
// The engine only ever shortcuts the accepting path. Any rejection —
// grammar, scalar or string bodies, control bytes, UTF-8, nesting past
// the machine's cap, or entry storage exhaustion — abandons the attempt
// and BuildIndexOptions proceeds with the portable builder, which then
// decides the exact error or result. Acceptance must therefore imply the
// fallback's acceptance, and the differential harness holds the two to
// byte-identical tapes.

// The machine hardcodes the tape layout; these equalities pin the
// private packing to the constants it writes.
const (
	_ = uint(unsafe.Sizeof(IndexEntry{}) - 16)
	_ = uint(16 - unsafe.Sizeof(IndexEntry{}))
	_ = uint(unsafe.Offsetof(IndexEntry{}.start) - 0)
	_ = uint(unsafe.Offsetof(IndexEntry{}.end) - 4)
	_ = uint(unsafe.Offsetof(IndexEntry{}.next) - 8)
	_ = uint(unsafe.Offsetof(IndexEntry{}.info) - 12)
	_ = uint(uint32(Object)<<infoKindShift - simdkernels.Stage2IndexInfoObject)
	_ = uint(simdkernels.Stage2IndexInfoObject - uint32(Object)<<infoKindShift)
	_ = uint(uint32(Array)<<infoKindShift - simdkernels.Stage2IndexInfoArray)
	_ = uint(simdkernels.Stage2IndexInfoArray - uint32(Array)<<infoKindShift)
	_ = uint(uint32(String)<<infoKindShift - simdkernels.Stage2IndexInfoString)
	_ = uint(simdkernels.Stage2IndexInfoString - uint32(String)<<infoKindShift)
	_ = uint(uint32(tapeFlagKey)<<infoFlagsShift - simdkernels.Stage2IndexKeyFlag)
	_ = uint(simdkernels.Stage2IndexKeyFlag - uint32(tapeFlagKey)<<infoFlagsShift)
	_ = uint(uint32(Invalid) - 0) // the machine's scalar placeholder kind
	_ = uint(fastWalkMaxDepth - simdkernels.Stage2IndexMaxDepth)
	_ = uint(simdkernels.Stage2IndexMaxDepth - fastWalkMaxDepth)
)

// indexBitmapChunkBlocks is the index engine's kernel batch: the full
// Stage1ChunkBlocks window. The Valid engine tightens to 16 for its
// leaner per-chunk work; here the finishing pass and the machine's
// suspend/resume amortize better over the widest batch, and the
// whitespace sample still decides at the first chunk boundary.
const indexBitmapChunkBlocks = simdkernels.Stage1ChunkBlocks

// indexBitmapMaxBytes bounds the engine to documents whose member counts
// cannot overflow the info word's 26-bit count field: a member costs at
// least two bytes (itself and a separator or closer), so any container
// in a document below 2^27 bytes holds fewer than 2^26 members. Larger
// documents keep the portable builder, which carries the explicit check.
const indexBitmapMaxBytes = 1 << 27

// buildIndexBitmap attempts the mask-driven build into storage's
// capacity. ok=false means the engine declined — the whitespace sample,
// a rejection, the depth cap, or full storage — and the caller must run
// the portable builder; the storage contents are then meaningless. On
// ok the returned slice aliases storage and holds the complete tape.
func buildIndexBitmap(src []byte, storage []IndexEntry) (entries []IndexEntry, ok bool) {
	n := len(src)
	base := unsafe.Pointer(unsafe.SliceData(src))
	full := storage[:cap(storage)]
	var entBase *byte
	if cap(storage) != 0 {
		entBase = (*byte)(unsafe.Pointer(unsafe.SliceData(full)))
	}

	var st simdkernels.Stage1Stream
	var recs [simdkernels.Stage1ChunkBlocks]simdkernels.Stage1Rec
	var scalars [simdkernels.Stage2IndexScalarSlots]uint32
	var g simdkernels.Stage2IndexState
	simdkernels.Stage2IndexReset(&g)
	var slab [simdkernels.Stage2IndexSlabLen]uint64

	utf8RunStart, utf8RunEnd := -1, 0
	wsSample, emitSample, inStrSample, escSample := 0, 0, 0, 0
	skipEscape := -1

	fullBlocks := n / 64
	nBlocks := (n + 63) / 64
	for chunk := 0; chunk < nBlocks; chunk += indexBitmapChunkBlocks {
		cnt := nBlocks - chunk
		if cnt > indexBitmapChunkBlocks {
			cnt = indexBitmapChunkBlocks
		}
		if chunk+cnt <= fullBlocks {
			simdkernels.Stage1BlocksGP((*byte)(unsafe.Add(base, chunk*64)), cnt, &st, &recs)
		} else {
			// The chunk contains the padded tail block. Space padding is
			// whitespace: it emits nothing and cannot invalidate the block.
			fullCnt := fullBlocks - chunk
			if fullCnt > 0 {
				simdkernels.Stage1BlocksGP((*byte)(unsafe.Add(base, chunk*64)), fullCnt, &st, &recs)
			}
			var tail [64]byte
			for i := range tail {
				tail[i] = ' '
			}
			copy(tail[:], src[fullBlocks*64:])
			var tailRecs [simdkernels.Stage1ChunkBlocks]simdkernels.Stage1Rec
			simdkernels.Stage1BlocksGP(&tail[0], 1, &st, &tailRecs)
			recs[fullCnt] = tailRecs[0]
		}

		var emits [indexBitmapChunkBlocks]uint64
		for i := 0; i < cnt; i++ {
			block := chunk + i
			pos := block * 64
			rec := &recs[i]

			// Raw control bytes always reject: with string interiors never
			// rescanned, this is the check that keeps them out of strings.
			if rec.Bad {
				return nil, false
			}
			// UTF-8 runs bracket exactly as in the validator; the portable
			// builder is the arbiter for documents this rejects.
			if rec.NonASCII {
				if utf8RunStart >= 0 && block-utf8RunEnd > 8 {
					if !validUTF8Fast(src[utf8RunStart*64 : utf8RunEnd*64]) {
						return nil, false
					}
					utf8RunStart = block
				} else if utf8RunStart < 0 {
					utf8RunStart = block
				}
				utf8RunEnd = block + 1
			}

			// Escape targets inside strings must name a legal escape;
			// string interiors are otherwise never rescanned.
			if bad := validBitmapEscapes(src, n, pos, rec.EscInStr, &skipEscape); bad {
				return nil, false
			}

			emits[i] = rec.Emit
			// The whitespace sample and thresholds are the validator's:
			// the engines commit to the same documents, so routing stays
			// one tuned decision. (When the in-string routing signal lands
			// in the stage-1 record, this is the seam that consumes it.)
			if block < validBitmapSampleBlocks {
				wsSample += bits.OnesCount64(rec.WsOut)
				emitSample += bits.OnesCount64(rec.Emit)
				inStrSample += bits.OnesCount64(rec.InStr)
				escSample += bits.OnesCount64(rec.EscInStr)
				if block == validBitmapSampleBlocks-1 &&
					!validBitmapSampleCommit(wsSample, emitSample, inStrSample, escSample) {
					return nil, false
				}
			}
		}
		// Emit bits at or past len(src) cannot arise from the space-padded
		// tail; reject them like the walk's j >= n guard, before the
		// machine dereferences one.
		if (chunk+cnt)*64 > n {
			for i := cnt - 1; i >= 0; i-- {
				wordBase := (chunk + i) * 64
				if wordBase >= n {
					if emits[i] != 0 {
						return nil, false
					}
					continue
				}
				if rel := uint(n - wordBase); rel < 64 && emits[i]>>rel != 0 {
					return nil, false
				}
				break
			}
		}

		prevOff := g.EntryOff
		nscalars := simdkernels.Stage2IndexWalk((*byte)(base), chunk*64, emits[:cnt], &slab, entBase, cap(storage), scalars[:], &g)
		if g.Bad != 0 {
			return nil, false
		}
		// Finish the chunk's new string and scalar entries while their
		// bytes are cache-warm. Containers are patched by the machine when
		// their closers arrive, possibly chunks later.
		if !indexBitmapFinish(src, base, n, full, prevOff, g.EntryOff, scalars[:nscalars], &recs, chunk*64, cnt) {
			return nil, false
		}
	}

	// A string that ends the document has no following token, so the
	// machine never fixed its end; anchor it to the document end.
	if g.PrevRowIO>>4&7 == 6 && g.EntryOff >= 16 {
		e := &full[g.EntryOff/16-1]
		k := n - 1
		for k > int(e.start) {
			if c := fastByteAt(base, k); c != ' ' && c != '\t' && c != '\n' && c != '\r' {
				break
			}
			k--
		}
		if k <= int(e.start) || fastByteAt(base, k) != '"' {
			return nil, false
		}
		e.end = uint32(k + 1)
	}
	if st.Carry.InString != 0 || !simdkernels.Stage2IndexFinish(&g) {
		return nil, false
	}
	if utf8RunStart >= 0 && !validUTF8Fast(src[utf8RunStart*64:min(utf8RunEnd*64, n)]) {
		return nil, false
	}
	return full[:g.EntryOff/16], true
}

// indexBitmapFinish completes the entries the machine wrote for one
// chunk. Containers are patched by the machine at their closers, string
// ends by the machine at their following tokens; what remains here is
// scalar bodies — the fallback builder's literal words and tagged number
// scanner, plus the terminator rule the grammar cannot see (a scalar's
// trailing bytes merge into its token, so `1x` reaches the scanner
// rather than the pair table) — and the escaped flags, driven by the
// chunk's escape-target bits so chunks without escapes pay nothing.
func indexBitmapFinish(src []byte, base unsafe.Pointer, n int, entries []IndexEntry, fromOff, toOff uint64, scalars []uint32,
	recs *[simdkernels.Stage1ChunkBlocks]simdkernels.Stage1Rec, chunkPos, cnt int) bool {
	fromEntry, toEntry := uint32(fromOff>>4), uint32(toOff>>4)
	for _, entryIndex := range scalars {
		if entryIndex < fromEntry || entryIndex >= toEntry {
			return false
		}
		e := &entries[entryIndex]
		if e.info != 0 {
			return false
		}
		s := int(e.start)
		var end int
		var kind Kind
		var flags uint8
		switch c := fastByteAt(base, s); {
		case c == '-' || isDigit(c):
			numEnd, integer, ok := scanNumberFastTagged(base, n, s)
			if !ok {
				return false
			}
			end, kind, flags = numEnd, Number, numberFlags(integer)
		case c == 't':
			if s+4 > n || loadUint32LE(unsafe.Add(base, s)) != wordTrueLE {
				return false
			}
			end, kind = s+4, Bool
		case c == 'f':
			if s+5 > n || loadUint32LE(unsafe.Add(base, s+1)) != wordAlseLE {
				return false
			}
			end, kind = s+5, Bool
		case c == 'n':
			if s+4 > n || loadUint32LE(unsafe.Add(base, s)) != wordNullLE {
				return false
			}
			end, kind = s+4, Null
		default:
			return false
		}
		if end < n && !isJSONSpaceOrStructural(fastByteAt(base, end)) {
			return false
		}
		e.end = uint32(end)
		e.info = packInfo(0, kind, flags)
	}

	// Escaped flags: every escape target sits inside some string, and that
	// string's entry is the last one whose start precedes the target. The
	// search runs over all entries so far, so strings spanning chunk
	// boundaries need no carried state.
	total := int(toOff >> 4)
	for i := 0; i < cnt; i++ {
		esc := recs[i].EscInStr
		if esc == 0 {
			continue
		}
		for ; esc != 0; esc &= esc - 1 {
			p := uint32(chunkPos + i*64 + bits.TrailingZeros64(esc))
			lo, hi := 0, total
			for lo < hi {
				mid := int(uint(lo+hi) >> 1)
				if entries[mid].start < p {
					lo = mid + 1
				} else {
					hi = mid
				}
			}
			if lo == 0 {
				return false
			}
			e := &entries[lo-1]
			if e.Kind() != String {
				return false
			}
			e.info |= uint32(tapeFlagEscaped) << infoFlagsShift
		}
	}
	return true
}
