package storeio

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
)

const defaultPrefetchQueue = 64

var (
	// ErrPageCacheClosed reports use after Close has started.
	ErrPageCacheClosed = errors.New("simdjson: Store page cache is closed")
	// ErrPageCachePinned reports that every resident frame is leased. Callers
	// must release a lease before another physical page can be admitted.
	ErrPageCachePinned = errors.New("simdjson: every Store page cache frame is pinned")
	// ErrPageCacheReference reports a malformed or physically unordered page
	// reference before any file I/O is attempted.
	ErrPageCacheReference = errors.New("simdjson: invalid Store page cache reference")
)

// PageCacheOptions fixes the complete resident and prefetch memory of a
// PageCache. ResidentBytes is rounded down to an integral number of pages; it
// must hold at least one page. StoreID binds every admitted page to one file.
type PageCacheOptions struct {
	PageSize      int
	ResidentBytes int64
	StoreID       [16]byte
	PrefetchQueue int
}

func (o PageCacheOptions) normalized() (PageCacheOptions, int, error) {
	if o.PageSize == 0 {
		o.PageSize = defaultBufferSize
	}
	if o.StoreID == ([16]byte{}) || o.PageSize < 0 ||
		uint64(o.PageSize) > uint64(^uint32(0)) || !validPhysicalPageSize(uint32(o.PageSize)) {
		return PageCacheOptions{}, 0, fmt.Errorf("%w: Store id or page size", ErrPageCacheReference)
	}
	if o.ResidentBytes < int64(o.PageSize) {
		return PageCacheOptions{}, 0, fmt.Errorf("%w: resident budget %d is smaller than one %d-byte page",
			ErrPageCacheReference, o.ResidentBytes, o.PageSize)
	}
	frames64 := o.ResidentBytes / int64(o.PageSize)
	maxInt := int64(^uint(0) >> 1)
	if frames64 <= 0 || frames64 > maxInt/int64(o.PageSize) {
		return PageCacheOptions{}, 0, fmt.Errorf("%w: resident budget overflows address space", ErrPageCacheReference)
	}
	if o.PrefetchQueue == 0 {
		o.PrefetchQueue = defaultPrefetchQueue
	}
	if o.PrefetchQueue < 1 || o.PrefetchQueue > maxDeviceQueueDepth {
		return PageCacheOptions{}, 0, fmt.Errorf("%w: prefetch queue %d", ErrPageCacheReference, o.PrefetchQueue)
	}
	o.ResidentBytes = frames64 * int64(o.PageSize)
	return o, int(frames64), nil
}

type pageCacheKey struct {
	offset     uint64
	logicalID  uint64
	generation uint64
	kind       PageKind
}

const (
	pageCacheEmpty uint8 = iota
	pageCacheLoading
	pageCacheReady
)

type pageCacheFrame struct {
	data       []byte
	payload    []byte
	key        pageCacheKey
	header     PageHeader
	pins       uint32
	state      uint8
	referenced bool
	prefetched bool
}

// PageCacheStats is a point-in-time accounting snapshot. ResidentBytes counts
// admitted fixed frames, including reads in progress. QueueDepth is sampled
// from the bounded prefetch queue.
type PageCacheStats struct {
	CapacityBytes   uint64
	ResidentBytes   uint64
	PinnedPages     uint64
	PageReads       uint64
	ReadBytes       uint64
	CacheHits       uint64
	PrefetchHits    uint64
	Evictions       uint64
	PrefetchQueued  uint64
	PrefetchDropped uint64
	QueueDepth      uint64
}

// PageCache owns a fixed off-heap frame arena on common Unix platforms and a
// portable pointer-free byte arena elsewhere. It performs explicit positional
// reads, validates every common page before publication, and applies a CLOCK
// replacement policy. It never relies on demand-paged mmap for admission or
// eviction decisions.
type PageCache struct {
	file    *os.File
	options PageCacheOptions
	arena   []byte
	frames  []pageCacheFrame
	byKey   map[pageCacheKey]int
	hand    int

	mu          sync.Mutex
	cond        *sync.Cond
	closing     bool
	closed      bool
	activeLoads int
	stopOnce    sync.Once
	stop        chan struct{}
	prefetch    chan PageRef
	done        chan struct{}

	pageReads       uint64
	readBytes       uint64
	cacheHits       uint64
	prefetchHits    uint64
	evictions       uint64
	prefetchQueued  uint64
	prefetchDropped uint64
}

