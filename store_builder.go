package simdjson

import (
	"errors"
	"fmt"
	"hash/maphash"
	"math/bits"
	"strings"

	"github.com/thesyncim/simdjson/internal/byteview"
)

var (
	// ErrStoreDuplicateKey reports that StoreBuilder.Append received a key it
	// already owns. Bulk construction requires unique keys so every document is
	// written exactly once into its final micro-page.
	ErrStoreDuplicateKey = errors.New("simdjson: duplicate StoreBuilder key")
	// ErrStoreBuilderClosed reports use after Build transferred the builder's
	// immutable graph into a Store.
	ErrStoreBuilderClosed = errors.New("simdjson: StoreBuilder is closed")
)

// StoreBuilder constructs a keyed Store without publishing and path-copying
// persistent metadata for every input row. It is the bulk-load complement to
// Store.Put: Append validates and copies one unique key/document into its final
// bounded micro-page, mutating only builder-owned key and chunk radix nodes;
// Build freezes that graph and publishes it once.
//
// A builder belongs to one goroutine. Append errors leave all previously
// appended rows intact and do not consume a key or slot. Build may be called
// once; the returned Store has ordinary snapshot, mutation, TTL, and index
// semantics. CreateIndex can include ready nested or compound indexes in the
// same publication. StoreBuilder is intentionally not an update API: online
// changes belong to Store.Put.
type StoreBuilder struct {
	options  StoreOptions
	seed     maphash.Seed
	keys     *storeKeyNode
	chunks   storeChunkVector
	current  *storeChunk
	count    int
	keyBytes int
	closed   bool
	exact    map[string]*storeExactIndex
	shapes   []*shapeRecord
	shapeSet map[*shapeRecord]struct{}
	// sourceHint is the exact JSON bytes in the preceding full chunk. It lets
	// the next unpublished chunk reserve one bounded arena instead of retaining
	// every geometric growth generation through already-built Index slices.
	sourceHint      int
	currentDocBytes int
}

// NewStoreBuilder returns an empty bulk builder. It validates StoreOptions up
// front so Append cannot discover a configuration error after consuming rows.
func NewStoreBuilder(options StoreOptions) (*StoreBuilder, error) {
	normalized, err := options.normalized()
	if err != nil {
		return nil, err
	}
	return &StoreBuilder{options: normalized, seed: maphash.MakeSeed()}, nil
}

// Len returns the number of documents successfully appended so far.
func (b *StoreBuilder) Len() int {
	if b == nil {
		return 0
	}
	return b.count
}

// CreateIndex declares a single-column or compound exact index to build inside
// the unpublished transaction. Paths have the same nested RFC 6901 semantics
// as [Store.CreateIndex]. Build returns the index Ready: it extracts and sorts
// page-local tuples, constructs immutable stable-slot postings in bulk, and
// publishes the documents, key directory, and index roots together.
//
// A declaration may be added before or after Append calls. Invalid or duplicate
// declarations leave the builder and all appended rows unchanged.
func (b *StoreBuilder) CreateIndex(def StoreIndexDefinition) error {
	if b == nil || b.closed {
		return ErrStoreBuilderClosed
	}
	exact, err := compileStoreExactIndex(def)
	if err != nil {
		return err
	}
	if b.exact != nil {
		if _, exists := b.exact[def.Name]; exists {
			return ErrStoreIndexExists
		}
	} else {
		b.exact = make(map[string]*storeExactIndex)
	}
	name := strings.Clone(def.Name)
	exact.seed = b.seed
	b.exact[name] = exact
	return nil
}

