package simdjson

import (
	"fmt"
	"testing"

	"github.com/thesyncim/simdjson/document"
)

// Benchmarks for the inverted posting layer (docset_postings.go), against the
// full column scan it replaces. Both scenarios sweep selectivity — the fraction
// of the corpus a predicate matches — because that is the axis postings win on:
// a full scan is O(corpus) whatever the answer size, while a posting probe
// visits only candidates, so the two cross where the match fraction stops being
// small. The corpus is the ADR 0002 phase-0 clustered row (~400-byte documents,
// fifteen scalar fields) so the numbers sit beside the phase-0 containment
// baseline the layer was built to prune. Each measurement is one query
// (ns/query); the reported match count is the selectivity.

var postingsBenchSink int

// postingsContainsCorpus returns count phase-0-shaped documents whose
// categorical field s0_f02 holds target in exactly matches of them (spread
// across the corpus) and a distinct per-document value in the rest, so a
// containment query on target has the given selectivity.
func postingsContainsCorpus(count, matches int, target string) [][]byte {
	docs := make([][]byte, count)
	stride := count
	if matches > 0 {
		stride = count / matches
	}
	for i := range docs {
		f02 := fmt.Sprintf("miss_%012d", i)
		if stride > 0 && i%stride == 0 && i/stride < matches {
			f02 = target
		}
		docs[i] = fmt.Appendf(nil,
			`{"s0_f00":"w%016d","s0_f01":%d,"s0_f02":"%s","s0_f03":"member_%012d","s0_f04":true,`+
				`"s0_f05":"region_eu_west_%d","s0_f06":%d.%02d,"s0_f07":"label_%012d","s0_f08":null,`+
				`"s0_f09":"tier_%d_class_%d","s0_f10":"suffix_%012d","s0_f11":"trail_%016d",`+
				`"s0_f12":"opaque_%016d","s0_f13":%d.%04d,"s0_f14":false}`,
			i, i*7, f02, i%9999, i%7, i%100, i%97, i%999983, i%5, i%3, i, i, i, i%1000, i%9973)
	}
	return docs
}

// postingsExistsCorpus returns count phase-0-shaped documents, matches of which
// carry the extra top-level key opt_field (a second shape) and the rest do not,
// so an existence query on opt_field has the given selectivity across two
// shape-clustered layouts.
func postingsExistsCorpus(count, matches int) [][]byte {
	docs := make([][]byte, count)
	stride := count
	if matches > 0 {
		stride = count / matches
	}
	for i := range docs {
		has := stride > 0 && i%stride == 0 && i/stride < matches
		opt := ""
		if has {
			opt = fmt.Sprintf(`,"opt_field":%d`, i)
		}
		docs[i] = fmt.Appendf(nil,
			`{"s0_f00":"w%016d","s0_f01":%d,"s0_f02":"cat_%d","s0_f03":"member_%012d","s0_f04":true,`+
				`"s0_f05":"region_eu_west_%d","s0_f06":%d.%02d,"s0_f07":"label_%012d"%s}`,
			i, i*7, i%5, i%9999, i%7, i%100, i%97, i%999983, opt)
	}
	return docs
}

// postingsBenchSet builds a DocSet over docs under the given modes.
func postingsBenchSet(b *testing.B, docs [][]byte, shapeTapes bool) *DocSet {
	b.Helper()
	s := &DocSet{Postings: true, ShapeTapes: shapeTapes, Options: document.IndexOptions{HashKeys: true}}
	for _, doc := range docs {
		if _, err := s.Append(doc); err != nil {
			b.Fatal(err)
		}
	}
	return s
}

// postingsSelectivities is the swept match count out of postingsBenchCount.
var postingsSelectivities = []int{1, 16, 256, 4096}

const postingsBenchCount = 4096

// BenchmarkPostingsContains sweeps a containment predicate's selectivity:
// WhereContains (posting probe plus per-candidate verify) against the full
// column scan (Get plus Contains on every document). Postings-only storage
// keeps verification O(1) per candidate; the crossover is where the match
// fraction stops being small.
func BenchmarkPostingsContains(b *testing.B) {
	target := "vAlpha"
	needle := []byte(`"` + target + `"`)
	for _, matches := range postingsSelectivities {
		docs := postingsContainsCorpus(postingsBenchCount, matches, target)
		set := postingsBenchSet(b, docs, false)
		needleIndex, err := containsIndex(needle)
		if err != nil {
			b.Fatal(err)
		}
		// Confirm the two paths agree before timing either.
		want := set.whereContainsScan("s0_f02", needleIndex.Root())
		got, err := set.WhereContains("s0_f02", needle)
		if err != nil || len(got) != len(want) {
			b.Fatalf("match=%d: WhereContains=%d scan=%d err=%v", matches, len(got), len(want), err)
		}

		b.Run(fmt.Sprintf("match=%d/postings", matches), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				res, err := set.WhereContains("s0_f02", needle)
				if err != nil {
					b.Fatal(err)
				}
				postingsBenchSink = len(res)
			}
		})
		b.Run(fmt.Sprintf("match=%d/postings-buffered", matches), func(b *testing.B) {
			res := make([]int, 0, postingsBenchCount)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				res = set.AppendWhereContainsIndex(res[:0], "s0_f02", needleIndex)
				postingsBenchSink = len(res)
			}
		})
		b.Run(fmt.Sprintf("match=%d/scan", matches), func(b *testing.B) {
			needleRoot := mustRoot(b, needle)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				postingsBenchSink = len(set.whereContainsScan("s0_f02", needleRoot))
			}
		})
	}
}

// BenchmarkPostingsExists sweeps an existence predicate's selectivity:
// WhereExists (shape lists plus remainder scan) against the full column scan
// (a shape-field probe per document). Shape tapes are on so existence resolves
// through the shape index; the crossover is where the matching shapes' document
// lists stop being a small fraction of the corpus.
func BenchmarkPostingsExists(b *testing.B) {
	for _, matches := range postingsSelectivities {
		docs := postingsExistsCorpus(postingsBenchCount, matches)
		set := postingsBenchSet(b, docs, true)
		if got, want := len(set.WhereExists("opt_field")), len(set.whereExistsScan("opt_field")); got != want {
			b.Fatalf("match=%d: WhereExists=%d scan=%d", matches, got, want)
		}

		b.Run(fmt.Sprintf("match=%d/postings", matches), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				postingsBenchSink = len(set.WhereExists("opt_field"))
			}
		})
		b.Run(fmt.Sprintf("match=%d/postings-buffered", matches), func(b *testing.B) {
			res := make([]int, 0, postingsBenchCount)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				res = set.AppendWhereExists(res[:0], "opt_field")
				postingsBenchSink = len(res)
			}
		})
		b.Run(fmt.Sprintf("match=%d/scan", matches), func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				postingsBenchSink = len(set.whereExistsScan("opt_field"))
			}
		})
	}
}

// mustRoot builds a needle index and returns its root, for the scan baseline.
func mustRoot(b *testing.B, needle []byte) Node {
	b.Helper()
	idx, err := containsIndex(needle)
	if err != nil {
		b.Fatal(err)
	}
	return idx.Root()
}
