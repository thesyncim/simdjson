package duckdbbench

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"github.com/thesyncim/simdjson"
	"github.com/thesyncim/simdjson/document"
	"github.com/thesyncim/simdjson/query"
)

// OursStore is one fully keyed, mutable Store measurement. HeapBytes includes
// documents, key lookup, snapshots, chunks, and the declared exact index. It
// is measured before the destructive mutation smoke so the storage row and
// read timings describe the same state.
type OursStore struct {
	LoadNS       int64  `json:"load_ns"`
	IndexBuildNS int64  `json:"index_build_ns,omitempty"`
	HeapBytes    int64  `json:"heap_bytes"`
	IndexBytes   uint64 `json:"index_bytes,omitempty"`

	PointNS   int64 `json:"point_ns"`
	FilterNS  int64 `json:"filter_ns,omitempty"`
	SumNS     int64 `json:"sum_ns,omitempty"`
	GroupNS   int64 `json:"group_ns,omitempty"`
	ContainNS int64 `json:"contain_ns,omitempty"`

	MutationOps  int   `json:"mutation_ops,omitempty"`
	UpdateNSOp   int64 `json:"update_ns_op,omitempty"`
	DeleteNSOp   int64 `json:"delete_ns_op,omitempty"`
	AfterDeletes int   `json:"after_deletes,omitempty"`

	DocsObserved int   `json:"docs_observed"`
	ExtractHits  int   `json:"extract_hits"`
	FilterCount  int   `json:"filter_count,omitempty"`
	SumObserved  int64 `json:"sum_observed,omitempty"`
	GroupCount   int   `json:"group_count,omitempty"`
	ContainCount int   `json:"contain_count,omitempty"`
}

// OursCorpus binds one Store measurement to the exact generated manifest.
type OursCorpus struct {
	Manifest Manifest  `json:"manifest"`
	Store    OursStore `json:"store"`
}

// OursResults is the deterministic JSON artifact consumed by the report step.
type OursResults struct {
	GoVersion string       `json:"go_version"`
	GOOS      string       `json:"goos"`
	GOARCH    string       `json:"goarch"`
	Host      string       `json:"host,omitempty"`
	Corpora   []OursCorpus `json:"corpora"`
}

// heapAlloc settles finalizers and returns the live Go heap. This is not RSS:
// the report labels it separately from DuckDB's file and buffer-manager bytes.
func heapAlloc() uint64 {
	runtime.GC()
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return ms.HeapAlloc
}

func timeMin(reps int, fn func()) int64 {
	if reps < 1 {
		reps = 1
	}
	var best int64
	for i := 0; i < reps; i++ {
		start := time.Now()
		fn()
		ns := time.Since(start).Nanoseconds()
		if i == 0 || ns < best {
			best = ns
		}
	}
	return best
}

func countValue(r query.Result) int64 {
	if len(r.Columns) == 0 || len(r.Columns[0].Cells) == 0 {
		return 0
	}
	n, _ := r.Columns[0].Cells[0].Int64()
	return n
}

const duckDBBenchStoreIndex = "duckdbbench_filter"

// buildMeasuredStore consumes NDJSON from memory. Keys are prepared before
// timing on both sides; Put still copies every key and document into Store.
func buildMeasuredStore(buf []byte, keys []string) (*simdjson.Store, error) {
	store := simdjson.NewStore(simdjson.StoreOptions{ShapeTapes: true})
	row := 0
	for len(buf) != 0 {
		end := bytes.IndexByte(buf, '\n')
		if end < 0 {
			end = len(buf)
		}
		if end != 0 {
			if row >= len(keys) {
				return nil, fmt.Errorf("NDJSON has more than %d documents", len(keys))
			}
			if _, err := store.Put(keys[row], buf[:end]); err != nil {
				return nil, fmt.Errorf("document %d: %w", row, err)
			}
			row++
		}
		if end == len(buf) {
			break
		}
		buf = buf[end+1:]
	}
	if row != len(keys) {
		return nil, fmt.Errorf("NDJSON has %d documents, manifest says %d", row, len(keys))
	}
	return store, nil
}

