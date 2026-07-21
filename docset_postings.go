package simdjson

import (
	"math"
	"sort"

	"github.com/thesyncim/simdjson/document"
)

// The inverted posting layer: existence and containment made sublinear.
//
// A full columnar scan answers "which documents have key K" or "which
// documents contain value V under path P" in one pass over every document —
// linear, and wasteful when the predicate is selective (a handful of the
// corpus matches). This layer builds, opt-in at ingest behind DocSet.Postings,
// two inverted structures that turn those questions into a hash probe plus a
// visit of only the candidates, so a selective WHERE (ADR 0003) prunes instead
// of scans.
//
//	existence   key K --interner--> id --keyShapes--> shapes with K
//	                                     --shapeDocs--> their document ordinals
//	                                     + a scan of the non-conforming remainder
//	containment (path P, value V) --hash--> bucket --value--> candidate ordinals
//	                                                          --Contains--> verified
//
// Key existence reuses shape deduplication (docset_shape.go). A shape-taped
// document's top-level keys are exactly its proven shape's fields — the shape
// was byte-verified against the document at ingest — so "shape S contains key
// K" is decided once per shape, and every document stored under S inherits the
// answer without a per-document look. Inverting the interner's key id to the
// shapes carrying it, and each shape to its document list, resolves existence
// by unioning those lists. Documents whose layout was not proven to a shape —
// the non-conforming remainder, everything stored classic — cannot borrow a
// shape's answer, so they are listed and scanned exactly, which stays cheap
// when the remainder is a small fraction (the shape-clustered corpus the whole
// layer targets). With ShapeTapes off no document is shape-proven and the
// remainder is the whole set, so existence stays correct but unaccelerated;
// the two flags are designed to be enabled together.
//
// Value containment is the RediSearch/inverted analogue. Every scalar a
// document carries under a top-level key — the key's own value, or a scalar
// element of a top-level array value — is bucketed by (path hash, canonical
// value hash). A query field @> needle hashes (path, needle) the same way,
// gathers the bucket, and verifies each candidate with Node.Contains, the
// landed containment evaluator (contains.go). The hash is canonical, not
// literal: numbers bucket by their float64 value and strings by decoded
// content, so 1.0 and 1 share a bucket and an escaped spelling matches its
// decoded twin — whatever Node.Contains judges equal must never miss the
// bucket, or a true match would be lost. Collisions only widen the candidate
// set; the verifier removes the false positives, so WhereContains returns the
// exact answer, equal to a full scan.
//
// The posting lists are ordinal slices, never handed out by reference —
// WhereExists and WhereContains return freshly allocated results — so, unlike
// the interner and shape arenas whose interior views are borrowed and must
// never move, these may grow and relocate freely. The key spellings and shape
// records they route through do live in the never-moving interner and shape
// arenas. Everything here is safe Go: slice indexing and map probes bounded by
// the ingest invariant that every indexed ordinal is a live document.

// docPostings holds a DocSet's inverted structures. It is built incrementally
// at ingest (indexPostings) and read by WhereExists and WhereContains. The zero
// value is not ready; DocSet.indexPostings constructs it on the first indexed
// document.
type docPostings struct {
	// docs counts the documents folded into the postings. Postings are trusted
	// only when this equals the set's Len: enabling DocSet.Postings after some
	// documents are already stored leaves earlier ordinals unindexed, and the
	// query paths detect the gap (postingsReady) and fall back to the full
	// scan, which is always correct.
	docs int

	// Key-existence family. keys interns each shape's top-level member names to
	// dense ids; keyShapes inverts a key id to the postings-local ids of every
	// shape carrying that key; shapeDocs holds, per shape id, the ascending
	// ordinals of the documents stored under it; shapeIDs assigns a shape its
	// postings-local id on first sighting. remainder lists the ordinals of the
	// non-conforming documents, in ascending order, for the exact query-time
	// scan.
	keys      KeyInterner
	keyShapes [][]int32
	shapeDocs [][]int32
	shapeIDs  map[*shapeRecord]int32
	remainder []int32

	// Value-containment family. value maps a (path, canonical value) bucket to
	// the ascending ordinals of documents carrying that scalar under that
	// top-level key, deduplicated so a document appears at most once per bucket
	// however many times the value recurs within it.
	value map[uint64][]int32

	// wide is scratch for widening one narrow value entry during ingest so the
	// per-member hash reads a Node without a heap local, the ShapeCache.wide
	// discipline: indexing is single-consumer (the commit path) so one shared
	// receiver suffices.
	wide IndexEntry
}

