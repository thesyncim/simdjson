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

func BenchmarkStoreMutationModes(b *testing.B) {
	for _, mode := range []struct {
		name    string
		options StoreOptions
	}{
		{"classic", StoreOptions{ChunkDocuments: 64}},
		{"shape-tapes", StoreOptions{ChunkDocuments: 64, ShapeTapes: true}},
		{"shape-tapes+postings", StoreOptions{ChunkDocuments: 64, ShapeTapes: true, Postings: true}},
		{"shape-tapes+value-dict", StoreOptions{ChunkDocuments: 64, ShapeTapes: true, ValueDict: true}},
	} {
		b.Run(mode.name, func(b *testing.B) {
			store := benchmarkStore(b, mode.options, 1024)
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

func BenchmarkStoreExactIndexMutation(b *testing.B) {
	for _, paths := range []struct {
		name  string
		paths []string
	}{
		{"single", []string{"/group"}},
		{"compound", []string{"/group", "/active"}},
	} {
		for _, mode := range []struct {
			name string
			doc  func(int, [2][]byte) []byte
		}{
			{"unchanged", func(i int, docs [2][]byte) []byte { return docs[i&1] }},
			{"changed", func(i int, docs [2][]byte) []byte { return docs[(i>>10)&1] }},
		} {
			b.Run(paths.name+"/"+mode.name, func(b *testing.B) {
				store := benchmarkStore(b, StoreOptions{ChunkDocuments: 64, ShapeTapes: true}, 1024)
				info, err := store.CreateIndex(StoreIndexDefinition{Name: "bench", Paths: paths.paths})
				if err != nil {
					b.Fatal(err)
				}
				if info, err = store.BackfillIndex(info.Name, 0); err != nil || info.State != StoreIndexReady {
					b.Fatalf("BackfillIndex = (%+v,%v)", info, err)
				}
				keys := make([]string, 1024)
				for i := range keys {
					keys[i] = fmt.Sprintf("key-%05d", i)
				}
				docs := [2][]byte{
					[]byte(`{"id":7,"group":3,"active":true,"name":"replacement"}`),
					[]byte(`{"id":7,"group":5,"active":false,"name":"replacement"}`),
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if _, err := store.Put(keys[i&1023], mode.doc(i, docs)); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

func BenchmarkStoreExactIndexProbe(b *testing.B) {
	store := benchmarkStore(b, StoreOptions{ChunkDocuments: 64, ShapeTapes: true}, 1<<16)
	for _, def := range []StoreIndexDefinition{
		{Name: "group", Paths: []string{"/group"}},
		{Name: "group_active", Paths: []string{"/group", "/active"}},
	} {
		info, err := store.CreateIndex(def)
		if err != nil {
			b.Fatal(err)
		}
		if info, err = store.BackfillIndex(info.Name, 0); err != nil || info.State != StoreIndexReady {
			b.Fatalf("BackfillIndex(%s) = (%+v,%v)", def.Name, info, err)
		}
	}
	snapshot := store.Snapshot()
	bytesPerDoc := make(map[string]float64, 2)
	for _, name := range []string{"group", "group_active"} {
		stats, err := snapshot.IndexStats(name)
		if err != nil {
			b.Fatal(err)
		}
		bytesPerDoc[name] = float64(stats.EstimatedBytes) / float64(snapshot.Len())
	}
	group := testScalarIndex(b, `7`)
	active := testScalarIndex(b, `true`)
	for _, test := range []struct {
		name   string
		index  string
		values []Index
	}{
		{"single-1/16", "group", []Index{group}},
		{"compound-1/16", "group_active", []Index{group, active}},
	} {
		b.Run(test.name, func(b *testing.B) {
			dst := make([]StoreMask, 0, snapshot.Len()/64)
			dst, err := snapshot.AppendIndexMasks(dst, test.index, test.values...)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				dst, err = snapshot.AppendIndexMasks(dst[:0], test.index, test.values...)
				if err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(bytesPerDoc[test.index], "index-B/doc")
		})
	}
}

func BenchmarkStoreExactIndexBackfill(b *testing.B) {
	for _, batch := range []int{0, 8} {
		b.Run("batch="+itoaStore(batch), func(b *testing.B) {
			store := benchmarkStore(b, StoreOptions{ChunkDocuments: 64, ShapeTapes: true}, 4096)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				info, err := store.CreateIndex(StoreIndexDefinition{Name: "bench", Paths: []string{"/group"}})
				if err != nil {
					b.Fatal(err)
				}
				for info.State != StoreIndexReady {
					info, err = store.BackfillIndex(info.Name, batch)
					if err != nil {
						b.Fatal(err)
					}
				}
				if err := store.DropIndex(info.Name); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func itoaStore(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return fmt.Sprint(n)
}
