package simdjson

import (
	"runtime"
	"testing"
)

func BenchmarkStorePackedIndex(b *testing.B) {
	packed, err := newStorePackedIndex(testStorePackedIndexPending())
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		runtime.SetFinalizer(packed, nil)
		packed.release()
	})
	b.ReportAllocs()
	for range b.N {
		packed.each(41, func(chunk uint32, mask uint64) bool {
			storePackedIndexSink ^= uint64(chunk) ^ mask
			return true
		})
	}
}
