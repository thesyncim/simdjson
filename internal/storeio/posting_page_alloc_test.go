package storeio

import "testing"

var (
	benchmarkPostingEntry         PostingEntry
	benchmarkPostingPageHeader    PostingPageHeader
	benchmarkPostingSegmentHeader PostingSegmentHeader
)

func testBenchmarkPostingEntries() []PostingEntry {
	entries := make([]PostingEntry, 1900)
	for i := range entries {
		entries[i] = PostingEntry{
			Chunk: uint32(10 + i*2),
			Bits:  uint64(1) << uint((i*17)&63),
		}
	}
	return entries
}

func TestPostingPageSteadyAllocation(t *testing.T) {
	entries := testBenchmarkPostingEntries()
	header := testPostingHeader()
	segments := []PostingSegment{{StreamID: 7, TupleHash: 99, Entries: entries}}
	page := make([]byte, testSuperblockPageSize)
	if _, err := EncodePostingPage(page, header, segments, testPostingNextLogicalID, testPostingIndexHighWater); err != nil {
		t.Fatal(err)
	}
	if allocs := testing.AllocsPerRun(1000, func() {
		if _, err := EncodePostingPage(page, header, segments, testPostingNextLogicalID, testPostingIndexHighWater); err != nil {
			panic(err)
		}
		view, err := OpenPostingPage(page, testPostingNextLogicalID, testPostingIndexHighWater)
		if err != nil {
			panic(err)
		}
		segment, ok := view.Lookup(7)
		if !ok {
			panic("posting lookup missed")
		}
		it := segment.Iterator()
		for range len(entries) {
			if _, ok := it.Next(); !ok {
				panic("posting iterator ended early")
			}
		}
	}); allocs != 0 {
		t.Fatalf("posting-page codec, lookup, and iteration allocations = %g, want 0", allocs)
	}
}

func BenchmarkPostingPage(b *testing.B) {
	entries := testBenchmarkPostingEntries()
	header := testPostingHeader()
	segments := []PostingSegment{{StreamID: 7, TupleHash: 99, Entries: entries}}
	page := make([]byte, testSuperblockPageSize)
	if _, err := EncodePostingPage(page, header, segments, testPostingNextLogicalID, testPostingIndexHighWater); err != nil {
		b.Fatal(err)
	}
	view, err := OpenPostingPage(page, testPostingNextLogicalID, testPostingIndexHighWater)
	if err != nil {
		b.Fatal(err)
	}
	segment, ok := view.Lookup(7)
	if !ok {
		b.Fatal("posting lookup missed")
	}
	b.Run("encode-1900-singletons-packed-4KiB", func(b *testing.B) {
		b.SetBytes(int64(len(page)))
		b.ReportAllocs()
		for range b.N {
			if _, err := EncodePostingPage(page, header, segments, testPostingNextLogicalID, testPostingIndexHighWater); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("open-1900-singletons-packed-4KiB", func(b *testing.B) {
		b.SetBytes(int64(len(page)))
		b.ReportAllocs()
		for range b.N {
			opened, err := OpenPostingPage(page, testPostingNextLogicalID, testPostingIndexHighWater)
			if err != nil {
				b.Fatal(err)
			}
			benchmarkPostingPageHeader = opened.Header()
		}
	})
	b.Run("lookup-one-stream", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			got, hit := view.Lookup(7)
			if !hit {
				b.Fatal("posting lookup missed")
			}
			benchmarkPostingSegmentHeader = got.Header()
		}
	})
	b.Run("iterate-1900-singletons", func(b *testing.B) {
		b.SetBytes(int64(len(entries)))
		b.ReportAllocs()
		for range b.N {
			it := segment.Iterator()
			for range len(entries) {
				benchmarkPostingEntry, _ = it.Next()
			}
		}
	})
}
