package simdjson

import (
	"math/bits"
	"unsafe"

	simdkernels "github.com/thesyncim/simdjson/simd"
)

// The bitmap validator is a validation-only consumer of the stage-1 masks:
// whitespace disappears into the block masks, string interiors are skipped
// wholesale by the in-string mask, and the grammar machine touches only
// structural characters, string openers, and the first byte of each scalar.
// That inverts the position economics that sank the position-driven parser:
// on indentation-heavy documents most bytes never reach the walk at all.
//
// Dense compact documents emit a position every few bytes, where the
// recursive scanner is still faster, so a whitespace sample of the leading
// blocks decides which engine runs. The engine only reports validity;
// Validate re-runs the scalar validator for exact error offsets.
//
// On arm64 the classification runs through the batched stage-1 kernel
// (simd.Stage1BlocksGP): a chunk of blocks is classified per call, so the
// vector constants load once per chunk instead of once per block, and the
// escape chain and quote prefix-XOR resolve inside the kernel. The grammar
// machine then consumes precomputed per-block records. Amd64 has no batched
// kernel — native movemask makes the per-block setup cheap enough that the
// batching win does not pay for the record round-trip — so there the
// per-block path below classifies one block at a time. Both paths share the
// grammar walk (validBitmapWalk) and produce identical verdicts.

// stage1ValidatorEnabled gates the bitmap engine to builds with the
// stage-1 kernels.
var stage1ValidatorEnabled = simdkernels.Stage1Enabled()

// stage1StreamEnabled selects the batched kernel over the per-block path.
var stage1StreamEnabled = simdkernels.Stage1StreamEnabled()

const (
	// validBitmapMinBytes keeps small and mid-size inputs on the recursive
	// scanner: below this even the sampling blocks would show up in their
	// latency.
	validBitmapMinBytes = 1 << 16

	// The density dispatch samples the first 2 KiB. The engine only pays
	// off when whitespace outside strings dominates AND grammar emits stay
	// sparse: a skipped whitespace byte saves about a nanosecond while an
	// emitted position costs several, so commitment requires at least 25%
	// outside whitespace and a ws:emit ratio above 3.5 (2ws >= 7emits).
	// The ratio was retuned from 4.5 after the ADDP mask kernels cut the
	// per-block cost by about thirty percent; citm-shaped documents at a
	// ratio near 3.8 now win with the engine.
	validBitmapSampleBlocks = 32
	validBitmapSampleMinWs  = 512

	// validBitmapStreamChunk is the number of blocks classified per batched
	// kernel call. Smaller chunks interleave kernel and grammar work more
	// tightly, which matters on emit-dense documents: at 8 blocks the phase
	// separation between a pure-SIMD kernel window and a pure-GP grammar
	// window cost 2% on the nested benchmark document, while 4 blocks won 3%
	// there and kept the whitespace-heavy win. Must divide
	// validBitmapSampleBlocks so the sampling bailout stays chunk-aligned,
	// and cannot exceed simdkernels.Stage1ChunkBlocks.
	validBitmapStreamChunk = 4
)

// Grammar machine states.
const (
	vbValue      = iota // expecting any value
	vbValueOrEnd        // expecting a value or ']' (freshly opened array)
	vbKeyOrEnd          // expecting a key string or '}' (freshly opened object)
	vbKey               // expecting a key string (after a comma)
	vbColon             // expecting ':'
	vbNext              // after a value: ',' or the container's closer
	vbDone              // top-level value complete
)

// vbState is the grammar machine's running state, shared by both engines
// so the JSON grammar lives in exactly one place. containers[d] records
// whether the container at depth d is an object (bit set) or array.
type vbState struct {
	state      int
	depth      int
	containers [defaultMaxDepth/64 + 1]uint64
}

// validBitmap reports strict validity of src via the stage-1 masks.
// decided=false means the whitespace sample chose the recursive scanner
// instead; the result is then meaningless. On arm64 the batched kernel
// classifies each chunk; elsewhere the per-block path runs directly.
func validBitmap(src []byte) (valid, decided bool) {
	if stage1StreamEnabled {
		return validBitmapStreamed(src)
	}
	return validBitmapPerBlock(src)
}

