package storeio

import "testing"

var benchmarkChunkDirectoryRef PageRef

func TestChunkDirectorySteadyAllocation(t *testing.T) {
	header := testChunkDirectoryHeader(0, 0, ^uint64(0))
	refs := testChunkDirectoryRefs(header)
	fileEnd := testChunkDirectoryFileEnd(refs)
	page := make([]byte, testSuperblockPageSize)
	if _, err := EncodeChunkDirectoryPage(page, header, refs, fileEnd, testChunkDirectoryNextLogicalID); err != nil {
		t.Fatal(err)
	}
	view, err := OpenChunkDirectoryPage(page, fileEnd, testChunkDirectoryNextLogicalID)
	if err != nil {
		t.Fatal(err)
	}
	if allocs := testing.AllocsPerRun(1000, func() {
		if _, err := EncodeChunkDirectoryPage(page, header, refs, fileEnd, testChunkDirectoryNextLogicalID); err != nil {
			panic(err)
		}
		opened, err := OpenChunkDirectoryPage(page, fileEnd, testChunkDirectoryNextLogicalID)
		if err != nil {
			panic(err)
		}
		if _, ok := opened.Lookup(63); !ok {
			panic("directory lookup miss")
		}
		if _, ok := view.RefAt(63); !ok {
			panic("directory rank miss")
		}
	}); allocs != 0 {
		t.Fatalf("chunk-directory codec and lookup allocations = %g, want 0", allocs)
	}
}

func BenchmarkChunkDirectory(b *testing.B) {
	header := testChunkDirectoryHeader(0, 0, ^uint64(0))
	refs := testChunkDirectoryRefs(header)
	fileEnd := testChunkDirectoryFileEnd(refs)
	page := make([]byte, testSuperblockPageSize)
	if _, err := EncodeChunkDirectoryPage(page, header, refs, fileEnd, testChunkDirectoryNextLogicalID); err != nil {
		b.Fatal(err)
	}
	view, err := OpenChunkDirectoryPage(page, fileEnd, testChunkDirectoryNextLogicalID)
	if err != nil {
		b.Fatal(err)
	}
	b.Run("encode-64-way-4KiB", func(b *testing.B) {
		b.SetBytes(int64(len(page)))
		b.ReportAllocs()
		for range b.N {
			if _, err := EncodeChunkDirectoryPage(page, header, refs, fileEnd, testChunkDirectoryNextLogicalID); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("open-64-way-4KiB", func(b *testing.B) {
		b.SetBytes(int64(len(page)))
		b.ReportAllocs()
		for range b.N {
			opened, err := OpenChunkDirectoryPage(page, fileEnd, testChunkDirectoryNextLogicalID)
			if err != nil {
				b.Fatal(err)
			}
			benchmarkChunkDirectoryRef, _ = opened.RefAt(63)
		}
	})
	b.Run("lookup-hit", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			benchmarkChunkDirectoryRef, _ = view.Lookup(63)
		}
	})
	b.Run("lookup-miss", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			benchmarkChunkDirectoryRef, _ = view.Lookup(64)
		}
	})
}
