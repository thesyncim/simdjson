package query

import (
	"fmt"
	"testing"

	"github.com/thesyncim/simdjson"
	"github.com/thesyncim/simdjson/document"
)

// Benchmarks for the four query shapes that feed the RedisJSON scoreboard:
// path projection (one and four fields), a filtered scan at ~10% selectivity,
// a scalar SUM, and a GROUP BY with SUM. Each runs over an inline fixture of
// flat sixteen-field numeric records and reports ns/doc and rows/sec, the
// units the scoreboard compares. The query compiles once (outside the timed
// loop) and each iteration is one full columnar pass over the corpus.

const benchDocs = 4096

// benchRecord is one flat sixteen-field record. f0 takes ten values so an
// equality filter on it selects about a tenth of the corpus, and the record is
// numeric so the aggregates have work to reduce.
func benchRecord(i int) []byte {
	return fmt.Appendf(nil,
		`{"f0":%d,"f1":%d,"f2":%d,"f3":%d,"f4":%d,"f5":%d,"f6":%d,"f7":%d,`+
			`"f8":%d,"f9":%d,"f10":%d,"f11":%d,"f12":%d,"f13":%d,"f14":%d,"f15":%d}`,
		i%10, i+1, i+2, i+3, i+4, i+5, i+6, i+7,
		i+8, i+9, i+10, i+11, i+12, i+13, i+14, i+15)
}

func benchDocSet(b *testing.B) *simdjson.DocSet {
	b.Helper()
	set := &simdjson.DocSet{}
	set.Options = document.IndexOptions{HashKeys: true}
	for i := 0; i < benchDocs; i++ {
		if _, err := set.Append(benchRecord(i)); err != nil {
			b.Fatalf("Append: %v", err)
		}
	}
	return set
}

var benchSink Result

func runQueryBench(b *testing.B, q *Query) {
	set := benchDocSet(b)
	if _, err := q.Run(set); err != nil { // compile once, off the clock
		b.Fatalf("Run: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r, err := q.Run(set)
		if err != nil {
			b.Fatalf("Run: %v", err)
		}
		benchSink = r
	}
	b.StopTimer()
	rows := float64(b.N) * float64(benchDocs)
	b.ReportMetric(rows/b.Elapsed().Seconds(), "rows/sec")
	b.ReportMetric(b.Elapsed().Seconds()*1e9/rows, "ns/doc")
}

// BenchmarkQueryProjection1 projects a single field.
func BenchmarkQueryProjection1(b *testing.B) {
	runQueryBench(b, Select(Path("f0")))
}

// BenchmarkQueryProjection4 projects four fields.
func BenchmarkQueryProjection4(b *testing.B) {
	runQueryBench(b, Select(Path("f0"), Path("f1"), Path("f2"), Path("f3")))
}

// BenchmarkQueryFilteredScan projects two fields under an equality filter that
// selects about a tenth of the rows.
func BenchmarkQueryFilteredScan(b *testing.B) {
	runQueryBench(b, Select(Path("f0"), Path("f1")).Where(Cmp("f0", Eq, 0)))
}

// BenchmarkQuerySumAggregate reduces one field with SUM.
func BenchmarkQuerySumAggregate(b *testing.B) {
	runQueryBench(b, Select(Sum("f3")))
}

// BenchmarkQueryGroupBySum groups by one field and sums another.
func BenchmarkQueryGroupBySum(b *testing.B) {
	runQueryBench(b, Select(Path("f0"), Sum("f3")).GroupBy("f0"))
}