// validBitmapPerBlock classifies one block at a time. It is the engine on
// builds without the batched kernel, and the reference shape the streamed
// engine reproduces mask-for-mask and verdict-for-verdict.
func validBitmapPerBlock(src []byte) (valid, decided bool) {
	n := len(src)
	base := unsafe.Pointer(unsafe.SliceData(src))

	var carry simdkernels.Stage1Carry
	var m simdkernels.Stage1Masks
	var g vbState
	g.state = vbValue

	follows := uint64(0) // bit 0: last byte of the previous block was a scalar byte
	// UTF-8 runs: [utf8RunStart, utf8RunEnd) brackets the current run of
	// blocks holding non-ASCII bytes, validated per run below.
	utf8RunStart, utf8RunEnd := -1, 0
	wsSample, emitSample := 0, 0
	skipEscape := -1 // low-surrogate escape already consumed by its high half

	nBlocks := (n + 63) / 64
	for block := 0; block < nBlocks; block++ {
		pos := block * 64
		if pos+64 <= n {
			simdkernels.Stage1Block((*[64]byte)(unsafe.Add(base, pos)), &m)
		} else {
			// Space padding is whitespace: it emits nothing and cannot
			// invalidate the tail block.
			var tail [64]byte
			for i := range tail {
				tail[i] = ' '
			}
			copy(tail[:], src[pos:])
			simdkernels.Stage1Block(&tail, &m)
		}

		escaped := simdkernels.Stage1Escaped(m.Backslash, &carry)
		quotes := m.Quote &^ escaped
		inStr := simdkernels.Stage1PrefixXOR(quotes, &carry)
		closers := quotes &^ inStr
		openers := quotes & inStr
		outside := ^(inStr | closers)

		// Raw control bytes are illegal inside strings, and outside them
		// only the three control whitespace bytes may appear.
		if m.Control&inStr != 0 {
			return false, true
		}
		if m.Control&outside&^m.Whitespace != 0 {
			return false, true
		}
		// UTF-8 is checked per run of non-ASCII blocks while the bytes are
		// still cache-warm, instead of a second full pass over the document.
		// A multi-byte sequence cannot cross a pure-ASCII block (every lead
		// and continuation byte has the high bit set), so a maximal run
		// validates as an independent slice. Runs separated by at most eight
		// ASCII blocks coalesce — ASCII is valid UTF-8, so validating the gap
		// is harmless and caps per-run kernel setup on alternating layouts.
		if m.NonASCII {
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

		// Escape targets inside strings must name a legal escape.
		if bad := validBitmapEscapes(src, n, pos, escaped&inStr, &skipEscape); bad {
			return false, true
		}

		// Scalar starts: the first byte of each run outside strings that is
		// neither whitespace, structural, nor a quote.
		cand := ^(m.Whitespace | m.Structural | m.Quote | inStr)
		starts := cand &^ (cand<<1 | follows)
		follows = cand >> 63

		emit := m.Structural&outside | openers | starts&outside
		if block < validBitmapSampleBlocks {
			wsSample += bits.OnesCount64(m.Whitespace & outside)
			emitSample += bits.OnesCount64(emit)
			if block == validBitmapSampleBlocks-1 &&
				(wsSample < validBitmapSampleMinWs || wsSample*2 < emitSample*7) {
				return false, false
			}
		}
		if v, done := validBitmapWalk(src, base, n, pos, emit, &g); done {
			return v, true
		}
	}

	if carry.InString != 0 || g.state != vbDone || g.depth != 0 {
		return false, true
	}
	if utf8RunStart >= 0 && !validUTF8Fast(src[utf8RunStart*64:min(utf8RunEnd*64, n)]) {
		return false, true
	}
	return true, true
}

// validBitmapStreamed is validBitmapPerBlock on the batched stage-1
// kernel: a chunk of blocks is classified per kernel call and the grammar
// machine consumes precomputed per-block records. The per-block logic and
// verdicts match validBitmapPerBlock exactly, including the per-run UTF-8
// bracketing driven by each record's NonASCII flag.
func validBitmapStreamed(src []byte) (valid, decided bool) {
	n := len(src)
	base := unsafe.Pointer(unsafe.SliceData(src))

	var st simdkernels.Stage1Stream
	var recs [simdkernels.Stage1ChunkBlocks]simdkernels.Stage1Rec
	var g vbState
	g.state = vbValue

	utf8RunStart, utf8RunEnd := -1, 0
	wsSample, emitSample := 0, 0
	skipEscape := -1

	fullBlocks := n / 64
	nBlocks := (n + 63) / 64
	for chunk := 0; chunk < nBlocks; chunk += validBitmapStreamChunk {
		cnt := nBlocks - chunk
		if cnt > validBitmapStreamChunk {
			cnt = validBitmapStreamChunk
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

		for i := 0; i < cnt; i++ {
			block := chunk + i
			pos := block * 64
			rec := &recs[i]

			if rec.Bad != 0 {
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

			if bad := validBitmapEscapes(src, n, pos, rec.EscInStr, &skipEscape); bad {
				return false, true
			}

			emit := rec.Emit
			if block < validBitmapSampleBlocks {
				wsSample += bits.OnesCount64(rec.WsOut)
				emitSample += bits.OnesCount64(emit)
				if block == validBitmapSampleBlocks-1 &&
					(wsSample < validBitmapSampleMinWs || wsSample*2 < emitSample*7) {
					return false, false
				}
			}
			if v, done := validBitmapWalk(src, base, n, pos, emit, &g); done {
				return v, true
			}
		}
	}

	if st.Carry.InString != 0 || g.state != vbDone || g.depth != 0 {
		return false, true
	}
	if utf8RunStart >= 0 && !validUTF8Fast(src[utf8RunStart*64:min(utf8RunEnd*64, n)]) {
		return false, true
	}
	return true, true
}

// validBitmapEscapes validates every escape-target byte named in escInStr
// (offsets are block-relative bits over pos). \u needs four hex digits,
// read directly from src across block boundaries; a matched surrogate pair
// records its low half in skipEscape so the next block skips it. It reports
// whether any escape is malformed.
func validBitmapEscapes(src []byte, n, pos int, escInStr uint64, skipEscape *int) (bad bool) {
	for e := escInStr; e != 0; e &= e - 1 {
		j := pos + bits.TrailingZeros64(e)
		if j >= n {
			return true
		}
		if j == *skipEscape {
			continue
		}
		switch src[j] {
		case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
		case 'u':
			u, ok := hex4(src, j+1)
			if !ok {
				return true
			}
			// Surrogate halves must pair, matching the scalar validator.
			if 0xDC00 <= u && u <= 0xDFFF {
				return true
			}
			if 0xD800 <= u && u <= 0xDBFF {
				if j+10 >= n || src[j+5] != '\\' || src[j+6] != 'u' {
					return true
				}
				lo, ok := hex4(src, j+7)
				if !ok || lo < 0xDC00 || lo > 0xDFFF {
					return true
				}
				*skipEscape = j + 6
			}
		default:
			return true
		}
	}
	return false
}

// validBitmapWalk feeds one block's emit mask to the grammar machine,
// advancing g. done reports that validation has concluded (valid carries
// the verdict); otherwise the caller proceeds to the next block. Both
// engines share this walk so the grammar lives in exactly one place.
func validBitmapWalk(src []byte, base unsafe.Pointer, n, pos int, emit uint64, g *vbState) (valid, done bool) {
	for emit != 0 {
		j := pos + bits.TrailingZeros64(emit)
		emit &= emit - 1
		if j >= n {
			return false, true
		}
		switch fastByteAt(base, j) {
		case '{':
			if g.state != vbValue && g.state != vbValueOrEnd {
				return false, true
			}
			if g.depth >= defaultMaxDepth {
				return false, true
			}
			g.depth++
			g.containers[g.depth>>6] |= 1 << (g.depth & 63)
			g.state = vbKeyOrEnd
		case '[':
			if g.state != vbValue && g.state != vbValueOrEnd {
				return false, true
			}
			if g.depth >= defaultMaxDepth {
				return false, true
			}
			g.depth++
			g.containers[g.depth>>6] &^= 1 << (g.depth & 63)
			g.state = vbValueOrEnd
		case '}':
			if g.depth == 0 || g.containers[g.depth>>6]&(1<<(g.depth&63)) == 0 ||
				(g.state != vbKeyOrEnd && g.state != vbNext) {
				return false, true
			}
			g.depth--
			g.state = vbNext
			if g.depth == 0 {
				g.state = vbDone
			}
		case ']':
			if g.depth == 0 || g.containers[g.depth>>6]&(1<<(g.depth&63)) != 0 ||
				(g.state != vbValueOrEnd && g.state != vbNext) {
				return false, true
			}
			g.depth--
			g.state = vbNext
			if g.depth == 0 {
				g.state = vbDone
			}
		case ':':
			if g.state != vbColon {
				return false, true
			}
			g.state = vbValue
		case ',':
			if g.state != vbNext {
				return false, true
			}
			if g.containers[g.depth>>6]&(1<<(g.depth&63)) != 0 {
				g.state = vbKey
			} else {
				g.state = vbValue
			}
		case '"':
			switch g.state {
			case vbKeyOrEnd, vbKey:
				g.state = vbColon
			case vbValue, vbValueOrEnd:
				g.state = vbNext
				if g.depth == 0 {
					g.state = vbDone
				}
			default:
				return false, true
			}
		default:
			// Scalar value: strict number or literal, which must end at
			// whitespace, a structural byte, or the document's end.
			if g.state != vbValue && g.state != vbValueOrEnd {
				return false, true
			}
			var end int
			switch c := fastByteAt(base, j); {
			case c == '-' || '0' <= c && c <= '9':
				var msg string
				end, msg = scanNumber(src, j)
				if msg != "" {
					return false, true
				}
			case c == 't':
				if !literalTrueAt(src, j) {
					return false, true
				}
				end = j + 4
			case c == 'f':
				if !literalFalseAt(src, j) {
					return false, true
				}
				end = j + 5
			case c == 'n':
				if !literalNullAt(src, j) {
					return false, true
				}
				end = j + 4
			default:
				return false, true
			}
			if end < n {
				if c := fastByteAt(base, end); !isJSONSpaceOrStructural(c) {
					return false, true
				}
			}
			g.state = vbNext
			if g.depth == 0 {
				g.state = vbDone
			}
		}
	}
	return false, false
}

func isJSONSpaceOrStructural(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r', ',', ':', '{', '}', '[', ']':
		return true
	}
	return false
}
