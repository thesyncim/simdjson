package benchmarks

// Shared tables for the stage-1 consumer study (stage1_consumer_bench_test.go):
// class codes, the packed per-byte class table, the pair-legality table,
// and the end-of-document row legality. They live outside the test files
// because the hand-written arm64 consumer's wrapper links against them.

// Token class codes. Openers and closers are arranged so the demo tables
// stay dense; ccRowStart and ccRowQk extend the row space for the pair
// table (rows are refined previous classes, columns raw current classes).
const (
	ccO = iota // {
	ccA        // [
	ccC        // }
	ccB        // ]
	ccL        // :
	ccM        // ,
	ccQ        // "
	ccS        // scalar start

	ccRowStart = 8  // virtual class before the first token
	ccRowQk    = 14 // ccQ refined to a key quote (ccQ | 1<<3)
)

// demoMaxDepth matches the production walker's rejection depth.
const demoMaxDepth = 10000

// consumerKindsLen sizes the container-kind slab: a power of two above
// demoMaxDepth plus slack, so depth&\(len-1\) addressing stays in bounds even
// while a too-deep or underflowed document runs on with bad already set.
const consumerKindsLen = 16384

// consumerClassTab packs everything the consumer needs per byte into one
// word, so one dependent load after the source byte yields class, flags,
// depth delta, and the entry's info word:
//
//	bits 0..2   class code
//	bit  3      isQ
//	bit  4      isOpen  ({ or [)
//	bit  5      isClose (} or ])
//	bit  6      isObjOpen ({ — also the isO refinement flag)
//	bit  7      wantObj (} — the kind a closer requires on top)
//	bit  8      isM (,)
//	bits 12..13 depth delta & 3 (+1 opens, -1 closes as 3)
//	bits 58..60 demo kind for the entry info word (class+1)
var consumerClassTab = func() (t [256]uint64) {
	for i := range t {
		var cls uint64
		switch byte(i) {
		case '{':
			cls = ccO
		case '[':
			cls = ccA
		case '}':
			cls = ccC
		case ']':
			cls = ccB
		case ':':
			cls = ccL
		case ',':
			cls = ccM
		case '"':
			cls = ccQ
		default:
			cls = ccS
		}
		w := cls
		if cls == ccQ {
			w |= 1 << 3
		}
		if cls == ccO || cls == ccA {
			w |= 1 << 4
			w |= 1 << 12 // delta +1
		}
		if cls == ccC || cls == ccB {
			w |= 1 << 5
			w |= 3 << 12 // delta -1
		}
		if cls == ccO {
			w |= 1 << 6
		}
		if cls == ccC {
			w |= 1 << 7
		}
		if cls == ccM {
			w |= 1 << 8
		}
		w |= (cls + 1) << 58
		t[i] = w
	}
	return
}()

// consumerPairBad is the pair-legality table: 1 marks an illegal
// (previous refined class, inObj, current raw class) triple. Index is
// prevRow<<4 | inObj<<3 | cls. Rows not constructible by the machine stay
// illegal. Depth-dependent rules (comma/closer at depth 0, closer kind,
// depth overflow) are enforced separately; this table is pure token-pair
// grammar.
var consumerPairBad = func() (t [256]uint8) {
	for i := range t {
		t[i] = 1
	}
	legal := func(row uint64, inObj int, cols ...uint64) {
		for _, c := range cols {
			t[row<<4|uint64(inObj)<<3|c] = 0
		}
	}
	both := func(row uint64, cols ...uint64) {
		legal(row, 0, cols...)
		legal(row, 1, cols...)
	}
	value := []uint64{ccO, ccA, ccQ, ccS}
	after := []uint64{ccM, ccC, ccB}
	both(ccO, ccQ, ccC)              // { : key or empty close
	both(ccA, append(value, ccB)...) // [ : any value or empty close
	both(ccC, after...)              // } : separator or another closer
	both(ccB, after...)              // ] : separator or another closer
	both(ccL, value...)              // : : any value
	legal(ccM, 0, value...)          // , in array: any value
	legal(ccM, 1, ccQ)               // , in object: key quote only
	both(ccQ, after...)              // value string: separator or closer
	both(ccS, after...)              // scalar: separator or closer
	both(ccRowQk, ccL)               // key string: colon only
	both(ccRowStart, value...)       // document start: any value
	return
}()

// consumerEOFBad marks refined previous classes that cannot end a
// document (indexed by the row code). Completed values — scalars, value
// strings, and closers — are the only legal final tokens; depth==0 is
// checked separately.
var consumerEOFBad = func() (t [16]uint8) {
	for i := range t {
		t[i] = 1
	}
	t[ccC], t[ccB], t[ccQ], t[ccS] = 0, 0, 0, 0
	return
}()
