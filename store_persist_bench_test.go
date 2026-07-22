package simdjson

import (
	"bytes"
	"fmt"
	"testing"
)

const storePersistBenchDocuments = 16 * 1024

// storePersistBenchImage builds enough stable-slot pages to expose both image
// bytes and the hot metadata OpenStore reconstructs. The exact-index variant
// deliberately measures today's cold-page index rebuild cost as well as open;
// it must not be mistaken for the future mapped-index-root design.
func storePersistBenchImage(tb testing.TB, indexed bool) ([]byte, int64) {
	tb.Helper()
	builder, err := NewStoreBuilder(StoreOptions{ChunkDocuments: 64, ShapeTapes: true})
	if err != nil {
		tb.Fatal(err)
	}
	if indexed {
		if err := builder.CreateIndex(StoreIndexDefinition{
			Name: "region_status", Paths: []string{"/profile/region", "/status"},
		}); err != nil {
			tb.Fatal(err)
		}
	}
	for i := 0; i < storePersistBenchDocuments; i++ {
		key := fmt.Sprintf("account:%08d", i)
		doc := fmt.Sprintf(`{"id":%d,"profile":{"region":"r%03d"},"status":"s%d","payload":"shared-payload-spelling"}`,
			i, (i/64)%128, i%4)
		if err := builder.Append(key, []byte(doc)); err != nil {
			tb.Fatal(err)
		}
	}
	store, err := builder.Build()
	if err != nil {
		tb.Fatal(err)
	}
	var image bytes.Buffer
	n, err := store.WriteTo(&image)
	if err != nil {
		tb.Fatal(err)
	}
	return image.Bytes(), n
}

// BenchmarkStorePersistOpen prices a standalone heap image and the hot
// metadata reconstruction separately for key-only and exact-index Stores.
// Copying the image models read-all startup; the mmap benchmark excludes that
// copy and reports the file-backed byte count explicitly.
func BenchmarkStorePersistOpen(b *testing.B) {
	for _, indexed := range []bool{false, true} {
		name := "keys"
		if indexed {
			name = "exact-index"
		}
		b.Run(name, func(b *testing.B) {
			image, size := storePersistBenchImage(b, indexed)
			b.SetBytes(size)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				store, err := OpenStore(append([]byte(nil), image...))
				if err != nil {
					b.Fatal(err)
				}
				if store.Len() != storePersistBenchDocuments {
					b.Fatalf("Len = %d", store.Len())
				}
			}
		})
	}
}

func BenchmarkStorePersistWriteTo(b *testing.B) {
	image, size := storePersistBenchImage(b, true)
	store, err := OpenStore(image)
	if err != nil {
		b.Fatal(err)
	}
	var dst bytes.Buffer
	dst.Grow(len(image))
	b.SetBytes(size)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dst.Reset()
		if _, err := store.WriteTo(&dst); err != nil {
			b.Fatal(err)
		}
	}
}
