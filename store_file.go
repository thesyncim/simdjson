package simdjson

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"

	"github.com/thesyncim/simdjson/internal/storeio"
)

var (
	// ErrFileStoreClosed reports use after FileStore.Close has started.
	ErrFileStoreClosed = errors.New("simdjson: FileStore is closed")
	// ErrFileStoreNotEmpty requires CreateFileStore to receive an empty file.
	ErrFileStoreNotEmpty = errors.New("simdjson: FileStore create requires an empty file")
	// ErrFileStoreKeyTooLarge reports a key beyond the configured durable page
	// bound.
	ErrFileStoreKeyTooLarge = errors.New("simdjson: FileStore key exceeds configured bound")
	// ErrFileStoreDocumentTooLarge reports a JSON value beyond the configured
	// transaction bound.
	ErrFileStoreDocumentTooLarge = errors.New("simdjson: FileStore document exceeds configured bound")
)

// FileStoreBackend selects the durable page-I/O implementation.
type FileStoreBackend uint8

const (
	FileStoreBackendAuto FileStoreBackend = iota
	FileStoreBackendPortable
	FileStoreBackendIOUring
)

// FileStoreOptions fixes every resident and in-flight memory bound. The zero
// value selects 4 KiB metadata pages, 64 KiB document/overflow extents, a
// 64 MiB read cache, and 4 MiB maximum documents.
type FileStoreOptions struct {
	Store StoreOptions

	PageSize          int
	MaxPageSize       int
	ResidentBytes     int64
	ReadConcurrency   int
	PrefetchQueue     int
	MaxKeyBytes       int
	InlineValueBytes  int
	MaxDocumentBytes  int
	BufferCount       int
	QueueSlots        int
	GroupLimit        int
	Backend           FileStoreBackend
	Synchronous       bool
	MaxSnapshotLeases int
	MaxRetiredExtents int
}

type normalizedFileStoreOptions struct {
	FileStoreOptions
	maxTransactionPages int
}

func (o FileStoreOptions) normalized() (normalizedFileStoreOptions, error) {
	storeOptions, err := o.Store.normalized()
	if err != nil {
		return normalizedFileStoreOptions{}, err
	}
	o.Store = storeOptions
	if o.PageSize == 0 {
		o.PageSize = 4096
	}
	if o.MaxPageSize == 0 {
		o.MaxPageSize = 64 << 10
	}
	if o.ResidentBytes == 0 {
		o.ResidentBytes = 64 << 20
	}
	if o.MaxKeyBytes == 0 {
		o.MaxKeyBytes = 256
	}
	if o.InlineValueBytes == 0 {
		o.InlineValueBytes = 512
	}
	if o.MaxDocumentBytes == 0 {
		o.MaxDocumentBytes = 4 << 20
	}
	if o.MaxSnapshotLeases == 0 {
		o.MaxSnapshotLeases = 1024
	}
	if o.MaxRetiredExtents == 0 {
		o.MaxRetiredExtents = 1 << 16
	}
	if o.Backend > FileStoreBackendIOUring || o.PageSize < 4096 || o.PageSize&(o.PageSize-1) != 0 ||
		o.MaxPageSize < o.PageSize || o.MaxPageSize&(o.MaxPageSize-1) != 0 || o.MaxPageSize%o.PageSize != 0 ||
		o.MaxKeyBytes < 1 || o.InlineValueBytes < 1 || o.MaxDocumentBytes < 1 ||
		o.InlineValueBytes > o.MaxDocumentBytes || uint64(o.MaxPageSize) > uint64(^uint32(0)) {
		return normalizedFileStoreOptions{}, fmt.Errorf("simdjson: invalid FileStore page, key, value, or backend option")
	}
	maxRowBytes := o.MaxKeyBytes + max(o.InlineValueBytes, storeio.DocumentOverflowDescriptorSize)
	worstDocumentPage := storeio.PageHeaderSize + storeio.PageTrailerSize + storeio.DocumentPagePayloadHeaderSize +
		o.Store.ChunkDocuments*storeio.DocumentPageRecordSize + o.Store.ChunkDocuments*maxRowBytes
	if worstDocumentPage > o.MaxPageSize {
		return normalizedFileStoreOptions{}, fmt.Errorf("simdjson: FileStore MaxPageSize cannot hold configured chunk/key/inline bounds")
	}
	overflowPayload := o.MaxPageSize - storeio.PageHeaderSize - storeio.PageTrailerSize - storeio.OverflowPagePayloadHeaderSize
	if overflowPayload <= 0 {
		return normalizedFileStoreOptions{}, fmt.Errorf("simdjson: FileStore overflow page has no payload")
	}
	overflowPages := (o.MaxDocumentBytes + overflowPayload - 1) / overflowPayload
	maxTransactionPages := overflowPages + 32
	if o.BufferCount == 0 {
		o.BufferCount = 1
		for o.BufferCount <= maxTransactionPages {
			o.BufferCount <<= 1
		}
	}
	if o.BufferCount <= maxTransactionPages || o.BufferCount > 32768 {
		return normalizedFileStoreOptions{}, fmt.Errorf("simdjson: FileStore BufferCount must exceed worst-case %d-page transaction", maxTransactionPages)
	}
	if o.ResidentBytes < int64(maxTransactionPages*o.MaxPageSize) {
		return normalizedFileStoreOptions{}, fmt.Errorf("simdjson: FileStore ResidentBytes cannot retain one worst-case dirty transaction")
	}
	return normalizedFileStoreOptions{FileStoreOptions: o, maxTransactionPages: maxTransactionPages}, nil
}

