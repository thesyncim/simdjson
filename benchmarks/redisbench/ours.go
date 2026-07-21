package redisbench

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"
	"unsafe"

	"github.com/thesyncim/simdjson"
	"github.com/thesyncim/simdjson/document"
	"github.com/thesyncim/simdjson/query"
)

// This file measures our side of the RedisJSON comparison: what it costs, in
// bytes and single-core nanoseconds, to hold the corpora in a DocSet and run
// the five scenarios against it. The three query-shaped scenarios — filtered
// scan, scalar aggregate, and group-by aggregate — run through the query
// subpackage's compiled executor (ADR 0003), so the scoreboard times the real
// query engine, not a hand-rolled stand-in; the whole-corpus column projection
// runs through it too. The point-read and pointer scan stay DocSet primitives
// (the JSON.GET analogue is a single-document resolve, not a query plan), and
// containment stays the Node.Contains primitive — the capability edge, kept as
// its cheapest spelling. The classic variants are the honest pre-optimization
// DocSet (16-byte-per-entry tape, no dedup); the shape-tape variants measure the
// space lever on top of it: DocSet.ShapeTapes stores each conforming document as
// a bare value array with its keys deduplicated into the shape cache.
//
// The scenarios mirror ADR 0003 and the RediSearch commands run-redis.sh times:
//
//   - projection: resolve one field across the corpus (DocSet.AppendPointer, and
//     the query executor's SELECT <field> column), and a single-document probe
//     by ordinal — the JSON.GET analogue;
//   - filtered scan: SELECT COUNT(*) WHERE ContainKey = ContainValue, the
//     compiled equality predicate — the FT.SEARCH TAG filter;
//   - scalar aggregate: SELECT SUM(SumField) reduced over the typed column —
//     FT.AGGREGATE REDUCE SUM;
//   - group-by aggregate: SELECT ContainKey, COUNT(*) GROUP BY ContainKey, whose
//     result cardinality is the group count — FT.AGGREGATE GROUPBY;
//   - containment: RawContains of {ContainKey: ContainValue} against each
//     document — a structural predicate RedisJSON and RediSearch have no
//     operator for.

// OursVariant holds one corpus measured under one DocSet configuration.
type OursVariant struct {
	HashKeys bool `json:"hash_keys"`
	// ShapeTapes marks the shape-deduplicated storage mode: value-only tapes
	// for conforming documents, classic tapes for the rest.
	ShapeTapes bool `json:"shape_tapes,omitempty"`

	// Ingest is the minimum wall time for DocSet.ReadFrom over the whole
	// NDJSON corpus (validate + index + arena copy), from memory.
	IngestNS int64 `json:"ingest_ns"`

	// RetainedBytes is the measured live-heap delta attributable to the
	// DocSet: runtime.MemStats.HeapAlloc after ingest and full GC, minus the
	// same reading before, with the input buffer released. It includes arena
	// slack and per-document headers; ModeledBytes is the analytic floor:
	// SourceBytes + 16 bytes per stored structural entry, plus 16 bytes of
	// header per shape-taped document. Entries counts every stored 16-byte
	// entry via DocSet.Stats, which never widens a document; ShapeTapedDocs is
	// how many documents dedup admitted.
	RetainedBytes  int64 `json:"retained_bytes"`
	Entries        int64 `json:"entries"`
	ShapeTapedDocs int64 `json:"shape_taped_docs,omitempty"`
	ModeledBytes   int64 `json:"modeled_bytes"`

	// Whole-corpus scenario costs, minimum over repetitions, in nanoseconds
	// for the full pass (divide by Docs for per-document cost).
	//
	// ProjectPointerNS resolves the manifest's projection field across all
	// documents via DocSet.AppendPointer; ProjectColumnNS does the same
	// through ShapeCache.AppendField (the amortized shape-column path), 0 on
	// heterogeneous corpora where a per-shape cache cannot amortize.
	// SingleDocNS is the average cost of resolving that field on one document
	// by ordinal (DocSet.Doc + PointerCompiled) — the JSON.GET point-read
	// analogue. FilterNS counts documents whose ContainKey equals ContainValue
	// (column scan + scalar compare). SumNS sums SumField across the corpus
	// (AppendFieldInt64 + reduce), 0 when the corpus has no numeric anchor.
	// GroupNS interns every ContainKey value and reports the distinct count.
	// ContainNS evaluates RawContains of {ContainKey: ContainValue} against
	// each document. Fields tied to an absent anchor are 0.
	ProjectPointerNS int64 `json:"project_pointer_ns"`
	ProjectColumnNS  int64 `json:"project_column_ns,omitempty"`
	SingleDocNS      int64 `json:"single_doc_ns"`
	FilterNS         int64 `json:"filter_ns,omitempty"`
	SumNS            int64 `json:"sum_ns,omitempty"`
	GroupNS          int64 `json:"group_ns,omitempty"`
	ContainNS        int64 `json:"contain_ns,omitempty"`

	// The observed results, cross-checked against the manifest by Verify.
	ExtractHits  int   `json:"extract_hits"`
	FilterCount  int   `json:"filter_count"`
	SumObserved  int64 `json:"sum_observed,omitempty"`
	GroupCount   int   `json:"group_count,omitempty"`
	ContainCount int   `json:"contain_count,omitempty"`
}

