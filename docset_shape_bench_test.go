package simdjson

import (
	"fmt"
	"strings"
	"testing"

	"github.com/thesyncim/simdjson/document"
)

// Benchmarks for shape-deduplicated tapes. The mode makes two measurable
// promises and pays one measurable cost, and each row exists to price one of
// them against classic storage on the same documents. Extraction: a
// shape-taped document is one memoized ordinal and one value-array index, so
// the fused loops should beat the classic verified positional read on
// cache-resident sets and beat it well on DRAM-resident ones, where the
// classic path's per-document key verification faults a source line the
// value array never touches. Ingest: the conformance proof — resolve, key
// compare, compaction — is the mode's admission price, visible as the
// ReadFrom delta. Doc: widening is deliberately the slow spelling; its first
// access materializes the classic tape and its steady state is a map hit,
// both priced against the classic Doc's field load.

// shapeTapeBenchDoc appends one deterministic 16-field flat document shaped
// like the phase-0 synthetics: shape-prefixed keys, mixed scalar kinds, and
// enough filler to land near 400 source bytes.
func shapeTapeBenchDoc(dst []byte, prefix string, i int) []byte {
	dst = fmt.Appendf(dst, `{"%s_f00":%d`, prefix, i)
	dst = fmt.Appendf(dst, `,"%s_f01":%d`, prefix, i%1000)
	dst = fmt.Appendf(dst, `,"%s_f02":"cat%02d"`, prefix, i%32)
	dst = fmt.Appendf(dst, `,"%s_f03":%d.%02d`, prefix, i%10000, i%100)
	dst = fmt.Appendf(dst, `,"%s_f04":%t`, prefix, i%2 == 0)
	dst = fmt.Appendf(dst, `,"%s_f05":%d`, prefix, 1700000000+i)
	dst = fmt.Appendf(dst, `,"%s_f06":"status%d"`, prefix, i%8)
	dst = fmt.Appendf(dst, `,"%s_f07":"token-%012d"`, prefix, i)
	dst = fmt.Appendf(dst, `,"%s_f08":%d`, prefix, i*2654435761%1_000_000_000)
	dst = fmt.Appendf(dst, `,"%s_f09":%d.%04d`, prefix, i%1000, i%10000)
	dst = fmt.Appendf(dst, `,"%s_f10":%t`, prefix, i%3 == 0)
	dst = fmt.Appendf(dst, `,"%s_f11":"flag%d"`, prefix, i%4)
	dst = fmt.Appendf(dst, `,"%s_f12":"%016d"`, prefix, i)
	dst = fmt.Appendf(dst, `,"%s_f13":%d`, prefix, i%1_000_000)
	dst = fmt.Appendf(dst, `,"%s_f14":%d`, prefix, i%100)
	dst = fmt.Appendf(dst, `,"%s_f15":"%0128d"`, prefix, i)
	return append(dst, '}')
}

// shapeTapeBenchSet ingests n phase-0-shaped documents cycling over shapes
// layouts, classic or shape-taped, with key hashes on as the engine
// workloads run.
func shapeTapeBenchSet(b *testing.B, n, shapes int, taped bool) *DocSet {
	b.Helper()
	set := &DocSet{
		Options:    document.IndexOptions{HashKeys: true},
		ShapeTapes: taped,
	}
	var doc []byte
	for i := 0; i < n; i++ {
		doc = shapeTapeBenchDoc(doc[:0], fmt.Sprintf("s%02d", i%shapes), i)
		if _, err := set.Append(doc); err != nil {
			b.Fatal(err)
		}
	}
	return set
}