type fileStoreState struct {
	root      storeio.StateRoot
	super     storeio.Superblock
	stateRef  storeio.PageRef
	keyRoot   storeio.PageRef
	chunkRoot storeio.PageRef
}

// FileStore is a bounded-residency, page-oriented JSON document store. It owns
// no caller file lifetime: file must remain open through Close. Mutations are
// copy-on-write and automatically persisted through a checksummed double root.
// Reads use explicit FileSnapshot leases and caller-owned copy-out buffers.
type FileStore struct {
	file    *os.File
	options normalizedFileStoreOptions
	storeID [16]byte

	writer       sync.Mutex
	snapshotGate sync.RWMutex
	closed       bool
	closeDone    bool
	state        atomic.Pointer[fileStoreState]

	committer *storeio.Committer
	cache     *storeio.PageCache
	leases    *storeio.GenerationLeases
	reclaimer *storeio.ExtentReclaimer

	parseScratch []IndexEntry
	appendChunk  uint32
	appendLive   uint64
}

// CreateFileStore initializes an empty durable Store in file and fences its
// first root before returning.
func CreateFileStore(file *os.File, options FileStoreOptions) (*FileStore, error) {
	if file == nil {
		return nil, fmt.Errorf("simdjson: nil FileStore file")
	}
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() != 0 {
		return nil, ErrFileStoreNotEmpty
	}
	normalized, err := options.normalized()
	if err != nil {
		return nil, err
	}
	var storeID [16]byte
	if _, err := rand.Read(storeID[:]); err != nil {
		return nil, fmt.Errorf("simdjson: create FileStore identity: %w", err)
	}
	store, err := newFileStoreResources(file, normalized, storeID)
	if err != nil {
		return nil, err
	}
	if err := store.createInitialState(); err != nil {
		_ = store.closeResources()
		return nil, err
	}
	return store, nil
}

// OpenFileStore performs bounded recovery: it reads the two superblocks, the
// selected state root, and its top-level directory pages, then starts with an
// empty read cache. It does not scan keys, documents, postings, or TTL leaves.
func OpenFileStore(file *os.File, options FileStoreOptions) (*FileStore, error) {
	if file == nil {
		return nil, fmt.Errorf("simdjson: nil FileStore file")
	}
	normalized, err := options.normalized()
	if err != nil {
		return nil, err
	}
	scratch := make([]byte, normalized.PageSize)
	super, root, _, err := storeio.RecoverStateRoot(file, uint32(normalized.PageSize), scratch)
	if err != nil {
		return nil, err
	}
	if root.ChunkDocuments != uint32(normalized.Store.ChunkDocuments) || root.IndexCount != 0 || root.TTLCount != 0 {
		return nil, fmt.Errorf("simdjson: FileStore options or unsupported durable catalog mismatch")
	}
	store, err := newFileStoreResources(file, normalized, root.StoreID)
	if err != nil {
		return nil, err
	}
	if err := store.committer.InitializeGeneration(root.Generation); err != nil {
		_ = store.closeResources()
		return nil, err
	}
	stateRef := storeio.PageRef{
		Offset: super.StateOffset, LogicalID: storeio.StateRootLogicalID,
		Generation: root.Generation, Length: super.StateLength, Kind: storeio.PageStateRoot,
	}
	state := &fileStoreState{
		root: root, super: super, stateRef: stateRef,
		keyRoot: root.KeyDirectory, chunkRoot: root.ChunkDirectory,
	}
	store.state.Store(state)
	store.appendChunk = root.ChunkHighWater
	if err := store.restoreAppendChunk(state); err != nil {
		_ = store.closeResources()
		return nil, err
	}
	return store, nil
}

