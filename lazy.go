package simdjson

import "unsafe"

// LazyValue is an on-demand handle into a parsed document. Unlike Parse, which
// eagerly materializes an ordered Value tree, ParseLazy builds only the
// structural index and reads each value straight from that index as the caller
// navigates. A document that is read in part therefore never pays to
// materialize the whole.
//
// A LazyValue owns the structural index and the source it reads from, so it
// stays valid for as long as the handle is reachable, independent of the
// original src slice unless ZeroCopy was requested. Navigation
// (Get, Index, ArrayIter, ObjectIter) yields further LazyValues that share the
// same backing storage without allocating.
type LazyValue struct {
	// src and entries own the backing storage so the handle keeps it alive.
	// node aliases into both and does the actual per-value work.
	src     []byte
	entries []IndexEntry
	node    Node
}

// ParseLazy validates src and returns an on-demand root handle. It builds the
// structural index but does not materialize a Value tree: values are read from
// the index only when the caller asks for them.
func ParseLazy(src []byte) (LazyValue, error) {
	return ParseLazyOptions(src, Options{})
}

// ParseLazyOptions is ParseLazy with parser options. When opts.ZeroCopy is set
// the handle reads strings and numbers straight from src, which the caller must
// then keep unmodified for as long as the handle is used. Otherwise the handle
// copies src once so it is self-contained.
func ParseLazyOptions(src []byte, opts Options) (LazyValue, error) {
	maxDepth := opts.MaxDepth
	if maxDepth <= 0 {
		maxDepth = defaultMaxDepth
	}

	// The index needs one entry per structural token. Reuse a pooled estimate
	// buffer for the common case; grow (and keep) a private buffer when the
	// document is larger than the estimate.
	pooled := parseTapePool.Get().(*[]IndexEntry)
	storage := (*pooled)[:cap(*pooled)]

	estimate := len(src)/8 + 8
	var entries []IndexEntry
	grown := false
	for {
		if cap(storage) < estimate {
			storage = make([]IndexEntry, estimate)
			grown = true
		}
		index, err := BuildIndexOptions(src, storage[:cap(storage)], IndexOptions{MaxDepth: maxDepth})
		if err == ErrIndexFull {
			estimate = cap(storage) * 2
			continue
		}
		if err != nil {
			if !grown {
				*pooled = storage[:0]
			}
			parseTapePool.Put(pooled)
			return LazyValue{}, err
		}
		entries = index.entries
		break
	}

	if len(entries) == 0 {
		if !grown {
			*pooled = storage[:0]
		}
		parseTapePool.Put(pooled)
		return LazyValue{}, syntaxError(src, 0, "expected value")
	}

	// The handle must own its index storage so it outlives this call. When the
	// pooled buffer was large enough we copy the used entries out and return the
	// buffer to the pool; a grown buffer is already private and belongs to the
	// handle, so we trim it and do not recycle it.
	var owned []IndexEntry
	if grown {
		owned = storage[:len(entries):len(entries)]
	} else {
		owned = make([]IndexEntry, len(entries))
		copy(owned, entries)
		*pooled = storage[:0]
	}
	parseTapePool.Put(pooled)

	body := src
	if !opts.ZeroCopy {
		body = append([]byte(nil), src...)
	}

	root := Node{src: unsafe.SliceData(body), entry: unsafe.SliceData(owned)}
	return LazyValue{src: body, entries: owned, node: root}, nil
}

// with rebinds a navigated Node back into this handle's owning storage so the
// result keeps the index and source alive.
func (v LazyValue) with(node Node) LazyValue {
	return LazyValue{src: v.src, entries: v.entries, node: node}
}

// Node returns the underlying lightweight cursor. The cursor is valid only
// while v is reachable.
func (v LazyValue) Node() Node { return v.node }

// Kind returns the JSON kind of v.
func (v LazyValue) Kind() Kind { return v.node.Kind() }

// IsNull reports whether v is null.
func (v LazyValue) IsNull() bool { return v.node.IsNull() }

// Bool returns v as a boolean.
func (v LazyValue) Bool() (bool, bool) { return v.node.Bool() }

