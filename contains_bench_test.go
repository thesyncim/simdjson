package simdjson

import (
	"fmt"
	"testing"

	"github.com/thesyncim/simdjson/document"
)

// Benchmarks for jsonb-compatible containment (contains.go). The shapes
// mirror the ADR 0002 phase-0 baseline: ~400-byte clustered synthetic
// documents probed with a one-member object needle, which is the row the
// phase-4 posting layer prunes candidates for. Each measurement is one
// document (ns/doc), for a hit and a miss, over plain and enriched
// indexes, against the two references the evaluator must beat: RawContains
// itself (per-document indexing included) and a naive decode-and-compare
// through the reference evaluator of contains_contract_test.go.

var containsBenchSink bool

// containsBenchDocs returns count ~400-byte documents shaped like the
// phase-0 clustered corpus: fifteen short scalar fields, one of them the
// categorical field s0_f02 the needle probes.
func containsBenchDocs(count int, f02 string) [][]byte {
	docs := make([][]byte, count)
	for i := range docs {
		docs[i] = fmt.Appendf(nil,
			`{"s0_f00":"w%016d","s0_f01":%d,"s0_f02":"%s","s0_f03":"member_%012d","s0_f04":true,`+
				`"s0_f05":"region_eu_west_%d_availability_zone_%d","s0_f06":%d.%02d,"s0_f07":"label_%012d","s0_f08":null,`+
				`"s0_f09":"tier_%d_class_%d_bucket_%02d","s0_f10":"suffix_%012d","s0_f11":"trail_%016d",`+
				`"s0_f12":"opaque_token_%016d_%08d","s0_f13":%d.%04d,"s0_f14":false}`,
			i, i*7, f02, i%9999, i%7, i%3, i%100, i%97, i%999983, i%5, i%3, i%89, i, i, i, i*31, i%1000, i%9973)
	}
	return docs
}

// containsBenchIndexes builds one index per document.
func containsBenchIndexes(b *testing.B, docs [][]byte, hashKeys bool) []Index {
	b.Helper()
	indexes := make([]Index, len(docs))
	for i, doc := range docs {
		count, err := RequiredIndexEntries(doc)
		if err != nil {
			b.Fatal(err)
		}
		indexes[i], err = BuildIndexOptions(doc, make([]IndexEntry, count), document.IndexOptions{HashKeys: hashKeys})
		if err != nil {
			b.Fatal(err)
		}
	}
	return indexes
}

// BenchmarkContainsSmall is the phase-0 containment row: a one-member
// needle against ~400-byte documents. hit documents carry the probed
// value, miss documents carry a different value under the same key.
func BenchmarkContainsSmall(b *testing.B) {
	needle := []byte(`{"s0_f02":"vAlpha"}`)
	corpus := map[string][][]byte{
		"hit":  containsBenchDocs(512, "vAlpha"),
		"miss": containsBenchDocs(512, "vOther"),
	}
	needleIndex, err := containsIndex(needle)
	if err != nil {
		b.Fatal(err)
	}
	needleRoot := needleIndex.Root()

	for _, outcome := range []string{"hit", "miss"} {
		docs := corpus[outcome]
		want := outcome == "hit"
		for _, variant := range []struct {
			name     string
			hashKeys bool
		}{
			{"plain", false},
			{"hashed", true},
		} {
			b.Run(variant.name+"/"+outcome, func(b *testing.B) {
				indexes := containsBenchIndexes(b, docs, variant.hashKeys)
				if indexes[0].Root().Contains(needleRoot) != want {
					b.Fatal("fixture verdict is wrong")
				}
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					containsBenchSink = indexes[i%len(indexes)].Root().Contains(needleRoot)
				}
			})
		}
		b.Run("raw/"+outcome, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				got, err := RawContains(docs[i%len(docs)], needle)
				if err != nil {
					b.Fatal(err)
				}
				containsBenchSink = got
			}
		})
		b.Run("naive/"+outcome, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				h, okH := refDecode(docs[i%len(docs)])
				n, okN := refDecode(needle)
				if !okH || !okN {
					b.Fatal("reference decode failed")
				}
				containsBenchSink = refContains(h, n)
			}
		})
	}
}

// BenchmarkContainsDeep probes a citm-like nested document with a needle
// that descends four object levels and one array, the deep-recursion
// regime, over enriched indexes.
func BenchmarkContainsDeep(b *testing.B) {
	doc := []byte(`{"venue":{"name":"Grand Hall","location":{"city":"Lisbon","geo":{"lat":38.72,"lon":-9.14}}},` +
		`"events":[{"id":101,"prices":[2500,3500],"tags":["opera","gala"]},` +
		`{"id":102,"prices":[1500],"tags":["matinee"]},` +
		`{"id":103,"prices":[4500,5500,6500],"tags":["premiere","opera"]}],` +
		`"capacity":{"total":1859,"sections":[{"name":"orchestra","seats":874},{"name":"balcony","seats":985}]}}`)
	needles := map[string][]byte{
		"hit":  []byte(`{"venue":{"location":{"geo":{"lon":-9.14}}},"events":[{"tags":["premiere"]}]}`),
		"miss": []byte(`{"venue":{"location":{"geo":{"lon":-9.15}}},"events":[{"tags":["premiere"]}]}`),
	}
	indexes := containsBenchIndexes(b, [][]byte{doc}, true)
	root := indexes[0].Root()

	for _, outcome := range []string{"hit", "miss"} {
		needleIndex, err := containsIndex(needles[outcome])
		if err != nil {
			b.Fatal(err)
		}
		needleRoot := needleIndex.Root()
		want := outcome == "hit"
		if root.Contains(needleRoot) != want {
			b.Fatal("fixture verdict is wrong")
		}
		b.Run(outcome, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				containsBenchSink = root.Contains(needleRoot)
			}
		})
	}
}
