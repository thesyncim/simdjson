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

func runQueryIntoBench(b *testing.B, q *Query) {
	set := benchDocSet(b)
	var dst Result
	var w Workspace
	if err := q.RunInto(&dst, set, &w); err != nil {
		b.Fatalf("RunInto: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := q.RunInto(&dst, set, &w); err != nil {
			b.Fatalf("RunInto: %v", err)
		}
	}
	benchSink = dst
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

func BenchmarkQueryRunInto(b *testing.B) {
	benchmarks := map[string]*Query{
		"projection-1": Select(Path("f0")),
		"projection-4": Select(Path("f0"), Path("f1"), Path("f2"), Path("f3")),
		"filtered":     Select(Path("f0"), Path("f1")).Where(Cmp("f0", Eq, 0)),
		"sum":          Select(Sum("f3")),
		"group-sum":    Select(Path("f0"), Sum("f3")).GroupBy("f0"),
	}
	for name, q := range benchmarks {
		b.Run(name, func(b *testing.B) { runQueryIntoBench(b, q) })
	}
}

func BenchmarkQueryRunSnapshotIndexed(b *testing.B) {
	store := simdjson.NewStore(simdjson.StoreOptions{ChunkDocuments: 64, ShapeTapes: true})
	for i := 0; i < benchDocs; i++ {
		if _, err := store.Put(fmt.Sprintf("key-%05d", i), benchRecord(i)); err != nil {
			b.Fatal(err)
		}
	}
	for _, def := range []simdjson.StoreIndexDefinition{
		{Name: "f0", Paths: []string{"/f0"}},
		{Name: "f0_f1", Paths: []string{"/f0", "/f1"}},
	} {
		info, err := store.CreateIndex(def)
		if err != nil {
			b.Fatal(err)
		}
		if info, err = store.BackfillIndex(info.Name, 0); err != nil || info.State != simdjson.StoreIndexReady {
			b.Fatalf("BackfillIndex(%s) = (%+v,%v)", def.Name, info, err)
		}
	}
	snapshot := store.Snapshot()
	for _, test := range []struct {
		name string
		q    *Query
	}{
		{"single-10pct", Select(Path("f0"), Path("f1")).Where(Cmp("f0", Eq, 0))},
		{"compound-point", Select(Path("f0"), Path("f1")).Where(And(Cmp("f0", Eq, 0), Cmp("f1", Eq, 101)))},
		{"or-20pct", Select(Path("f0"), Path("f1")).Where(Or(Cmp("f0", Eq, 0), Cmp("f0", Eq, 1)))},
		{"not-90pct", Select(Count()).Where(Not(Cmp("f0", Eq, 0)))},
	} {
		b.Run(test.name, func(b *testing.B) {
			var dst Result
			var w Workspace
			if err := test.q.RunSnapshotInto(&dst, snapshot, &w); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := test.q.RunSnapshotInto(&dst, snapshot, &w); err != nil {
					b.Fatal(err)
				}
			}
			benchSink = dst
			b.StopTimer()
			rows := float64(b.N) * float64(benchDocs)
			b.ReportMetric(rows/b.Elapsed().Seconds(), "rows/sec")
			b.ReportMetric(b.Elapsed().Seconds()*1e9/rows, "ns/doc")
		})
	}
}
