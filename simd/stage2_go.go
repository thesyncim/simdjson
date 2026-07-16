package simd

import (
	"math/bits"
	"unsafe"
)

// Stage2WalkGo is the Go-native direct stage-2 machine. It has the same
// contract and state representation as Stage2Walk, allowing exact same-process
// comparisons with architecture implementations and a portable production
// fallback without changing the caller's structural pipeline.
func Stage2WalkGo(base *byte, emit []uint64, kinds *[Stage2KindsLen]byte, scalars []uint32, st *Stage2State) int {
	if len(emit) == 0 {
		return 0
	}
	if len(scalars) < 64*len(emit) {
		panic("simd: Stage2WalkGo scalars shorter than the emit-bit bound")
	}
	return stage2LoopGo(base, emit, kinds, scalars, st)
}

// stage2LoopGo mirrors the assembly machine's direct state transitions and
// object superinstructions. All unchecked writes are bounded by Stage2WalkGo's
// emit-bit capacity check; kind indexes are masked into the fixed slab.
func stage2LoopGo(base *byte, emit []uint64, kinds *[Stage2KindsLen]byte, scalars []uint32, st *Stage2State) int {
	basep := unsafe.Pointer(base)
	ptp := unsafe.Pointer(&stage2PairBad[0])
	kindp := unsafe.Pointer(&kinds[0])
	scalarp := unsafe.Pointer(unsafe.SliceData(scalars))

	bad := st.Bad
	depth := st.Depth
	prev := st.PrevRowIO
	key := st.KeyRow8
	inObj := prev & 8
	nscalars := 0
	wordBase := 0
	wi := 1
	m := emit[0]
	var j int
	var cls uint64

dispatch:
	if m == 0 {
		goto wordNext
	}
	j = wordBase + bits.TrailingZeros64(m)
	m &= m - 1
	cls = uint64(stage2Class[*(*byte)(unsafe.Add(basep, j))])
	goto handleKnown

wordNext:
	wordBase += 64
	if wi == len(emit) {
		goto done
	}
	m = emit[wi]
	wi++
	goto dispatch

handleKnown:
	bad |= uint64(*(*byte)(unsafe.Add(ptp, prev|cls)))
	switch cls {
	case stage2ccO:
		depth++
		if depth > Stage2MaxDepth {
			bad |= 1
		}
		*(*byte)(unsafe.Add(kindp, uintptr(uint64(depth)&(Stage2KindsLen-1)))) = 8
		inObj = 8
		prev = 8
		key = 8
		goto fusedKey
	case stage2ccA:
		depth++
		if depth > Stage2MaxDepth {
			bad |= 1
		}
		*(*byte)(unsafe.Add(kindp, uintptr(uint64(depth)&(Stage2KindsLen-1)))) = 0
		inObj = 0
		prev = 16
		key = 0
		goto fusedArrayValue
	case stage2ccC:
		bad |= (inObj ^ 8) >> 3
		depth--
		if depth < 0 {
			bad |= 1
		}
		inObj = uint64(*(*byte)(unsafe.Add(kindp, uintptr(uint64(depth)&(Stage2KindsLen-1))))) & 8
		prev = 32 | inObj
		key = 0
		if inObj != 0 {
			goto fusedComma
		}
		if depth > 0 {
			goto fusedArrayComma
		}
		goto dispatch
	case stage2ccB:
		bad |= inObj >> 3
		depth--
		if depth < 0 {
			bad |= 1
		}
		inObj = uint64(*(*byte)(unsafe.Add(kindp, uintptr(uint64(depth)&(Stage2KindsLen-1))))) & 8
		prev = 48 | inObj
		key = 0
		if inObj != 0 {
			goto fusedComma
		}
		if depth > 0 {
			goto fusedArrayComma
		}
		goto dispatch
	case stage2ccL:
		prev = 64 | inObj
		key = 0
		goto dispatch
	case stage2ccM:
		if depth == 0 {
			bad |= 1
		}
		prev = 80 | inObj
		key = inObj
		if inObj != 0 {
			goto fusedKey
		}
		if depth > 0 {
			goto fusedArrayValue
		}
		goto dispatch
	case stage2ccQ:
		isKey := key
		prev = 96 | key<<4 | inObj
		key = 0
		if isKey != 0 {
			goto fusedColon
		}
		if inObj != 0 {
			goto fusedComma
		}
		if depth > 0 {
			goto fusedArrayComma
		}
		goto dispatch
	default:
		*(*uint32)(unsafe.Add(scalarp, uintptr(nscalars)*4)) = uint32(j)
		nscalars++
		prev = 112 | inObj
		key = 0
		if inObj != 0 {
			goto fusedComma
		}
		if depth > 0 {
			goto fusedArrayComma
		}
		goto dispatch
	}

fusedComma:
	if m == 0 {
		goto fusedCommaNext
	}
	j = wordBase + bits.TrailingZeros64(m)
	switch *(*byte)(unsafe.Add(basep, j)) {
	case ',':
		m &= m - 1
		prev = 80 | inObj
		key = inObj
		goto fusedKey
	case '}':
		m &= m - 1
		depth--
		if depth < 0 {
			bad |= 1
		}
		inObj = uint64(*(*byte)(unsafe.Add(kindp, uintptr(uint64(depth)&(Stage2KindsLen-1))))) & 8
		prev = 32 | inObj
		key = 0
		if inObj != 0 {
			goto fusedComma
		}
		if depth > 0 {
			goto fusedArrayComma
		}
		goto dispatch
	default:
		m &= m - 1
		cls = uint64(stage2Class[*(*byte)(unsafe.Add(basep, j))])
		goto handleKnown
	}

fusedKey:
	if m == 0 {
		goto fusedKeyNext
	}
	j = wordBase + bits.TrailingZeros64(m)
	if *(*byte)(unsafe.Add(basep, j)) != '"' {
		m &= m - 1
		cls = uint64(stage2Class[*(*byte)(unsafe.Add(basep, j))])
		goto handleKnown
	}
	m &= m - 1
	prev = 224 | inObj
	goto fusedColon

fusedColon:
	if m == 0 {
		goto fusedColonNext
	}
	j = wordBase + bits.TrailingZeros64(m)
	if *(*byte)(unsafe.Add(basep, j)) != ':' {
		m &= m - 1
		cls = uint64(stage2Class[*(*byte)(unsafe.Add(basep, j))])
		goto handleKnown
	}
	m &= m - 1
	prev = 64 | inObj
	key = 0
	goto fusedValue

fusedValue:
	if m == 0 {
		goto fusedValueNext
	}
	j = wordBase + bits.TrailingZeros64(m)
	switch c := *(*byte)(unsafe.Add(basep, j)); c {
	case '"':
		m &= m - 1
		prev = 96 | inObj
		key = 0
		goto fusedComma
	default:
		cls = uint64(stage2Class[c])
		if cls != stage2ccS {
			m &= m - 1
			goto handleKnown
		}
		m &= m - 1
		*(*uint32)(unsafe.Add(scalarp, uintptr(nscalars)*4)) = uint32(j)
		nscalars++
		prev = 112 | inObj
		key = 0
		goto fusedComma
	}

fusedArrayComma:
	if m == 0 {
		goto fusedArrayCommaNext
	}
	j = wordBase + bits.TrailingZeros64(m)
	switch *(*byte)(unsafe.Add(basep, j)) {
	case ',':
		m &= m - 1
		prev = 80
		key = 0
		goto fusedArrayValue
	case ']':
		m &= m - 1
		depth--
		if depth < 0 {
			bad |= 1
		}
		inObj = uint64(*(*byte)(unsafe.Add(kindp, uintptr(uint64(depth)&(Stage2KindsLen-1))))) & 8
		prev = 48 | inObj
		key = 0
		if inObj != 0 {
			goto fusedComma
		}
		if depth > 0 {
			goto fusedArrayComma
		}
		goto dispatch
	default:
		m &= m - 1
		cls = uint64(stage2Class[*(*byte)(unsafe.Add(basep, j))])
		goto handleKnown
	}

fusedArrayValue:
	if m == 0 {
		goto fusedArrayValueNext
	}
	j = wordBase + bits.TrailingZeros64(m)
	switch c := *(*byte)(unsafe.Add(basep, j)); uint64(stage2Class[c]) {
	case stage2ccQ:
		m &= m - 1
		prev = 96
		key = 0
		goto fusedArrayComma
	case stage2ccS:
		m &= m - 1
		*(*uint32)(unsafe.Add(scalarp, uintptr(nscalars)*4)) = uint32(j)
		nscalars++
		prev = 112
		key = 0
		goto fusedArrayComma
	case stage2ccA:
		m &= m - 1
		depth++
		if depth > Stage2MaxDepth {
			bad |= 1
		}
		*(*byte)(unsafe.Add(kindp, uintptr(uint64(depth)&(Stage2KindsLen-1)))) = 0
		inObj = 0
		prev = 16
		key = 0
		goto fusedArrayValue
	case stage2ccO:
		m &= m - 1
		depth++
		if depth > Stage2MaxDepth {
			bad |= 1
		}
		*(*byte)(unsafe.Add(kindp, uintptr(uint64(depth)&(Stage2KindsLen-1)))) = 8
		inObj = 8
		prev = 8
		key = 8
		goto fusedKey
	default:
		m &= m - 1
		cls = uint64(stage2Class[c])
		goto handleKnown
	}

fusedCommaNext:
	wordBase += 64
	if wi == len(emit) {
		goto done
	}
	m = emit[wi]
	wi++
	goto fusedComma

fusedKeyNext:
	wordBase += 64
	if wi == len(emit) {
		goto done
	}
	m = emit[wi]
	wi++
	goto fusedKey

fusedColonNext:
	wordBase += 64
	if wi == len(emit) {
		goto done
	}
	m = emit[wi]
	wi++
	goto fusedColon

fusedValueNext:
	wordBase += 64
	if wi == len(emit) {
		goto done
	}
	m = emit[wi]
	wi++
	goto fusedValue

fusedArrayCommaNext:
	wordBase += 64
	if wi == len(emit) {
		goto done
	}
	m = emit[wi]
	wi++
	goto fusedArrayComma

fusedArrayValueNext:
	wordBase += 64
	if wi == len(emit) {
		goto done
	}
	m = emit[wi]
	wi++
	goto fusedArrayValue

done:
	st.Bad = bad
	st.Depth = depth
	st.PrevRowIO = prev
	st.KeyRow8 = key
	return nscalars
}