// Float64 parses a number value as float64.
func (v LazyValue) Float64() (float64, bool) { return v.node.Float64() }

// Int64 parses an integer number value as int64.
func (v LazyValue) Int64() (int64, bool) { return v.node.Int64() }

// NumberText returns the original JSON number spelling.
func (v LazyValue) NumberText() (string, bool) { return v.node.NumberText() }

// Text returns v's decoded string. Escaped strings decode into fresh storage;
// unescaped strings alias v's owned source.
func (v LazyValue) Text() (string, bool) {
	if v.node.Kind() != String {
		return "", false
	}
	if b, ok := v.node.StringBytes(); ok {
		return ownedBytesString(b), true
	}
	out, _ := v.node.AppendString(nil)
	return ownedBytesString(out), true
}

// Raw returns v's exact source range.
func (v LazyValue) Raw() RawValue { return v.node.Raw() }

// ArrayLen returns the number of array elements.
func (v LazyValue) ArrayLen() (int, bool) { return v.node.ArrayLen() }

// ObjectLen returns the number of object members.
func (v LazyValue) ObjectLen() (int, bool) { return v.node.ObjectLen() }

// Index returns the ith array element.
func (v LazyValue) Index(i int) (LazyValue, bool) {
	node, ok := v.node.Index(i)
	if !ok {
		return LazyValue{}, false
	}
	return v.with(node), true
}

// Get returns the last object member with key, matching encoding/json's
// last-occurrence semantics for duplicate keys.
func (v LazyValue) Get(key string) (LazyValue, bool) {
	node, ok := v.node.Get(key)
	if !ok {
		return LazyValue{}, false
	}
	return v.with(node), true
}

// Pointer resolves an RFC 6901 JSON Pointer relative to v.
func (v LazyValue) Pointer(pointer string) (LazyValue, bool, error) {
	node, ok, err := v.node.Pointer(pointer)
	if err != nil || !ok {
		return LazyValue{}, ok, err
	}
	return v.with(node), true, nil
}

// PointerCompiled resolves a precompiled JSON Pointer relative to v.
func (v LazyValue) PointerCompiled(pointer CompiledPointer) (LazyValue, bool, error) {
	node, ok, err := v.node.PointerCompiled(pointer)
	if err != nil || !ok {
		return LazyValue{}, ok, err
	}
	return v.with(node), true, nil
}

// ArrayIter returns an iterator over v's array elements. The iterator shares
// v's backing storage.
func (v LazyValue) ArrayIter() (ArrayIter, bool) { return v.node.ArrayIter() }

// ObjectIter returns an iterator over v's object members.
func (v LazyValue) ObjectIter() (ObjectIter, bool) { return v.node.ObjectIter() }

// Value materializes v (and everything under it) into an eager Value tree,
// bridging back to the ordered-DOM API when a caller needs the whole subtree.
func (v LazyValue) Value() Value {
	idx := v.nodeIndex()
	span := 1
	if k := v.entries[idx].kind; k == Array || k == Object {
		span = int(v.entries[idx].next)
	}
	b := astBuilder{src: v.src, entries: v.entries, zeroCopy: true}
	// Size the value and member arenas exactly as ParseOptions does, but only
	// over the subtree rooted at idx. Object member values live inside Member
	// entries, so the value arena holds only array elements.
	members := 0
	values := 0
	for i := idx; i < idx+span; i++ {
		if v.entries[i].flags&tapeFlagKey != 0 {
			members++
		} else {
			values++
		}
	}
	if arrayValues := values - members - 1; arrayValues > 0 {
		b.valueArena = make([]Value, arrayValues)
	}
	if members > 0 {
		b.memberArena = make([]Member, members)
	}
	root, _ := b.build(idx)
	return root
}

// nodeIndex recovers v's entry offset within the owned tape.
func (v LazyValue) nodeIndex() int {
	base := uintptr(unsafe.Pointer(unsafe.SliceData(v.entries)))
	cur := uintptr(unsafe.Pointer(v.node.entry))
	return int((cur - base) / unsafe.Sizeof(IndexEntry{}))
}