// indexPostings folds one just-committed document into the postings. ord is the
// document's ordinal, index its stored form, and ref its shape header (a nil
// rec marks a classic document). It is called from commitDoc under
// DocSet.Postings, after the document is appended, so ord is live and — for a
// narrow shape tape — its value slab entries are already in DocSet.narrow.
func (s *DocSet) indexPostings(ord int, index Index, ref shapeTapeRef) {
	p := s.postings
	if p == nil {
		p = &docPostings{
			shapeIDs: make(map[*shapeRecord]int32),
			value:    make(map[uint64][]int32),
		}
		s.postings = p
	}
	o := int32(ord)
	if ref.rec != nil {
		p.addShapeDoc(ref.rec, o)
		p.indexShapeValues(s, index, ref, o)
	} else {
		p.remainder = append(p.remainder, o)
		p.indexClassicValues(index, o)
	}
	p.docs++
}

// addShapeDoc records that document ord is stored under shape rec. A shape
// sighted for the first time is assigned the next postings-local id and its key
// set is inverted into keyShapes; every distinct decoded name is interned once,
// exact because a shape-taped shape has no duplicate decoded names (duplicate
// layouts are stored classic). Ordinals arrive in commit order, so each shape's
// document list stays ascending.
func (p *docPostings) addShapeDoc(rec *shapeRecord, ord int32) {
	sid, seen := p.shapeIDs[rec]
	if !seen {
		sid = int32(len(p.shapeDocs))
		p.shapeIDs[rec] = sid
		p.shapeDocs = append(p.shapeDocs, nil)
		for i := range rec.fields {
			id := p.keys.InternString(rec.fields[i].decoded)
			for int(id) >= len(p.keyShapes) {
				p.keyShapes = append(p.keyShapes, nil)
			}
			p.keyShapes[id] = append(p.keyShapes[id], sid)
		}
	}
	p.shapeDocs[sid] = append(p.shapeDocs[sid], ord)
}

// indexShapeValues buckets the scalar member values of a shape-taped document.
// The flatness the shape proved means every member value is a single entry — a
// scalar or an empty container — so there are no nested arrays to descend:
// empty containers carry no scalar and are skipped, and each scalar is bucketed
// under its key's path hash. A narrow tape's values live in the set's slab; a
// wide tape's in the document's own entry array.
func (p *docPostings) indexShapeValues(s *DocSet, index Index, ref shapeTapeRef, ord int32) {
	src := index.src
	if len(src) == 0 {
		return
	}
	base := &src[0]
	rec := ref.rec
	for m := range rec.fields {
		var entry *IndexEntry
		if ref.narrow {
			p.wide = s.narrow[int(ref.off)+m].widen()
			entry = &p.wide
		} else {
			entry = &index.entries[m]
		}
		if h, ok := postValueHash(Node{src: base, entry: entry}); ok {
			p.addValue(hashKeyString(rec.fields[m].decoded), h, ord)
		}
	}
}

// indexClassicValues buckets a classic document's top-level scalars. Only the
// root object's members are indexed, in document order (duplicates included, so
// a value shadowed by a later same-key member still contributes a candidate the
// verifier resolves): a scalar member is bucketed under its key, and a
// top-level array member contributes each of its scalar elements — the
// documented top-level containment case where an array contains a scalar equal
// to some element. Object members and deeper structure carry no top-level
// scalar and are skipped. A non-object root has no members.
func (p *docPostings) indexClassicValues(index Index, ord int32) {
	root := index.Root()
	if root.Kind() != document.Object {
		return
	}
	it, _ := root.ObjectIter()
	for {
		key, value, ok := it.Next()
		if !ok {
			return
		}
		pathHash := postKeyHash(key)
		if value.Kind() == document.Array {
			elems, _ := value.ArrayIter()
			for {
				el, ok := elems.Next()
				if !ok {
					break
				}
				if h, ok := postValueHash(el); ok {
					p.addValue(pathHash, h, ord)
				}
			}
			continue
		}
		if h, ok := postValueHash(value); ok {
			p.addValue(pathHash, h, ord)
		}
	}
}

