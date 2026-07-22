package storeio

import (
	"errors"
	"io"
	"os"
	"testing"
	"time"
)

const pageCacheTestPageSize = 4096

func TestPageCacheBoundedAdmissionEvictionAndIdentity(t *testing.T) {
	file, storeID, refs := newPageCacheFixture(t, 3)
	cache, err := NewPageCache(file, PageCacheOptions{
		PageSize: pageCacheTestPageSize, ResidentBytes: 2 * pageCacheTestPageSize,
		StoreID: storeID, PrefetchQueue: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := cache.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	first, err := cache.Acquire(refs[0])
	if err != nil || len(first.Payload()) != 32 || first.Payload()[0] != 1 {
		t.Fatalf("first Acquire = payload %v, %v", first.Payload(), err)
	}
	second, err := cache.Acquire(refs[1])
	if err != nil || second.Payload()[0] != 2 {
		t.Fatalf("second Acquire = payload %v, %v", second.Payload(), err)
	}
	if _, err := cache.Acquire(refs[2]); !errors.Is(err, ErrPageCachePinned) {
		t.Fatalf("fully pinned Acquire error = %v, want %v", err, ErrPageCachePinned)
	}

	first.Release()
	third, err := cache.Acquire(refs[2])
	if err != nil || third.Payload()[0] != 3 {
		t.Fatalf("third Acquire = payload %v, %v", third.Payload(), err)
	}
	third.Release()
	second.Release()

	stats := cache.Stats()
	if stats.CapacityBytes != 2*pageCacheTestPageSize ||
		stats.ResidentBytes != stats.CapacityBytes || stats.PinnedPages != 0 ||
		stats.PageReads != 3 || stats.ReadBytes != 3*pageCacheTestPageSize || stats.Evictions != 1 {
		t.Fatalf("unexpected stats: %+v", stats)
	}

	wrong := refs[2]
	wrong.LogicalID++
	if _, err := cache.Acquire(wrong); !errors.Is(err, ErrPageCacheReference) {
		t.Fatalf("identity mismatch error = %v, want %v", err, ErrPageCacheReference)
	}
}

func TestPageCachePrefetchOrderingAndHit(t *testing.T) {
	file, storeID, refs := newPageCacheFixture(t, 3)
	cache, err := NewPageCache(file, PageCacheOptions{
		PageSize: pageCacheTestPageSize, ResidentBytes: 3 * pageCacheTestPageSize,
		StoreID: storeID, PrefetchQueue: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := cache.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	if queued, err := cache.Prefetch([]PageRef{refs[1], refs[0]}); queued != 0 ||
		!errors.Is(err, ErrPageCacheReference) {
		t.Fatalf("unordered Prefetch = %d, %v", queued, err)
	}
	if queued, err := cache.Prefetch(refs[:2]); err != nil || queued != 2 {
		t.Fatalf("Prefetch = %d, %v", queued, err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for cache.Stats().PageReads < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if stats := cache.Stats(); stats.PageReads != 2 {
		t.Fatalf("prefetch did not complete: %+v", stats)
	}
	lease, err := cache.Acquire(refs[0])
	if err != nil {
		t.Fatal(err)
	}
	lease.Release()
	stats := cache.Stats()
	if stats.CacheHits != 1 || stats.PrefetchHits != 1 || stats.PrefetchQueued != 2 {
		t.Fatalf("unexpected prefetch stats: %+v", stats)
	}
}

func TestPageCacheRejectsCorruptionAndShortRead(t *testing.T) {
	file, storeID, refs := newPageCacheFixture(t, 2)
	cache, err := NewPageCache(file, PageCacheOptions{
		PageSize: pageCacheTestPageSize, ResidentBytes: pageCacheTestPageSize,
		StoreID: storeID,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := cache.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	if _, err := file.WriteAt([]byte{0xff}, int64(refs[0].Offset+PageHeaderSize+3)); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Acquire(refs[0]); !errors.Is(err, ErrPageCorrupt) {
		t.Fatalf("corrupt Acquire error = %v, want %v", err, ErrPageCorrupt)
	}
	if err := file.Truncate(int64(refs[1].Offset + uint64(refs[1].Length)/2)); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Acquire(refs[1]); !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("short Acquire error = %v, want EOF", err)
	}
}

func TestPageCacheCloseRequiresReleasedLeases(t *testing.T) {
	file, storeID, refs := newPageCacheFixture(t, 1)
	cache, err := NewPageCache(file, PageCacheOptions{
		PageSize: pageCacheTestPageSize, ResidentBytes: pageCacheTestPageSize,
		StoreID: storeID,
	})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := cache.Acquire(refs[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.Close(); !errors.Is(err, ErrPageCachePinned) {
		t.Fatalf("Close with lease = %v, want %v", err, ErrPageCachePinned)
	}
	if _, err := cache.Acquire(refs[0]); !errors.Is(err, ErrPageCacheClosed) {
		t.Fatalf("Acquire while closing = %v, want %v", err, ErrPageCacheClosed)
	}
	lease.Release()
	if err := cache.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cache.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPageCacheWarmAcquireSteadyAllocation(t *testing.T) {
	file, storeID, refs := newPageCacheFixture(t, 1)
	cache, err := NewPageCache(file, PageCacheOptions{
		PageSize: pageCacheTestPageSize, ResidentBytes: pageCacheTestPageSize,
		StoreID: storeID,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	lease, err := cache.Acquire(refs[0])
	if err != nil {
		t.Fatal(err)
	}
	lease.Release()
	allocs := testing.AllocsPerRun(1000, func() {
		lease, acquireErr := cache.Acquire(refs[0])
		if acquireErr != nil {
			panic(acquireErr)
		}
		if lease.Payload()[0] != 1 {
			panic("wrong page")
		}
		lease.Release()
	})
	if allocs != 0 {
		t.Fatalf("warm Acquire/Release allocations = %v, want 0", allocs)
	}
}

func TestPageCacheVariableDocumentExtent(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "store-page-cache-variable-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	storeID := [16]byte{9, 7, 5, 3, 1, 2, 4, 6, 8, 10, 12, 14, 16, 15, 13, 11}
	const extentSize = 2 * pageCacheTestPageSize
	page := make([]byte, extentSize)
	payload, err := InitPage(page, PageHeader{
		StoreID: storeID, Generation: 3, LogicalID: 7,
		PageSize: extentSize, PayloadLength: 17, Kind: PageDocument,
	})
	if err != nil {
		t.Fatal(err)
	}
	copy(payload, "variable document")
	if _, err := SealPage(page); err != nil {
		t.Fatal(err)
	}
	ref := PageRef{
		Offset: 2 * pageCacheTestPageSize, LogicalID: 7, Generation: 3,
		Length: extentSize, Kind: PageDocument,
	}
	if _, err := file.WriteAt(page, int64(ref.Offset)); err != nil {
		t.Fatal(err)
	}
	cache, err := NewPageCache(file, PageCacheOptions{
		PageSize: pageCacheTestPageSize, MaxPageSize: extentSize,
		ResidentBytes: extentSize, StoreID: storeID, ReadConcurrency: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := cache.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	lease, err := cache.Acquire(ref)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(lease.Payload()); got != "variable document" {
		t.Fatalf("Payload = %q", got)
	}
	if got := lease.Header().PageSize; got != extentSize {
		t.Fatalf("PageSize = %d, want %d", got, extentSize)
	}
	lease.Release()

	metadata := ref
	metadata.Kind = PageKeyDirectory
	if _, err := cache.Acquire(metadata); !errors.Is(err, ErrPageCacheReference) {
		t.Fatalf("oversize metadata error = %v, want %v", err, ErrPageCacheReference)
	}
	stats := cache.Stats()
	if stats.CapacityBytes != extentSize || stats.ResidentBytes != extentSize ||
		stats.PageReads != 1 || stats.ReadBytes != extentSize {
		t.Fatalf("unexpected variable-extent stats: %+v", stats)
	}
}

func newPageCacheFixture(t testing.TB, count int) (*os.File, [16]byte, []PageRef) {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "store-page-cache-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { file.Close() })
	storeID := [16]byte{1, 3, 5, 7, 9, 11, 13, 15, 2, 4, 6, 8, 10, 12, 14, 16}
	refs := make([]PageRef, count)
	for i := range refs {
		offset := uint64(2+i) * pageCacheTestPageSize
		logicalID := uint64(i + 2)
		page := make([]byte, pageCacheTestPageSize)
		payload, initErr := InitPage(page, PageHeader{
			StoreID: storeID, Generation: 1, LogicalID: logicalID,
			PageSize: pageCacheTestPageSize, PayloadLength: 32, Kind: PageDocument,
		})
		if initErr != nil {
			t.Fatal(initErr)
		}
		payload[0] = byte(i + 1)
		if _, sealErr := SealPage(page); sealErr != nil {
			t.Fatal(sealErr)
		}
		if _, writeErr := file.WriteAt(page, int64(offset)); writeErr != nil {
			t.Fatal(writeErr)
		}
		refs[i] = PageRef{
			Offset: offset, LogicalID: logicalID, Generation: 1,
			Length: pageCacheTestPageSize, Kind: PageDocument,
		}
	}
	return file, storeID, refs
}
