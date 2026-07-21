package simdjson

import (
	"github.com/thesyncim/simdjson/document"
	"github.com/thesyncim/simdjson/internal/byteview"
)

// Corpus-wide value dictionary: structural deduplication of repeated value
// spans across a document set.
//
// Real corpora repeat their values as hard as they repeat their keys: a
// ticketing feed names the same handful of venues, seat categories, and area
// sub-objects across every performance; a social feed repeats the same language
// tags, source strings, and boilerplate across every post. A general-purpose
// compressor removes this byte-wise and pays whole-value decompression on every
// read. A ValueDictionary removes it structurally, once, without surrendering
// random access: it interns each distinct value span into a ValueInterner
// (value_dict.go) and records, per document, a compact reference in place of
// each repeated occurrence, from which a compacting store can drop the repeated
// source bytes while every value still resolves to a stable arena view in O(1).
//
//	doc i: [ ... "PLEYEL_PLEYEL" ... {"areaId":205705999,"blockIds":[]} ... ]
//	doc j: [ ... "PLEYEL_PLEYEL" ... {"areaId":205705999,"blockIds":[]} ... ]
//	dictionary: id7 -> "PLEYEL_PLEYEL"   id9 -> {"areaId":205705999,"blockIds":[]}
//	doc i splices: (off,id7) (off,id9)   doc j splices: (off,id7) (off,id9)
//
// A building block, not a storage mode. A ValueDictionary indexes a DocSet
// through its public Doc accessor and the tape's own coordinates; it never
// modifies the set. That keeps it orthogonal to the storage modes it overlays —
// it composes with ShapeTapes for free, because Doc yields a conforming
// document's classic tape on demand, so the shape removes key redundancy and
// the dictionary removes value redundancy over the same documents. The value
// contract is unchanged: a document still reads through its Index exactly as
// before; the dictionary is the parallel structure from which the repeated
// bytes could be dropped.
//
// What is interned. A value span is any complete value the tape carries — a
// scalar or a whole container subtree — identified by its raw bytes. Interning
// by raw bytes keeps the value exact: a spliced value reads back byte-identical,
// number spellings included, and a container subtree reinstates verbatim. The
// lever that closes the real-corpus gap is the container span: one four-byte
// reference replaces a recurring sub-object's bytes and its whole entry subtree,
// where a scalar reference would replace only bytes comparable in size to the
// reference itself. The walk is therefore greedy and top-down — a repeated span
// is taken whole, its members never separately considered — and gates on a
// length floor so a span too short to out-save its reference stays inline.
//
// Sighting economics. Interning a value seen once costs a dictionary entry with
// no saving, so — as the shape cache gates compilation behind a repeat sighting
// — a span is interned only on its second appearance. The first occurrence stays
// inline; every later one is a reference. A corpus whose values never recur
// therefore pays only a hash-set probe per candidate, never a dictionary entry
// it cannot amortize.
//
// Read contract. A spliced value resolves through ValueInterner.Value in O(1) to
// a stable arena view — no decompression. The dictionary's invariant
// (value_dict.go) is that Value(id) is byte-identical to the span interned for
// id, so a spliced reference reads back exactly the source bytes it replaced;
// the bounded-exhaustive differential test checks this against classic reads for
// the whole small-scope domain. The one thing given up is reconstructing a
// document's exact original byte layout cheaply — the bytes are split between the
// shared dictionary and each document's residual — so Reconstruct splices them
// back, the cold operation the space win is traded for; every value stays
// directly addressed.
//
// Everything here is safe Go: the interning walk, the splice records, and the
// reconstruction use ordinary slice indexing bounded by the tape's own next
// links.

// valueDictMinSpan is the default length floor for interning. A splice costs
// eight bytes (a source offset and a dictionary id); a span must exceed that to
// save once its reference is charged, and the tape entries a container span also
// collapses make the effective floor lower still. Sixteen bytes keeps short
// scalars — the numbers whose spelling is no longer than their reference —
// inline, where they cost no dictionary entry and no splice record.
const valueDictMinSpan = 16

// A valueSplice records one dictionary-backed value occurrence in a document:
// the value's start offset in the document's source and the dictionary id whose
// bytes it stands for. The value's end is implicit — start + len(Value(id)) —
// because the dictionary holds the exact span, so the record needs no length.
// The invariant every read rests on is src[start : start+len(Value(id))] ==
// Value(id): the reference reads back the bytes it replaced.
type valueSplice struct {
	start uint32
	id    uint32
}

