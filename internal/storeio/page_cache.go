package storeio

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
)

var (
	// ErrPageCacheClosed reports admission after a cache starts closing.
	ErrPageCacheClosed = errors.New("simdjson: Store page cache closed")
	// ErrPageCacheFull reports that every bounded frame is pinned or loading.
	// Callers apply backpressure or release a lease; the cache never grows.
	ErrPageCacheFull = errors.New("simdjson: Store page cache has no evictable frame")
	// ErrPageLeaseClosed reports repeated release of one lease value.
	ErrPageLeaseClosed = errors.New("simdjson: Store page lease already closed")
	// ErrPageReference reports a physical reference outside the cache contract.
	ErrPageReference = errors.New("simdjson: invalid Store page reference")
)

const (
	cacheFrameEmpty cacheFrameState = iota
	cacheFrameLoading
	cacheFrameReady
	cacheFrameFailed

	cacheTableEmpty     = uint32(0)
	cacheTableTombstone = ^uint32(0)
	cacheFrameClaimed   = uint32(1 << 31)
	cacheFramePinMask   = cacheFrameClaimed - 1
)

type cacheFrameState uint8

// PageCacheOptions fixes the complete resident data budget. FrameSize is the
// largest admitted physical page; ResidentBytes is rounded down to a whole
// number of frames. StoreID binds every admitted page to one file.
type PageCacheOptions struct {
	StoreID       [16]byte
	ResidentBytes int64
	FrameSize     uint32
	// Validate, when non-nil, verifies the kind-specific payload after the
	// cache has verified the common checksum and durable identity. A frame is
	// never published ready unless both validations succeed.
	Validate func([]byte, PageRef) error
}

func (o PageCacheOptions) normalized() (PageCacheOptions, int, error) {
	if o.StoreID == ([16]byte{}) || !validPhysicalPageSize(o.FrameSize) || o.ResidentBytes < int64(o.FrameSize) {
		return PageCacheOptions{}, 0, fmt.Errorf("%w: store id, frame size, or resident budget", ErrPageReference)
	}
	frames64 := o.ResidentBytes / int64(o.FrameSize)
	if frames64 <= 0 || frames64 > int64(maxIntValue()/int(o.FrameSize)) || frames64 >= int64(cacheTableTombstone-1) {
		return PageCacheOptions{}, 0, fmt.Errorf("%w: resident budget overflows address space", ErrPageReference)
	}
	return o, int(frames64), nil
}

type pageReaderAt interface {
	ReadAt([]byte, int64) (int, error)
}

// PageCache is a bounded, checksum-verifying cache of immutable Store pages.
// Its data arena is pointer-free and outside Go HeapAlloc on supported Unix
// systems. The Go heap contains O(frame count) control words, never one object
// per key or database page.
//
// Pin coalesces concurrent misses for the same PageRef. Ready hits probe an
// atomic table and pin through one combined eviction gate without taking the
// miss lock. CLOCK eviction can never claim a pinned frame. Close waits for
// outstanding loads and leases before releasing the arena. The file is
// borrowed and is not closed.
type PageCache struct {
	reader    pageReaderAt
	validate  func([]byte, PageRef) error
	storeID   [16]byte
	frameSize int
	capacity  uint64
	arena     []byte
	frames    []pageCacheFrame
	table     []atomic.Uint32
	tombs     int
	hand      uint32

	mu      sync.RWMutex
	changed *sync.Cond
	loading int
	closing atomic.Bool
	closed  bool

	hits       atomic.Uint64
	misses     atomic.Uint64
	coalesced  atomic.Uint64
	reads      atomic.Uint64
	readBytes  atomic.Uint64
	evictions  atomic.Uint64
	readErrors atomic.Uint64
	prefetches atomic.Uint64
	copyOuts   atomic.Uint64
}

