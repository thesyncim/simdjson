//go:build darwin || linux

package simdjson

import (
	"bytes"
	"runtime"
	"slices"
	"testing"
)

func TestOpenStoreMappedLifetimeAndMutation(t *testing.T) {
	store, want, _ := buildStorePersistFixture(t)
	var buf bytes.Buffer
	if _, err := store.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	mapped := persistBenchMmap(t, buf.Bytes())
	buf.Reset()
	store = nil
	runtime.GC()

	reopened, err := OpenStore(mapped)
	if err != nil {
		t.Fatal(err)
	}
	compiled := reopened.CompileKey("key:00")
	dst := make([]byte, 0, len(want["key:00"]))
	if dst, ok := reopened.AppendRawKey(dst, compiled); !ok || string(dst) != want["key:00"] {
		t.Fatalf("mapped AppendRawKey = (%q,%v)", dst, ok)
	}
	keys, err := reopened.Snapshot().AppendIndexRawKeys(nil, "country_status", []byte(`"PT"`), []byte(`"active"`))
	if err != nil || !slices.Equal(keys, []string{"key:00", "key:06"}) {
		t.Fatalf("mapped exact lookup = (%v,%v)", keys, err)
	}

	retained := reopened.Snapshot()
	if _, err := reopened.Put("key:00", []byte(`{"status":"changed"}`)); err != nil {
		t.Fatal(err)
	}
	if !reopened.Delete("key:06") {
		t.Fatal("mapped delete missed")
	}
	reopened = nil
	runtime.GC()
	if raw, ok := retained.GetRaw("key:00"); !ok || string(raw.Bytes()) != want["key:00"] {
		t.Fatalf("retained mapped snapshot = (%q,%v)", raw.Bytes(), ok)
	}
	runtime.KeepAlive(retained)
}

// BenchmarkStorePersistOpenMapped measures the production caller-owned mmap
// path. Mapping setup is outside the timer. mapped-B is cold file-backed data;
// B/op is the hot heap metadata OpenStore currently reconstructs.
func BenchmarkStorePersistOpenMapped(b *testing.B) {
	for _, indexed := range []bool{false, true} {
		name := "keys"
		if indexed {
			name = "exact-index"
		}
		b.Run(name, func(b *testing.B) {
			image, size := storePersistBenchImage(b, indexed)
			mapped := persistBenchMmap(b, image)
			image = nil
			runtime.GC()

			b.SetBytes(size)
			b.ReportAllocs()
			b.ResetTimer()
			var last *Store
			for i := 0; i < b.N; i++ {
				store, err := OpenStore(mapped)
				if err != nil {
					b.Fatal(err)
				}
				if store.Len() != storePersistBenchDocuments {
					b.Fatalf("Len = %d", store.Len())
				}
				last = store
				runtime.KeepAlive(store)
			}
			b.StopTimer()
			stats := last.Stats()
			b.ReportMetric(float64(size), "mapped-B")
			b.ReportMetric(float64(stats.ExternalKeyBytes), "external-key-B")
			b.ReportMetric(float64(stats.ExternalDocumentBytes), "external-doc-B")
			b.ReportMetric(float64(stats.ExternalIndexBytes), "external-index-B")
		})
	}
}

// BenchmarkStorePersistMappedPointRead verifies that borrowing source bytes
// does not tax the steady read path. Both forms return mapped bytes directly;
// the compiled form also bypasses hashing and both key directories after its
// verified stable-slot check.
func BenchmarkStorePersistMappedPointRead(b *testing.B) {
	image, _ := storePersistBenchImage(b, false)
	mapped := persistBenchMmap(b, image)
	image = nil
	runtime.GC()
	store, err := OpenStore(mapped)
	if err != nil {
		b.Fatal(err)
	}
	const key = "account:00008192"
	compiled := store.CompileKey(key)

	b.Run("ordinary", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if raw, ok := store.GetRaw(key); !ok || len(raw.Bytes()) == 0 {
				b.Fatal("mapped ordinary lookup missed")
			}
		}
	})
	b.Run("compiled", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if raw, ok := store.GetRawKey(compiled); !ok || len(raw.Bytes()) == 0 {
				b.Fatal("mapped compiled lookup missed")
			}
		}
	})
	runtime.KeepAlive(store)
}

// BenchmarkStorePersistMappedExactQuery probes a two-column nested index whose
// answer occupies two of 256 micro-pages. The query combines stable-slot masks
// before exact rechecks, so rejected source pages stay untouched after open.
func BenchmarkStorePersistMappedExactQuery(b *testing.B) {
	image, _ := storePersistBenchImage(b, true)
	mapped := persistBenchMmap(b, image)
	image = nil
	runtime.GC()
	store, err := OpenStore(mapped)
	if err != nil {
		b.Fatal(err)
	}
	snapshot := store.Snapshot()
	region, status := []byte(`"r042"`), []byte(`"s2"`)
	dst := make([]string, 0, 32)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dst, err = snapshot.AppendIndexRawKeys(dst[:0], "region_status", region, status)
		if err != nil || len(dst) != 32 {
			b.Fatalf("mapped exact query = (%d,%v)", len(dst), err)
		}
	}
	runtime.KeepAlive(store)
}