// A valueDictRef is one document's splice header: its references occupy
// splices[off : off+n], in ascending start order. The zero ref (n == 0) marks a
// document with no dictionary-backed values — every value inline, first-sighted
// or below the length floor.
type valueDictRef struct {
	off uint32
	n   uint32
}

// A ValueDictionary is a corpus-wide value dictionary built over the documents
// of a DocSet. It owns its storage under the never-moving arena discipline of
// the rest of the layer; a slice returned by Value stays valid for the
// dictionary's lifetime. The zero ValueDictionary is not ready — use
// NewValueDictionary. A ValueDictionary is not safe for concurrent use.
type ValueDictionary struct {
	values  ValueInterner
	refs    []valueDictRef // one per document added, in add order
	splices []valueSplice
	seen    map[uint64]struct{}
	floor   uint32
}

// NewValueDictionary returns an empty dictionary whose interning length floor is
// floor bytes; a floor of zero selects the default (valueDictMinSpan). The
// differential and bounded-exhaustive suites lower the floor to one so every
// repeated value, however short, is dictionary-backed and the arena read path is
// exercised on every value shape.
func NewValueDictionary(floor int) *ValueDictionary {
	f := uint32(valueDictMinSpan)
	if floor > 0 {
		f = uint32(floor)
	}
	return &ValueDictionary{seen: make(map[uint64]struct{}), floor: f}
}

// Build indexes every document of s into the dictionary in ordinal order and
// returns the dictionary, so BuildValueDictionary(s) is one call. Under
// ShapeTapes each document's classic tape is materialized through Doc, so the
// mode composes with shape deduplication without special handling.
func BuildValueDictionary(s *DocSet, floor int) *ValueDictionary {
	d := NewValueDictionary(floor)
	for i := 0; i < s.Len(); i++ {
		d.AddDoc(s.Doc(i))
	}
	return d
}

// AddDoc interns one document's repeated value spans and appends its splice
// header, aligning refs with the order documents are added. idx is the
// document's classic tape (Index): scalars and whole recurring sub-objects are
// interned, keys never — that redundancy belongs to the shape layer.
func (d *ValueDictionary) AddDoc(idx Index) {
	off := uint32(len(d.splices))
	ent := idx.entries
	src := idx.src
	// Greedy top-down: at each value, a repeated span at or above the floor is
	// taken whole and its subtree skipped, so a recurring sub-object is one
	// reference rather than a reference per scalar inside it; only a value not
	// taken is descended, and only into a container's member values. The root is
	// descended, never interned: a whole-document repeat is a corpus artifact of
	// a tiled benchmark, not a property to harvest.
	var walk func(i int, mayIntern bool) int
	walk = func(i int, mayIntern bool) int {
		e := &ent[i]
		if mayIntern && e.end-e.start >= d.floor {
			span := byteview.SliceRange(&src[0], e.start, e.end)
			h := valueDictHash(span)
			if _, sighted := d.seen[h]; sighted {
				id := d.values.Intern(span)
				d.splices = append(d.splices, valueSplice{start: e.start, id: id})
				return i + int(e.next)
			}
			d.seen[h] = struct{}{}
		}
		if k := e.Kind(); k != document.Object && k != document.Array {
			return i + 1
		}
		j := i + 1
		end := i + int(e.next)
		for j < end {
			if ent[j].flags()&tapeFlagKey != 0 {
				j++ // step over the key to its value
				continue
			}
			j = walk(j, true)
		}
		return end
	}
	if len(ent) > 0 {
		walk(0, false)
	}
	d.refs = append(d.refs, valueDictRef{off: off, n: uint32(len(d.splices)) - off})
}

// valueDictHash is the 64-bit sighting hash: it gates the second-sighting
// interning decision only, so it never needs to be collision-resistant — a
// collision merely admits an unrelated singleton to interning, where the
// dictionary's own byte-verified Intern keeps it a distinct, correct entry. A
// 64-bit fold makes even that waste vanishingly rare. FNV-1a over the raw span.
func valueDictHash(b []byte) uint64 {
	h := uint64(1469598103934665603)
	for _, c := range b {
		h ^= uint64(c)
		h *= 1099511628211
	}
	return h
}

