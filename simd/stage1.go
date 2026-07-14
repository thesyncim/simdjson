//go:build goexperiment.simd && (arm64 || amd64)

package simd

// Stage-1 structural scanning in the style of the simdjson paper: each
// 64-byte block is classified into one-bit-per-byte masks from which the
// caller derives string extents and structural positions.
//
// The production consumer is the bitmap validation engine in the root
// package (valid_bitmap.go), which reads the masks without materializing
// positions. A position-driven parser was also built on these kernels and
// measured about twice as slow as the recursive-descent scanner — the cost
// was position extraction and cursor dispatch, not the masks — so tape
// building stays recursive; the economics flip only with a consumer near
// one nanosecond per emitted position.

// Stage1Enabled reports whether this build provides the stage-1 kernel.
func Stage1Enabled() bool { return true }

// stage1ClassLo and stage1ClassHi classify bytes by nibble lookup. A byte
// with low nibble l and high nibble h has class bits lo[l] & hi[h]. The
// bit products are exact: each bit's low-set x high-set cross product
// contains only the intended characters.
//
// bit 0: space (0x20)      bit 1: tab, LF, CR
// bit 2: comma             bit 3: colon
// bit 4: [ and {           bit 5: ] and }
var stage1ClassLo = [16]uint8{
	1 << 0, 0, 0, 0, 0, 0, 0, 0,
	0, 1 << 1, 1<<1 | 1<<3, 1 << 4, 1 << 2, 1<<1 | 1<<5, 0, 0,
}

var stage1ClassHi = [16]uint8{
	1 << 1, 0, 1<<0 | 1<<2, 1 << 3, 0, 1<<4 | 1<<5, 0, 1<<4 | 1<<5,
	0, 0, 0, 0, 0, 0, 0, 0,
}

const (
	stage1WhitespaceBits = 1<<0 | 1<<1
	stage1StructuralBits = 1<<2 | 1<<3 | 1<<4 | 1<<5
)