func newFileStoreResources(file *os.File, options normalizedFileStoreOptions, storeID [16]byte) (*FileStore, error) {
	committer, err := storeio.NewCommitter(file, storeio.DeviceOptions{
		Backend: storeio.Backend(options.Backend), BufferCount: options.BufferCount,
		BufferSize: options.MaxPageSize, QueueDepth: options.BufferCount,
	}, storeio.CommitterOptions{
		QueueSlots: options.QueueSlots, MaxPagesPerBatch: options.maxTransactionPages,
		GroupLimit: options.GroupLimit,
	})
	if err != nil {
		return nil, err
	}
	cache, err := storeio.NewPageCache(file, storeio.PageCacheOptions{
		PageSize: options.PageSize, MaxPageSize: options.MaxPageSize,
		ResidentBytes: options.ResidentBytes, StoreID: storeID,
		PrefetchQueue: options.PrefetchQueue, ReadConcurrency: options.ReadConcurrency,
	})
	if err != nil {
		_ = committer.Close()
		return nil, err
	}
	leases, err := storeio.NewGenerationLeases(storeio.GenerationLeaseOptions{MaxLeases: options.MaxSnapshotLeases})
	if err != nil {
		_ = cache.Close()
		_ = committer.Close()
		return nil, err
	}
	reclaimer, err := storeio.NewExtentReclaimer(leases, storeio.ExtentReclaimerOptions{MaxRetiredExtents: options.MaxRetiredExtents})
	if err != nil {
		_ = leases.Close()
		_ = cache.Close()
		_ = committer.Close()
		return nil, err
	}
	return &FileStore{
		file: file, options: options, storeID: storeID, committer: committer, cache: cache,
		leases: leases, reclaimer: reclaimer,
	}, nil
}

func (s *FileStore) createInitialState() error {
	tx, err := storeio.BeginWriteTransaction(s.committer, s.cache, 1, storeio.WriteTransactionOptions{
		StoreID: s.cacheStoreID(), Generation: 1, PageSize: uint32(s.options.PageSize),
		FileEnd: 2 * uint64(s.options.PageSize), NextLogicalID: 2,
	})
	if err != nil {
		return err
	}
	statePage, err := tx.Allocate(storeio.PageStateRoot, uint32(s.options.PageSize), storeio.StateRootLogicalID)
	if err != nil {
		_ = tx.Abort()
		return err
	}
	root := storeio.StateRoot{
		StoreID: s.cacheStoreID(), Generation: 1, PageSize: uint32(s.options.PageSize),
		NextLogicalID: tx.NextLogicalID(), ChunkDocuments: uint32(s.options.Store.ChunkDocuments),
	}
	if _, err := storeio.EncodeStateRootPage(statePage.Bytes(), root, tx.FileEnd()); err != nil {
		_ = tx.Abort()
		return err
	}
	if err := statePage.Stage(); err != nil {
		_ = tx.Abort()
		return err
	}
	if err := tx.Publish(statePage.Ref(), storeio.PageChecksum(statePage.Bytes()), 0, 0, 0); err != nil {
		_ = tx.Abort()
		return err
	}
	if err := s.committer.Wait(1); err != nil {
		return err
	}
	s.cache.MarkDurable(1)
	super := storeio.Superblock{
		StoreID: root.StoreID, Generation: 1, StateOffset: statePage.Ref().Offset,
		StateLength: statePage.Ref().Length, StateChecksum: storeio.PageChecksum(statePage.Bytes()),
		FileEnd: tx.FileEnd(), PageSize: uint32(s.options.PageSize),
	}
	s.state.Store(&fileStoreState{root: root, super: super, stateRef: statePage.Ref()})
	return nil
}

