package benchmarks

import (
	"strings"
	"testing"

	simdjsongo "github.com/minio/simdjson-go"
	"github.com/thesyncim/simdjson"
	stdlibcorpus "github.com/thesyncim/simdjson/tests/stdlib"
)

// BenchmarkStdlibCorpusNativeParse compares reusable, navigable structural
// representations. simdjson Index keeps source ranges; minio/simdjson-go
// additionally parses numbers and copies escaped strings into its tape arena.
func BenchmarkStdlibCorpusNativeParse(b *testing.B) {
	for _, name := range stdlibcorpus.Names {
		src, err := stdlibcorpus.Read(name)
		if err != nil {
			b.Fatal(err)
		}
		label := strings.TrimSuffix(name, ".json.zst")
		b.Run(label, func(b *testing.B) {
			benchmarkNativeParse(b, src)
		})
	}
}

func benchmarkNativeParse(b *testing.B, src []byte) {
	need, err := simdjson.RequiredIndexEntries(src)
	if err != nil {
		b.Fatal(err)
	}
	storage := make([]simdjson.IndexEntry, need)
	if _, err := simdjson.BuildIndex(src, storage); err != nil {
		b.Fatal(err)
	}
	b.Run("simdjson-index-reused", func(b *testing.B) {
		b.SetBytes(int64(len(src)))
		b.ReportAllocs()
		for b.Loop() {
			index, err := simdjson.BuildIndex(src, storage)
			if err != nil {
				b.Fatal(err)
			}
			intSink = index.Len()
		}
	})

	if !simdjsongo.SupportedCPU() {
		b.Run("minio-simdjson-go-reused-zero-copy", func(b *testing.B) {
			b.Skip("minio/simdjson-go requires amd64 AVX2 and CLMUL")
		})
		return
	}
	reuse, err := simdjsongo.Parse(src, nil, simdjsongo.WithCopyStrings(false))
	if err != nil {
		b.Fatal(err)
	}
	b.Run("minio-simdjson-go-reused-zero-copy", func(b *testing.B) {
		b.SetBytes(int64(len(src)))
		b.ReportAllocs()
		b.ResetTimer()
		for b.Loop() {
			reuse, err = simdjsongo.Parse(src, reuse, simdjsongo.WithCopyStrings(false))
			if err != nil {
				b.Fatal(err)
			}
		}
		simdParsedSink = reuse
	})
}