// Append validates and copies one unique keyed document. The caller may reuse
// key and src after return. A duplicate key, invalid JSON, closed builder, or
// exhausted chunk address space changes no committed row.
func (b *StoreBuilder) Append(key string, src []byte) error {
	if b == nil || b.closed {
		return ErrStoreBuilderClosed
	}
	if uint64(len(key)) > uint64(^uint32(0)) || len(key) > maxInt()-b.keyBytes {
		return ErrStorePersistTooLarge
	}
	hash := maphash.String(b.seed, key)
	if _, exists := storeKeyLookup(b.keys, hash, key); exists {
		return fmt.Errorf("%w %q", ErrStoreDuplicateKey, key)
	}
	if b.current == nil {
		if b.chunks.count == ^uint32(0) {
			return ErrStoreTooLarge
		}
		capacity := storeBuilderSourceCapacity(b.options.ChunkDocuments, len(src), b.sourceHint)
		b.current = newStoreBuilderChunk(b.options, b.shapes, capacity)
	}

	// DocSet.Append owns and validates src before any key or directory state is
	// committed. Its rollback contract leaves the page unchanged on error.
	ord, err := b.current.docs.Append(src)
	if err != nil {
		return err
	}
	b.currentDocBytes += len(src)
	keyStart := len(b.current.keyBytes)
	b.current.keyBytes = append(b.current.keyBytes, key...)
	storedKey := byteview.String(b.current.keyBytes[keyStart:])
	b.keyBytes += len(key)
	slot := int(b.current.count)
	b.current.keys[slot] = storedKey
	b.current.ord[slot] = uint8(ord)
	b.current.live |= uint64(1) << uint(slot)
	b.current.count++
	b.count++
	loc := storeLocation{chunk: b.chunks.count, slot: uint8(slot)}
	storeKeyInsertTransient(&b.keys, 0, &storeKeyLeaf{hash: hash, key: storedKey, loc: loc})

	if int(b.current.count) == b.options.ChunkDocuments {
		b.flush()
	}
	return nil
}

func newStoreBuilderChunk(options StoreOptions, shapes []*shapeRecord, sourceCapacity int) *storeChunk {
	chunk := &storeChunk{
		keys: make([]string, options.ChunkDocuments),
	}
	initChunkDocSet(&chunk.docs, options, options.Postings)
	if sourceCapacity > 0 {
		chunk.docs.srcChunk = make([]byte, 0, sourceCapacity)
	}
	for _, rec := range shapes {
		chunk.docs.shapes.seedRecord(rec)
	}
	return chunk
}

func storeBuilderSourceCapacity(chunkDocuments, firstDocumentBytes, previousBytes int) int {
	if chunkDocuments <= 0 || firstDocumentBytes <= 0 {
		return 0
	}
	sample := docSetMaxSrcChunk
	if firstDocumentBytes <= docSetMaxSrcChunk/chunkDocuments {
		sample = firstDocumentBytes * chunkDocuments
	}
	if previousBytes <= 0 {
		return storeBuilderSourceHeadroom(sample, chunkDocuments)
	}
	previousBytes = min(previousBytes, docSetMaxSrcChunk)
	average := (previousBytes + chunkDocuments - 1) / chunkDocuments
	// Reuse the exact preceding-page size only while the new first row is a
	// plausible member of the same size distribution. A phase change switches
	// immediately to the current sample instead of pinning stale capacity.
	if firstDocumentBytes >= max(average/2, 1) && firstDocumentBytes <= average*2 {
		return storeBuilderSourceHeadroom(previousBytes, chunkDocuments)
	}
	return storeBuilderSourceHeadroom(sample, chunkDocuments)
}

func storeBuilderSourceHeadroom(size, chunkDocuments int) int {
	if size >= docSetMaxSrcChunk {
		return docSetMaxSrcChunk
	}
	if chunkDocuments <= 1 {
		return size
	}
	// One average row absorbs ordinary page-to-page variance without the
	// 2x retained cost of crossing an arena boundary by a few bytes.
	headroom := max(size/chunkDocuments, 256)
	if size > docSetMaxSrcChunk-headroom {
		return docSetMaxSrcChunk
	}
	return size + headroom
}

func (b *StoreBuilder) flush() {
	if b.options.ShapeTapes {
		compactStoreBuilderShapes(&b.current.docs)
		if b.shapeSet == nil {
			b.shapeSet = make(map[*shapeRecord]struct{})
		}
		for _, rec := range b.current.docs.shapes.shapes {
			if _, exists := b.shapeSet[rec]; exists {
				continue
			}
			b.shapeSet[rec] = struct{}{}
			b.shapes = append(b.shapes, rec)
		}
	}
	b.chunks.appendTransient(b.current)
	if int(b.current.count) == b.options.ChunkDocuments {
		b.sourceHint = b.currentDocBytes
	}
	b.currentDocBytes = 0
	b.current = nil
}

