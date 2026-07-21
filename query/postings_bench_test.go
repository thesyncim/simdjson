package query

import (
	"fmt"
	"testing"

	"github.com/thesyncim/simdjson"
	"github.com/thesyncim/simdjson/document"
)

// Benchmarks for the postings-accelerated WHERE: the same filter at four
// selectivities, run once with the inverted postings on (DocSet.Postings) and
// once off (the full columnar scan), so the ratio at each selectivity shows the
// primitive's pruning propagating up to the query layer. Selective posting
// ordinals are pushed into sparse column gathers, so equality and containment
// both avoid O(corpus) extraction; the exact compiled predicate still rechecks
// every gathered candidate. The 100% row prices the deliberate dense fallback
// once random gather no longer wins.

const selBenchDocs = 20000

// selRecord is one flat record carrying a scalar bucket key (sel) for equality
// and a single-element tag array (tags) for containment, both taking one of
// buckets values so a filter on value 0 selects 1/buckets of the corpus.
func selRecord(i, buckets int) []byte {
	return fmt.Appendf(nil, `{"sel":%d,"tags":["cat%d"],"v":%d}`, i%buckets, i%buckets, i)
}

func selDocSet(b *testing.B, buckets int, postings bool) *simdjson.DocSet {
	b.Helper()
	set := &simdjson.DocSet{}
	set.Options = document.IndexOptions{HashKeys: true}
	set.Postings = postings
	for i := 0; i < selBenchDocs; i++ {
		if _, err := set.Append(selRecord(i, buckets)); err != nil {
			b.Fatalf("Append: %v", err)
		}
	}
	return set
}

// selectivityLevels maps each target fraction to the bucket count that produces
// it over selBenchDocs rows.
var selectivityLevels = []struct {
	name    string
	buckets int
}{
	{"0.1pct", 1000},
	{"1pct", 100},
	{"10pct", 10},
	{"100pct", 1},
}

// benchWhere runs q over set b.N times, reporting rows/sec and ns/doc, the units
// the scoreboard compares.
func benchWhere(b *testing.B, q *Query, set *simdjson.DocSet) {
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
	rows := float64(b.N) * float64(selBenchDocs)
	b.ReportMetric(rows/b.Elapsed().Seconds(), "rows/sec")
	b.ReportMetric(b.Elapsed().Seconds()*1e9/rows, "ns/doc")
}

// BenchmarkWhereEqualitySelectivity contrasts postings against the full scan for
// a scalar equality at each selectivity.
func BenchmarkWhereEqualitySelectivity(b *testing.B) {
	for _, lvl := range selectivityLevels {
		q := Select(Count()).Where(Cmp("sel", Eq, 0))
		post := selDocSet(b, lvl.buckets, true)
		full := selDocSet(b, lvl.buckets, false)
		b.Run(lvl.name+"/postings", func(b *testing.B) { benchWhere(b, q, post) })
		b.Run(lvl.name+"/fullscan", func(b *testing.B) { benchWhere(b, q, full) })
	}
}

// BenchmarkWhereContainmentSelectivity contrasts postings against the full scan
// for a containment filter — the expensive per-row predicate whose pruning is
// the postings' largest query-layer win.
func BenchmarkWhereContainmentSelectivity(b *testing.B) {
	for _, lvl := range selectivityLevels {
		q := Select(Count()).Where(Contains("tags", `"cat0"`))
		post := selDocSet(b, lvl.buckets, true)
		full := selDocSet(b, lvl.buckets, false)
		b.Run(lvl.name+"/postings", func(b *testing.B) { benchWhere(b, q, post) })
		b.Run(lvl.name+"/fullscan", func(b *testing.B) { benchWhere(b, q, full) })
	}
}

// BenchmarkParserCompile measures the one-time cost of parsing and compiling a
// representative query. It is paid once per query, then amortized over every row
// of every Run: divided by a corpus of selBenchDocs rows it is a small fraction
// of a nanosecond per row, and it vanishes entirely on reuse across corpora.
func BenchmarkParserCompile(b *testing.B) {
	const sql = `SELECT team, SUM(score), COUNT(*) FROM players ` +
		`WHERE active = true AND score >= 10 OR region = 'eu' ` +
		`GROUP BY team ORDER BY team ASC LIMIT 10`
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		q, err := Compile(sql)
		if err != nil {
			b.Fatalf("Compile: %v", err)
		}
		benchCompileSink = q
	}
}

var benchCompileSink *Query