func (s *FileStore) cacheStoreID() [16]byte {
	return s.storeID
}

// FileSnapshot pins one immutable durable root generation. Close must be
// called; copy-out methods remain valid independently of page eviction.
type FileSnapshot struct {
	store *FileStore
	state *fileStoreState
	lease storeio.GenerationLease
	once  sync.Once
}

// Snapshot acquires an explicit generation lease.
func (s *FileStore) Snapshot() (*FileSnapshot, error) {
	if s == nil {
		return nil, ErrFileStoreClosed
	}
	s.snapshotGate.RLock()
	state := s.state.Load()
	if state == nil {
		s.snapshotGate.RUnlock()
		return nil, ErrFileStoreClosed
	}
	lease, err := s.leases.Acquire(state.root.Generation)
	s.snapshotGate.RUnlock()
	if err != nil {
		return nil, err
	}
	return &FileSnapshot{store: s, state: state, lease: lease}, nil
}

// Close releases the snapshot generation. It is idempotent.
func (s *FileSnapshot) Close() error {
	if s == nil {
		return nil
	}
	s.once.Do(func() {
		s.lease.Release()
		s.store = nil
		s.state = nil
	})
	return nil
}

// Len returns the number of keys visible to the snapshot.
func (s *FileSnapshot) Len() uint64 {
	if s == nil || s.state == nil {
		return 0
	}
	return s.state.root.DocumentCount
}

// Generation returns the pinned durable publication generation.
func (s *FileSnapshot) Generation() uint64 {
	if s == nil || s.state == nil {
		return 0
	}
	return s.state.root.Generation
}

// AppendRaw appends key's exact JSON spelling into dst. It never returns a
// borrowed page slice.
func (s *FileSnapshot) AppendRaw(dst []byte, key string) ([]byte, bool, error) {
	if s == nil || s.store == nil || s.state == nil {
		return dst, false, ErrFileStoreClosed
	}
	state := s.state
	bounds := storeio.KeyTreeBounds{
		FileEnd: state.super.FileEnd, NextLogicalID: state.root.NextLogicalID,
		ChunkHighWater: state.root.ChunkHighWater,
		ChunkDocuments: uint8(state.root.ChunkDocuments),
	}
	location, ok, err := storeio.LookupKeyTree(s.store.cache, state.keyRoot, []byte(key), bounds)
	if err != nil || !ok {
		return dst, false, err
	}
	documentRef, ok, err := storeio.LookupChunkTree(s.store.cache, state.chunkRoot, location.Chunk, storeio.ChunkTreeBounds{
		FileEnd: state.super.FileEnd, NextLogicalID: state.root.NextLogicalID,
	})
	if err != nil || !ok {
		return dst, false, err
	}
	lease, err := s.store.cache.Acquire(documentRef)
	if err != nil {
		return dst, false, err
	}
	view, err := storeio.OpenDocumentPageWithOverflow(
		lease.Page(), state.root.ChunkHighWater, state.root.NextLogicalID,
		state.super.FileEnd, state.root.PageSize,
	)
	if err != nil {
		lease.Release()
		return dst, false, err
	}
	value, ok := view.LookupStringValue(location.Slot, key)
	if !ok {
		lease.Release()
		return dst, false, nil
	}
	if value.Overflow == (storeio.PageRef{}) {
		dst = append(dst, value.Inline...)
		lease.Release()
		return dst, true, nil
	}
	lease.Release()
	dst, err = s.appendOverflow(dst, value, location)
	return dst, err == nil, err
}

