package simdjson

import (
	"errors"
	"fmt"
	"hash/maphash"
	"strings"
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
	options StoreOptions
	seed    maphash.Seed
	keys    *storeKeyNode
	chunks  storeChunkVector
	current *storeChunk
	count   int
	closed  bool
	exact   map[string]*storeExactIndex
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
	hash := maphash.String(b.seed, key)
	if _, exists := storeKeyLookup(b.keys, hash, key); exists {
		return fmt.Errorf("%w %q", ErrStoreDuplicateKey, key)
	}
	if b.current == nil {
		if b.chunks.count == ^uint32(0) {
			return ErrStoreTooLarge
		}
		b.current = newStoreBuilderChunk(b.options)
	}

	// DocSet.Append owns and validates src before any key or directory state is
	// committed. Its rollback contract leaves the page unchanged on error.
	ord, err := b.current.docs.Append(src)
	if err != nil {
		return err
	}
	storedKey := strings.Clone(key)
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

func newStoreBuilderChunk(options StoreOptions) *storeChunk {
	chunk := &storeChunk{
		keys: make([]string, options.ChunkDocuments),
		ord:  make([]uint8, options.ChunkDocuments),
	}
	initChunkDocSet(&chunk.docs, options, options.Postings)
	return chunk
}

func (b *StoreBuilder) flush() {
	b.chunks.appendTransient(b.current)
	b.current = nil
}

// Build freezes the accumulated graph and transfers it to a new Store. Empty
// input produces an initialized empty Store. The builder closes even when it
// is empty, preventing accidental aliasing through later Append calls.
func (b *StoreBuilder) Build() (*Store, error) {
	if b == nil || b.closed {
		return nil, ErrStoreBuilderClosed
	}
	if b.current != nil {
		b.flush()
	}
	b.closed = true

	state := &storeState{
		count:      b.count,
		chunkCount: b.chunks.count,
		seed:       b.seed,
		options:    b.options,
		keys:       b.keys,
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
	b.buildExactIndexes(store, state)
	store.state.Store(state)
	return store, nil
}

// buildExactIndexes constructs complete roots while store and state are still
// unreachable by readers. storeIndexCollectChunk coalesces equal tuples inside
// each page; radix traversal supplies ascending chunk ids, so every posting's
// masks are already in the order required by the one-allocation bulk builders.
func (b *StoreBuilder) buildExactIndexes(store *Store, state *storeState) {
	if len(b.exact) == 0 {
		return
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
		store.indexes[name] = &storeIndexBuild{
			info:  info,
			exact: exact,
			root:  storeIndexBuildBulk(pending),
			all:   true,
		}
	}
	state.indexes = store.indexInfosLocked()
	state.secondary = store.indexSnapshotsLocked()
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