func buildMeasuredStoreIndex(store *simdjson.Store, m Manifest) error {
	if m.ContainKey == "" {
		return nil
	}
	if _, err := store.CreateIndex(simdjson.StoreIndexDefinition{
		Name:  duckDBBenchStoreIndex,
		Paths: []string{"/" + m.ContainKey},
	}); err != nil {
		return err
	}
	info, err := store.BackfillIndex(duckDBBenchStoreIndex, 0)
	if err != nil {
		return err
	}
	if info.State != simdjson.StoreIndexReady || info.CoveredChunks != info.TotalChunks {
		return fmt.Errorf("index stopped at state=%d coverage=%d/%d", info.State, info.CoveredChunks, info.TotalChunks)
	}
	return nil
}

func runStoreQuery(q *query.Query, snapshot simdjson.Snapshot, reps int) (query.Result, int64, error) {
	var result query.Result
	var workspace query.Workspace
	if err := q.RunSnapshotInto(&result, snapshot, &workspace); err != nil {
		return query.Result{}, 0, err
	}
	var runErr error
	ns := timeMin(reps, func() {
		runErr = q.RunSnapshotInto(&result, snapshot, &workspace)
	})
	return result, ns, runErr
}

// MeasureStoreCorpus measures the same logical state used by DuckDB: stable
// string keys, exact JSON bytes, and one materialized/indexed scalar path.
// Reads are warmed. Updates and deletes are separate per-key commits over at
// most 256 keys; they are a mutation smoke, not a claim of batch atomicity.
func MeasureStoreCorpus(dir string, m Manifest, reps int) (OursStore, error) {
	var v OursStore
	base := heapAlloc()
	buf, err := os.ReadFile(filepath.Join(dir, "docs.ndjson"))
	if err != nil {
		return v, err
	}
	keys := make([]string, m.Docs)
	for i := range keys {
		keys[i] = keyPrefix + strconv.Itoa(i)
	}

	var store *simdjson.Store
	for rep := 0; rep < max(reps, 1); rep++ {
		start := time.Now()
		candidate, buildErr := buildMeasuredStore(buf, keys)
		loadNS := time.Since(start).Nanoseconds()
		if buildErr != nil {
			return v, buildErr
		}
		if rep == 0 || loadNS < v.LoadNS {
			v.LoadNS = loadNS
		}

		start = time.Now()
		if err := buildMeasuredStoreIndex(candidate, m); err != nil {
			return v, err
		}
		indexNS := time.Since(start).Nanoseconds()
		if m.ContainKey != "" && (rep == 0 || indexNS < v.IndexBuildNS) {
			v.IndexBuildNS = indexNS
		}
		store = candidate
	}
	v.DocsObserved = store.Len()

	buf = nil
	keys = nil
	if retained := int64(heapAlloc()) - int64(base); retained > 0 {
		v.HeapBytes = retained
	}
	if m.ContainKey != "" {
		stats, err := store.IndexStats(duckDBBenchStoreIndex)
		if err != nil {
			return v, err
		}
		v.IndexBytes = stats.EstimatedBytes
	}

	snapshot := store.Snapshot()
	project, err := simdjson.CompilePointer("/" + m.ExtractField)
	if err != nil {
		return v, err
	}
	projected, err := snapshot.AppendPointer(nil, project)
	if err != nil {
		return v, err
	}
	for _, raw := range projected {
		if raw.Kind() != document.Invalid {
			v.ExtractHits++
		}
	}

	probeKey := keyPrefix + "0"
	if _, ok := snapshot.Get(probeKey); !ok {
		return v, fmt.Errorf("point key %q missing", probeKey)
	}
	pointBytes := 0
	v.PointNS = timeMin(reps, func() {
		index, _ := snapshot.Get(probeKey)
		node, ok, pointErr := index.PointerCompiled(project)
		if pointErr != nil {
			err = pointErr
			return
		}
		if ok {
			pointBytes += len(node.Raw().Bytes())
		}
	})
	if err != nil {
		return v, err
	}
	runtime.KeepAlive(pointBytes)

	if m.ContainKey != "" {
		filter := query.Select(query.Count()).Where(query.Cmp(m.ContainKey, query.Eq, m.ContainValue))
		result, ns, err := runStoreQuery(filter, snapshot, reps)
		if err != nil {
			return v, err
		}
		v.FilterNS, v.FilterCount = ns, int(countValue(result))

		group := query.Select(query.Path(m.ContainKey), query.Count()).GroupBy(m.ContainKey)
		result, v.GroupNS, err = runStoreQuery(group, snapshot, reps)
		if err != nil {
			return v, err
		}
		v.GroupCount = result.RowCount

		needle := fmt.Sprintf(`{%q:%q}`, m.ContainKey, m.ContainValue)
		contain := query.Select(query.Count()).Where(query.Contains("", needle))
		result, v.ContainNS, err = runStoreQuery(contain, snapshot, reps)
		if err != nil {
			return v, err
		}
		v.ContainCount = int(countValue(result))
	}
	if m.SumField != "" {
		sum := query.Select(query.Sum(m.SumField))
		result, ns, err := runStoreQuery(sum, snapshot, reps)
		if err != nil {
			return v, err
		}
		v.SumNS = ns
		if f, ok := result.Columns[0].Cells[0].Float64(); ok {
			v.SumObserved = int64(f)
		}
	}

	// Drop the reader generation before destructive smoke measurements.
	snapshot = simdjson.Snapshot{}
	projected = nil
	runtime.GC()
	v.MutationOps = min(m.Docs, 256)
	mutationKeys := make([]string, v.MutationOps)
	for i := range mutationKeys {
		mutationKeys[i] = keyPrefix + strconv.Itoa(i)
	}
	replacement := []byte(`{"bench_mutation":true}`)
	start := time.Now()
	for i := 0; i < v.MutationOps; i++ {
		created, putErr := store.Put(mutationKeys[i], replacement)
		if putErr != nil || created {
			return v, fmt.Errorf("update %s: created=%v err=%v", mutationKeys[i], created, putErr)
		}
	}
	v.UpdateNSOp = time.Since(start).Nanoseconds() / int64(v.MutationOps)
	if store.Len() != m.Docs {
		return v, fmt.Errorf("updates changed cardinality to %d, want %d", store.Len(), m.Docs)
	}
	start = time.Now()
	for i := 0; i < v.MutationOps; i++ {
		if !store.Delete(mutationKeys[i]) {
			return v, fmt.Errorf("delete %s: missing key", mutationKeys[i])
		}
	}
	v.DeleteNSOp = time.Since(start).Nanoseconds() / int64(v.MutationOps)
	v.AfterDeletes = store.Len()
	if v.AfterDeletes != m.Docs-v.MutationOps {
		return v, fmt.Errorf("after deletes %d documents, want %d", v.AfterDeletes, m.Docs-v.MutationOps)
	}
	runtime.KeepAlive(store)
	return v, nil
}