type pageCacheFrame struct {
	ref PageRef
	// sequence is odd while ref or bytes are being replaced and even while
	// stable. A ready reader pins between two equal even observations before
	// it reads ref, so an evictor can never race that read or reuse its bytes.
	sequence atomic.Uint64
	state    atomic.Uint32
	err      error
	// gate combines eviction exclusion and the lease count. An evictor can
	// atomically change exactly zero to cacheFrameClaimed; a reader can only
	// increment an unclaimed value. This avoids the refcount-admission ABA in
	// which a late reader increment could cross frame reuse.
	gate       atomic.Uint32
	referenced atomic.Uint32
}

// NewPageCache allocates a bounded cache over file. It performs no I/O and
// does not take ownership of file.
func NewPageCache(file *os.File, options PageCacheOptions) (*PageCache, error) {
	if file == nil {
		return nil, fmt.Errorf("%w: nil file", ErrPageReference)
	}
	return newPageCache(file, options)
}

func newPageCache(reader pageReaderAt, options PageCacheOptions) (*PageCache, error) {
	options, frameCount, err := options.normalized()
	if err != nil {
		return nil, err
	}
	if reader == nil {
		return nil, fmt.Errorf("%w: nil reader", ErrPageReference)
	}
	arena, err := allocateArena(frameCount * int(options.FrameSize))
	if err != nil {
		return nil, fmt.Errorf("allocate Store page cache: %w", err)
	}
	tableSize := 2
	for tableSize < frameCount*2 {
		tableSize <<= 1
	}
	cache := &PageCache{
		reader:    reader,
		validate:  options.Validate,
		storeID:   options.StoreID,
		frameSize: int(options.FrameSize),
		capacity:  uint64(len(arena)),
		arena:     arena,
		frames:    make([]pageCacheFrame, frameCount),
		table:     make([]atomic.Uint32, tableSize),
	}
	cache.changed = sync.NewCond(&cache.mu)
	return cache, nil
}

// PageLease pins one admitted frame. It must not be copied and must be closed.
// Bytes becomes invalid immediately after Close. A zero lease is closed.
type PageLease struct {
	cache *PageCache
	frame uint32
	epoch uint64
	bytes []byte
}

// Bytes returns the complete checksum-covered physical page. The slice aliases
// the cache arena and remains valid only until Close.
func (l *PageLease) Bytes() []byte {
	if l == nil {
		return nil
	}
	return l.bytes
}

// Close releases the frame pin. It is safe to call twice on the same lease
// variable; copying a live lease is invalid.
func (l *PageLease) Close() error {
	if l == nil || l.cache == nil {
		return nil
	}
	cache := l.cache
	index := l.frame
	epoch := l.epoch
	l.cache, l.bytes = nil, nil

	if int(index) >= len(cache.frames) {
		return ErrPageLeaseClosed
	}
	frame := &cache.frames[index]
	current := frame.sequence.Load()
	if current != epoch {
		return fmt.Errorf("%w: frame=%d lease-epoch=%d frame-epoch=%d pins=%d",
			ErrPageLeaseClosed, index, epoch, current, framePinCount(frame))
	}
	pins, ok := releaseFramePin(frame)
	if !ok {
		return ErrPageLeaseClosed
	}
	if pins == 1 && cache.closing.Load() {
		cache.mu.Lock()
		cache.changed.Broadcast()
		cache.mu.Unlock()
	}
	return nil
}

