package simdjson

import "github.com/thesyncim/simdjson/document"

// JSONB-compatible containment.
//
// This file evaluates PostgreSQL's jsonb containment operator (@>) over
// indexed documents: Node.Contains is the primitive, running directly on
// two tapes, and RawContains is the one-shot spelling that indexes its
// operands first. The oracle is PostgreSQL's documented behavior — the
// curated table in testdata/contains_oracle.tsv is verified against a real
// server by benchmarks/pgbaseline/run-pg-contains.sh — and ADR 0002's
// phase 4 prunes containment candidates with postings that this evaluator
// then verifies.
//
// The evaluation is one structural recursion with no allocation on clean
// input. Object members resolve through the Get lookup ladder, so an
// enriched haystack (document.IndexOptions.HashKeys) rejects non-matching
// members on one hash-word compare: the needle key is hashed once and
// gates every member of the probed object. Array containment pre-filters
// candidate elements by kind before recursing. Scalars compare through the
// package's exact kernels: strings by decoded content (tapeKeyEqual's
// incremental escape comparison), numbers by exact decimal value
// (jsonNumberEqual), never through float64 rounding.
//
// jsonb collapses duplicate object keys to the last occurrence when a
// document is converted, and the package's Get contract keeps the same
// rule, so both sides agree by construction. The evaluator handles the
// needle's own duplicates without a pre-pass: members are checked in
// order, and a member that fails is re-resolved through Get once to
// decide whether it was shadowed by a later duplicate — free when there
// are no duplicates, exact when there are.

// Contains reports whether needle is contained in v under PostgreSQL's
// documented jsonb containment (@>) semantics:
//
//   - An object contains an object when, for every member of the needle,
//     the haystack has a member with the same key whose value contains the
//     needle member's value, by this definition recursively. Extra
//     haystack members are ignored; the empty object is contained in
//     every object.
//   - An array contains an array when every needle element is contained
//     in some haystack element. Order is ignored and duplicates collapse:
//     one haystack element may satisfy any number of needle elements, so
//     [1] contains [1, 1] and the empty array is contained in every
//     array.
//   - A scalar contains exactly an equal scalar: null equals null,
//     booleans compare by value, strings compare by decoded content (an
//     escape spelling equals its decoded form), and numbers compare by
//     exact numeric value rather than spelling — 1.0 contains 1 and 1e2
//     contains 100 — with exact decimal precision at any magnitude, so
//     integers beyond float64 do not falsely collapse.
//   - Structure must otherwise match: an object never contains an array
//     or scalar needle, an array never contains an object needle, and a
//     scalar never contains a container. The one exception, at the top
//     level only, is PostgreSQL's documented special case: an array v
//     contains a scalar needle when some element of v equals it. The
//     exception does not nest, and it never applies in reverse.
//
// Duplicate keys on either side resolve to an object's last member with
// that key, matching both the package's Get contract and what jsonb's
// document conversion keeps; semantics of duplicate keys before that
// conversion are out of scope. An invalid Node contains nothing and is
// contained in nothing.
//
// The Nodes may come from different documents, or from the same one.
// Contains does not allocate except for needle strings or keys that
// contain escape sequences too long for a small stack buffer. The cost is
// one haystack lookup per needle object member and, for arrays, one scan
// of the haystack array per needle element.
func (v Node) Contains(needle Node) bool {
	if v.Kind() == document.Array {
		switch needle.Kind() {
		case document.Null, document.Bool, document.Number, document.String:
			// The top-level exception: a scalar matches an array haystack
			// when some element equals it. Only scalar elements can;
			// deeper structure never participates.
			it, _ := v.ArrayIter()
			for {
				element, ok := it.Next()
				if !ok {
					return false
				}
				if scalarNodesEqual(element, needle) {
					return true
				}
			}
		}
	}
	return nodeContains(v, needle)
}

// RawContains reports whether needle is contained in haystack under the
// containment contract documented at [Node.Contains]. Both arguments must
// each hold exactly one JSON document; an invalid document returns the
// error a failed [BuildIndex] reports. RawContains indexes both documents
// per call — callers evaluating one needle against many documents, or
// many needles against one document, should build the indexes once and
// use Node.Contains directly.
func RawContains(haystack, needle []byte) (bool, error) {
	h, err := containsIndex(haystack)
	if err != nil {
		return false, err
	}
	n, err := containsIndex(needle)
	if err != nil {
		return false, err
	}
	return h.Root().Contains(n.Root()), nil
}