// compactStoreBuilderShapes revisits the first sighting of every shape after
// its page-local repeat has compiled the immutable record. Ordinary DocSet
// append cannot rewrite an already returned Index, but an unpublished builder
// owns every row and can safely drop those redundant classic key tapes before
// publication. Existing postings and value dictionaries are independent and
// remain exact because the document bytes do not change.
func compactStoreBuilderShapes(docs *DocSet) {
	if docs == nil || len(docs.shapes.shapes) == 0 || len(docs.tapeRefs) == 0 {
		return
	}
	allCompact := true
	for i := range docs.docs {
		if docs.shapeTapeRefAt(i).rec != nil {
			continue
		}
		index, ref, ok := copyStoreShapeTape(docs, docs.docs[i])
		if !ok {
			allCompact = false
			continue
		}
		docs.docs[i] = index
		docs.tapeRefs[i] = ref
	}
	if allCompact {
		docs.entryChunk = nil
		docs.scratch = nil
	}
}

// Build freezes the accumulated graph, compacts immutable keys and document
// source/tapes into pointer-free owned blocks, and transfers them to a new
// Store. Empty input produces an initialized empty Store. The builder closes
// even when it is empty, preventing accidental aliasing through later Append
// calls.
func (b *StoreBuilder) Build() (*Store, error) {
	if b == nil || b.closed {
		return nil, ErrStoreBuilderClosed
	}
	if b.current != nil {
		b.flush()
	}
	baseKeys, err := b.compactBaseKeys()
	if err != nil {
		return nil, err
	}
	b.closed = true

	state := &storeState{
		count:      b.count,
		chunkCount: b.chunks.count,
		seed:       b.seed,
		options:    b.options,
		baseKeys:   baseKeys,
		chunks:     b.chunks,
	}
	if b.count != 0 || len(b.exact) != 0 {
		state.generation = 1
	}
	store := &Store{Options: b.options, options: b.options}
	store.free.pos = make(map[uint32]int)
	store.postingChunks.pos = make(map[uint32]int)
	for id := uint32(0); id < b.chunks.count; id++ {
		chunk := b.chunks.get(id)
		if chunk != nil && int(chunk.count) < b.options.ChunkDocuments {
			store.free.add(id)
		}
		if chunk != nil && chunk.docs.Postings {
			store.postingChunks.add(id)
		}
	}
	if err := b.buildExactIndexes(store, state); err != nil {
		return nil, err
	}
	if err := b.compactDocuments(state); err != nil {
		return nil, err
	}
	store.state.Store(state)
	b.keys = nil
	b.chunks = storeChunkVector{}
	b.current = nil
	b.exact = nil
	b.shapes = nil
	b.shapeSet = nil
	b.sourceHint = 0
	b.currentDocBytes = 0
	return store, nil
}

