package storeio

import (
	"bytes"
	"errors"
	"io"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const cacheTestPageSize = 4096

var cacheTestStoreID = [16]byte{0xc1, 0xa0, 0x5e, 0x44, 1}

func cacheTestImage(t testing.TB, count int) ([]byte, []PageRef) {
	t.Helper()
	image := make([]byte, count*cacheTestPageSize)
	refs := make([]PageRef, count)
	for i := range count {
		page := image[i*cacheTestPageSize : (i+1)*cacheTestPageSize]
		header := PageHeader{
			StoreID:       cacheTestStoreID,
			Generation:    1,
			LogicalID:     uint64(i + 2),
			PageSize:      cacheTestPageSize,
			PayloadLength: 16,
			Kind:          PageDocument,
		}
		payload, err := InitPage(page, header)
		if err != nil {
			t.Fatal(err)
		}
		for j := range payload {
			payload[j] = byte(i + j + 1)
		}
		if _, err := SealPage(page); err != nil {
			t.Fatal(err)
		}
		refs[i] = PageRef{
			Offset:     uint64(i * cacheTestPageSize),
			LogicalID:  header.LogicalID,
			Generation: header.Generation,
			Length:     header.PageSize,
			Kind:       header.Kind,
		}
	}
	return image, refs
}

func newCacheForTest(t testing.TB, reader pageReaderAt, frames int) *PageCache {
	t.Helper()
	cache, err := newPageCache(reader, PageCacheOptions{
		StoreID:       cacheTestStoreID,
		ResidentBytes: int64(frames * cacheTestPageSize),
		FrameSize:     cacheTestPageSize,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := cache.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return cache
}

func TestPageCacheBoundedCLOCKAndPinnedBackpressure(t *testing.T) {
	image, refs := cacheTestImage(t, 4)
	cache := newCacheForTest(t, bytes.NewReader(image), 2)

	for _, ref := range refs[:2] {
		lease, err := cache.Pin(ref)
		if err != nil {
			t.Fatal(err)
		}
		if len(lease.Bytes()) != cacheTestPageSize {
			t.Fatalf("lease length = %d", len(lease.Bytes()))
		}
		if err := lease.Close(); err != nil {
			t.Fatal(err)
		}
	}
	hit, err := cache.Pin(refs[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := hit.Close(); err != nil {
		t.Fatal(err)
	}
	third, err := cache.Pin(refs[2])
	if err != nil {
		t.Fatal(err)
	}
	if err := third.Close(); err != nil {
		t.Fatal(err)
	}
	stats := cache.Stats()
	if stats.CapacityBytes != 2*cacheTestPageSize || stats.ResidentBytes > stats.CapacityBytes ||
		stats.Misses != 3 || stats.Hits != 1 || stats.Evictions != 1 || stats.PageReads != 3 {
		t.Fatalf("stats = %+v", stats)
	}

	first, err := cache.Pin(refs[0])
	if err != nil {
		t.Fatal(err)
	}
	second, err := cache.Pin(refs[2])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Pin(refs[3]); !errors.Is(err, ErrPageCacheFull) {
		t.Fatalf("all-pinned admission error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}

type blockingPageReader struct {
	data    []byte
	started chan struct{}
	release chan struct{}
	once    sync.Once
	reads   atomic.Uint64
}

func (r *blockingPageReader) ReadAt(dst []byte, offset int64) (int, error) {
	r.reads.Add(1)
	r.once.Do(func() { close(r.started) })
	<-r.release
	return bytes.NewReader(r.data).ReadAt(dst, offset)
}

func TestPageCacheCoalescesConcurrentMiss(t *testing.T) {
	image, refs := cacheTestImage(t, 1)
	reader := &blockingPageReader{
		data: image, started: make(chan struct{}), release: make(chan struct{}),
	}
	cache := newCacheForTest(t, reader, 2)
	const workers = 16
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wait sync.WaitGroup
	wait.Add(workers)
	for range workers {
		go func() {
			defer wait.Done()
			<-start
			lease, err := cache.Pin(refs[0])
			if err == nil {
				err = lease.Close()
			}
			errs <- err
		}()
	}
	close(start)
	<-reader.started
	deadline := time.Now().Add(5 * time.Second)
	for cache.Stats().Coalesced < workers-1 && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	close(reader.release)
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	stats := cache.Stats()
	if reader.reads.Load() != 1 || stats.PageReads != 1 || stats.Misses != 1 || stats.Coalesced != workers-1 {
		t.Fatalf("coalescing stats = %+v, reads=%d", stats, reader.reads.Load())
	}
}

func TestPageCacheRejectsCorruptionAndCanRetry(t *testing.T) {
	image, refs := cacheTestImage(t, 1)
	image[100] ^= 0xff
	cache := newCacheForTest(t, bytes.NewReader(image), 1)
	if _, err := cache.Pin(refs[0]); !errors.Is(err, ErrPageCorrupt) {
		t.Fatalf("corrupt Pin error = %v", err)
	}
	if stats := cache.Stats(); stats.ReadErrors != 1 || stats.FailedFrames != 1 {
		t.Fatalf("failed stats = %+v", stats)
	}
	if !cache.Invalidate(refs[0]) {
		t.Fatal("failed frame was not invalidated")
	}
	if stats := cache.Stats(); stats.FailedFrames != 0 {
		t.Fatalf("post-invalidate stats = %+v", stats)
	}
}

func TestPageCacheClassifiesShortReadAsCorruption(t *testing.T) {
	image, refs := cacheTestImage(t, 1)
	cache := newCacheForTest(t, bytes.NewReader(image[:len(image)-1]), 1)
	if _, err := cache.Pin(refs[0]); !errors.Is(err, ErrPageCorrupt) || !errors.Is(err, io.EOF) {
		t.Fatalf("short read error = %v, want corruption and EOF", err)
	}
}

func TestPageCacheCloseWaitsForLease(t *testing.T) {
	image, refs := cacheTestImage(t, 1)
	cache := newCacheForTest(t, bytes.NewReader(image), 1)
	lease, err := cache.Pin(refs[0])
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cache.Close() }()
	select {
	case err := <-done:
		t.Fatalf("Close returned with live lease: %v", err)
	case <-time.After(10 * time.Millisecond):
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not resume")
	}
}

func TestPageCacheAppendPageAllocs(t *testing.T) {
	image, refs := cacheTestImage(t, 1)
	cache := newCacheForTest(t, bytes.NewReader(image), 1)
	dst := make([]byte, 0, cacheTestPageSize)
	var err error
	if dst, err = cache.AppendPage(dst[:0], refs[0]); err != nil {
		t.Fatal(err)
	}
	allocs := testing.AllocsPerRun(100, func() {
		dst, err = cache.AppendPage(dst[:0], refs[0])
		if err != nil {
			panic(err)
		}
	})
	if allocs != 0 {
		t.Fatalf("warm AppendPage allocated %.2f times, want zero", allocs)
	}
}

func TestPageCachePublishesEvenStableEpoch(t *testing.T) {
	image, refs := cacheTestImage(t, 1)
	cache := newCacheForTest(t, bytes.NewReader(image), 1)
	lease, err := cache.Pin(refs[0])
	if err != nil {
		t.Fatal(err)
	}
	if sequence := cache.frames[0].sequence.Load(); sequence == 0 || sequence&1 != 0 {
		t.Fatalf("published frame sequence = %d, want non-zero even epoch", sequence)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	lease, err = cache.Pin(refs[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if stats := cache.Stats(); stats.Hits != 1 || stats.Misses != 1 {
		t.Fatalf("cache stats = %+v, want one resident hit", stats)
	}
}

func TestPageCacheAcceptsQuantumAlignedVariableExtent(t *testing.T) {
	const large = 8192
	image := make([]byte, cacheTestPageSize+large)
	page := image[cacheTestPageSize:]
	header := PageHeader{
		StoreID: cacheTestStoreID, Generation: 1, LogicalID: 2,
		PageSize: large, PayloadLength: 16, Kind: PageDocument,
	}
	payload, err := InitPage(page, header)
	if err != nil {
		t.Fatal(err)
	}
	copy(payload, "variable extent")
	if _, err := SealPage(page); err != nil {
		t.Fatal(err)
	}
	cache, err := newPageCache(bytes.NewReader(image), PageCacheOptions{
		StoreID: cacheTestStoreID, ResidentBytes: 2 * large, FrameSize: large,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := cache.Close(); err != nil {
			t.Error(err)
		}
	}()
	lease, err := cache.Pin(PageRef{
		Offset: cacheTestPageSize, LogicalID: 2, Generation: 1,
		Length: large, Kind: PageDocument,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPageCacheRunsPayloadValidatorOncePerAdmission(t *testing.T) {
	image, refs := cacheTestImage(t, 1)
	var calls atomic.Uint64
	cache, err := newPageCache(bytes.NewReader(image), PageCacheOptions{
		StoreID: cacheTestStoreID, ResidentBytes: cacheTestPageSize, FrameSize: cacheTestPageSize,
		Validate: func(page []byte, ref PageRef) error {
			calls.Add(1)
			if len(page) != cacheTestPageSize || ref != refs[0] {
				return errors.New("unexpected validation input")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	for range 10 {
		lease, err := cache.Pin(refs[0])
		if err != nil {
			t.Fatal(err)
		}
		if err := lease.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("validator calls = %d, want one admission", calls.Load())
	}
}

func TestPageCacheConcurrentHitsAndEvictions(t *testing.T) {
	image, refs := cacheTestImage(t, 32)
	cache := newCacheForTest(t, bytes.NewReader(image), 4)
	const workers = 16
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wait sync.WaitGroup
	wait.Add(workers)
	for worker := range workers {
		go func() {
			defer wait.Done()
			<-start
			state := uint32(worker + 1)
			for range 2000 {
				state = state*1664525 + 1013904223
				ref := refs[state%uint32(len(refs))]
				lease, err := cache.Pin(ref)
				if errors.Is(err, ErrPageCacheFull) {
					runtime.Gosched()
					continue
				}
				if err != nil {
					errs <- err
					return
				}
				header, _, err := OpenPage(lease.Bytes())
				if err == nil && (header.LogicalID != ref.LogicalID || header.Generation != ref.Generation) {
					err = errors.New("lease observed a replaced frame")
				}
				if closeErr := lease.Close(); err == nil {
					err = closeErr
				}
				if err != nil {
					errs <- err
					return
				}
			}
			errs <- nil
		}()
	}
	close(start)
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func BenchmarkPageCacheResidentHit(b *testing.B) {
	image, refs := cacheTestImage(b, 1)
	cache := newCacheForTest(b, bytes.NewReader(image), 1)
	lease, err := cache.Pin(refs[0])
	if err != nil {
		b.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		lease, err = cache.Pin(refs[0])
		if err != nil {
			b.Fatal(err)
		}
		if err = lease.Close(); err != nil {
			b.Fatal(err)
		}
	}
}