// Len returns the number of documents indexed.
func (d *ValueDictionary) Len() int { return len(d.refs) }

// Value returns the raw span of an interned value, borrowing the dictionary's
// arena. See ValueInterner.Value for the lifetime contract.
func (d *ValueDictionary) Value(id uint32) []byte { return d.values.Value(id) }

// spliceAt returns the dictionary id for the value at source offset start in the
// document at ordinal doc, or false when that value is stored inline. It
// binary-searches the document's ascending splice records — the walk appends
// them in source order — so a value-dict read resolves a candidate in
// O(log splices).
func (d *ValueDictionary) spliceAt(doc int, start uint32) (uint32, bool) {
	if uint(doc) >= uint(len(d.refs)) {
		return 0, false
	}
	r := d.refs[doc]
	lo, hi := r.off, r.off+r.n
	for lo < hi {
		mid := lo + (hi-lo)/2
		switch sp := d.splices[mid]; {
		case sp.start == start:
			return sp.id, true
		case sp.start < start:
			lo = mid + 1
		default:
			hi = mid
		}
	}
	return 0, false
}

// Raw returns node v's bytes for the document at ordinal doc, resolved through
// the dictionary when v is a dictionary-backed value: its span was interned, so
// the bytes come from the shared arena in O(1) rather than the document's
// source. A value stored inline reads from source as usual. The two are
// byte-identical by the dictionary invariant, so the result never changes; the
// routing is what lets a compacting store drop the backed span and still read it
// directly. v must be a node of the same document, obtained from the Index this
// ordinal was added from.
func (d *ValueDictionary) Raw(doc int, v Node) RawValue {
	if v.entry != nil {
		if id, ok := d.spliceAt(doc, v.entry.start); ok {
			return RawValue{src: d.values.Value(id)}
		}
	}
	return v.Raw()
}

// Reconstruct assembles the document's exact original source bytes into dst,
// splicing each dictionary reference back inline from the arena. It is the cold
// operation the space win is traded for — the only read that materializes a
// whole document rather than addressing a value directly — and its output is
// byte-identical to the source idx was built from, the property the
// bounded-exhaustive differential test checks across the small-scope domain. idx
// must be the Index the document at ordinal doc was added from. dst is grown as
// needed and returned.
func (d *ValueDictionary) Reconstruct(dst []byte, idx Index, doc int) []byte {
	if uint(doc) >= uint(len(d.refs)) {
		return append(dst, idx.src...)
	}
	r := d.refs[doc]
	prev := uint32(0)
	for k := r.off; k < r.off+r.n; k++ {
		sp := d.splices[k]
		dst = append(dst, idx.src[prev:sp.start]...)
		dst = append(dst, d.values.Value(sp.id)...)
		prev = sp.start + uint32(len(d.values.Value(sp.id)))
	}
	return append(dst, idx.src[prev:]...)
}

// ValueDictStats reports the dictionary's storage composition, the accounting
// behind the space model: distinct value spans held once, the occurrences they
// stand in for, and the source bytes those occurrences repeat.
type ValueDictStats struct {
	// Entries distinct value spans held once cost DictBytes.
	Entries   int
	DictBytes int64
	// Splices dictionary-backed occurrences reference the entries, removing
	// SplicedBytes of source (sum of the referenced spans' lengths) — the
	// redundancy the dictionary removed.
	Splices      int64
	SplicedBytes int64
}

// Stats summarizes the dictionary. It costs one pass over the splice slab and
// materializes no document, so it is safe for accounting at any point.
func (d *ValueDictionary) Stats() ValueDictStats {
	st := ValueDictStats{Entries: d.values.Len(), DictBytes: d.values.Bytes(), Splices: int64(len(d.splices))}
	for _, sp := range d.splices {
		st.SplicedBytes += int64(len(d.values.Value(sp.id)))
	}
	return st
}

// ModeledBytes returns the source bytes a compacting store would hold if it
// dropped every dictionary-backed span and kept a reference in its place:
// sourceBytes - SplicedBytes + Splices*8 + DictBytes. It is the value
// dictionary's warm at-rest floor — the tape and residual source stay, so every
// value remains directly addressable — expressed against the caller's measured
// source total.
func (d *ValueDictionary) ModeledBytes(sourceBytes int64) int64 {
	st := d.Stats()
	return sourceBytes - st.SplicedBytes + st.Splices*8 + st.DictBytes
}
