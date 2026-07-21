package simdjson

import (
	"fmt"
	"testing"
	"time"
)

var (
	storeRawSink  RawValue
	storeKeysSink []string
	storeBoolSink bool
)

func benchmarkStore(b *testing.B, options StoreOptions, documents int) *Store {
	b.Helper()
	store := NewStore(options)
	for i := 0; i < documents; i++ {
		doc := fmt.Sprintf(`{"id":%d,"group":%d,"active":true,"name":"document-%05d"}`, i, i%16, i)
		if _, err := store.Put(fmt.Sprintf("key-%05d", i), []byte(doc)); err != nil {
			b.Fatal(err)
		}
	}
	return store
}

func BenchmarkStoreGetRaw(b *testing.B) {
	store := benchmarkStore(b, StoreOptions{ChunkDocuments: 64, ShapeTapes: true}, 1<<16)
	snapshot := store.Snapshot()
	keys := make([]string, 1024)
	for i := range keys {
		keys[i] = fmt.Sprintf("key-%05d", (i*8191)&((1<<16)-1))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		storeRawSink, storeBoolSink = snapshot.GetRaw(keys[i&1023])
	}
}

func BenchmarkStoreMutation(b *testing.B) {
	for _, chunkDocuments := range []int{1, 8, 64} {
		b.Run("update/chunk="+itoaStore(chunkDocuments), func(b *testing.B) {
			store := benchmarkStore(b, StoreOptions{ChunkDocuments: chunkDocuments, ShapeTapes: true}, 1024)
			keys := make([]string, 1024)
			for i := range keys {
				keys[i] = fmt.Sprintf("key-%05d", i)
			}
			doc := []byte(`{"id":7,"group":3,"active":true,"name":"replacement"}`)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := store.Put(keys[i&1023], doc); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run("delete-insert/chunk="+itoaStore(chunkDocuments), func(b *testing.B) {
			store := benchmarkStore(b, StoreOptions{ChunkDocuments: chunkDocuments, ShapeTapes: true}, 1024)
			keys := make([]string, 1024)
			for i := range keys {
				keys[i] = fmt.Sprintf("key-%05d", i)
			}
			doc := []byte(`{"id":7,"group":3,"active":true,"name":"replacement"}`)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				key := keys[i&1023]
				if !store.Delete(key) {
					b.Fatal("delete miss")
				}
				if _, err := store.Put(key, doc); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkStoreTTLChange(b *testing.B) {
	store := benchmarkStore(b, StoreOptions{ChunkDocuments: 8}, 1)
	base := time.Now().Add(24 * time.Hour)
	store.SetDeadline("key-00000", base)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		storeBoolSink = store.SetDeadline("key-00000", base.Add(time.Duration(i&1023)*time.Millisecond))
	}
}

func BenchmarkStoreIndexedSnapshotProbe(b *testing.B) {
	store := benchmarkStore(b, StoreOptions{ChunkDocuments: 64, ShapeTapes: true, Postings: true}, 1<<16)
	snapshot := store.Snapshot()
	entries := make([]IndexEntry, 8)
	needle, err := BuildIndex([]byte(`7`), entries)
	if err != nil {
		b.Fatal(err)
	}
	for _, test := range []struct {
		name string
		run  func([]string) []string
	}{
		{"exists-all", func(dst []string) []string { return snapshot.AppendWhereExistsKeys(dst, "active") }},
		{"contains-1/16", func(dst []string) []string {
			return snapshot.AppendWhereContainsIndexKeys(dst, "group", needle)
		}},
	} {
		b.Run(test.name, func(b *testing.B) {
			dst := make([]string, 0, snapshot.Len())
			dst = test.run(dst)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				dst = test.run(dst[:0])
			}
			storeKeysSink = dst
		})
	}
}

func itoaStore(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return fmt.Sprint(n)
}
