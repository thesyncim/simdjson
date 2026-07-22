package simdjson

import (
	"encoding/binary"
	"math/bits"
	"runtime"
	"unsafe"

	"github.com/thesyncim/simdjson/internal/byteview"
	"github.com/thesyncim/simdjson/internal/storemem"
)

// storeMappedKeyRef is the compact, pointer-free authority for one key in a
// Store image. off and length address the already-validated manifest; loc is
// the stable Store row. The open-addressed table stores a one-based index into
// refs, keeping an empty bucket representable without reserving a hash value.
type storeMappedKeyRef struct {
	off    uint64
	length uint32
	loc    storeLocation
}

// storeMappedKeys replaces the pointer-heavy key HAMT for an immutable mapped
// base. refs and buckets share one anonymous pointer-free block outside the Go
// heap on common Unix systems. Later mutations live in the ordinary persistent
// HAMT overlay; a lookup checks that overlay first, then this base, and always
// verifies the complete current key spelling at the returned stable slot.
//
// No view into block escapes a method. The finalizer is therefore a resource
// backstop, not a borrowed-value lifetime mechanism: every Store state and
// mapped chunk keeps this object reachable, and runtime.KeepAlive covers the
// last native load. Returned key strings borrow source, the caller-owned Store
// image, whose existing lifetime contract is unchanged.
type storeMappedKeys struct {
	source   []byte
	refs     []storeMappedKeyRef
	controls []byte
	slots    []uint64
	mask     uint64
	count    int
	block    *storemem.Block
}

func newStoreMappedKeys(source []byte, count int) (*storeMappedKeys, error) {
	refSize := int(unsafe.Sizeof(storeMappedKeyRef{}))
	if count < 0 || count > maxInt()/refSize {
		return nil, ErrStorePersistTooLarge
	}
	capacity := 1
	for capacity < 8 || count > capacity-capacity/4 {
		if capacity > maxInt()/2 {
			return nil, ErrStorePersistTooLarge
		}
		capacity *= 2
	}
	// controls repeats its first seven bytes after the logical table so every
	// wrapping eight-byte probe is one bounds-checked native load. slots starts
	// at the next eight-byte boundary.
	controlBytes := capacity + storeMappedKeyGroup - 1
	slotOffset := (controlBytes + 7) &^ 7
	if capacity > maxInt()/8 || slotOffset > maxInt()-capacity*8 {
		return nil, ErrStorePersistTooLarge
	}
	refBytes := uint64(count * refSize)
	tableBytes := uint64(slotOffset + capacity*8)
	if refBytes > uint64(maxInt())-tableBytes {
		return nil, ErrStorePersistTooLarge
	}
	block, err := storemem.Allocate(int(refBytes + tableBytes))
	if err != nil {
		return nil, err
	}
	data := block.Bytes()
	var refs []storeMappedKeyRef
	if count != 0 {
		refs = unsafe.Slice((*storeMappedKeyRef)(unsafe.Pointer(unsafe.SliceData(data))), count)
	}
	tableData := data[int(refBytes):]
	controls := tableData[:controlBytes]
	slotData := tableData[slotOffset:]
	slots := unsafe.Slice((*uint64)(unsafe.Pointer(unsafe.SliceData(slotData))), capacity)
	m := &storeMappedKeys{
		source: source, refs: refs, controls: controls, slots: slots,
		mask: uint64(capacity - 1), block: block,
	}
	runtime.SetFinalizer(m, (*storeMappedKeys).release)
	return m, nil
}

func (m *storeMappedKeys) release() {
	if m == nil || m.block == nil {
		return
	}
	_ = m.block.Close()
	m.block = nil
	m.refs = nil
	m.controls = nil
	m.slots = nil
}

func (m *storeMappedKeys) externalBytes() uint64 {
	if m == nil || m.block == nil || !m.block.OutsideHeap() {
		return 0
	}
	return uint64(m.block.Len())
}

func (m *storeMappedKeys) key(ref uint64) string {
	r := m.refs[ref]
	end := r.off + uint64(r.length)
	key := byteview.String(m.source[r.off:end])
	runtime.KeepAlive(m)
	return key
}

const (
	storeMappedKeyGroup = 8
	storeMappedKeyLo    = uint64(0x0101010101010101)
	storeMappedKeyHi    = uint64(0x8080808080808080)
)

func storeMappedKeyFingerprint(hash uint64) byte { return byte(hash>>57) + 1 }