func (s *FileSnapshot) appendOverflow(dst []byte, value storeio.DocumentValue, location storeio.KeyLocation) ([]byte, error) {
	ref := value.Overflow
	offset := uint64(0)
	for ref != (storeio.PageRef{}) {
		lease, err := s.store.cache.Acquire(ref)
		if err != nil {
			return dst, err
		}
		view, err := storeio.OpenOverflowPage(
			lease.Page(), s.state.super.FileEnd, s.state.root.NextLogicalID,
			s.state.root.PageSize, s.state.root.ChunkHighWater, uint8(s.state.root.ChunkDocuments),
		)
		if err != nil {
			lease.Release()
			return dst, err
		}
		header := view.Header()
		if header.Chunk != location.Chunk || header.Slot != location.Slot ||
			header.Total != value.Length || header.Offset != offset {
			lease.Release()
			return dst, storeio.ErrOverflowPageCorrupt
		}
		dst = append(dst, view.Data()...)
		offset += uint64(len(view.Data()))
		next := header.Next
		lease.Release()
		if next != (storeio.PageRef{}) {
			_, _ = s.store.cache.Prefetch([]storeio.PageRef{next})
		}
		ref = next
	}
	if offset != value.Length {
		return dst, storeio.ErrOverflowPageCorrupt
	}
	return dst, nil
}

// AppendRaw is the current-snapshot convenience form.
func (s *FileStore) AppendRaw(dst []byte, key string) ([]byte, bool, error) {
	snapshot, err := s.Snapshot()
	if err != nil {
		return dst, false, err
	}
	defer snapshot.Close()
	return snapshot.AppendRaw(dst, key)
}

// Len returns the current durable-state key count.
func (s *FileStore) Len() uint64 {
	if s == nil || s.state.Load() == nil {
		return 0
	}
	return s.state.Load().root.DocumentCount
}

// Generation returns the current reader-visible generation.
func (s *FileStore) Generation() uint64 {
	if s == nil || s.state.Load() == nil {
		return 0
	}
	return s.state.Load().root.Generation
}

// DurableGeneration returns the newest crash-safe generation.
func (s *FileStore) DurableGeneration() uint64 {
	if s == nil || s.committer == nil {
		return 0
	}
	return s.committer.DurableGeneration()
}

func (s *FileStore) restoreAppendChunk(state *fileStoreState) error {
	if state.root.ChunkHighWater == 0 || state.chunkRoot == (storeio.PageRef{}) {
		return nil
	}
	last := state.root.ChunkHighWater - 1
	ref, ok, err := storeio.LookupChunkTree(s.cache, state.chunkRoot, last, storeio.ChunkTreeBounds{
		FileEnd: state.super.FileEnd, NextLogicalID: state.root.NextLogicalID,
	})
	if err != nil || !ok {
		return err
	}
	lease, err := s.cache.Acquire(ref)
	if err != nil {
		return err
	}
	view, err := storeio.OpenDocumentPageWithOverflow(
		lease.Page(), state.root.ChunkHighWater, state.root.NextLogicalID,
		state.super.FileEnd, state.root.PageSize,
	)
	lease.Release()
	if err != nil {
		return err
	}
	limit := ^uint64(0)
	if state.root.ChunkDocuments < 64 {
		limit = uint64(1)<<state.root.ChunkDocuments - 1
	}
	if view.Header().Live != limit {
		s.appendChunk = last
		s.appendLive = view.Header().Live
	}
	return nil
}

// Flush waits until the current reader-visible generation is crash-safe.
func (s *FileStore) Flush() error {
	if s == nil || s.committer == nil {
		return ErrFileStoreClosed
	}
	generation := s.Generation()
	if err := s.committer.Wait(generation); err != nil {
		return err
	}
	s.cache.MarkDurable(generation)
	return nil
}

// Close fences every publication and releases bounded I/O resources. It does
// not close the caller-owned file. Active snapshots must be closed first.
func (s *FileStore) Close() error {
	if s == nil {
		return nil
	}
	s.writer.Lock()
	if s.closeDone {
		s.writer.Unlock()
		return nil
	}
	s.closed = true
	s.writer.Unlock()
	if err := s.leases.Close(); err != nil {
		return err
	}
	if err := s.closeResources(); err != nil {
		return err
	}
	s.writer.Lock()
	s.closeDone = true
	s.writer.Unlock()
	return nil
}

func (s *FileStore) closeResources() error {
	if s.committer != nil {
		if err := s.committer.Close(); err != nil {
			return err
		}
		s.cache.MarkDurable(s.committer.DurableGeneration())
	}
	if s.cache != nil {
		if err := s.cache.Close(); err != nil {
			return err
		}
	}
	return nil
}