// containsIndex validates one containment operand and builds its exactly
// sized index.
func containsIndex(src []byte) (Index, error) {
	entries, err := RequiredIndexEntries(src)
	if err != nil {
		return Index{}, err
	}
	return BuildIndex(src, make([]IndexEntry, entries))
}

// nodeContains is the structural recursion below the top level: kinds must
// match exactly, containers recurse, scalars compare by value.
func nodeContains(h, n Node) bool {
	switch n.Kind() {
	case document.Object:
		if h.Kind() != document.Object {
			return false
		}
		return objectContains(h, n)
	case document.Array:
		if h.Kind() != document.Array {
			return false
		}
		return arrayContains(h, n)
	case document.Invalid:
		return false
	default:
		return scalarNodesEqual(h, n)
	}
}

// objectContains reports whether every effective member of needle object n
// is matched in haystack object h. Members are checked in document order;
// h.Get supplies the last-duplicate rule on the haystack side and, when h
// is enriched, the hash-gated scan. A failing member is re-resolved
// through n.Get once so that a needle member shadowed by a later
// duplicate — whose value is not the effective one — cannot cause a false
// negative.
func objectContains(h, n Node) bool {
	it, _ := n.ObjectIter()
	for {
		key, value, ok := it.Next()
		if !ok {
			return true
		}
		k := containsKeyText(key)
		hv, ok := h.Get(k)
		if !ok {
			// The key is absent from the haystack. Every duplicate of a
			// key resolves the same lookup, so shadowing cannot save it.
			return false
		}
		if !nodeContains(hv, value) {
			if effective, _ := n.Get(k); effective.entry == value.entry {
				return false
			}
			// A later duplicate shadows this member; that occurrence
			// decides when the iteration reaches it.
		}
	}
}

// containsKeyText returns key's decoded text for a Get lookup. An
// unescaped key aliases the source; an escaped key decodes through a
// small stack buffer, allocating only when the decoded key outgrows it.
func containsKeyText(key Node) string {
	if content, ok := key.StringBytes(); ok {
		return ownedBytesString(content)
	}
	var buf [48]byte
	decoded, _ := key.AppendText(buf[:0])
	return ownedBytesString(decoded)
}

// arrayContains reports whether every element of needle array n is
// contained in some element of haystack array h. The haystack scan skips
// elements of a different kind before recursing: containment below the
// top level never crosses kinds.
func arrayContains(h, n Node) bool {
	nit, _ := n.ArrayIter()
	for {
		element, ok := nit.Next()
		if !ok {
			return true
		}
		kind := element.Kind()
		hit, _ := h.ArrayIter()
		for {
			candidate, ok := hit.Next()
			if !ok {
				return false
			}
			if candidate.Kind() == kind && nodeContains(candidate, element) {
				break
			}
		}
	}
}

// scalarNodesEqual reports whether two Nodes are equal scalars: same kind,
// same value. Containers and invalid Nodes report false; callers dispatch
// containers before value comparison.
func scalarNodesEqual(a, b Node) bool {
	kind := a.Kind()
	if kind != b.Kind() {
		return false
	}
	switch kind {
	case document.Null:
		return true
	case document.Bool:
		av, _ := a.Bool()
		bv, _ := b.Bool()
		return av == bv
	case document.Number:
		av, _ := a.NumberBytes()
		bv, _ := b.NumberBytes()
		return jsonNumberEqual(av, bv)
	case document.String:
		return stringNodesEqual(a, b)
	default:
		return false
	}
}

// stringNodesEqual compares two string Nodes by decoded content. Clean
// spellings compare bytes directly; an escaped side compares through
// tapeKeyEqual's incremental decoder, and only the escaped-versus-escaped
// case materializes one side, through a small stack buffer.
func stringNodesEqual(a, b Node) bool {
	ac, aClean := a.StringBytes()
	bc, bClean := b.StringBytes()
	switch {
	case aClean && bClean:
		return bytesEqualString(ac, ownedBytesString(bc))
	case aClean:
		return tapeKeyEqual(b.Raw().Bytes(), tapeFlagEscaped, ownedBytesString(ac))
	case bClean:
		return tapeKeyEqual(a.Raw().Bytes(), tapeFlagEscaped, ownedBytesString(bc))
	default:
		var buf [48]byte
		decoded, _ := a.AppendText(buf[:0])
		return tapeKeyEqual(b.Raw().Bytes(), tapeFlagEscaped, ownedBytesString(decoded))
	}
}