// benchShapeTapeExtract prices the fused extractors on classic and taped
// builds of one corpus: the raw column, the typed int64 column, and the
// batch pointer walk, each in ns/doc.
func benchShapeTapeExtract(b *testing.B, n, shapes int) {
	field := "s00_f07"
	intField := "s00_f05"
	pointer := MustCompilePointer("/" + field)
	for _, mode := range []struct {
		name  string
		taped bool
	}{{"classic", false}, {"taped", true}} {
		set := shapeTapeBenchSet(b, n, shapes, mode.taped)
		docs := set.Len()
		perDoc := func(b *testing.B, got int) {
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/float64(docs), "ns/doc")
			shapeBenchSink = got
		}
		b.Run("AppendField/"+mode.name, func(b *testing.B) {
			var cache ShapeCache
			dst := cache.AppendField(nil, set, field)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				dst = cache.AppendField(dst[:0], set, field)
			}
			perDoc(b, len(dst))
		})
		b.Run("AppendFieldInt64/"+mode.name, func(b *testing.B) {
			var cache ShapeCache
			dst, valid := cache.AppendFieldInt64(nil, nil, set, intField)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				dst, valid = cache.AppendFieldInt64(dst[:0], valid[:0], set, intField)
			}
			perDoc(b, len(dst))
		})
		b.Run("AppendPointer/"+mode.name, func(b *testing.B) {
			dst, err := set.AppendPointer(nil, pointer)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				dst, err = set.AppendPointer(dst[:0], pointer)
				if err != nil {
					b.Fatal(err)
				}
			}
			perDoc(b, len(dst))
		})
	}
}

// BenchmarkShapeTapeExtract is the cache-resident comparison: 1024 documents
// of one shape, everything in L2.
func BenchmarkShapeTapeExtract(b *testing.B) {
	benchShapeTapeExtract(b, 1024, 1)
}

// BenchmarkShapeTapeExtractLarge is the DRAM-resident comparison on the
// phase-0 anomaly's scale: 262144 ~400 B documents over four shapes, far
// beyond cache, where the classic path's per-document source touch dominates
// and the value array's single tape line should not.
func BenchmarkShapeTapeExtractLarge(b *testing.B) {
	benchShapeTapeExtract(b, 262144, 4)
}

// BenchmarkShapeTapeIngest prices the conformance proof: ReadFrom of one
// NDJSON corpus into a classic set against a shape-taped set, in MB/s.
func BenchmarkShapeTapeIngest(b *testing.B) {
	var stream strings.Builder
	var doc []byte
	const n = 65536
	for i := 0; i < n; i++ {
		doc = shapeTapeBenchDoc(doc[:0], fmt.Sprintf("s%02d", i%4), i)
		stream.Write(doc)
		stream.WriteByte('\n')
	}
	input := stream.String()
	for _, mode := range []struct {
		name  string
		taped bool
	}{{"classic", false}, {"taped", true}} {
		b.Run(mode.name, func(b *testing.B) {
			b.SetBytes(int64(len(input)))
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				set := &DocSet{
					Options:    document.IndexOptions{HashKeys: true},
					ShapeTapes: mode.taped,
				}
				if _, err := set.ReadFrom(strings.NewReader(input)); err != nil {
					b.Fatal(err)
				}
				shapeBenchSink = set.Len()
			}
		})
	}
}

// BenchmarkShapeTapeDoc prices Doc under the widening contract: the classic
// field load, the taped steady state (a map hit), and the taped first access
// (materializing the classic tape), each in ns/doc.
func BenchmarkShapeTapeDoc(b *testing.B) {
	const n = 1024
	b.Run("classic", func(b *testing.B) {
		set := shapeTapeBenchSet(b, n, 1, false)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			for d := 0; d < n; d++ {
				shapeBenchSink = set.Doc(d).Len()
			}
		}
		b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/float64(n), "ns/doc")
	})
	b.Run("tapedCached", func(b *testing.B) {
		set := shapeTapeBenchSet(b, n, 1, true)
		for d := 0; d < n; d++ {
			shapeBenchSink = set.Doc(d).Len() // widen everything once
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			for d := 0; d < n; d++ {
				shapeBenchSink = set.Doc(d).Len()
			}
		}
		b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/float64(n), "ns/doc")
	})
	b.Run("tapedFirst", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			b.StopTimer()
			set := shapeTapeBenchSet(b, n, 1, true)
			b.StartTimer()
			for d := 0; d < n; d++ {
				shapeBenchSink = set.Doc(d).Len()
			}
		}
		b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/float64(n), "ns/doc")
	})
}