// NewPageCache creates a bounded read cache over file. The file remains
// caller-owned and must outlive the cache. Construction allocates all frame
// bytes and starts one portable prefetch worker.
func NewPageCache(file *os.File, options PageCacheOptions) (*PageCache, error) {
	if file == nil {
		return nil, fmt.Errorf("%w: nil file", ErrPageCacheReference)
	}
	normalized, frameCount, err := options.normalized()
	if err != nil {
		return nil, err
	}
	arena, err := allocateArena(frameCount * normalized.PageSize)
	if err != nil {
		return nil, fmt.Errorf("simdjson: allocate Store page cache: %w", err)
	}
	c := &PageCache{
		file:     file,
		options:  normalized,
		arena:    arena,
		frames:   make([]pageCacheFrame, frameCount),
		byKey:    make(map[pageCacheKey]int, frameCount),
		stop:     make(chan struct{}),
		prefetch: make(chan PageRef, normalized.PrefetchQueue),
		done:     make(chan struct{}),
	}
	c.cond = sync.NewCond(&c.mu)
	for i := range c.frames {
		start := i * normalized.PageSize
		c.frames[i].data = arena[start : start+normalized.PageSize : start+normalized.PageSize]
	}
	go c.runPrefetch()
	return c, nil
}

// PageLease pins one validated frame. The value is single-owner and must not
// be copied after first use. Payload and Header remain valid until Release.
type PageLease struct {
	cache   *PageCache
	frame   int
	key     pageCacheKey
	header  PageHeader
	payload []byte
}

// Header returns the immutable identity of the leased page.
func (l *PageLease) Header() PageHeader {
	if l == nil {
		return PageHeader{}
	}
	return l.header
}

// Payload returns a capacity-clipped view of the validated page payload. The
// view becomes invalid after Release.
func (l *PageLease) Payload() []byte {
	if l == nil {
		return nil
	}
	return l.payload
}

// Release unpins the frame. It is idempotent for one PageLease value.
func (l *PageLease) Release() {
	if l == nil || l.cache == nil {
		return
	}
	cache := l.cache
	cache.release(l.frame, l.key)
	l.cache = nil
	l.payload = nil
	l.header = PageHeader{}
}

// Acquire returns a lease over ref. Concurrent misses for the same ref share
// one physical read. A miss returns ErrPageCachePinned when the fixed budget
// contains no unleased victim; it never grows the resident set.
func (c *PageCache) Acquire(ref PageRef) (PageLease, error) {
	return c.load(ref, true, false)
}

