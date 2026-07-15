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

// stage1ValidatorEnabled gates the bitmap engine to builds with the
// stage-1 kernels.
var stage1ValidatorEnabled = simdkernels.Stage1Enabled()

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

// validBitmap reports strict validity of src via the stage-1 masks.
// decided=false means the whitespace sample chose the recursive scanner
// instead; the result is then meaningless.
func validBitmap(src []byte) (valid, decided bool) {
	n := len(src)
	base := unsafe.Pointer(unsafe.SliceData(src))

	var carry simdkernels.Stage1Carry
	var m simdkernels.Stage1Masks
	var containers [defaultMaxDepth/64 + 1]uint64

	state := vbValue
	depth := 0
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

		// Escape targets inside strings must name a legal escape; \u needs
		// four hex digits, read directly from src across block boundaries.
		for e := escaped & inStr; e != 0; e &= e - 1 {
			j := pos + bits.TrailingZeros64(e)
			if j >= n {
				return false, true
			}
			if j == skipEscape {
				continue
			}
			switch src[j] {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
			case 'u':
				u, ok := hex4(src, j+1)
				if !ok {
					return false, true
				}
				// Surrogate halves must pair, matching the scalar validator.
				if 0xDC00 <= u && u <= 0xDFFF {
					return false, true
				}
				if 0xD800 <= u && u <= 0xDBFF {
					if j+10 >= n || src[j+5] != '\\' || src[j+6] != 'u' {
						return false, true
					}
					lo, ok := hex4(src, j+7)
					if !ok || lo < 0xDC00 || lo > 0xDFFF {
						return false, true
					}
					skipEscape = j + 6
				}
			default:
				return false, true
			}
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
		for emit != 0 {
			j := pos + bits.TrailingZeros64(emit)
			emit &= emit - 1
			if j >= n {
				return false, true
			}
			switch fastByteAt(base, j) {
			case '{':
				if state != vbValue && state != vbValueOrEnd {
					return false, true
				}
				if depth >= defaultMaxDepth {
					return false, true
				}
				depth++
				containers[depth>>6] |= 1 << (depth & 63)
				state = vbKeyOrEnd
			case '[':
				if state != vbValue && state != vbValueOrEnd {
					return false, true
				}
				if depth >= defaultMaxDepth {
					return false, true
				}
				depth++
				containers[depth>>6] &^= 1 << (depth & 63)
				state = vbValueOrEnd
			case '}':
				if depth == 0 || containers[depth>>6]&(1<<(depth&63)) == 0 ||
					(state != vbKeyOrEnd && state != vbNext) {
					return false, true
				}
				depth--
				state = vbNext
				if depth == 0 {
					state = vbDone
				}
			case ']':
				if depth == 0 || containers[depth>>6]&(1<<(depth&63)) != 0 ||
					(state != vbValueOrEnd && state != vbNext) {
					return false, true
				}
				depth--
				state = vbNext
				if depth == 0 {
					state = vbDone
				}
			case ':':
				if state != vbColon {
					return false, true
				}
				state = vbValue
			case ',':
				if state != vbNext {
					return false, true
				}
				if containers[depth>>6]&(1<<(depth&63)) != 0 {
					state = vbKey
				} else {
					state = vbValue
				}
			case '"':
				switch state {
				case vbKeyOrEnd, vbKey:
					state = vbColon
				case vbValue, vbValueOrEnd:
					state = vbNext
					if depth == 0 {
						state = vbDone
					}
				default:
					return false, true
				}
			default:
				// Scalar value: strict number or literal, which must end at
				// whitespace, a structural byte, or the document's end.
				if state != vbValue && state != vbValueOrEnd {
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
				state = vbNext
				if depth == 0 {
					state = vbDone
				}
			}
		}
	}

	if carry.InString != 0 || state != vbDone || depth != 0 {
		return false, true
	}
	if utf8RunStart >= 0 && !validUTF8Fast(src[utf8RunStart*64:min(utf8RunEnd*64, n)]) {
		return false, true
	}
	return true, true
}

func isJSONSpaceOrStructural(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r', ',', ':', '{', '}', '[', ']':
		return true
	}
	return false
}