// Verify rejects a result before any cross-engine ratio is rendered.
func (v OursStore) Verify(m Manifest) []string {
	var bad []string
	if v.DocsObserved != m.Docs {
		bad = append(bad, fmt.Sprintf("documents %d, want %d", v.DocsObserved, m.Docs))
	}
	if v.ExtractHits != m.ExtractHits {
		bad = append(bad, fmt.Sprintf("projection hits %d, want %d", v.ExtractHits, m.ExtractHits))
	}
	if m.ContainKey != "" {
		if v.FilterCount != m.ContainExpected {
			bad = append(bad, fmt.Sprintf("filter count %d, want %d", v.FilterCount, m.ContainExpected))
		}
		if v.GroupCount != m.GroupExpected {
			bad = append(bad, fmt.Sprintf("group count %d, want %d", v.GroupCount, m.GroupExpected))
		}
		if v.ContainCount != m.ContainExpected {
			bad = append(bad, fmt.Sprintf("contain count %d, want %d", v.ContainCount, m.ContainExpected))
		}
	}
	if m.SumField != "" && v.SumObserved != m.SumExpected {
		bad = append(bad, fmt.Sprintf("sum %d, want %d", v.SumObserved, m.SumExpected))
	}
	if v.AfterDeletes != m.Docs-v.MutationOps {
		bad = append(bad, fmt.Sprintf("after deletes %d, want %d", v.AfterDeletes, m.Docs-v.MutationOps))
	}
	return bad
}

// MeasureDir loads the manifest, measures Store, and enforces generator truth.
func MeasureDir(dir string, reps int) (OursCorpus, error) {
	m, err := ReadManifest(dir)
	if err != nil {
		return OursCorpus{}, err
	}
	v, err := MeasureStoreCorpus(dir, m, reps)
	if err != nil {
		return OursCorpus{}, err
	}
	if bad := v.Verify(m); len(bad) != 0 {
		return OursCorpus{}, fmt.Errorf("%s: verification failed: %v", m.Name, bad)
	}
	return OursCorpus{Manifest: m, Store: v}, nil
}
