package pgbaseline

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
)

// This file measures our side of the baseline: what it costs, in bytes and
// single-core nanoseconds, to hold the phase-0 corpora in a DocSet and run
// the comparison queries against it with today's public API. The classic
// variants are the honest pre-optimization starting point the ADR asks for
// — the 16-byte-per-entry tape, no postings, no containment evaluator — and
// the shape-tape variants measure the phase-1 lever on top of it:
// DocSet.ShapeTapes stores each conforming document as a bare value array
// with its keys deduplicated into the shape cache.

// OursVariant holds one corpus measured under one DocSet configuration.
type OursVariant struct {
	HashKeys bool `json:"hash_keys"`
	// ShapeTapes marks the phase-1 storage mode: shape-deduplicated tapes
	// for conforming documents, classic tapes for the rest.
	ShapeTapes bool `json:"shape_tapes,omitempty"`

	// Ingest is the minimum wall time for DocSet.ReadFrom over the whole
	// NDJSON corpus (validate + index + arena copy), from memory.
	IngestNS int64 `json:"ingest_ns"`

	// RetainedBytes is the measured live-heap delta attributable to the
	// DocSet: runtime.MemStats.HeapAlloc after ingest and full GC, minus
	// the same reading before, with the input buffer released. It includes
	// arena slack and per-document headers; ModeledBytes is the analytic
	// floor: SourceBytes + 16 bytes per stored structural entry, plus 16
	// bytes of header per shape-taped document. The gap between the two is
	// allocator/arena overhead (and the key-hash enrichment when HashKeys
	// is on). Entries counts every stored 16-byte entry — classic tapes
	// plus value arrays — via DocSet.Stats, which never widens a document;
	// ShapeTapedDocs is how many documents dedup admitted.
	RetainedBytes  int64 `json:"retained_bytes"`
	Entries        int64 `json:"entries"`
	ShapeTapedDocs int64 `json:"shape_taped_docs,omitempty"`
	ModeledBytes   int64 `json:"modeled_bytes"`

	// Whole-corpus query costs, minimum over repetitions, in nanoseconds
	// for the full pass (divide by Docs for per-document cost).
	// ExtractPointerNS resolves the manifest's extraction field across all
	// documents via DocSet.AppendPointer. ExtractColumnNS does the same
	// through ShapeCache.AppendField (the amortized shape-column path); it
	// is 0 when skipped (heterogeneous corpora, where a per-shape cache
	// cannot amortize). ExistNS counts documents containing ExistKey and
	// ContainNS counts documents whose ContainKey equals the manifest's
	// value, both via AppendPointer, which is today's only spelling for
	// either predicate: a full column scan, no index. ContainNS is 0 when
	// the corpus skips containment.
	ExtractPointerNS int64 `json:"extract_pointer_ns"`
	ExtractColumnNS  int64 `json:"extract_column_ns,omitempty"`
	ExistNS          int64 `json:"exist_ns"`
	ContainNS        int64 `json:"contain_ns,omitempty"`

	// SingleDocNS is the average cost of resolving the extraction field on
	// one document by ordinal (DocSet.Doc + PointerCompiled) — the analogue
	// of PostgreSQL's single-row-by-ctid probe.
	SingleDocNS int64 `json:"single_doc_ns"`

	// The observed counts, cross-checked against the manifest by Verify.
	ExtractHits  int `json:"extract_hits"`
	ExistCount   int `json:"exist_count"`
	ContainCount int `json:"contain_count"`
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

// MeasureCorpus ingests the corpus in dir into a DocSet with the given
// options and measures space and the manifest's queries. reps is the
// repetition count for every timed section (minimum wins).
func MeasureCorpus(dir string, m Manifest, hashKeys, shapeTapes bool, reps int) (OursVariant, error) {
	v := OursVariant{HashKeys: hashKeys, ShapeTapes: shapeTapes}

	base := heapAlloc()
	buf, err := os.ReadFile(filepath.Join(dir, "docs.ndjson"))
	if err != nil {
		return v, err
	}

	// Ingest: repeated from-scratch builds, keeping the last set. The
	// input is in memory on both sides of the comparison (PostgreSQL's
	// COPY reads a freshly written file from the page cache).
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

	// Space: release the input, settle the heap, and charge everything
	// still live above the pre-read baseline to the DocSet. Entry counts
	// come from Stats, never Doc: widening a shape-taped document would
	// materialize the classic tape this variant exists to drop.
	buf = nil
	if retained := int64(heapAlloc()) - int64(base); retained > 0 {
		v.RetainedBytes = retained
	}
	entrySize := int64(unsafe.Sizeof(simdjson.IndexEntry{}))
	st := set.Stats()
	// Entries counts every stored value entry across both widths; the modeled
	// floor costs the wide ones (classic tapes and wide value arrays) at the
	// full entry size and the narrow ones at half, plus one entry-sized header
	// per shape-taped document.
	v.Entries = st.TapeEntries + st.ValueEntries + st.NarrowValueEntries
	v.ShapeTapedDocs = int64(st.ShapeTaped)
	v.ModeledBytes = m.SourceBytes +
		(st.TapeEntries+st.ValueEntries)*entrySize +
		st.NarrowValueEntries*(entrySize/2) +
		v.ShapeTapedDocs*entrySize

	// Queries. Every predicate below is a full column scan via
	// AppendPointer — the pre-postings baseline.
	extract, err := simdjson.CompilePointer("/" + m.ExtractField)
	if err != nil {
		return v, err
	}
	exist, err := simdjson.CompilePointer("/" + m.ExistKey)
	if err != nil {
		return v, err
	}

	var col []simdjson.RawValue
	v.ExtractPointerNS = timeMin(reps, func() {
		col, err = set.AppendPointer(col[:0], extract)
	})
	if err != nil {
		return v, err
	}
	for _, rv := range col {
		if rv.Kind() != document.Invalid {
			v.ExtractHits++
		}
	}

	// The shape-column path amortizes per shape; on the heterogeneous
	// corpus every document is its own shape, so the cache cannot pay for
	// itself and the pointer path above is the honest spelling.
	if m.Class != "heterogeneous" {
		var cache simdjson.ShapeCache
		var fcol []simdjson.RawValue
		fcol = cache.AppendField(fcol[:0], set, m.ExtractField) // warm the cache
		v.ExtractColumnNS = timeMin(reps, func() {
			fcol = cache.AppendField(fcol[:0], set, m.ExtractField)
		})
		hits := 0
		for _, rv := range fcol {
			if rv.Kind() != document.Invalid {
				hits++
			}
		}
		if hits != v.ExtractHits {
			return v, fmt.Errorf("%s: column extraction found %d hits, pointer found %d", m.Name, hits, v.ExtractHits)
		}
	}

	count := 0
	v.ExistNS = timeMin(reps, func() {
		col, err = set.AppendPointer(col[:0], exist)
		count = 0
		for _, rv := range col {
			if rv.Kind() != document.Invalid {
				count++
			}
		}
	})
	if err != nil {
		return v, err
	}
	v.ExistCount = count

	if m.ContainKey != "" {
		contain, err := simdjson.CompilePointer("/" + m.ContainKey)
		if err != nil {
			return v, err
		}
		want := []byte(`"` + m.ContainValue + `"`)
		v.ContainNS = timeMin(reps, func() {
			col, err = set.AppendPointer(col[:0], contain)
			count = 0
			for _, rv := range col {
				if bytes.Equal(rv.Bytes(), want) {
					count++
				}
			}
		})
		if err != nil {
			return v, err
		}
		v.ContainCount = count
	}

	// Single-document probe: 1024 ordinals spread across the set.
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
			if _, _, err2 := doc.PointerCompiled(extract); err2 != nil {
				err = err2
			}
		}
	})
	if err != nil {
		return v, err
	}
	v.SingleDocNS = total / int64(probes)

	runtime.KeepAlive(set)
	return v, nil
}

// Verify cross-checks a variant's observed counts against the manifest's
// expectations, returning a list of mismatches (empty means verified).
// This is the differential gate: both engines must agree with the
// generator before any ratio is meaningful.
func (v OursVariant) Verify(m Manifest) []string {
	var bad []string
	if v.ExtractHits != m.ExtractHits {
		bad = append(bad, fmt.Sprintf("extract hits %d, want %d", v.ExtractHits, m.ExtractHits))
	}
	if v.ExistCount != m.ExistExpected {
		bad = append(bad, fmt.Sprintf("exist count %d, want %d", v.ExistCount, m.ExistExpected))
	}
	if m.ContainKey != "" && v.ContainCount != m.ContainExpected {
		bad = append(bad, fmt.Sprintf("contain count %d, want %d", v.ContainCount, m.ContainExpected))
	}
	return bad
}

// MeasureDir measures one corpus directory under both HashKeys settings and,
// on top of the enriched configuration, the phase-1 shape-tape mode. Every
// variant must reproduce the manifest's expected counts before its numbers
// mean anything.
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