// addValue appends ord to the (path, value) bucket, skipping the append when
// the bucket's last ordinal is already ord. A document indexes all of its own
// occurrences of a bucket contiguously before the next document, so the
// adjacent check deduplicates a repeated value within one document while the
// list stays ascending across documents.
func (p *docPostings) addValue(pathHash uint32, valueHash uint64, ord int32) {
	bucket := postBucket(pathHash, valueHash)
	list := p.value[bucket]
	if n := len(list); n > 0 && list[n-1] == ord {
		return
	}
	p.value[bucket] = append(list, ord)
}

// postingsReady reports whether the postings cover every stored document. It is
// false when DocSet.Postings was never set, or was set after documents were
// already appended, so the query paths fall back to a correct full scan rather
// than trusting a partial index.
func (s *DocSet) postingsReady() bool {
	return s.postings != nil && s.postings.docs == len(s.docs)
}

// WhereExists returns, in ascending order, the ordinals of the documents whose
// root object has a member named path — the documents for which Doc(i).Root().
// Get(path) succeeds, the value irrelevant (a present member with a null value
// exists). It is the execution primitive behind ADR 0003's EXISTS and IS [NOT]
// NULL over a top-level column.
//
// With postings built (DocSet.Postings) and covering the set, existence
// resolves through the shape index: the key's interner id selects the shapes
// carrying it, and their document lists are unioned, plus an exact scan of the
// non-conforming remainder — sublinear when the key is selective. Without
// postings, or when they were enabled late, it falls back to a full scan that
// tests each document's proven shape or classic tape directly. Both paths
// return the same set; postings only change its cost. The result is freshly
// allocated and owned by the caller.
func (s *DocSet) WhereExists(path string) []int {
	if !s.postingsReady() {
		return s.whereExistsScan(path)
	}
	p := s.postings
	var res []int
	if id, ok := p.keys.LookupString(path); ok && int(id) < len(p.keyShapes) {
		for _, sid := range p.keyShapes[id] {
			for _, ord := range p.shapeDocs[sid] {
				res = append(res, int(ord))
			}
		}
	}
	for _, ord := range p.remainder {
		if _, ok := s.docs[ord].Root().Get(path); ok {
			res = append(res, int(ord))
		}
	}
	// The per-shape and remainder lists are each ascending but interleaved
	// across shapes; every ordinal appears once by the storage partition, so a
	// sort orders the union without deduplication.
	sort.Ints(res)
	return res
}

// whereExistsScan is WhereExists's full-scan fallback: it tests each document
// for the key without postings. A shape-taped document answers from its proven
// shape's field table — no widening, no source touch — and a classic document
// through Get on its tape, so the scan is a fair columnar baseline and never
// materializes a shape tape. Ordinals are produced in ascending order.
func (s *DocSet) whereExistsScan(path string) []int {
	key := CompileKey(path)
	var res []int
	for i := range s.docs {
		if r := s.shapeTapeRefAt(i); r.rec != nil {
			if _, ok := r.rec.fieldOrd(key.key, key.hash); ok {
				res = append(res, i)
			}
			continue
		}
		if _, ok := s.docs[i].Root().GetCompiled(key); ok {
			res = append(res, i)
		}
	}
	return res
}

// WhereContains returns, in ascending order, the ordinals of the documents
// whose value at top-level key path contains needle under the jsonb @>
// semantics of [Node.Contains] — the exact answer, equal to a full scan, not a
// candidate set. needle is one JSON document; an invalid one returns the error
// a failed [BuildIndex] reports. It is the execution primitive behind ADR
// 0003's @> predicate over a top-level column.
//
// A scalar needle with postings built (DocSet.Postings) prunes: the (path,
// needle) bucket yields the candidate documents that carry a matching scalar,
// and each is confirmed with Node.Contains, so hash collisions and values
// shadowed by a later duplicate key are filtered out and the returned set is
// exactly the full scan's. A structured needle — an array or object, whose
// containment the scalar buckets do not describe — and any query without
// postings take the full scan directly. Verification widens a shape-taped
// candidate through Doc, cached thereafter; a store that keeps its documents
// classic verifies without materializing anything. The result is freshly
// allocated and owned by the caller.
func (s *DocSet) WhereContains(path string, needle []byte) ([]int, error) {
	n, err := containsIndex(needle)
	if err != nil {
		return nil, err
	}
	root := n.Root()
	valueHash, scalar := postValueHash(root)
	if !scalar || !s.postingsReady() {
		return s.whereContainsScan(path, root), nil
	}
	bucket := postBucket(hashKeyString(path), valueHash)
	var res []int
	for _, ord := range s.postings.value[bucket] {
		if v, ok := s.Doc(int(ord)).Root().Get(path); ok && v.Contains(root) {
			res = append(res, int(ord))
		}
	}
	// Candidates are ascending and already deduplicated, so verified survivors
	// come out ascending and unique with no further work.
	return res, nil
}

