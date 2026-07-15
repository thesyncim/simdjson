//go:build goexperiment.simd && arm64

package simd

import (
	"math/bits"
	"simd/archsimd"
	"unsafe"
)

// Stage1StreamEnabled reports whether this build provides the batched
// stage-1 kernel.
func Stage1StreamEnabled() bool { return true }

// Stage1BlocksGP classifies nblocks consecutive 64-byte blocks at p and
// derives one Stage1Rec per block. Classification runs in NEON; each
// mask crosses to a general-purpose register through the ADDP movemask
// tree, and the escape chain, prefix-XOR, and derived-mask math run in
// scalar code. This is the C++ simdjson pipeline shape (json_scanner ->
// to_bitmask -> GP bit math), batched so vector constants load once per
// chunk instead of once per block.
func Stage1BlocksGP(p *byte, nblocks int, st *Stage1Stream, out *[Stage1ChunkBlocks]Stage1Rec) {
	base := unsafe.Pointer(p)

	zip := archsimd.LoadUint8x16Array(&stage1ZipIndex)
	weights := archsimd.LoadUint8x16Array(&stage1Weights)
	quoteB := archsimd.BroadcastUint8x16('"')
	slashB := archsimd.BroadcastUint8x16('\\')
	ctrlB := archsimd.BroadcastUint8x16(0x20)
	lowNibble := archsimd.BroadcastUint8x16(0x0f)
	loTable := archsimd.LoadUint8x16Array(&stage1ClassLo)
	hiTable := archsimd.LoadUint8x16Array(&stage1ClassHi)
	wsBits := archsimd.BroadcastUint8x16(stage1WhitespaceBits)
	structBits := archsimd.BroadcastUint8x16(stage1StructuralBits)
	zero := archsimd.BroadcastUint8x16(0)

	carryEsc := st.Carry.Escaped
	carryStr := st.Carry.InString
	follows := st.Follows

	const evenBits = uint64(0x5555555555555555)

	for i := 0; i < nblocks; i++ {
		bp := unsafe.Add(base, i*64)
		r0 := archsimd.LoadUint8x16Array((*[16]uint8)(bp))
		r1 := archsimd.LoadUint8x16Array((*[16]uint8)(unsafe.Add(bp, 16)))
		r2 := archsimd.LoadUint8x16Array((*[16]uint8)(unsafe.Add(bp, 32)))
		r3 := archsimd.LoadUint8x16Array((*[16]uint8)(unsafe.Add(bp, 48)))

		v0 := r0.LookupOrZero(zip)
		v1 := r1.LookupOrZero(zip)
		v2 := r2.LookupOrZero(zip)
		v3 := r3.LookupOrZero(zip)

		c0 := loTable.LookupOrZero(v0.And(lowNibble)).And(hiTable.LookupOrZero(v0.ShiftAllRight(4)))
		c1 := loTable.LookupOrZero(v1.And(lowNibble)).And(hiTable.LookupOrZero(v1.ShiftAllRight(4)))
		c2 := loTable.LookupOrZero(v2.And(lowNibble)).And(hiTable.LookupOrZero(v2.ShiftAllRight(4)))
		c3 := loTable.LookupOrZero(v3.And(lowNibble)).And(hiTable.LookupOrZero(v3.ShiftAllRight(4)))

		ws := stage1Movemask64(
			c0.And(wsBits).Greater(zero),
			c1.And(wsBits).Greater(zero),
			c2.And(wsBits).Greater(zero),
			c3.And(wsBits).Greater(zero),
			weights,
		)
		structural := stage1Movemask64(
			c0.And(structBits).Greater(zero),
			c1.And(structBits).Greater(zero),
			c2.And(structBits).Greater(zero),
			c3.And(structBits).Greater(zero),
			weights,
		)
		quoteRaw := stage1Movemask64(v0.Equal(quoteB), v1.Equal(quoteB), v2.Equal(quoteB), v3.Equal(quoteB), weights)
		backslash := stage1Movemask64(v0.Equal(slashB), v1.Equal(slashB), v2.Equal(slashB), v3.Equal(slashB), weights)
		control := stage1Movemask64(v0.Less(ctrlB), v1.Less(ctrlB), v2.Less(ctrlB), v3.Less(ctrlB), weights)

		// Escape chain in GP (the production Stage1Escaped, inlined so the
		// carry stays in a register across blocks).
		var escaped uint64
		if backslash == 0 {
			escaped = carryEsc
			carryEsc = 0
		} else {
			backslash &^= carryEsc
			followsEscape := backslash<<1 | carryEsc
			oddSequenceStarts := backslash & ^evenBits & ^followsEscape
			sum, overflow := bits.Add64(oddSequenceStarts, backslash, 0)
			carryEsc = overflow
			escaped = (evenBits ^ sum<<1) & followsEscape
		}

		// Prefix-XOR in GP (the production Stage1PrefixXOR shape).
		quotes := quoteRaw &^ escaped
		m := quotes
		m ^= m << 1
		m ^= m << 2
		m ^= m << 4
		m ^= m << 8
		m ^= m << 16
		m ^= m << 32
		inStr := m ^ carryStr
		carryStr = uint64(int64(inStr) >> 63)

		closers := quotes &^ inStr
		openers := quotes & inStr
		outside := ^(inStr | closers)
		cand := ^(ws | structural | quoteRaw | inStr)
		starts := cand &^ (cand<<1 | follows)
		follows = cand >> 63

		out[i].Emit = structural&outside | openers | starts&outside
		out[i].EscInStr = escaped & inStr
		out[i].Bad = control&inStr | control&outside&^ws
		out[i].WsOut = ws & outside
		// Per-block non-ASCII: the validator brackets UTF-8 runs by block, so
		// each record carries its own flag rather than a document-wide OR.
		out[i].NonASCII = r0.Or(r1).Or(r2).Or(r3).ReduceMax() >= 0x80
	}

	st.Carry.Escaped = carryEsc
	st.Carry.InString = carryStr
	st.Follows = follows
}
