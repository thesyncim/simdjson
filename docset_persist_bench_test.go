package simdjson

import (
	"bytes"
	"testing"
)

// Benchmarks for log-structured DocSet persistence. The mode's promise is a
// zero-reparse reopen: a corpus indexed once is serialized, and reopening the
// image reconstructs the set by viewing straight into the bytes rather than
// revalidating and rebuilding a tape per document. These rows price that
// promise against the work it replaces. Rebuild is the cost avoided — a fresh
// DocSet that Appends every document, the full validate-and-index pass. Open is
// the cost paid — header and manifest validation, per-record view construction,
// the shape-table rebuild, and (here) the postings and value-dictionary replay.
// WriteTo prices the serialization itself. The Open-versus-Rebuild ratio is the
// zero-reparse win.

// persistBenchCorpus builds the shared shape-clustered corpus the rows reopen:
// enough documents to amortize framing, appended twice so the conforming
// layouts reach shape-taped storage.
func persistBenchCorpus() []string {
	base := shapeTapeClusteredDocs(2000, 6, 12)
	docs := make([]string, 0, 2*len(base))
	for _, d := range base {
		docs = append(docs, d, d)
	}
	return docs
}

// persistBenchImage indexes the corpus into a fully-featured set and returns its
// serialized image alongside the byte size, for the reopen rows.
func persistBenchImage(tb testing.TB, docs []string) ([]byte, int64) {
	tb.Helper()
	set := &DocSet{ShapeTapes: true, Postings: true, ValueDict: true}
	for _, d := range docs {
		if _, err := set.Append([]byte(d)); err != nil {
			tb.Fatal(err)
		}
	}
	var buf bytes.Buffer
	n, err := set.WriteTo(&buf)
	if err != nil {
		tb.Fatal(err)
	}
	return buf.Bytes(), n
}

// BenchmarkDocSetPersistRebuild is the baseline the reopen replaces: a fresh set
// that revalidates and indexes every document from its source. It is the
// per-corpus cost Open avoids.
func BenchmarkDocSetPersistRebuild(b *testing.B) {
	docs := persistBenchCorpus()
	var bytesLen int64
	for _, d := range docs {
		bytesLen += int64(len(d))
	}
	b.SetBytes(bytesLen)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		set := &DocSet{ShapeTapes: true, Postings: true, ValueDict: true}
		for _, d := range docs {
			if _, err := set.Append([]byte(d)); err != nil {
				b.Fatal(err)
			}
		}
		if set.Len() != len(docs) {
			b.Fatalf("Len = %d", set.Len())
		}
	}
}

// BenchmarkDocSetPersistOpen is the zero-reparse reopen: reconstruct the set
// from a ready image. Its ratio to Rebuild is the win. The image is copied per
// iteration so each Open reconstructs against its own bytes, as a fresh mapping
// would.
func BenchmarkDocSetPersistOpen(b *testing.B) {
	docs := persistBenchCorpus()
	image, size := persistBenchImage(b, docs)
	b.SetBytes(size)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		set, err := Open(append([]byte(nil), image...))
		if err != nil {
			b.Fatal(err)
		}
		if set.Len() != len(docs) {
			b.Fatalf("Len = %d", set.Len())
		}
	}
}

// BenchmarkDocSetPersistWriteTo prices the serialization pass that produces the
// image the reopen consumes.
func BenchmarkDocSetPersistWriteTo(b *testing.B) {
	docs := persistBenchCorpus()
	set := &DocSet{ShapeTapes: true, Postings: true, ValueDict: true}
	for _, d := range docs {
		if _, err := set.Append([]byte(d)); err != nil {
			b.Fatal(err)
		}
	}
	var buf bytes.Buffer
	n, err := set.WriteTo(&buf)
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(n)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf.Reset()
		if _, err := set.WriteTo(&buf); err != nil {
			b.Fatal(err)
		}
	}
}