func (c *PageCache) load(ref PageRef, pin, prefetch bool) (PageLease, error) {
	key, err := c.validateRef(ref)
	if err != nil {
		return PageLease{}, err
	}

	c.mu.Lock()
	for {
		if c.closing || c.closed {
			c.mu.Unlock()
			return PageLease{}, ErrPageCacheClosed
		}
		if index, ok := c.byKey[key]; ok {
			frame := &c.frames[index]
			switch frame.state {
			case pageCacheLoading:
				if !pin {
					c.mu.Unlock()
					return PageLease{}, nil
				}
				c.cond.Wait()
				continue
			case pageCacheReady:
				if !pin {
					c.mu.Unlock()
					return PageLease{}, nil
				}
				frame.pins++
				frame.referenced = true
				c.cacheHits++
				if frame.prefetched {
					frame.prefetched = false
					c.prefetchHits++
				}
				lease := PageLease{cache: c, frame: index, key: key, header: frame.header, payload: frame.payload}
				c.mu.Unlock()
				return lease, nil
			}
		}

		index, ok := c.reserveLocked()
		if !ok {
			if prefetch {
				c.prefetchDropped++
				c.mu.Unlock()
				return PageLease{}, nil
			}
			c.mu.Unlock()
			return PageLease{}, ErrPageCachePinned
		}
		frame := &c.frames[index]
		frame.key = key
		frame.header = PageHeader{}
		frame.payload = nil
		frame.state = pageCacheLoading
		frame.pins = 0
		if pin {
			frame.pins = 1
		}
		frame.referenced = pin
		frame.prefetched = prefetch
		c.byKey[key] = index
		c.activeLoads++
		data := frame.data
		c.mu.Unlock()

		n, readErr := c.file.ReadAt(data, int64(ref.Offset))
		if readErr == nil && n != len(data) {
			readErr = io.ErrUnexpectedEOF
		}
		if readErr == nil {
			var payload []byte
			var header PageHeader
			header, payload, readErr = OpenPage(data)
			if readErr == nil && (header.StoreID != c.options.StoreID || header.PageSize != uint32(c.options.PageSize) ||
				header.LogicalID != ref.LogicalID || header.Generation != ref.Generation ||
				header.Kind != ref.Kind || header.Flags != ref.Flags) {
				readErr = fmt.Errorf("%w: physical page identity does not match reference", ErrPageCacheReference)
			}
			c.mu.Lock()
			c.pageReads++
			c.readBytes += uint64(n)
			c.activeLoads--
			frame = &c.frames[index]
			if readErr == nil {
				frame.header = header
				frame.payload = payload
				frame.state = pageCacheReady
				c.cond.Broadcast()
				if !pin {
					c.mu.Unlock()
					return PageLease{}, nil
				}
				lease := PageLease{cache: c, frame: index, key: key, header: header, payload: payload}
				c.mu.Unlock()
				return lease, nil
			}
			delete(c.byKey, key)
			resetPageCacheFrame(frame)
			c.cond.Broadcast()
			c.mu.Unlock()
			return PageLease{}, readErr
		}

		c.mu.Lock()
		c.pageReads++
		c.readBytes += uint64(n)
		c.activeLoads--
		frame = &c.frames[index]
		delete(c.byKey, key)
		resetPageCacheFrame(frame)
		c.cond.Broadcast()
		c.mu.Unlock()
		return PageLease{}, readErr
	}
}

func (c *PageCache) reserveLocked() (int, bool) {
	for i := range c.frames {
		if c.frames[i].state == pageCacheEmpty {
			return i, true
		}
	}
	for scanned := 0; scanned < len(c.frames)*2; scanned++ {
		index := c.hand
		c.hand++
		if c.hand == len(c.frames) {
			c.hand = 0
		}
		frame := &c.frames[index]
		if frame.state != pageCacheReady || frame.pins != 0 {
			continue
		}
		if frame.referenced {
			frame.referenced = false
			continue
		}
		delete(c.byKey, frame.key)
		resetPageCacheFrame(frame)
		c.evictions++
		return index, true
	}
	return 0, false
}

func resetPageCacheFrame(frame *pageCacheFrame) {
	frame.payload = nil
	frame.key = pageCacheKey{}
	frame.header = PageHeader{}
	frame.pins = 0
	frame.state = pageCacheEmpty
	frame.referenced = false
	frame.prefetched = false
}

func (c *PageCache) validateRef(ref PageRef) (pageCacheKey, error) {
	pageSize := uint64(c.options.PageSize)
	if ref.Length != uint32(c.options.PageSize) || ref.Flags != 0 || !validPageKind(ref.Kind) ||
		ref.LogicalID <= StateRootLogicalID || ref.Generation == 0 ||
		ref.Offset < uint64(superblockCopies)*pageSize || ref.Offset%pageSize != 0 ||
		ref.Offset > uint64(^uint64(0)>>1)-pageSize {
		return pageCacheKey{}, fmt.Errorf("%w: offset, identity, kind, or length", ErrPageCacheReference)
	}
	return pageCacheKey{offset: ref.Offset, logicalID: ref.LogicalID, generation: ref.Generation, kind: ref.Kind}, nil
}