// Pin admits ref and returns a lease. A hit performs no I/O or allocation.
// The first miss reader verifies the page checksum and full durable identity;
// concurrent readers of the same reference wait for that result.
func (c *PageCache) Pin(ref PageRef) (PageLease, error) {
	if c == nil {
		return PageLease{}, ErrPageCacheClosed
	}
	if err := c.validateRef(ref); err != nil {
		return PageLease{}, err
	}
	hash := cacheRefHash(ref)
	for {
		if c.closing.Load() {
			return PageLease{}, ErrPageCacheClosed
		}
		if lease, ok := c.tryPinReady(hash, ref); ok {
			c.hits.Add(1)
			return lease, nil
		}

		c.mu.Lock()
		if c.closed || c.closing.Load() {
			c.mu.Unlock()
			return PageLease{}, ErrPageCacheClosed
		}
		index, found := c.lookupLocked(hash, ref)
		if found {
			frame := &c.frames[index]
			switch cacheFrameState(frame.state.Load()) {
			case cacheFrameReady:
				if !tryFramePin(frame) {
					c.mu.Unlock()
					continue
				}
				markFrameReferenced(frame)
				lease := c.leaseLocked(index)
				c.mu.Unlock()
				c.hits.Add(1)
				return lease, nil
			case cacheFrameFailed:
				err := frame.err
				c.mu.Unlock()
				return PageLease{}, err
			case cacheFrameLoading:
				c.coalesced.Add(1)
				for !c.closed && cacheFrameState(frame.state.Load()) == cacheFrameLoading && frame.ref == ref {
					c.changed.Wait()
				}
				c.mu.Unlock()
				continue
			}
		}

		var frame *pageCacheFrame
		claimed := false
		for range len(c.frames) * 2 {
			index, found = c.victimLocked()
			if !found {
				break
			}
			frame = &c.frames[index]
			if frame.gate.CompareAndSwap(0, cacheFrameClaimed) {
				claimed = true
				break
			}
			markFrameReferenced(frame)
		}
		if !claimed {
			c.mu.Unlock()
			return PageLease{}, ErrPageCacheFull
		}
		// Publish the replacement epoch before changing ref or bytes. A table
		// probe that raced removal either fails the claimed gate or observes
		// this odd sequence and retries. Completion advances it to even.
		frame.sequence.Add(1)
		if cacheFrameState(frame.state.Load()) != cacheFrameEmpty {
			c.removeLocked(cacheRefHash(frame.ref), frame.ref)
			c.evictions.Add(1)
		}
		frame.ref = ref
		frame.state.Store(uint32(cacheFrameLoading))
		frame.err = nil
		markFrameReferenced(frame)
		c.insertLocked(hash, index)
		c.loading++
		c.misses.Add(1)
		c.reads.Add(1)
		c.mu.Unlock()

		page := c.frameBytes(index, int(ref.Length))
		err := c.readPage(page, ref)
		c.mu.Lock()
		c.loading--
		if err != nil {
			frame.state.Store(uint32(cacheFrameFailed))
			frame.err = err
			frame.sequence.Add(1)
			frame.gate.Store(0)
			c.readErrors.Add(1)
			c.changed.Broadcast()
			c.mu.Unlock()
			return PageLease{}, err
		}
		frame.state.Store(uint32(cacheFrameReady))
		frame.sequence.Add(1)
		frame.gate.Store(1)
		lease := c.leaseLocked(index)
		c.readBytes.Add(uint64(ref.Length))
		c.changed.Broadcast()
		c.mu.Unlock()
		return lease, nil
	}
}

// tryPinReady is the resident read path. The table can transiently point at a
// frame being replaced; the odd/even sequence makes that harmless. Once pins
// is incremented between equal even sequence observations, eviction must skip
// the frame and ref plus arena bytes are stable until the lease is released.
func (c *PageCache) tryPinReady(hash uint64, ref PageRef) (PageLease, bool) {
	mask := uint64(len(c.table) - 1)
	for probe := uint64(0); probe < uint64(len(c.table)); probe++ {
		entry := c.table[(hash+probe)&mask].Load()
		if entry == cacheTableEmpty {
			return PageLease{}, false
		}
		if entry == cacheTableTombstone {
			continue
		}
		index := entry - 1
		frame := &c.frames[index]
		sequence := frame.sequence.Load()
		if sequence&1 != 0 {
			continue
		}
		if !tryFramePin(frame) {
			continue
		}
		if c.closing.Load() || frame.sequence.Load() != sequence ||
			cacheFrameState(frame.state.Load()) != cacheFrameReady {
			_, _ = releaseFramePin(frame)
			continue
		}
		if frame.ref != ref {
			_, _ = releaseFramePin(frame)
			continue
		}
		markFrameReferenced(frame)
		return PageLease{
			cache: c, frame: index, epoch: sequence,
			bytes: c.frameBytes(index, int(ref.Length)),
		}, true
	}
	return PageLease{}, false
}