// storeMappedKeyMatches returns a high bit for every byte equal to value. The
// subtraction idiom may mark an adjacent byte after a match because of borrow;
// that is harmless because callers recheck the control byte and exact key.
// It never misses a matching byte.
func storeMappedKeyMatches(word uint64, value byte) uint64 {
	x := word ^ uint64(value)*storeMappedKeyLo
	return (x - storeMappedKeyLo) &^ x & storeMappedKeyHi
}

func storeMappedKeyHasEmpty(word uint64) bool {
	return (word-storeMappedKeyLo)&^word&storeMappedKeyHi != 0
}

// insert probes eight one-byte fingerprints per native word. Open is single-
// threaded and the table is immutable after publication. false reports a
// complete-key duplicate; hashes and seven-bit fingerprints are routing only.
func (m *storeMappedKeys) insert(hash, ref uint64) bool {
	want := m.key(ref)
	fingerprint := storeMappedKeyFingerprint(hash)
	index := hash & m.mask
	for {
		word := binary.LittleEndian.Uint64(m.controls[index : index+storeMappedKeyGroup])
		matches := storeMappedKeyMatches(word, fingerprint)
		for matches != 0 {
			lane := uint(bits.TrailingZeros64(matches)) >> 3
			slot := (index + uint64(lane)) & m.mask
			if m.controls[index+uint64(lane)] == fingerprint && m.key(m.slots[slot]-1) == want {
				runtime.KeepAlive(m)
				return false
			}
			matches &= matches - 1
		}
		if storeMappedKeyHasEmpty(word) {
			for lane := uint64(0); lane < storeMappedKeyGroup; lane++ {
				controlIndex := index + lane
				if m.controls[controlIndex] != 0 {
					continue
				}
				slot := controlIndex & m.mask
				m.controls[slot] = fingerprint
				if slot < storeMappedKeyGroup-1 {
					m.controls[uint64(len(m.slots))+slot] = fingerprint
				}
				m.slots[slot] = ref + 1
				m.count++
				runtime.KeepAlive(m)
				return true
			}
		}
		index = (index + storeMappedKeyGroup) & m.mask
	}
}

func (m *storeMappedKeys) lookup(hash uint64, key string) (storeLocation, bool) {
	if m == nil || m.count == 0 {
		return storeLocation{}, false
	}
	fingerprint := storeMappedKeyFingerprint(hash)
	index := hash & m.mask
	for {
		word := binary.LittleEndian.Uint64(m.controls[index : index+storeMappedKeyGroup])
		matches := storeMappedKeyMatches(word, fingerprint)
		for matches != 0 {
			lane := uint(bits.TrailingZeros64(matches)) >> 3
			controlIndex := index + uint64(lane)
			slot := controlIndex & m.mask
			if m.controls[controlIndex] == fingerprint {
				ref := m.slots[slot] - 1
				if m.key(ref) == key {
					loc := m.refs[ref].loc
					runtime.KeepAlive(m)
					return loc, true
				}
			}
			matches &= matches - 1
		}
		if storeMappedKeyHasEmpty(word) {
			runtime.KeepAlive(m)
			return storeLocation{}, false
		}
		index = (index + storeMappedKeyGroup) & m.mask
	}
}

func (m *storeMappedKeys) keyAt(base uint64, ordinal uint8) string {
	return m.key(base + uint64(ordinal))
}

func storeStateKeyLookup(state *storeState, hash uint64, key string) (storeLocation, bool) {
	_, loc, ok := storeStateKeyLookupChunk(state, hash, key)
	return loc, ok
}

// storeStateKeyLookupChunk returns the already-resolved chunk with the stable
// location. Hot callers therefore walk the persistent chunk vector once. The
// mapped base's table already performed exact-key comparison; only a page that
// has since been rebuilt needs a second comparison against its current slot.
func storeStateKeyLookupChunk(state *storeState, hash uint64, key string) (*storeChunk, storeLocation, bool) {
	if state == nil {
		return nil, storeLocation{}, false
	}
	if loc, ok := storeKeyLookup(state.keys, hash, key); ok {
		chunk := state.chunks.get(loc.chunk)
		if chunk != nil && chunk.live&(uint64(1)<<loc.slot) != 0 {
			return chunk, loc, true
		}
	}
	if loc, ok := state.baseKeys.lookup(hash, key); ok {
		chunk := state.chunks.get(loc.chunk)
		if chunk != nil && chunk.live&(uint64(1)<<loc.slot) != 0 &&
			(chunk.mappedKeys == state.baseKeys || chunk.key(int(loc.slot)) == key) {
			return chunk, loc, true
		}
	}
	return nil, storeLocation{}, false
}
