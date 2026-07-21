package simdjson

import (
	"testing"
)

// BenchmarkValueDictSpace measures the value dictionary's space lever on a
// repeat-heavy corpus: enum-style records whose venue, category, status, and
// area sub-object recur across documents while their identifiers stay unique.
// It reports retained bytes per document for classic storage and for the
// modeled value-dictionary storage, the fraction of retained bytes the
// dictionary recovers, and the dedup hit rate — the deliverable's absolute
// internal metrics, measured, not asserted. The timed body rebuilds the set, so
// the benchmark also tracks ingest cost under interning; the space metrics are
// computed once from the last build's Stats.
//
//	GOEXPERIMENT=simd gotip test -bench BenchmarkValueDictSpace -benchmem -cpu 1 ./
func BenchmarkValueDictSpace(b *testing.B) {
	corpus := valueDictEnumCorpus(4096)
	var source int64
	for _, doc := range corpus {
		source += int64(len(doc))
	}

	occurrences := valueDictOccurrences(corpus)

	b.Run("classic", func(b *testing.B) {
		var st DocSetStats
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			set := &DocSet{}
			for _, doc := range corpus {
				if _, err := set.Append(doc); err != nil {
					b.Fatal(err)
				}
			}
			st = set.Stats()
		}
		retained := docSetTapeBytes(st) + source
		b.ReportMetric(float64(retained)/float64(len(corpus)), "retained-B/doc")
	})

	b.Run("valuedict", func(b *testing.B) {
		var st DocSetStats
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			set := &DocSet{ValueDict: true}
			for _, doc := range corpus {
				if _, err := set.Append(doc); err != nil {
					b.Fatal(err)
				}
			}
			st = set.Stats()
		}
		tape := docSetTapeBytes(st)
		// Modeled retained bytes: the tape (unchanged) plus the source a
		// compacting store keeps — the residual after dropping spliced spans,
		// the four-byte references, and the shared arena.
		retained := tape + source - st.DictSavedBytes
		b.ReportMetric(float64(retained)/float64(len(corpus)), "retained-B/doc")
		b.ReportMetric(float64(st.DictSavedBytes)/float64(source+tape)*100, "space-saved-%")
		b.ReportMetric(float64(st.DictSplices)/float64(occurrences)*100, "hit-rate-%")
		b.ReportMetric(float64(st.DictValues), "dict-entries")
	})
}

// docSetTapeBytes returns the entry-storage bytes a set's Stats describe:
// classic and wide value entries at sixteen bytes, narrow ones at eight. It is
// the tape side of the at-rest space model, held constant by the value
// dictionary so the comparison isolates the source-side saving.
func docSetTapeBytes(st DocSetStats) int64 {
	return (st.TapeEntries+st.ValueEntries)*16 + st.NarrowValueEntries*8
}

// valueDictOccurrences counts the value nodes across the corpus — the
// denominator of the dedup hit rate. It builds a throwaway classic set and sums
// each document's value entries (its tape entries less its keys).
func valueDictOccurrences(corpus [][]byte) int64 {
	var set DocSet
	for _, doc := range corpus {
		if _, err := set.Append(doc); err != nil {
			panic(err)
		}
	}
	var values int64
	for i := 0; i < set.Len(); i++ {
		idx := set.Doc(i)
		for j := range idx.entries {
			if idx.entries[j].flags()&tapeFlagKey == 0 {
				values++
			}
		}
	}
	return values
}