func markFrameReferenced(frame *pageCacheFrame) {
	if frame.referenced.Load() == 0 {
		frame.referenced.CompareAndSwap(0, 1)
	}
}

func tryFramePin(frame *pageCacheFrame) bool {
	for {
		gate := frame.gate.Load()
		if gate&cacheFrameClaimed != 0 || gate == cacheFramePinMask {
			return false
		}
		if frame.gate.CompareAndSwap(gate, gate+1) {
			return true
		}
	}
}

func releaseFramePin(frame *pageCacheFrame) (uint32, bool) {
	for {
		gate := frame.gate.Load()
		if gate == 0 || gate&cacheFrameClaimed != 0 {
			return 0, false
		}
		if frame.gate.CompareAndSwap(gate, gate-1) {
			return gate, true
		}
	}
}

func framePinCount(frame *pageCacheFrame) uint32 { return frame.gate.Load() & cacheFramePinMask }

func (c *PageCache) readPage(dst []byte, ref PageRef) error {
	n, err := c.reader.ReadAt(dst, int64(ref.Offset))
	if n != len(dst) {
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		return fmt.Errorf("%w: short read at %d: %w", ErrPageCorrupt, ref.Offset, err)
	}
	if err != nil {
		return fmt.Errorf("read Store page at %d: %w", ref.Offset, err)
	}
	header, _, err := OpenPage(dst)
	if err != nil {
		return err
	}
	if header.StoreID != c.storeID || header.PageSize != ref.Length || header.Kind != ref.Kind ||
		header.Flags != ref.Flags || header.LogicalID != ref.LogicalID || header.Generation != ref.Generation {
		return fmt.Errorf("%w: physical page identity does not match reference", ErrPageCorrupt)
	}
	if c.validate != nil {
		if err := c.validate(dst, ref); err != nil {
			return err
		}
	}
	return nil
}

// AppendPage appends a verified physical page to dst and releases its frame.
// It is the lifetime-independent, caller-buffered read path.
func (c *PageCache) AppendPage(dst []byte, ref PageRef) ([]byte, error) {
	lease, err := c.Pin(ref)
	if err != nil {
		return dst, err
	}
	dst = append(dst, lease.Bytes()...)
	c.copyOuts.Add(1)
	if err := lease.Close(); err != nil {
		return dst, err
	}
	return dst, nil
}

// Prefetch admits refs in caller order and immediately releases each lease.
// The cache stays within its fixed budget; a working set larger than the cache
// may evict earlier refs. Duplicate refs become ordinary cache hits.
func (c *PageCache) Prefetch(refs []PageRef) error {
	for _, ref := range refs {
		lease, err := c.Pin(ref)
		if err != nil {
			return err
		}
		c.prefetches.Add(1)
		if err := lease.Close(); err != nil {
			return err
		}
	}
	return nil
}

// Invalidate removes one failed or ready unpinned reference. It is useful after
// a transient read failure or file repair. A loading or pinned page is left in
// place and reports false.
func (c *PageCache) Invalidate(ref PageRef) bool {
	if c == nil {
		return false
	}
	hash := cacheRefHash(ref)
	c.mu.Lock()
	defer c.mu.Unlock()
	index, ok := c.lookupLocked(hash, ref)
	if !ok {
		return false
	}
	frame := &c.frames[index]
	if cacheFrameState(frame.state.Load()) == cacheFrameLoading ||
		!frame.gate.CompareAndSwap(0, cacheFrameClaimed) {
		return false
	}
	c.removeLocked(hash, ref)
	frame.ref = PageRef{}
	frame.sequence.Add(1)
	frame.state.Store(uint32(cacheFrameEmpty))
	frame.err = nil
	frame.ref = PageRef{}
	frame.referenced.Store(0)
	frame.sequence.Add(1)
	frame.gate.Store(0)
	return true
}