// OursCorpus holds one corpus measured under both DocSet configurations.
type OursCorpus struct {
	Manifest Manifest      `json:"manifest"`
	Variants []OursVariant `json:"variants"`
}

// OursResults is the ours.json artifact.
type OursResults struct {
	GoVersion string       `json:"go_version"`
	GOARCH    string       `json:"goarch"`
	Corpora   []OursCorpus `json:"corpora"`
}

// heapAlloc returns HeapAlloc after settling the heap: two GC cycles so
// finalizer-freed memory is actually collected.
func heapAlloc() uint64 {
	runtime.GC()
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return ms.HeapAlloc
}

// timeMin runs fn reps times and returns the minimum wall time.
func timeMin(reps int, fn func()) int64 {
	best := int64(0)
	for i := 0; i < reps; i++ {
		start := time.Now()
		fn()
		d := time.Since(start).Nanoseconds()
		if i == 0 || d < best {
			best = d
		}
	}
	return best
}

// countValue reads the single COUNT cell of a one-column, one-row aggregate
// result as an int64, the shape SELECT COUNT(*) returns.
func countValue(r query.Result) int64 {
	if len(r.Columns) == 0 || len(r.Columns[0].Cells) == 0 {
		return 0
	}
	n, _ := r.Columns[0].Cells[0].Int64()
	return n
}