// compactBaseKeys replaces the builder-only HAMT and its leaf objects with one
// immutable Swiss-style table plus packed key bytes. On common Unix systems
// both regions are outside the Go heap. The published Store therefore retains
// neither a string allocation nor a directory leaf per input row.
func (b *StoreBuilder) compactBaseKeys() (*storeMappedKeys, error) {
	if b.count == 0 {
		return nil, nil
	}
	base, err := newStoreOwnedKeys(b.count, b.keyBytes, b.chunks.count >= storeMappedLocationMaxChunk, b.options.ChunkDocuments)
	if err != nil {
		return nil, fmt.Errorf("simdjson: compact StoreBuilder keys: %w", err)
	}
	position := 0
	refBase := uint64(0)
	valid := true
	b.chunks.each(func(id uint32, chunk *storeChunk) bool {
		for live := chunk.live; live != 0; live &= live - 1 {
			slot := bits.TrailingZeros64(live)
			key := chunk.keys[slot]
			start := position
			position += copy(base.source[position:], key)
			ref := refBase + uint64(chunk.ord[slot])
			if ref >= uint64(base.keyRefCount()) {
				valid = false
				return false
			}
			base.setKeySpan(ref, uint64(start), uint32(len(key)))
			base.setLocation(ref, storeLocation{chunk: id, slot: uint8(slot)})
			if !base.insert(maphash.String(b.seed, key), ref) {
				valid = false
				return false
			}
		}
		refBase += uint64(chunk.count)
		return true
	})
	if !valid || position != len(base.source) || refBase != uint64(b.count) {
		base.release()
		return nil, errors.New("simdjson: StoreBuilder compact key invariant")
	}
	refBase = 0
	b.chunks.each(func(_ uint32, chunk *storeChunk) bool {
		chunk.keys = nil
		chunk.keyBytes = nil
		chunk.mappedKeys = base
		chunk.mappedBase = refBase
		refBase += uint64(chunk.count)
		return true
	})
	return base, nil
}

// buildExactIndexes constructs complete roots while store and state are still
// unreachable by readers. storeIndexCollectChunk coalesces equal tuples inside
// each page; radix traversal supplies ascending chunk ids, so every posting's
// masks are already in the order required by the packed-page builder.
func (b *StoreBuilder) buildExactIndexes(store *Store, state *storeState) error {
	if len(b.exact) == 0 {
		return nil
	}
	if store.indexes == nil {
		store.indexes = make(map[string]*storeIndexBuild, len(b.exact))
	}
	for name, exact := range b.exact {
		pending := make(map[uint64][]storeIndexChunkMask)
		state.chunks.each(func(id uint32, chunk *storeChunk) bool {
			var storage [storeMaxChunkDocuments]storeIndexHashMask
			for _, entry := range storeIndexCollectChunk(storage[:0], exact, chunk) {
				pending[entry.hash] = append(pending[entry.hash], storeIndexChunkMask{
					chunk: id,
					mask:  entry.mask,
				})
			}
			return true
		})
		info := StoreIndexInfo{
			Name:          name,
			Kind:          StoreIndexExact,
			State:         StoreIndexReady,
			CoveredChunks: state.chunkCount,
			TotalChunks:   state.chunkCount,
			ColumnCount:   exact.n,
		}
		copy(info.Columns[:], exact.specs[:exact.n])
		base, err := newStorePackedIndex(pending)
		if err != nil {
			return fmt.Errorf("simdjson: build packed exact index %q: %w", name, err)
		}
		store.indexes[name] = &storeIndexBuild{
			info:  info,
			exact: exact,
			base:  base,
			all:   true,
		}
	}
	state.indexes = store.indexInfosLocked()
	state.secondary = store.indexSnapshotsLocked()
	return nil
}

// storeKeyInsertTransient is StoreBuilder's uniquely-owned HAMT insertion.
// It has the same terminal-bucket shape as storeKeyInsert but mutates nodes in
// place, avoiding O(depth) immutable copies before any snapshot can exist.
func storeKeyInsertTransient(root **storeKeyNode, shift uint, add *storeKeyLeaf) {
	if *root == nil {
		*root = &storeKeyNode{}
	}
	slot := &(*root).slots[(add.hash>>shift)&31]
	if slot.child != nil {
		storeKeyInsertTransient(&slot.child, shift+storeTrieBits, add)
		return
	}
	if slot.leaf == nil {
		slot.leaf = add
		return
	}
	if storeKeyLeafHasHash(slot.leaf, add.hash) ||
		shift >= storeKeyBucketShift && storeKeyLeafCount(slot.leaf) < storeKeyLeafBucket {
		add.next = slot.leaf
		slot.leaf = add
		return
	}

	leaves := slot.leaf
	slot.leaf = nil
	for leaves != nil {
		next := leaves.next
		leaves.next = nil
		storeKeyInsertTransient(&slot.child, shift+storeTrieBits, leaves)
		leaves = next
	}
	storeKeyInsertTransient(&slot.child, shift+storeTrieBits, add)
}