// PageCacheStats is a coherent control-plane snapshot plus monotonic counters.
type PageCacheStats struct {
	CapacityBytes uint64
	ResidentBytes uint64
	FrameSize     uint32
	Frames        uint32
	ReadyFrames   uint32
	LoadingFrames uint32
	FailedFrames  uint32
	PinnedFrames  uint32
	Pins          uint64
	Hits          uint64
	Misses        uint64
	Coalesced     uint64
	PageReads     uint64
	ReadBytes     uint64
	Evictions     uint64
	ReadErrors    uint64
	Prefetches    uint64
	CopyOuts      uint64
}

// Stats reports bounded residency and I/O behavior without allocating.
func (c *PageCache) Stats() PageCacheStats {
	if c == nil {
		return PageCacheStats{}
	}
	stats := PageCacheStats{
		CapacityBytes: c.capacity,
		FrameSize:     uint32(c.frameSize),
		Frames:        uint32(len(c.frames)),
		Hits:          c.hits.Load(),
		Misses:        c.misses.Load(),
		Coalesced:     c.coalesced.Load(),
		PageReads:     c.reads.Load(),
		ReadBytes:     c.readBytes.Load(),
		Evictions:     c.evictions.Load(),
		ReadErrors:    c.readErrors.Load(),
		Prefetches:    c.prefetches.Load(),
		CopyOuts:      c.copyOuts.Load(),
	}
	c.mu.RLock()
	for i := range c.frames {
		frame := &c.frames[i]
		pins := framePinCount(frame)
		stats.Pins += uint64(pins)
		if pins != 0 {
			stats.PinnedFrames++
		}
		switch cacheFrameState(frame.state.Load()) {
		case cacheFrameReady:
			stats.ReadyFrames++
			stats.ResidentBytes += uint64(frame.ref.Length)
		case cacheFrameLoading:
			stats.LoadingFrames++
		case cacheFrameFailed:
			stats.FailedFrames++
		}
	}
	c.mu.RUnlock()
	return stats
}

// Close prevents new admissions, waits for all loads and leases, and releases
// the external arena. It does not close the borrowed file.
func (c *PageCache) Close() error {
	if c == nil {
		return nil
	}
	c.closing.Store(true)
	c.mu.Lock()
	for !c.closed && (c.loading != 0 || c.pinCountLocked() != 0) {
		c.changed.Wait()
	}
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	arena := c.arena
	c.arena = nil
	// Keep the small Go control slices until PageCache itself becomes
	// unreachable. A Pin racing the closing transition may already hold a
	// frame pointer; the post-pin closing check prevents it from touching the
	// released arena, while retaining these headers makes that check safe.
	c.reader = nil
	c.validate = nil
	c.mu.Unlock()
	if err := releaseArena(arena); err != nil {
		return fmt.Errorf("release Store page cache: %w", err)
	}
	return nil
}

func (c *PageCache) validateRef(ref PageRef) error {
	if ref.Offset > uint64(^uint64(0)>>1) || ref.LogicalID == 0 || ref.Generation == 0 ||
		ref.Flags != 0 || !validPageKind(ref.Kind) || !validPhysicalPageSize(ref.Length) ||
		int(ref.Length) > c.frameSize || ref.Offset%uint64(physicalPageQuantum) != 0 {
		return fmt.Errorf("%w: %+v", ErrPageReference, ref)
	}
	return nil
}

func (c *PageCache) leaseLocked(index uint32) PageLease {
	frame := &c.frames[index]
	return PageLease{
		cache: c,
		frame: index,
		epoch: frame.sequence.Load(),
		bytes: c.frameBytes(index, int(frame.ref.Length)),
	}
}