// MeasureCorpus ingests the corpus in dir into a DocSet with the given options
// and measures space and the manifest's scenarios. reps is the repetition
// count for every timed section (minimum wins).
func MeasureCorpus(dir string, m Manifest, hashKeys, shapeTapes bool, reps int) (OursVariant, error) {
	v := OursVariant{HashKeys: hashKeys, ShapeTapes: shapeTapes}

	base := heapAlloc()
	buf, err := os.ReadFile(filepath.Join(dir, "docs.ndjson"))
	if err != nil {
		return v, err
	}

	// Ingest: repeated from-scratch builds, keeping the last set. The input is
	// in memory on both sides of the comparison (redis-cli --pipe streams a
	// freshly written file from the page cache).
	var set *simdjson.DocSet
	v.IngestNS = timeMin(reps, func() {
		s := &simdjson.DocSet{
			Options:    document.IndexOptions{HashKeys: hashKeys},
			ShapeTapes: shapeTapes,
		}
		if _, err2 := s.ReadFrom(bytes.NewReader(buf)); err2 != nil {
			err = err2
		}
		set = s
	})
	if err != nil {
		return v, err
	}
	if set.Len() != m.Docs {
		return v, fmt.Errorf("%s: ingested %d documents, manifest says %d", m.Name, set.Len(), m.Docs)
	}

	// Space: release the input, settle the heap, and charge everything still
	// live above the pre-read baseline to the DocSet. Entry counts come from
	// Stats, never Doc: widening a shape-taped document would materialize the
	// classic tape this variant exists to drop.
	buf = nil
	if retained := int64(heapAlloc()) - int64(base); retained > 0 {
		v.RetainedBytes = retained
	}
	entrySize := int64(unsafe.Sizeof(simdjson.IndexEntry{}))
	st := set.Stats()
	v.Entries = st.TapeEntries + st.ValueEntries + st.NarrowValueEntries
	v.ShapeTapedDocs = int64(st.ShapeTaped)
	v.ModeledBytes = m.SourceBytes +
		(st.TapeEntries+st.ValueEntries)*entrySize +
		st.NarrowValueEntries*(entrySize/2) +
		v.ShapeTapedDocs*entrySize

	// Projection. AppendPointer scans one field across the corpus; on
	// clustered corpora ShapeCache.AppendField amortizes the same read per
	// shape. Both are the pre-plan spelling the query executor will inherit.
	project, err := simdjson.CompilePointer("/" + m.ExtractField)
	if err != nil {
		return v, err
	}
	var col []simdjson.RawValue
	v.ProjectPointerNS = timeMin(reps, func() {
		col, err = set.AppendPointer(col[:0], project)
	})
	if err != nil {
		return v, err
	}
	for _, rv := range col {
		if rv.Kind() != document.Invalid {
			v.ExtractHits++
		}
	}
	if m.Class != "heterogeneous" {
		// Whole-corpus column projection through the query executor: SELECT
		// <ExtractField>. For a single top-level field the executor extracts the
		// same fused ShapeCache.AppendField column the primitive did, now behind
		// the compiled plan the scoreboard exists to measure. The projection's
		// null cells are the absent values, so the non-null count is the present
		// count the pointer pass recorded (these anchor fields are never an
		// explicit null).
		projQ := query.Select(query.Path(m.ExtractField))
		var projRes query.Result
		v.ProjectColumnNS = timeMin(reps, func() {
			projRes, err = projQ.Run(set)
		})
		if err != nil {
			return v, err
		}
		hits := 0
		for _, cell := range projRes.Columns[0].Cells {
			if !cell.IsNull() {
				hits++
			}
		}
		if hits != v.ExtractHits {
			return v, fmt.Errorf("%s: query projection found %d non-null values, pointer found %d present", m.Name, hits, v.ExtractHits)
		}
	}

	// Single-document projection: 1024 ordinals spread across the set — the
	// JSON.GET point-read analogue.
	stride := set.Len() / 1024
	if stride == 0 {
		stride = 1
	}
	probes := 0
	for i := 0; i < set.Len(); i += stride {
		probes++
	}
	total := timeMin(reps, func() {
		for i := 0; i < set.Len(); i += stride {
			doc := set.Doc(i)
			if _, _, err2 := doc.PointerCompiled(project); err2 != nil {
				err = err2
			}
		}
	})
	if err != nil {
		return v, err
	}
	v.SingleDocNS = total / int64(probes)

	// The filter/group/containment scenarios all key off ContainKey.
	if m.ContainKey != "" {
		// Filtered scan through the query executor: SELECT COUNT(*) WHERE
		// ContainKey = ContainValue — the compiled equality predicate over the
		// extracted column, the engine's own filtered count.
		filterQ := query.Select(query.Count()).Where(query.Cmp(m.ContainKey, query.Eq, m.ContainValue))
		var filterRes query.Result
		v.FilterNS = timeMin(reps, func() {
			filterRes, err = filterQ.Run(set)
		})
		if err != nil {
			return v, err
		}
		v.FilterCount = int(countValue(filterRes))

		// Group-by aggregate through the query executor: SELECT ContainKey,
		// COUNT(*) GROUP BY ContainKey. The result cardinality is the group
		// count — one row per distinct value plus the null group for documents
		// missing the field, the SQL semantics groupCardinality models and the
		// single empty-tag group RediSearch collects.
		groupQ := query.Select(query.Path(m.ContainKey), query.Count()).GroupBy(m.ContainKey)
		var groupRes query.Result
		v.GroupNS = timeMin(reps, func() {
			groupRes, err = groupQ.Run(set)
		})
		if err != nil {
			return v, err
		}
		v.GroupCount = groupRes.RowCount

		// Containment: {ContainKey: ContainValue} against each document. This
		// is the many-documents form RawContains documents — the needle is
		// indexed once and evaluated with Node.Contains against each already
		// indexed document root, the same containment contract the one-shot
		// RawContains wraps — and it is exactly the capability RedisJSON and
		// RediSearch have no operator for.
		needle := []byte(fmt.Sprintf(`{%q:%q}`, m.ContainKey, m.ContainValue))
		ne, err := simdjson.RequiredIndexEntries(needle)
		if err != nil {
			return v, err
		}
		nidx, err := simdjson.BuildIndex(needle, make([]simdjson.IndexEntry, ne))
		if err != nil {
			return v, err
		}
		needleRoot := nidx.Root()
		cc := 0
		v.ContainNS = timeMin(reps, func() {
			cc = 0
			for i := 0; i < set.Len(); i++ {
				if set.Doc(i).Root().Contains(needleRoot) {
					cc++
				}
			}
		})
		v.ContainCount = cc
	}

	// Scalar aggregate through the query executor: SELECT SUM(SumField). The
	// executor parses the typed numeric column at scan speed and reduces it; the
	// exact int64 sum of these corpora is within float64's integer range, so the
	// observed value read back off the result cell is exact.
	if m.SumField != "" {
		sumQ := query.Select(query.Sum(m.SumField))
		var sumRes query.Result
		v.SumNS = timeMin(reps, func() {
			sumRes, err = sumQ.Run(set)
		})
		if err != nil {
			return v, err
		}
		if f, ok := sumRes.Columns[0].Cells[0].Float64(); ok {
			v.SumObserved = int64(f)
		}
	}

	runtime.KeepAlive(set)
	return v, nil
}

