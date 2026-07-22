package simdjson

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/bits"
	"os"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/thesyncim/simdjson/document"
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
	maxTransactionPages := overflowPages + 48
	if o.MaxRetiredExtents < maxTransactionPages {
		return normalizedFileStoreOptions{}, fmt.Errorf("simdjson: FileStore MaxRetiredExtents must retain one worst-case transaction")
	}
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
	freeRoot  storeio.PageRef
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

	parseScratch  []IndexEntry
	retireScratch []storeio.FreeExtent
	reusable      []storeio.FreeExtent
	reuseJournal  []storeio.ReuseEdit
	freeLoaded    bool
	unpersisted   int
	appendChunk   uint32
	appendLive    uint64
}

// FileStoreStats is a point-in-time resource and I/O accounting snapshot.
// Every byte and queue counter corresponds to a configured finite budget.
type FileStoreStats struct {
	CapacityBytes   uint64
	ResidentBytes   uint64
	PinnedPages     uint64
	DirtyBytes      uint64
	PageReads       uint64
	ReadBytes       uint64
	CacheHits       uint64
	PrefetchHits    uint64
	Evictions       uint64
	PrefetchQueued  uint64
	PrefetchDropped uint64
	ReadQueueDepth  uint64

	PublishedGeneration uint64
	DurableGeneration   uint64
	CommitQueueDepth    uint64
	DeviceCommits       uint64
	CommittedBatches    uint64
	LargestCommitGroup  uint32
	Backend             FileStoreBackend

	SnapshotCapacity         uint64
	ActiveSnapshots          uint64
	OldestSnapshotGeneration uint64
	RetiredExtentCapacity    uint64
	PendingRetiredExtents    uint64
	PendingRetiredBytes      uint64
	ReusableExtents          uint64
	ReusableBytes            uint64
	DocumentCount            uint64
	LiveChunks               uint32
	FileEnd                  uint64
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
	var freeRoot storeio.PageRef
	if super.FreeLength != 0 {
		page := scratch[:super.FreeLength]
		n, readErr := file.ReadAt(page, int64(super.FreeOffset))
		if readErr != nil || n != len(page) {
			_ = store.closeResources()
			if readErr != nil {
				return nil, readErr
			}
			return nil, storeio.ErrFreeDirectoryCorrupt
		}
		view, openErr := storeio.OpenFreeDirectoryPage(page, super.FileEnd, root.NextLogicalID)
		if openErr != nil {
			_ = store.closeResources()
			return nil, openErr
		}
		header := view.Header()
		freeRoot = storeio.PageRef{
			Offset: super.FreeOffset, LogicalID: header.LogicalID, Generation: header.Generation,
			Length: super.FreeLength, Kind: storeio.PageFreeDirectory,
		}
	}
	state := &fileStoreState{
		root: root, super: super, stateRef: stateRef,
		keyRoot: root.KeyDirectory, chunkRoot: root.ChunkDirectory, freeRoot: freeRoot,
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
		BufferSize: max(options.MaxPageSize, os.Getpagesize()), QueueDepth: options.BufferCount,
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
		retireScratch: make([]storeio.FreeExtent, 0, options.maxTransactionPages+32),
		reusable:      make([]storeio.FreeExtent, 0, options.MaxRetiredExtents),
		reuseJournal:  make([]storeio.ReuseEdit, 0, options.maxTransactionPages),
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
	s.freeLoaded = true
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

// PrefetchKeys resolves keys through the pinned directories and submits their
// document extents to the bounded asynchronous read queue in physical order.
// It returns the number submitted; missing keys are ignored and queue pressure
// is visible through FileStoreStats.PrefetchDropped.
func (s *FileSnapshot) PrefetchKeys(keys []string) (int, error) {
	if s == nil || s.store == nil || s.state == nil {
		return 0, ErrFileStoreClosed
	}
	var refs [64]storeio.PageRef
	count := 0
	queued := 0
	flush := func() error {
		if count == 0 {
			return nil
		}
		batch := refs[:count]
		slices.SortFunc(batch, func(a, b storeio.PageRef) int {
			if a.Offset < b.Offset {
				return -1
			}
			if a.Offset > b.Offset {
				return 1
			}
			return 0
		})
		unique := batch[:0]
		for _, ref := range batch {
			if len(unique) == 0 || unique[len(unique)-1].Offset != ref.Offset {
				unique = append(unique, ref)
			}
		}
		n, err := s.store.cache.Prefetch(unique)
		queued += n
		count = 0
		return err
	}
	state := s.state
	keyBounds := storeio.KeyTreeBounds{
		FileEnd: state.super.FileEnd, NextLogicalID: state.root.NextLogicalID,
		ChunkHighWater: state.root.ChunkHighWater, ChunkDocuments: uint8(state.root.ChunkDocuments),
	}
	chunkBounds := storeio.ChunkTreeBounds{FileEnd: state.super.FileEnd, NextLogicalID: state.root.NextLogicalID}
	for _, key := range keys {
		location, ok, err := storeio.LookupKeyTree(s.store.cache, state.keyRoot, []byte(key), keyBounds)
		if err != nil {
			return queued, err
		}
		if !ok {
			continue
		}
		ref, ok, err := storeio.LookupChunkTree(s.store.cache, state.chunkRoot, location.Chunk, chunkBounds)
		if err != nil {
			return queued, err
		}
		if !ok {
			return queued, storeio.ErrChunkDirectoryCorrupt
		}
		refs[count] = ref
		count++
		if count == len(refs) {
			if err := flush(); err != nil {
				return queued, err
			}
		}
	}
	return queued, flush()
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

// PrefetchKeys submits current-snapshot document reads to the bounded
// asynchronous prefetch queue.
func (s *FileStore) PrefetchKeys(keys []string) (int, error) {
	snapshot, err := s.Snapshot()
	if err != nil {
		return 0, err
	}
	defer snapshot.Close()
	return snapshot.PrefetchKeys(keys)
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

// Stats reports configured residency, page I/O, prefetch, durability queue,
// snapshot, and reclamation pressure without performing file I/O.
func (s *FileStore) Stats() FileStoreStats {
	if s == nil || s.cache == nil || s.committer == nil {
		return FileStoreStats{}
	}
	s.writer.Lock()
	defer s.writer.Unlock()
	cache := s.cache.Stats()
	commit := s.committer.Stats()
	state := s.state.Load()
	current := uint64(0)
	if state != nil {
		current = state.root.Generation
	}
	leases := s.leases.Stats(current)
	retired := s.reclaimer.Stats()
	stats := FileStoreStats{
		CapacityBytes: cache.CapacityBytes, ResidentBytes: cache.ResidentBytes,
		PinnedPages: cache.PinnedPages, DirtyBytes: cache.DirtyBytes,
		PageReads: cache.PageReads, ReadBytes: cache.ReadBytes, CacheHits: cache.CacheHits,
		PrefetchHits: cache.PrefetchHits, Evictions: cache.Evictions,
		PrefetchQueued: cache.PrefetchQueued, PrefetchDropped: cache.PrefetchDropped,
		ReadQueueDepth:      cache.QueueDepth,
		PublishedGeneration: commit.PublishedGeneration, DurableGeneration: commit.DurableGeneration,
		CommitQueueDepth: commit.QueuedGenerations, DeviceCommits: commit.DeviceCommits,
		CommittedBatches: commit.CommittedBatches, LargestCommitGroup: commit.LargestGroup,
		Backend:          FileStoreBackend(commit.Backend),
		SnapshotCapacity: leases.Capacity, ActiveSnapshots: leases.Active,
		OldestSnapshotGeneration: leases.MinimumGeneration,
		RetiredExtentCapacity:    retired.Capacity, PendingRetiredExtents: retired.Pending,
		PendingRetiredBytes: retired.PendingBytes, ReusableExtents: uint64(len(s.reusable)),
	}
	for _, extent := range s.reusable {
		stats.ReusableBytes += extent.Length
	}
	if state != nil {
		stats.DocumentCount = state.root.DocumentCount
		stats.LiveChunks = state.root.LiveChunks
		stats.FileEnd = state.super.FileEnd
	}
	return stats
}

// Put validates and copies src, then atomically publishes a copy-on-write file
// generation. created reports whether key was absent. Async mode returns after
// the bounded committer accepts the generation; Synchronous waits for the
// double-root durability fence.
func (s *FileStore) Put(key string, src []byte) (created bool, err error) {
	if s == nil {
		return false, ErrFileStoreClosed
	}
	s.writer.Lock()
	defer s.writer.Unlock()
	if s.closed {
		return false, ErrFileStoreClosed
	}
	if len(key) > s.options.MaxKeyBytes {
		return false, ErrFileStoreKeyTooLarge
	}
	if len(src) > s.options.MaxDocumentBytes {
		return false, ErrFileStoreDocumentTooLarge
	}
	if err := s.validateDocument(src); err != nil {
		return false, err
	}
	state := s.state.Load()
	if state == nil {
		return false, ErrFileStoreClosed
	}
	keyBytes := []byte(key)
	keyBounds := storeio.KeyTreeBounds{
		FileEnd: state.super.FileEnd, NextLogicalID: state.root.NextLogicalID,
		ChunkHighWater: state.root.ChunkHighWater,
		ChunkDocuments: uint8(state.root.ChunkDocuments),
	}
	var location storeio.KeyLocation
	found := false
	if state.keyRoot != (storeio.PageRef{}) {
		location, found, err = storeio.LookupKeyTree(s.cache, state.keyRoot, keyBytes, keyBounds)
		if err != nil {
			return false, err
		}
	}
	created = !found
	prospectiveHighWater := state.root.ChunkHighWater
	if !found {
		limit := fileStoreLiveMask(state.root.ChunkDocuments)
		if s.appendChunk < state.root.ChunkHighWater && s.appendLive != limit {
			location.Chunk = s.appendChunk
			location.Slot = uint8(bits.TrailingZeros64(^s.appendLive & limit))
		} else {
			if state.root.ChunkHighWater == ^uint32(0) {
				return false, ErrStoreTooLarge
			}
			location = storeio.KeyLocation{Chunk: state.root.ChunkHighWater}
			prospectiveHighWater++
		}
	}
	if err := s.ensureDirtyCapacity(); err != nil {
		return false, err
	}
	return s.putLocked(state, keyBytes, src, location, created, prospectiveHighWater)
}

func (s *FileStore) putLocked(state *fileStoreState, key, src []byte, location storeio.KeyLocation, created bool, prospectiveHighWater uint32) (bool, error) {
	generation := state.root.Generation + 1
	if generation == 0 {
		return false, storeio.ErrGenerationOrder
	}
	if err := s.refreshReusable(state); err != nil {
		return false, err
	}
	tx, err := storeio.BeginWriteTransaction(s.committer, s.cache, s.options.maxTransactionPages, storeio.WriteTransactionOptions{
		StoreID: s.storeID, Generation: generation, PageSize: uint32(s.options.PageSize),
		FileEnd: state.super.FileEnd, NextLogicalID: state.root.NextLogicalID,
		Reusable: s.reusable, ReuseJournal: s.reuseJournal, SingleReuseExtent: true,
	})
	if err != nil {
		return false, err
	}
	abort := true
	retirementReserved := false
	defer func() {
		if abort {
			if retirementReserved {
				_ = s.reclaimer.CancelRetiredGeneration(state.root.Generation)
			}
			_ = tx.Abort()
		}
	}()
	s.retireScratch = s.retireScratch[:0]

	oldRef, oldView, oldLease, err := s.loadFileChunk(state, location.Chunk)
	if err != nil {
		return false, err
	}
	if oldLease != nil {
		defer oldLease.Release()
	}
	if created {
		if oldView != nil {
			if _, occupied := oldView.Lookup(location.Slot); occupied {
				return false, storeio.ErrDocumentPageCorrupt
			}
		}
	} else {
		if oldView == nil {
			return false, storeio.ErrDocumentPageCorrupt
		}
		oldValue, ok := oldView.LookupKeyValue(location.Slot, key)
		if !ok {
			return false, storeio.ErrDocumentPageCorrupt
		}
		if err := s.appendOverflowRetirements(state, oldValue, location); err != nil {
			return false, err
		}
	}
	newRecord, err := s.stageFileValue(tx, location, key, src)
	if err != nil {
		return false, err
	}
	rows, live, err := s.buildFileRows(oldView, location.Slot, newRecord, true)
	if err != nil {
		return false, err
	}
	documentSize, err := s.fileDocumentPageSize(rows)
	if err != nil {
		return false, err
	}
	documentLogicalID := uint64(0)
	if oldRef != (storeio.PageRef{}) {
		documentLogicalID = oldRef.LogicalID
	}
	documentPage, err := tx.Allocate(storeio.PageDocument, documentSize, documentLogicalID)
	if err != nil {
		return false, err
	}
	if _, err := storeio.EncodeDocumentPageWithOverflow(documentPage.Bytes(), storeio.DocumentPageHeader{
		StoreID: s.storeID, Generation: generation, LogicalID: documentPage.Ref().LogicalID,
		PageSize: documentPage.Ref().Length, ChunkID: location.Chunk, Live: live,
	}, rows, tx.NextLogicalID(), tx.FileEnd(), uint32(s.options.PageSize)); err != nil {
		return false, err
	}
	if err := documentPage.Stage(); err != nil {
		return false, err
	}
	chunkMutation, err := storeio.UpsertChunkTree(s.cache, tx, state.chunkRoot, location.Chunk, documentPage.Ref(), storeio.ChunkTreeBounds{
		FileEnd: state.super.FileEnd, NextLogicalID: state.root.NextLogicalID,
	})
	if err != nil {
		return false, err
	}
	keyRoot := state.keyRoot
	var keyMutation storeio.KeyTreeMutation
	if created {
		keyMutation, err = storeio.UpsertKeyTree(s.cache, tx, state.keyRoot, key, location, storeio.KeyTreeBounds{
			FileEnd: state.super.FileEnd, NextLogicalID: state.root.NextLogicalID,
			ChunkHighWater: prospectiveHighWater, ChunkDocuments: uint8(state.root.ChunkDocuments),
		})
		if err != nil {
			return false, err
		}
		keyRoot = keyMutation.Root
	}
	documentCount := state.root.DocumentCount
	liveChunks := state.root.LiveChunks
	if created {
		documentCount++
		if oldRef == (storeio.PageRef{}) {
			liveChunks++
		}
	}
	freeRoot, freeChecksum, promoted, err := s.syncFileFreeTree(tx, state)
	if err != nil {
		return false, err
	}
	nextState, statePage, err := s.stageFileState(tx, state, generation, prospectiveHighWater, documentCount, liveChunks, chunkMutation.Root, keyRoot, freeRoot, freeChecksum)
	if err != nil {
		return false, err
	}
	if err := s.reserveFileRetirements(state, oldRef, keyMutation, chunkMutation); err != nil {
		return false, err
	}
	retirementReserved = true
	if err := tx.Publish(statePage.Ref(), storeio.PageChecksum(statePage.Bytes()), nextState.super.FreeOffset, nextState.super.FreeLength, nextState.super.FreeChecksum); err != nil {
		return false, err
	}
	abort = false
	s.finalizeReusable(promoted)
	s.snapshotGate.Lock()
	s.state.Store(nextState)
	s.snapshotGate.Unlock()
	if location.Chunk >= state.root.ChunkHighWater || location.Chunk == s.appendChunk {
		s.appendChunk = location.Chunk
		s.appendLive = live
	}
	if live == fileStoreLiveMask(state.root.ChunkDocuments) {
		s.appendChunk = prospectiveHighWater
		s.appendLive = 0
	}
	if s.options.Synchronous {
		if err := s.committer.Wait(generation); err != nil {
			return created, err
		}
		s.cache.MarkDurable(generation)
	}
	return created, nil
}

// Delete removes key through the same failure-atomic page publication.
func (s *FileStore) Delete(key string) (bool, error) {
	if s == nil {
		return false, ErrFileStoreClosed
	}
	s.writer.Lock()
	defer s.writer.Unlock()
	if s.closed {
		return false, ErrFileStoreClosed
	}
	state := s.state.Load()
	if state == nil || state.keyRoot == (storeio.PageRef{}) {
		return false, nil
	}
	location, found, err := storeio.LookupKeyTree(s.cache, state.keyRoot, []byte(key), storeio.KeyTreeBounds{
		FileEnd: state.super.FileEnd, NextLogicalID: state.root.NextLogicalID,
		ChunkHighWater: state.root.ChunkHighWater, ChunkDocuments: uint8(state.root.ChunkDocuments),
	})
	if err != nil || !found {
		return false, err
	}
	if err := s.ensureDirtyCapacity(); err != nil {
		return false, err
	}
	return s.deleteLocked(state, []byte(key), location)
}

func (s *FileStore) deleteLocked(state *fileStoreState, key []byte, location storeio.KeyLocation) (bool, error) {
	generation := state.root.Generation + 1
	if err := s.refreshReusable(state); err != nil {
		return false, err
	}
	tx, err := storeio.BeginWriteTransaction(s.committer, s.cache, s.options.maxTransactionPages, storeio.WriteTransactionOptions{
		StoreID: s.storeID, Generation: generation, PageSize: uint32(s.options.PageSize),
		FileEnd: state.super.FileEnd, NextLogicalID: state.root.NextLogicalID,
		Reusable: s.reusable, ReuseJournal: s.reuseJournal, SingleReuseExtent: true,
	})
	if err != nil {
		return false, err
	}
	abort := true
	retirementReserved := false
	defer func() {
		if abort {
			if retirementReserved {
				_ = s.reclaimer.CancelRetiredGeneration(state.root.Generation)
			}
			_ = tx.Abort()
		}
	}()
	s.retireScratch = s.retireScratch[:0]
	oldRef, oldView, oldLease, err := s.loadFileChunk(state, location.Chunk)
	if err != nil || oldView == nil {
		return false, err
	}
	defer oldLease.Release()
	oldValue, ok := oldView.LookupKeyValue(location.Slot, key)
	if !ok {
		return false, storeio.ErrDocumentPageCorrupt
	}
	if err := s.appendOverflowRetirements(state, oldValue, location); err != nil {
		return false, err
	}
	rows, live, err := s.buildFileRows(oldView, location.Slot, storeio.DocumentRecord{}, false)
	if err != nil {
		return false, err
	}
	chunkRoot := state.chunkRoot
	var chunkMutation storeio.ChunkTreeMutation
	if live == 0 {
		chunkMutation, err = storeio.DeleteChunkTree(s.cache, tx, state.chunkRoot, location.Chunk, storeio.ChunkTreeBounds{
			FileEnd: state.super.FileEnd, NextLogicalID: state.root.NextLogicalID,
		})
	} else {
		documentSize, sizeErr := s.fileDocumentPageSize(rows)
		if sizeErr != nil {
			return false, sizeErr
		}
		documentPage, allocateErr := tx.Allocate(storeio.PageDocument, documentSize, oldRef.LogicalID)
		if allocateErr != nil {
			return false, allocateErr
		}
		if _, encodeErr := storeio.EncodeDocumentPageWithOverflow(documentPage.Bytes(), storeio.DocumentPageHeader{
			StoreID: s.storeID, Generation: generation, LogicalID: documentPage.Ref().LogicalID,
			PageSize: documentPage.Ref().Length, ChunkID: location.Chunk, Live: live,
		}, rows, tx.NextLogicalID(), tx.FileEnd(), uint32(s.options.PageSize)); encodeErr != nil {
			return false, encodeErr
		}
		if stageErr := documentPage.Stage(); stageErr != nil {
			return false, stageErr
		}
		chunkMutation, err = storeio.UpsertChunkTree(s.cache, tx, state.chunkRoot, location.Chunk, documentPage.Ref(), storeio.ChunkTreeBounds{
			FileEnd: state.super.FileEnd, NextLogicalID: state.root.NextLogicalID,
		})
	}
	if err != nil {
		return false, err
	}
	chunkRoot = chunkMutation.Root
	keyMutation, err := storeio.DeleteKeyTree(s.cache, tx, state.keyRoot, key, storeio.KeyTreeBounds{
		FileEnd: state.super.FileEnd, NextLogicalID: state.root.NextLogicalID,
		ChunkHighWater: state.root.ChunkHighWater, ChunkDocuments: uint8(state.root.ChunkDocuments),
	})
	if err != nil || !keyMutation.Found {
		return false, err
	}
	liveChunks := state.root.LiveChunks
	if live == 0 {
		liveChunks--
	}
	freeRoot, freeChecksum, promoted, err := s.syncFileFreeTree(tx, state)
	if err != nil {
		return false, err
	}
	nextState, statePage, err := s.stageFileState(
		tx, state, generation, state.root.ChunkHighWater,
		state.root.DocumentCount-1, liveChunks, chunkRoot, keyMutation.Root, freeRoot, freeChecksum,
	)
	if err != nil {
		return false, err
	}
	if err := s.reserveFileRetirements(state, oldRef, keyMutation, chunkMutation); err != nil {
		return false, err
	}
	retirementReserved = true
	if err := tx.Publish(statePage.Ref(), storeio.PageChecksum(statePage.Bytes()), nextState.super.FreeOffset, nextState.super.FreeLength, nextState.super.FreeChecksum); err != nil {
		return false, err
	}
	abort = false
	s.finalizeReusable(promoted)
	s.snapshotGate.Lock()
	s.state.Store(nextState)
	s.snapshotGate.Unlock()
	if location.Chunk == s.appendChunk {
		s.appendLive = live
	}
	if s.options.Synchronous {
		if err := s.committer.Wait(generation); err != nil {
			return true, err
		}
		s.cache.MarkDurable(generation)
	}
	return true, nil
}

func (s *FileStore) validateDocument(src []byte) error {
	estimate := len(src)/8 + 8
	if estimate < 8 {
		estimate = 8
	}
	if cap(s.parseScratch) < estimate {
		s.parseScratch = make([]IndexEntry, estimate)
	}
	for {
		_, err := BuildIndexOptions(src, s.parseScratch[:cap(s.parseScratch)], s.options.Store.IndexOptions)
		if err != document.ErrIndexFull {
			return err
		}
		if cap(s.parseScratch) > s.options.MaxDocumentBytes {
			return ErrFileStoreDocumentTooLarge
		}
		s.parseScratch = make([]IndexEntry, cap(s.parseScratch)*2)
	}
}

func (s *FileStore) ensureDirtyCapacity() error {
	stats := s.cache.Stats()
	required := uint64(s.options.maxTransactionPages * s.options.MaxPageSize)
	if stats.CapacityBytes-stats.DirtyBytes >= required {
		return nil
	}
	if err := s.committer.Flush(); err != nil {
		return err
	}
	s.cache.MarkDurable(s.committer.DurableGeneration())
	return nil
}

func (s *FileStore) refreshReusable(state *fileStoreState) error {
	if !s.freeLoaded {
		before := len(s.reusable)
		var err error
		s.reusable, err = storeio.AppendFreeTreeExtents(
			s.cache, state.freeRoot,
			storeio.FreeTreeBounds{FileEnd: state.super.FileEnd, NextLogicalID: state.root.NextLogicalID},
			s.reusable, s.options.MaxRetiredExtents,
		)
		if err != nil {
			clear(s.reusable[before:])
			s.reusable = s.reusable[:before]
			return err
		}
		s.freeLoaded = true
	}
	durable := s.committer.DurableGeneration()
	s.cache.MarkDurable(durable)
	stats := s.reclaimer.Stats()
	if stats.Pending > uint64(cap(s.reusable)-len(s.reusable)) {
		return nil
	}
	oldestRecovery := uint64(1)
	if durable > 1 {
		oldestRecovery = durable - 1
	}
	before := len(s.reusable)
	s.reusable = s.reclaimer.AppendReusable(s.reusable, state.root.Generation, oldestRecovery)
	added := len(s.reusable) - before
	if added == 0 {
		return nil
	}
	s.unpersisted += added
	start := len(s.reusable) - s.unpersisted
	tail := s.reusable[start:]
	slices.SortFunc(tail, func(a, b storeio.FreeExtent) int {
		if a.Offset < b.Offset {
			return -1
		}
		if a.Offset > b.Offset {
			return 1
		}
		return 0
	})
	out := tail[:0]
	for _, extent := range tail {
		last := len(out) - 1
		if last >= 0 && out[last].Offset+out[last].Length == extent.Offset {
			out[last].Length += extent.Length
			out[last].RetiredGeneration = max(out[last].RetiredGeneration, extent.RetiredGeneration)
			continue
		}
		out = append(out, extent)
	}
	clear(tail[len(out):])
	s.reusable = s.reusable[:start+len(out)]
	s.unpersisted = len(out)
	return nil
}

func (s *FileStore) finalizeReusable(promoted int) {
	persisted := len(s.reusable) - s.unpersisted
	if promoted >= persisted {
		s.reusable[persisted], s.reusable[promoted] = s.reusable[promoted], s.reusable[persisted]
		persisted++
		s.unpersisted--
	}
	out := s.reusable[:0]
	for _, extent := range s.reusable[:persisted] {
		if extent.Length != 0 {
			out = append(out, extent)
		}
	}
	newPersisted := len(out)
	for _, extent := range s.reusable[persisted:] {
		if extent.Length != 0 {
			out = append(out, extent)
		}
	}
	clear(s.reusable[len(out):])
	s.reusable = out
	s.unpersisted = len(out) - newPersisted
}

func (s *FileStore) syncFileFreeTree(tx *storeio.WriteTransaction, state *fileStoreState) (storeio.PageRef, uint32, int, error) {
	root := state.freeRoot
	promoted := -1
	persisted := len(s.reusable) - s.unpersisted
	chosen := -1
	if edits := tx.ReuseEdits(); len(edits) != 0 {
		chosen = int(edits[0].Index)
	} else if s.unpersisted != 0 {
		chosen = persisted
	}
	exclude := -1
	if chosen >= persisted {
		exclude = chosen
	}
	if err := tx.SetReuseRange(persisted, len(s.reusable), exclude); err != nil {
		return storeio.PageRef{}, 0, -1, err
	}
	if chosen >= 0 {
		bounds := storeio.FreeTreeBounds{FileEnd: tx.FileEnd(), NextLogicalID: tx.NextLogicalID()}
		current := s.reusable[chosen]
		var mutation storeio.FreeTreeMutation
		var err error
		if chosen < persisted {
			before := tx.ReuseEdits()[0].Before
			if current.Length == 0 {
				mutation, err = storeio.DeleteFreeTree(s.cache, tx, root, before.Offset, bounds)
			} else {
				mutation, err = storeio.UpsertFreeTree(s.cache, tx, root, current, bounds)
			}
		} else {
			promoted = chosen
			if current.Length != 0 {
				mutation, err = storeio.UpsertFreeTree(s.cache, tx, root, current, bounds)
			}
		}
		if err != nil {
			return storeio.PageRef{}, 0, -1, err
		}
		if mutation.Changed {
			root = mutation.Root
			for i := 0; i < int(mutation.RetiredCount); i++ {
				if len(s.retireScratch) == cap(s.retireScratch) {
					return storeio.PageRef{}, 0, -1, storeio.ErrRetiredExtentCapacity
				}
				ref := mutation.Retired[i]
				s.retireScratch = append(s.retireScratch, storeio.FreeExtent{
					Offset: ref.Offset, Length: uint64(ref.Length), RetiredGeneration: state.root.Generation,
				})
			}
		}
	}
	if root == (storeio.PageRef{}) {
		return root, 0, promoted, nil
	}
	if root == state.freeRoot {
		return root, state.super.FreeChecksum, promoted, nil
	}
	lease, err := s.cache.Acquire(root)
	if err != nil {
		return storeio.PageRef{}, 0, -1, err
	}
	checksum := storeio.PageChecksum(lease.Page())
	lease.Release()
	return root, checksum, promoted, nil
}

func (s *FileStore) stageFileValue(tx *storeio.WriteTransaction, location storeio.KeyLocation, key, src []byte) (storeio.DocumentRecord, error) {
	record := storeio.DocumentRecord{Key: key, Slot: location.Slot}
	if len(src) <= s.options.InlineValueBytes {
		record.JSON = src
		return record, nil
	}
	payloadBytes := s.options.MaxPageSize - storeio.PageHeaderSize - storeio.PageTrailerSize - storeio.OverflowPagePayloadHeaderSize
	pageCount := (len(src) + payloadBytes - 1) / payloadBytes
	pages := make([]storeio.TransactionPage, pageCount)
	for i := range pages {
		page, err := tx.Allocate(storeio.PageOverflow, uint32(s.options.MaxPageSize), 0)
		if err != nil {
			return storeio.DocumentRecord{}, err
		}
		pages[i] = page
	}
	position := 0
	for i, page := range pages {
		end := min(position+payloadBytes, len(src))
		var next storeio.PageRef
		if i+1 < len(pages) {
			next = pages[i+1].Ref()
		}
		header := storeio.OverflowPageHeader{
			StoreID: s.storeID, Generation: tx.Generation(), LogicalID: page.Ref().LogicalID,
			PageSize: page.Ref().Length, Chunk: location.Chunk, Slot: location.Slot,
			Total: uint64(len(src)), Offset: uint64(position), Next: next,
		}
		if _, err := storeio.EncodeOverflowPage(
			page.Bytes(), header, src[position:end], tx.FileEnd(), tx.NextLogicalID(),
			uint32(s.options.PageSize), location.Chunk+1, uint8(s.options.Store.ChunkDocuments),
		); err != nil {
			return storeio.DocumentRecord{}, err
		}
		if err := page.Stage(); err != nil {
			return storeio.DocumentRecord{}, err
		}
		position = end
	}
	record.Overflow = pages[0].Ref()
	record.JSONLength = uint64(len(src))
	return record, nil
}

func (s *FileStore) loadFileChunk(state *fileStoreState, chunkID uint32) (storeio.PageRef, *storeio.DocumentPageView, *storeio.PageLease, error) {
	if chunkID >= state.root.ChunkHighWater || state.chunkRoot == (storeio.PageRef{}) {
		return storeio.PageRef{}, nil, nil, nil
	}
	ref, ok, err := storeio.LookupChunkTree(s.cache, state.chunkRoot, chunkID, storeio.ChunkTreeBounds{
		FileEnd: state.super.FileEnd, NextLogicalID: state.root.NextLogicalID,
	})
	if err != nil || !ok {
		return storeio.PageRef{}, nil, nil, err
	}
	lease, err := s.cache.Acquire(ref)
	if err != nil {
		return storeio.PageRef{}, nil, nil, err
	}
	view, err := storeio.OpenDocumentPageWithOverflow(
		lease.Page(), state.root.ChunkHighWater, state.root.NextLogicalID,
		state.super.FileEnd, state.root.PageSize,
	)
	if err != nil {
		lease.Release()
		return storeio.PageRef{}, nil, nil, err
	}
	return ref, &view, &lease, nil
}

func (s *FileStore) buildFileRows(old *storeio.DocumentPageView, target uint8, replacement storeio.DocumentRecord, keep bool) ([]storeio.DocumentRecord, uint64, error) {
	var storage [storeMaxChunkDocuments]storeio.DocumentRecord
	position := 0
	var live uint64
	for slot := uint8(0); slot < uint8(s.options.Store.ChunkDocuments); slot++ {
		if slot == target {
			if keep {
				storage[position] = replacement
				position++
				live |= uint64(1) << slot
			}
			continue
		}
		if old == nil {
			continue
		}
		record, ok := old.Lookup(slot)
		if !ok {
			continue
		}
		storage[position] = record
		position++
		live |= uint64(1) << slot
	}
	if old != nil {
		if _, existed := old.Lookup(target); !keep && !existed {
			return nil, 0, storeio.ErrDocumentPageCorrupt
		}
	}
	return storage[:position], live, nil
}

func (s *FileStore) fileDocumentPageSize(rows []storeio.DocumentRecord) (uint32, error) {
	needed := storeio.PageHeaderSize + storeio.PageTrailerSize + storeio.DocumentPagePayloadHeaderSize + len(rows)*storeio.DocumentPageRecordSize
	for _, row := range rows {
		needed += len(row.Key)
		if row.Overflow == (storeio.PageRef{}) {
			needed += len(row.JSON)
		} else {
			needed += storeio.DocumentOverflowDescriptorSize
		}
	}
	size := s.options.PageSize
	for size < needed && size < s.options.MaxPageSize {
		size <<= 1
	}
	if size < needed || size > s.options.MaxPageSize {
		return 0, ErrFileStoreDocumentTooLarge
	}
	return uint32(size), nil
}

func (s *FileStore) stageFileState(tx *storeio.WriteTransaction, old *fileStoreState, generation uint64, chunkHighWater uint32, documentCount uint64, liveChunks uint32, chunkRoot, keyRoot, freeRoot storeio.PageRef, freeChecksum uint32) (*fileStoreState, storeio.TransactionPage, error) {
	statePage, err := tx.Allocate(storeio.PageStateRoot, uint32(s.options.PageSize), storeio.StateRootLogicalID)
	if err != nil {
		return nil, storeio.TransactionPage{}, err
	}
	root := storeio.StateRoot{
		StoreID: s.storeID, Generation: generation, PageSize: uint32(s.options.PageSize),
		DocumentCount: documentCount, NextLogicalID: tx.NextLogicalID(),
		ChunkHighWater: chunkHighWater, LiveChunks: liveChunks,
		ChunkDocuments: uint32(s.options.Store.ChunkDocuments),
		ChunkDirectory: chunkRoot, KeyDirectory: keyRoot,
	}
	if _, err := storeio.EncodeStateRootPage(statePage.Bytes(), root, tx.FileEnd()); err != nil {
		return nil, storeio.TransactionPage{}, err
	}
	if err := statePage.Stage(); err != nil {
		return nil, storeio.TransactionPage{}, err
	}
	super := storeio.Superblock{
		StoreID: s.storeID, Generation: generation,
		StateOffset: statePage.Ref().Offset, StateLength: statePage.Ref().Length,
		StateChecksum: storeio.PageChecksum(statePage.Bytes()), FileEnd: tx.FileEnd(),
		PageSize: uint32(s.options.PageSize),
	}
	if freeRoot != (storeio.PageRef{}) {
		super.FreeOffset = freeRoot.Offset
		super.FreeLength = freeRoot.Length
		super.FreeChecksum = freeChecksum
	}
	return &fileStoreState{
		root: root, super: super, stateRef: statePage.Ref(),
		keyRoot: keyRoot, chunkRoot: chunkRoot, freeRoot: freeRoot,
	}, statePage, nil
}

func (s *FileStore) reserveFileRetirements(old *fileStoreState, oldDocument storeio.PageRef, key storeio.KeyTreeMutation, chunk storeio.ChunkTreeMutation) error {
	appendRef := func(ref storeio.PageRef) error {
		if ref == (storeio.PageRef{}) {
			return nil
		}
		if len(s.retireScratch) == cap(s.retireScratch) {
			return storeio.ErrRetiredExtentCapacity
		}
		s.retireScratch = append(s.retireScratch, storeio.FreeExtent{
			Offset: ref.Offset, Length: uint64(ref.Length), RetiredGeneration: old.root.Generation,
		})
		return nil
	}
	if err := appendRef(old.stateRef); err != nil {
		return err
	}
	if err := appendRef(oldDocument); err != nil {
		return err
	}
	for i := 0; i < int(key.RetiredCount); i++ {
		if err := appendRef(key.Retired[i]); err != nil {
			return err
		}
	}
	for i := 0; i < int(chunk.RetiredCount); i++ {
		if err := appendRef(chunk.Retired[i]); err != nil {
			return err
		}
	}
	return s.reclaimer.RetireBatch(s.retireScratch)
}

func (s *FileStore) appendOverflowRetirements(state *fileStoreState, value storeio.DocumentValue, location storeio.KeyLocation) error {
	ref := value.Overflow
	if ref == (storeio.PageRef{}) {
		return nil
	}
	offset := uint64(0)
	for ref != (storeio.PageRef{}) {
		if len(s.retireScratch) == cap(s.retireScratch) {
			return storeio.ErrRetiredExtentCapacity
		}
		lease, err := s.cache.Acquire(ref)
		if err != nil {
			return err
		}
		view, err := storeio.OpenOverflowPage(
			lease.Page(), state.super.FileEnd, state.root.NextLogicalID,
			state.root.PageSize, state.root.ChunkHighWater, uint8(state.root.ChunkDocuments),
		)
		if err != nil {
			lease.Release()
			return err
		}
		header := view.Header()
		if header.Chunk != location.Chunk || header.Slot != location.Slot ||
			header.Total != value.Length || header.Offset != offset {
			lease.Release()
			return storeio.ErrOverflowPageCorrupt
		}
		s.retireScratch = append(s.retireScratch, storeio.FreeExtent{
			Offset: ref.Offset, Length: uint64(ref.Length), RetiredGeneration: state.root.Generation,
		})
		offset += uint64(len(view.Data()))
		ref = header.Next
		lease.Release()
	}
	if offset != value.Length {
		return storeio.ErrOverflowPageCorrupt
	}
	return nil
}

func fileStoreLiveMask(chunkDocuments uint32) uint64 {
	if chunkDocuments >= 64 {
		return ^uint64(0)
	}
	return uint64(1)<<chunkDocuments - 1
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
