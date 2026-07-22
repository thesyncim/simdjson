//go:build darwin || linux

package simdjson

import (
	"os"
	"runtime"
	"syscall"
	"testing"
)

// persistBenchMmap writes image once and returns a read-only shared mapping.
// Open borrows the returned bytes; cleanup therefore unmaps only after the
// benchmark has released every reopened DocSet and forced a collection.
func persistBenchMmap(tb testing.TB, image []byte) []byte {
	tb.Helper()
	file, err := os.CreateTemp(tb.TempDir(), "docset-*.bin")
	if err != nil {
		tb.Fatal(err)
	}
	if _, err := file.Write(image); err != nil {
		file.Close()
		tb.Fatal(err)
	}
	mapped, err := syscall.Mmap(int(file.Fd()), 0, len(image), syscall.PROT_READ, syscall.MAP_SHARED)
	closeErr := file.Close()
	if err != nil {
		tb.Fatal(err)
	}
	if closeErr != nil {
		syscall.Munmap(mapped)
		tb.Fatal(closeErr)
	}
	tb.Cleanup(func() {
		runtime.GC()
		if err := syscall.Munmap(mapped); err != nil {
			tb.Errorf("munmap: %v", err)
		}
	})
	return mapped
}

// BenchmarkDocSetPersistOpenMapped measures the production off-heap mode that
// already exists: Open reconstructs metadata over a caller-owned mmap without
// copying the image into the Go heap. Mapping setup is deliberately outside
// the timer because a service normally maps once and keeps the corpus open.
func BenchmarkDocSetPersistOpenMapped(b *testing.B) {
	docs := persistBenchCorpus()
	image, size := persistBenchImage(b, docs)
	mapped := persistBenchMmap(b, image)
	image = nil
	runtime.GC()

	b.SetBytes(size)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		set, err := Open(mapped)
		if err != nil {
			b.Fatal(err)
		}
		if set.Len() != len(docs) {
			b.Fatalf("Len = %d", set.Len())
		}
		runtime.KeepAlive(set)
	}
	b.ReportMetric(float64(size), "mapped-B")
}