// Verify cross-checks a variant's observed results against the manifest's
// expectations, returning a list of mismatches (empty means verified). This is
// the differential gate: our side must agree with the generator before any
// ratio against RedisJSON is meaningful.
func (v OursVariant) Verify(m Manifest) []string {
	var bad []string
	if v.ExtractHits != m.ExtractHits {
		bad = append(bad, fmt.Sprintf("projection hits %d, want %d", v.ExtractHits, m.ExtractHits))
	}
	if m.ContainKey != "" {
		if v.FilterCount != m.ContainExpected {
			bad = append(bad, fmt.Sprintf("filter count %d, want %d", v.FilterCount, m.ContainExpected))
		}
		if v.ContainCount != m.ContainExpected {
			bad = append(bad, fmt.Sprintf("contain count %d, want %d", v.ContainCount, m.ContainExpected))
		}
		if v.GroupCount != m.GroupExpected {
			bad = append(bad, fmt.Sprintf("group count %d, want %d", v.GroupCount, m.GroupExpected))
		}
	}
	if m.SumField != "" && v.SumObserved != m.SumExpected {
		bad = append(bad, fmt.Sprintf("sum %d, want %d", v.SumObserved, m.SumExpected))
	}
	return bad
}

// MeasureDir measures one corpus directory under both HashKeys settings and,
// on top of the enriched configuration, the shape-tape mode. Every variant
// must reproduce the manifest's expected results before its numbers mean
// anything.
func MeasureDir(dir string, reps int) (OursCorpus, error) {
	m, err := ReadManifest(dir)
	if err != nil {
		return OursCorpus{}, err
	}
	c := OursCorpus{Manifest: m}
	for _, cfg := range []struct{ hashKeys, shapeTapes bool }{
		{false, false}, {true, false}, {true, true},
	} {
		v, err := MeasureCorpus(dir, m, cfg.hashKeys, cfg.shapeTapes, reps)
		if err != nil {
			return c, fmt.Errorf("%s hashkeys=%t shapetapes=%t: %v", m.Name, cfg.hashKeys, cfg.shapeTapes, err)
		}
		if bad := v.Verify(m); len(bad) != 0 {
			return c, fmt.Errorf("%s hashkeys=%t shapetapes=%t: verification failed: %v", m.Name, cfg.hashKeys, cfg.shapeTapes, bad)
		}
		c.Variants = append(c.Variants, v)
	}
	return c, nil
}