func (c *PageCache) frameBytes(index uint32, length int) []byte {
	start := int(index) * c.frameSize
	return c.arena[start : start+length : start+length]
}

func (c *PageCache) victimLocked() (uint32, bool) {
	for i := range c.frames {
		if cacheFrameState(c.frames[i].state.Load()) == cacheFrameEmpty {
			return uint32(i), true
		}
	}
	limit := len(c.frames) * 2
	for range limit {
		index := c.hand
		c.hand++
		if c.hand == uint32(len(c.frames)) {
			c.hand = 0
		}
		frame := &c.frames[index]
		state := cacheFrameState(frame.state.Load())
		if state == cacheFrameLoading || frame.gate.Load() != 0 {
			continue
		}
		if state == cacheFrameReady && frame.referenced.Swap(0) != 0 {
			continue
		}
		return index, true
	}
	return 0, false
}

func (c *PageCache) lookupLocked(hash uint64, ref PageRef) (uint32, bool) {
	mask := uint64(len(c.table) - 1)
	for probe := uint64(0); probe < uint64(len(c.table)); probe++ {
		entry := c.table[(hash+probe)&mask].Load()
		if entry == cacheTableEmpty {
			return 0, false
		}
		if entry == cacheTableTombstone {
			continue
		}
		index := entry - 1
		if c.frames[index].ref == ref {
			return index, true
		}
	}
	return 0, false
}

func (c *PageCache) insertLocked(hash uint64, index uint32) {
	if c.tombs > len(c.table)/4 {
		c.rebuildTableLocked()
	}
	mask := uint64(len(c.table) - 1)
	firstTomb := -1
	for probe := uint64(0); probe < uint64(len(c.table)); probe++ {
		slot := int((hash + probe) & mask)
		switch c.table[slot].Load() {
		case cacheTableEmpty:
			if firstTomb >= 0 {
				slot = firstTomb
				c.tombs--
			}
			c.table[slot].Store(index + 1)
			return
		case cacheTableTombstone:
			if firstTomb < 0 {
				firstTomb = slot
			}
		}
	}
	if firstTomb >= 0 {
		c.table[firstTomb].Store(index + 1)
		c.tombs--
		return
	}
	panic("storeio: page-cache table capacity invariant")
}

func (c *PageCache) removeLocked(hash uint64, ref PageRef) {
	mask := uint64(len(c.table) - 1)
	for probe := uint64(0); probe < uint64(len(c.table)); probe++ {
		slot := (hash + probe) & mask
		entry := c.table[slot].Load()
		if entry == cacheTableEmpty {
			return
		}
		if entry != cacheTableTombstone && c.frames[entry-1].ref == ref {
			c.table[slot].Store(cacheTableTombstone)
			c.tombs++
			return
		}
	}
}

func (c *PageCache) rebuildTableLocked() {
	for i := range c.table {
		c.table[i].Store(cacheTableEmpty)
	}
	c.tombs = 0
	for i := range c.frames {
		if cacheFrameState(c.frames[i].state.Load()) != cacheFrameEmpty {
			c.insertLocked(cacheRefHash(c.frames[i].ref), uint32(i))
		}
	}
}

func (c *PageCache) pinCountLocked() uint64 {
	var total uint64
	for i := range c.frames {
		total += uint64(framePinCount(&c.frames[i]))
	}
	return total
}

func cacheRefHash(ref PageRef) uint64 {
	x := ref.Offset ^ ref.LogicalID*0x9e3779b97f4a7c15 ^ ref.Generation*0xbf58476d1ce4e5b9
	x ^= uint64(ref.Length)<<32 | uint64(ref.Kind)<<8 | uint64(ref.Flags)
	x ^= x >> 30
	x *= 0xbf58476d1ce4e5b9
	x ^= x >> 27
	x *= 0x94d049bb133111eb
	return x ^ x>>31
}

func maxIntValue() int { return int(^uint(0) >> 1) }
