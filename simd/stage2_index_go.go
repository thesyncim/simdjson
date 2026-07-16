package simd

import "unsafe"

// Stage2IndexPositionsFused keeps the common object and array transitions in
// one straight-line machine. A complete string pair is consumed together, and
// object keys additionally fuse their colon, removing three dispatches from
// the dominant object-member sequence.
func Stage2IndexPositionsFused(base *byte, n int, positions []uint32, slab *[Stage2IndexSlabLen]uint64, ent *byte, entCap int, st *Stage2IndexState) {
	basep := unsafe.Pointer(base)
	posp := unsafe.Pointer(unsafe.SliceData(positions))
	ptp := unsafe.Pointer(&stage2PairBad[0])
	slabp := unsafe.Pointer(&slab[0])
	entryp := unsafe.Pointer(ent)

	bad := st.Bad
	depth := st.Depth
	prev := st.PrevRowIO
	key := st.KeyRow8
	count := st.Count
	entryOff := st.EntryOff
	stringEntry := st.StringEntry
	inString := st.InString
	inObj := prev & 8
	pi := 0
	var j, cls, members, entryIndex, scope, parent, next, isKey uint64
	var c, d byte
	var p unsafe.Pointer
	var info, scalarInfo uint32
	var scan int
	var integer, scalarOK bool

dispatch:
	if pi == len(positions) {
		goto done
	}
	j = uint64(*(*uint32)(unsafe.Add(posp, uintptr(pi)*4)))
	pi++
	c = *(*byte)(unsafe.Add(basep, uintptr(j)))
	if inString != 0 {
		if c != '"' || uint64(stringEntry)<<4 >= entryOff {
			bad |= 1
			goto done
		}
		*(*uint32)(unsafe.Add(entryp, uintptr(stringEntry)*16+4)) = uint32(j + 1)
		inString = 0
		if prev>>4&15 == stage2RowQk {
			goto fusedColon
		}
		if inObj != 0 {
			goto fusedObjectComma
		}
		if depth > 0 {
			goto fusedArrayComma
		}
		goto dispatch
	}
	cls = uint64(stage2Class[c])

handleKnown:
	bad |= uint64(*(*byte)(unsafe.Add(ptp, uintptr(prev|cls))))
	switch cls {
	case stage2ccO, stage2ccA:
		goto openContainer
	case stage2ccC, stage2ccB:
		members = count
		if (cls == stage2ccC && prev != 8) || (cls == stage2ccB && prev != 16) {
			members++
		}
		goto closeContainer
	case stage2ccL:
		prev = 64 | inObj
		key = 0
		goto fusedValue
	case stage2ccM:
		if depth == 0 {
			bad |= 1
		}
		count++
		prev = 80 | inObj
		key = inObj
		if inObj != 0 {
			goto fusedKey
		}
		goto fusedArrayValue
	case stage2ccQ:
		isKey = key
		goto writeString
	default:
		goto writeScalar
	}

openContainer:
	if entryOff>>4 >= uint64(entCap) {
		bad |= Stage2IndexFull
		goto done
	}
	depth++
	if depth > Stage2IndexMaxDepth {
		bad |= Stage2IndexDeep
		goto done
	}
	entryIndex = entryOff >> 4
	scope = entryIndex<<32 | count<<4
	info = Stage2IndexInfoArray
	if cls == stage2ccO {
		scope |= 8
		info = Stage2IndexInfoObject
	}
	*(*uint64)(unsafe.Add(slabp, uintptr(uint64(depth)&(Stage2IndexSlabLen-1))*8)) = scope
	p = unsafe.Add(entryp, uintptr(entryOff))
	*(*uint64)(p) = j
	*(*uint64)(unsafe.Add(p, 8)) = uint64(info) << 32
	entryOff += 16
	count = 0
	if cls == stage2ccO {
		inObj = 8
		prev = 8
		key = 8
		goto fusedKey
	}
	inObj = 0
	prev = 16
	key = 0
	goto fusedArrayValue

closeContainer:
	depth--
	if depth < 0 {
		bad |= 1
		goto done
	}
	scope = *(*uint64)(unsafe.Add(slabp, uintptr(uint64(depth+1)&(Stage2IndexSlabLen-1))*8))
	if cls == stage2ccC {
		bad |= (scope&8 ^ 8) >> 3
		info = Stage2IndexInfoObject
	} else {
		bad |= scope & 8 >> 3
		info = Stage2IndexInfoArray
	}
	count = scope >> 4 & (1<<26 - 1)
	entryIndex = scope >> 32
	p = unsafe.Add(entryp, uintptr(entryIndex)*16)
	*(*uint32)(unsafe.Add(p, 4)) = uint32(j + 1)
	next = entryOff>>4 - entryIndex
	*(*uint64)(unsafe.Add(p, 8)) = next | uint64(info|uint32(members))<<32
	parent = *(*uint64)(unsafe.Add(slabp, uintptr(uint64(depth)&(Stage2IndexSlabLen-1))*8))
	inObj = parent & 8
	if cls == stage2ccC {
		prev = 32 | inObj
	} else {
		prev = 48 | inObj
	}
	key = 0
	if inObj != 0 {
		goto fusedObjectComma
	}
	if depth > 0 {
		goto fusedArrayComma
	}
	goto dispatch

writeString:
	if entryOff>>4 >= uint64(entCap) {
		bad |= Stage2IndexFull
		goto done
	}
	entryIndex = entryOff >> 4
	info = Stage2IndexInfoString
	if isKey != 0 {
		info |= Stage2IndexKeyFlag
	}
	p = unsafe.Add(entryp, uintptr(entryOff))
	*(*uint64)(p) = j
	*(*uint64)(unsafe.Add(p, 8)) = 1 | uint64(info)<<32
	entryOff += 16
	stringEntry = uint32(entryIndex)
	prev = 96 | isKey<<4 | inObj
	key = 0
	if pi == len(positions) {
		inString = 1
		goto done
	}
	j = uint64(*(*uint32)(unsafe.Add(posp, uintptr(pi)*4)))
	c = *(*byte)(unsafe.Add(basep, uintptr(j)))
	if c != '"' {
		bad |= 1
		goto done
	}
	pi++
	*(*uint32)(unsafe.Add(p, 4)) = uint32(j + 1)
	inString = 0
	if isKey != 0 {
		goto fusedColon
	}
	if inObj != 0 {
		goto fusedObjectComma
	}
	if depth > 0 {
		goto fusedArrayComma
	}
	goto dispatch

writeScalar:
	if entryOff>>4 >= uint64(entCap) {
		bad |= Stage2IndexFull
		goto done
	}
	scan = int(j)
	scalarOK = true
	switch c {
	case 't':
		if scan+4 > n || *(*uint32)(unsafe.Add(basep, scan)) != 0x65757274 {
			scalarOK = false
		} else {
			scan += 4
			scalarInfo = Stage2IndexInfoBool
		}
	case 'f':
		if scan+5 > n || *(*uint32)(unsafe.Add(basep, scan+1)) != 0x65736c61 {
			scalarOK = false
		} else {
			scan += 5
			scalarInfo = Stage2IndexInfoBool
		}
	case 'n':
		if scan+4 > n || *(*uint32)(unsafe.Add(basep, scan)) != 0x6c6c756e {
			scalarOK = false
		} else {
			scan += 4
			scalarInfo = Stage2IndexInfoNull
		}
	default:
		if c == '-' {
			scan++
			if scan >= n {
				scalarOK = false
				goto scalarScanned
			}
		}
		d = *(*byte)(unsafe.Add(basep, scan))
		if d == '0' {
			scan++
		} else if '1' <= d && d <= '9' {
			for scan++; scan < n; scan++ {
				d = *(*byte)(unsafe.Add(basep, scan))
				if d < '0' || d > '9' {
					break
				}
			}
		} else {
			scalarOK = false
			goto scalarScanned
		}
		integer = true
		if scan < n && *(*byte)(unsafe.Add(basep, scan)) == '.' {
			integer = false
			scan++
			if scan >= n {
				scalarOK = false
				goto scalarScanned
			}
			d = *(*byte)(unsafe.Add(basep, scan))
			if d < '0' || d > '9' {
				scalarOK = false
				goto scalarScanned
			}
			for scan++; scan < n; scan++ {
				d = *(*byte)(unsafe.Add(basep, scan))
				if d < '0' || d > '9' {
					break
				}
			}
		}
		if scan < n {
			d = *(*byte)(unsafe.Add(basep, scan))
			if d == 'e' || d == 'E' {
				integer = false
				scan++
				if scan < n {
					d = *(*byte)(unsafe.Add(basep, scan))
					if d == '+' || d == '-' {
						scan++
					}
				}
				if scan >= n {
					scalarOK = false
					goto scalarScanned
				}
				d = *(*byte)(unsafe.Add(basep, scan))
				if d < '0' || d > '9' {
					scalarOK = false
					goto scalarScanned
				}
				for scan++; scan < n; scan++ {
					d = *(*byte)(unsafe.Add(basep, scan))
					if d < '0' || d > '9' {
						break
					}
				}
			}
		}
		scalarInfo = Stage2IndexInfoNumber
		if integer {
			scalarInfo |= Stage2IndexIntFlag
		}
	}

scalarScanned:
	if scalarOK && scan < n {
		d = *(*byte)(unsafe.Add(basep, scan))
		switch d {
		case ' ', '\t', '\n', '\r', ',', ':', '{', '}', '[', ']':
		default:
			scalarOK = false
		}
	}
	if !scalarOK {
		bad |= 1
		goto done
	}
	p = unsafe.Add(entryp, uintptr(entryOff))
	*(*uint64)(p) = j | uint64(uint32(scan))<<32
	*(*uint64)(unsafe.Add(p, 8)) = 1 | uint64(scalarInfo)<<32
	entryOff += 16
	prev = 112 | inObj
	key = 0
	if inObj != 0 {
		goto fusedObjectComma
	}
	if depth > 0 {
		goto fusedArrayComma
	}
	goto dispatch

fusedKey:
	if pi == len(positions) {
		goto done
	}
	j = uint64(*(*uint32)(unsafe.Add(posp, uintptr(pi)*4)))
	c = *(*byte)(unsafe.Add(basep, uintptr(j)))
	if c != '"' {
		pi++
		cls = uint64(stage2Class[c])
		goto handleKnown
	}
	pi++
	isKey = 8
	goto writeString

fusedColon:
	if pi == len(positions) {
		goto done
	}
	j = uint64(*(*uint32)(unsafe.Add(posp, uintptr(pi)*4)))
	c = *(*byte)(unsafe.Add(basep, uintptr(j)))
	if c != ':' {
		pi++
		cls = uint64(stage2Class[c])
		goto handleKnown
	}
	pi++
	prev = 64 | inObj
	key = 0
	goto fusedValue

fusedValue:
	if pi == len(positions) {
		goto done
	}
	j = uint64(*(*uint32)(unsafe.Add(posp, uintptr(pi)*4)))
	c = *(*byte)(unsafe.Add(basep, uintptr(j)))
	pi++
	switch c {
	case '"':
		isKey = 0
		goto writeString
	case '{':
		cls = stage2ccO
		goto openContainer
	case '[':
		cls = stage2ccA
		goto openContainer
	default:
		cls = uint64(stage2Class[c])
		if cls == stage2ccS {
			goto writeScalar
		}
		goto handleKnown
	}

fusedObjectComma:
	if pi == len(positions) {
		goto done
	}
	j = uint64(*(*uint32)(unsafe.Add(posp, uintptr(pi)*4)))
	c = *(*byte)(unsafe.Add(basep, uintptr(j)))
	pi++
	if c == ',' {
		count++
		prev = 88
		key = 8
		goto fusedKey
	}
	if c == '}' {
		cls = stage2ccC
		members = count + 1
		goto closeContainer
	}
	cls = uint64(stage2Class[c])
	goto handleKnown

fusedArrayValue:
	if pi == len(positions) {
		goto done
	}
	j = uint64(*(*uint32)(unsafe.Add(posp, uintptr(pi)*4)))
	c = *(*byte)(unsafe.Add(basep, uintptr(j)))
	pi++
	switch c {
	case '"':
		isKey = 0
		goto writeString
	case '{':
		cls = stage2ccO
		goto openContainer
	case '[':
		cls = stage2ccA
		goto openContainer
	case ']':
		cls = stage2ccB
		if prev == 16 {
			members = count
			goto closeContainer
		}
		goto handleKnown
	default:
		cls = uint64(stage2Class[c])
		if cls == stage2ccS {
			goto writeScalar
		}
		goto handleKnown
	}

fusedArrayComma:
	if pi == len(positions) {
		goto done
	}
	j = uint64(*(*uint32)(unsafe.Add(posp, uintptr(pi)*4)))
	c = *(*byte)(unsafe.Add(basep, uintptr(j)))
	pi++
	if c == ',' {
		count++
		prev = 80
		key = 0
		goto fusedArrayValue
	}
	if c == ']' {
		cls = stage2ccB
		members = count + 1
		goto closeContainer
	}
	cls = uint64(stage2Class[c])
	goto handleKnown

done:
	st.Bad = bad
	st.Depth = depth
	st.PrevRowIO = prev
	st.KeyRow8 = key
	st.Count = count
	st.EntryOff = entryOff
	st.StringEntry = stringEntry
	st.InString = inString
}
