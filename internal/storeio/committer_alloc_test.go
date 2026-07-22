package storeio

import (
	"os"
	"testing"
)

func TestCommitterSteadyAllocation(t *testing.T) {
	committer, _, pageSize := newPortableCommitter(t, 4, 1)
	defer committer.Close()
	var generation uint64
	if allocs := testing.AllocsPerRun(20, func() {
		generation++
		batch, err := committer.Begin(1)
		if err != nil {
			panic(err)
		}
		page, err := batch.PageBuffer(0)
		if err != nil {
			panic(err)
		}
		root, err := batch.RootBuffer()
		if err != nil {
			panic(err)
		}
		copy(page, "page")
		copy(root, "root")
		if err := batch.SetPage(0, int64(pageSize), 4); err != nil {
			panic(err)
		}
		if err := batch.SetRoot(0, 4); err != nil {
			panic(err)
		}
		if err := batch.Publish(generation); err != nil {
			panic(err)
		}
		if err := committer.Wait(generation); err != nil {
			panic(err)
		}
	}); allocs != 0 {
		t.Fatalf("Begin/fill/Publish/Wait allocations = %g, want 0", allocs)
	}
}

func BenchmarkCommitterBeginAbort(b *testing.B) {
	file, err := os.CreateTemp(b.TempDir(), "committer-benchmark")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = file.Close() })
	committer, err := NewCommitter(file, DeviceOptions{
		Backend: BackendPortable, BufferCount: 4, BufferSize: os.Getpagesize(),
	}, CommitterOptions{QueueSlots: 4, MaxPagesPerBatch: 1, GroupLimit: 1})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = committer.Close() })
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		batch, err := committer.Begin(1)
		if err != nil {
			b.Fatal(err)
		}
		if err := batch.Abort(); err != nil {
			b.Fatal(err)
		}
	}
}