// whereContainsScan is WhereContains's full-scan fallback and the reference its
// pruned path must equal: every document's value at path tested against the
// needle with the same Node.Contains verifier. Ordinals are produced ascending.
func (s *DocSet) whereContainsScan(path string, needle Node) []int {
	var res []int
	for i := range s.docs {
		if v, ok := s.Doc(i).Root().Get(path); ok && v.Contains(needle) {
			res = append(res, i)
		}
	}
	return res
}

// postKeyHash returns an object key's path hash — the content hash of its
// decoded spelling, so it agrees with hashKeyString of a query path. A clean
// key hashes its source alias; an escaped key decodes through a small stack
// buffer first, matching how the containment verifier resolves keys.
func postKeyHash(key Node) uint32 {
	if content, ok := key.StringBytes(); ok {
		return hashKeyContent(content)
	}
	var buf [48]byte
	decoded, _ := key.AppendText(buf[:0])
	return hashKeyContent(decoded)
}

// The value bucket tags separate scalar kinds so that unequal values of
// different kinds — the string "1" and the number 1, which never contain one
// another — land in different buckets, keeping the candidate set tight. They
// are hash inputs only; a collision would merely widen candidates, which the
// verifier removes.
const (
	postTagNull uint64 = iota + 1
	postTagFalse
	postTagTrue
	postTagString
	postTagNumber
	// postTagNumberWide buckets numbers whose magnitude overflows float64 to a
	// single value; distinct such numbers become false-positive candidates the
	// exact verifier resolves, and they never arise in ordinary corpora.
	postTagNumberWide
)

// postValueHash returns a scalar Node's canonical bucket hash, and false for a
// container. The hash respects scalar-equality exactly as [Node.Contains]
// judges it, so two values Contains treats as equal always share a bucket:
// numbers by their float64 value (so 1, 1.0, and 1e0 agree, and -0 folds to 0),
// strings by decoded content (so an escape spelling matches its decoded twin).
// This is the no-false-negative property the candidate filter rests on;
// coarser collisions are allowed because verification follows.
func postValueHash(v Node) (uint64, bool) {
	switch v.Kind() {
	case document.Null:
		return postScalarBucket(postTagNull, 0), true
	case document.Bool:
		b, _ := v.Bool()
		tag := postTagFalse
		if b {
			tag = postTagTrue
		}
		return postScalarBucket(tag, 0), true
	case document.Number:
		f, ok := v.Float64()
		if !ok {
			// A magnitude beyond float64: bucket every such number together.
			return postScalarBucket(postTagNumberWide, 0), true
		}
		if f == 0 {
			// Fold negative zero onto positive zero; JSON admits the -0
			// spelling and jsonb equates it with 0.
			f = 0
		}
		return postScalarBucket(postTagNumber, math.Float64bits(f)), true
	case document.String:
		if content, clean := v.StringBytes(); clean {
			return postScalarBucket(postTagString, postFNV(content)), true
		}
		var buf [64]byte
		decoded, _ := v.AppendText(buf[:0])
		return postScalarBucket(postTagString, postFNV(decoded)), true
	default:
		return 0, false
	}
}

// The bucket hashes fold their inputs with the FNV-1a constants the value
// dictionary uses (docset_valuedict.go); they gate candidate membership only,
// never correctness, so a non-cryptographic fold is enough.
const (
	postFNVOffset uint64 = 1469598103934665603
	postFNVPrime  uint64 = 1099511628211
)

// postScalarBucket folds a scalar's kind tag and payload into its value hash.
func postScalarBucket(tag, payload uint64) uint64 {
	h := postFNVOffset
	h = (h ^ tag) * postFNVPrime
	h = (h ^ payload) * postFNVPrime
	return h
}

// postBucket folds a path hash and a value hash into the containment bucket key.
func postBucket(pathHash uint32, valueHash uint64) uint64 {
	h := postFNVOffset
	h = (h ^ uint64(pathHash)) * postFNVPrime
	h = (h ^ valueHash) * postFNVPrime
	return h
}

// postFNV is FNV-1a over a byte span, the string-content payload hash.
func postFNV(b []byte) uint64 {
	h := postFNVOffset
	for _, c := range b {
		h = (h ^ uint64(c)) * postFNVPrime
	}
	return h
}