func (c *PageCache) release(index int, key pageCacheKey) {
	c.mu.Lock()
	if index >= 0 && index < len(c.frames) {
		frame := &c.frames[index]
		if frame.key == key && frame.state == pageCacheReady && frame.pins != 0 {
			frame.pins--
			if frame.pins == 0 {
				c.cond.Broadcast()
			}
		}
	}
	c.mu.Unlock()
}

// Prefetch enqueues physically ordered refs without blocking on I/O. The input
// must be monotonically ordered by non-overlapping physical offset so query
// planning, rather than the cache, owns sorting scratch. Invalid order queues
// nothing. Queue exhaustion drops the remaining refs and is observable in
// Stats.
func (c *PageCache) Prefetch(refs []PageRef) (int, error) {
	var previousEnd uint64
	for i, ref := range refs {
		if _, err := c.validateRef(ref); err != nil {
			return 0, err
		}
		if i != 0 && ref.Offset < previousEnd {
			return 0, fmt.Errorf("%w: prefetch references are not physically ordered", ErrPageCacheReference)
		}
		previousEnd = ref.Offset + uint64(ref.Length)
	}
	c.mu.Lock()
	if c.closing || c.closed {
		c.mu.Unlock()
		return 0, ErrPageCacheClosed
	}
	c.mu.Unlock()

	queued := 0
	for _, ref := range refs {
		select {
		case <-c.stop:
			return queued, ErrPageCacheClosed
		default:
		}
		select {
		case c.prefetch <- ref:
			queued++
			c.mu.Lock()
			c.prefetchQueued++
			c.mu.Unlock()
		default:
			c.mu.Lock()
			c.prefetchDropped += uint64(len(refs) - queued)
			c.mu.Unlock()
			return queued, nil
		}
	}
	return queued, nil
}

func (c *PageCache) runPrefetch() {
	defer close(c.done)
	for {
		select {
		case <-c.stop:
			return
		default:
		}
		select {
		case <-c.stop:
			return
		case ref := <-c.prefetch:
			_, _ = c.load(ref, false, true)
		}
	}
}

// Stats returns bounded residency, lease, I/O, eviction, and prefetch
// accounting without performing file I/O.
func (c *PageCache) Stats() PageCacheStats {
	c.mu.Lock()
	stats := PageCacheStats{
		CapacityBytes:   uint64(len(c.frames) * c.options.PageSize),
		PageReads:       c.pageReads,
		ReadBytes:       c.readBytes,
		CacheHits:       c.cacheHits,
		PrefetchHits:    c.prefetchHits,
		Evictions:       c.evictions,
		PrefetchQueued:  c.prefetchQueued,
		PrefetchDropped: c.prefetchDropped,
		QueueDepth:      uint64(len(c.prefetch)),
	}
	for i := range c.frames {
		if c.frames[i].state != pageCacheEmpty {
			stats.ResidentBytes += uint64(c.options.PageSize)
		}
		if c.frames[i].pins != 0 {
			stats.PinnedPages++
		}
	}
	c.mu.Unlock()
	return stats
}

// Close stops admission and prefetch, then releases the fixed arena. If a
// caller still owns a lease, Close returns ErrPageCachePinned without releasing
// the arena; release those leases and call Close again.
func (c *PageCache) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closing = true
	c.stopOnce.Do(func() { close(c.stop) })
	c.cond.Broadcast()
	c.mu.Unlock()
	<-c.done

	c.mu.Lock()
	for c.activeLoads != 0 {
		c.cond.Wait()
	}
	for i := range c.frames {
		if c.frames[i].pins != 0 {
			c.mu.Unlock()
			return ErrPageCachePinned
		}
	}
	arena := c.arena
	c.arena = nil
	c.frames = nil
	c.byKey = nil
	c.closed = true
	c.mu.Unlock()
	if err := releaseArena(arena); err != nil {
		return fmt.Errorf("simdjson: release Store page cache: %w", err)
	}
	return nil
}
