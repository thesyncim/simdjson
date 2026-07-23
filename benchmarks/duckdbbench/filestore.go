package duckdbbench

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strconv"
	"time"

	"github.com/thesyncim/simdjson"
	"github.com/thesyncim/simdjson/internal/storeio"
	"github.com/thesyncim/simdjson/query"
)

const duckDBBenchFileCacheBytes = int64(8 << 20)

// OursFileStore is the durable, bounded-residency measurement over the same
// keys, JSON bytes, and frozen exact index as OursStore. HeapBytes excludes the
// released NDJSON transport. CacheResidentBytes is admitted page data, while
// cache, commit, and reusable-extent capacities are independently bounded
// pointer-free arenas. AccountedResidentBytes deliberately uses the full
// commit/reusable arenas as a conservative upper bound and only the admitted
// portion of the read cache.
type OursFileStore struct {
	LoadNS   int64 `json:"load_ns"`
	ReopenNS int64 `json:"reopen_ns"`

	FileBytes               int64  `json:"file_bytes"`
	HeapBytes               int64  `json:"heap_bytes"`
	CacheResidentBytes      uint64 `json:"cache_resident_bytes"`
	CacheCapacityBytes      uint64 `json:"cache_capacity_bytes"`
	CommitCapacityBytes     uint64 `json:"commit_capacity_bytes"`
	ReusableCapacityBytes   uint64 `json:"reusable_capacity_bytes"`
	ReusableExternalBytes   uint64 `json:"reusable_external_bytes,omitempty"`
	QueryBufferedBytes      int64  `json:"query_buffered_bytes,omitempty"`
	LoadDeviceCommits       uint64 `json:"load_device_commits,omitempty"`
	LoadCommittedBatches    uint64 `json:"load_committed_batches,omitempty"`
	LoadPendingRetiredBytes uint64 `json:"load_pending_retired_bytes,omitempty"`
	LoadReusableBytes       uint64 `json:"load_reusable_bytes,omitempty"`

	PointNS   int64 `json:"point_ns"`
	FilterNS  int64 `json:"filter_ns,omitempty"`
	SumNS     int64 `json:"sum_ns,omitempty"`
	GroupNS   int64 `json:"group_ns,omitempty"`
	ContainNS int64 `json:"contain_ns,omitempty"`

	MutationOps           int    `json:"mutation_ops,omitempty"`
	UpdateNSOp            int64  `json:"update_ns_op,omitempty"`
	DeleteNSOp            int64  `json:"delete_ns_op,omitempty"`
	AfterDeletes          int    `json:"after_deletes,omitempty"`
	PostMutationFileBytes int64  `json:"post_mutation_file_bytes,omitempty"`
	ReusableBytes         uint64 `json:"reusable_bytes,omitempty"`
	MutationReopenNS      int64  `json:"mutation_reopen_ns,omitempty"`

	PageReads   uint64 `json:"page_reads,omitempty"`
	ReadBytes   uint64 `json:"read_bytes,omitempty"`
	CacheHits   uint64 `json:"cache_hits,omitempty"`
	CacheMisses uint64 `json:"cache_misses,omitempty"`
	Evictions   uint64 `json:"evictions,omitempty"`

	DocsObserved int   `json:"docs_observed"`
	ExtractHits  int   `json:"extract_hits"`
	FilterCount  int   `json:"filter_count,omitempty"`
	SumObserved  int64 `json:"sum_observed,omitempty"`
	GroupCount   int   `json:"group_count,omitempty"`
	ContainCount int   `json:"contain_count,omitempty"`
}

// AccountedResidentBytes returns settled Go heap plus currently admitted
// cache extents, the complete commit staging arena, and the external portion
// of the reusable-extent arena. It is an engine-owned upper-bound view, not
// process RSS.
func (v OursFileStore) AccountedResidentBytes() int64 {
	const maxInt64 = int64(^uint64(0) >> 1)
	external := v.CacheResidentBytes
	if external > uint64(maxInt64) || v.CommitCapacityBytes > uint64(maxInt64)-external {
		return 0
	}
	external += v.CommitCapacityBytes
	if v.ReusableExternalBytes > uint64(maxInt64)-external {
		return 0
	}
	external += v.ReusableExternalBytes
	if v.HeapBytes < 0 || v.HeapBytes > maxInt64-int64(external) {
		return 0
	}
	return v.HeapBytes + int64(external)
}

func maxNDJSONDocumentBytes(buf []byte, docs int) (int, error) {
	maxBytes := 0
	err := forEachNDJSON(buf, docs, func(_ int, document []byte) error {
		maxBytes = max(maxBytes, len(document))
		return nil
	})
	return maxBytes, err
}

func fileStoreBenchOptions(m Manifest, maxDocumentBytes int, synchronous bool) simdjson.FileStoreOptions {
	maxDocumentBytes = max(maxDocumentBytes, 1)
	inlineBytes := min(maxDocumentBytes, 512)
	maxKeyBytes := len(keyPrefix) + len(strconv.Itoa(max(m.Docs-1, 0)))
	const chunkDocuments = 8
	worstDocumentPage := storeio.PageHeaderSize + storeio.PageTrailerSize +
		storeio.DocumentPagePayloadHeaderSize + chunkDocuments*storeio.DocumentPageRecordSize +
		chunkDocuments*(maxKeyBytes+max(inlineBytes, storeio.DocumentOverflowDescriptorSize))
	maxPageSize := 4096
	for maxPageSize < worstDocumentPage {
		maxPageSize <<= 1
	}
	residentBytes := duckDBBenchFileCacheBytes
	// Large single documents increase the worst-case dirty transaction. Keep
	// the common fixed 8 MiB cache, but scale the explicit bound rather than
	// silently accepting a corpus-dependent default.
	if needed := int64(maxDocumentBytes)*2 + 1<<20; needed > residentBytes {
		residentBytes = needed
	}
	options := simdjson.FileStoreOptions{
		// Eight stable slots keep this write-heavy 400-byte corpus in one 4 KiB
		// document page. The read/space default of 64 would repeatedly rewrite
		// growing 8-32 KiB chunks during ingestion and measure configuration
		// amplification rather than the page format.
		Store:             simdjson.StoreOptions{ChunkDocuments: chunkDocuments, ShapeTapes: true},
		PageSize:          4096,
		MaxPageSize:       maxPageSize,
		ResidentBytes:     residentBytes,
		ReadConcurrency:   1,
		ReadQueueDepth:    16,
		PrefetchQueue:     64,
		MaxKeyBytes:       maxKeyBytes,
		InlineValueBytes:  inlineBytes,
		MaxDocumentBytes:  maxDocumentBytes,
		Backend:           simdjson.FileStoreBackendPortable,
		Synchronous:       synchronous,
		MaxRetiredExtents: 512,
	}
	if m.ContainKey != "" {
		options.Indexes = []simdjson.StoreIndexDefinition{{
			Name: duckDBBenchStoreIndex, Paths: []string{"/" + m.ContainKey},
		}}
	}
	return options
}

type measuredFileStoreBuild struct {
	loadNS    int64
	fileBytes int64
	stats     simdjson.FileStoreStats
}

func buildMeasuredFileStore(path string, buf []byte, m Manifest, options simdjson.FileStoreOptions) (measuredFileStoreBuild, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return measuredFileStoreBuild{}, err
	}
	defer file.Close()
	start := time.Now()
	builder, err := simdjson.NewStoreBuilder(options.Store)
	if err != nil {
		return measuredFileStoreBuild{}, err
	}
	if err := forEachNDJSON(buf, m.Docs, func(row int, document []byte) error {
		if appendErr := builder.Append(keyPrefix+strconv.Itoa(row), document); appendErr != nil {
			return fmt.Errorf("durable document %d: %w", row, appendErr)
		}
		return nil
	}); err != nil {
		return measuredFileStoreBuild{}, err
	}
	source, err := builder.Build()
	if err != nil {
		return measuredFileStoreBuild{}, err
	}
	fileBytes, err := source.WriteFileStore(file, options)
	if err != nil {
		return measuredFileStoreBuild{}, err
	}
	loadNS := time.Since(start).Nanoseconds()
	// The compact creator performs one data/tree fence and one root fence. It
	// owns no persistent commit arena and creates no retired generations.
	stats := simdjson.FileStoreStats{
		DeviceCommits: 2, CommittedBatches: 1, LargestCommitGroup: 1,
		DocumentCount: uint64(m.Docs), FileEnd: uint64(fileBytes),
	}
	return measuredFileStoreBuild{loadNS: loadNS, fileBytes: fileBytes, stats: stats}, nil
}

func runFileQuery(q *query.Query, snapshot *simdjson.FileSnapshot, reps int, workspace *query.FileExecutionWorkspace) (query.Result, query.FileExecutionStats, int64, error) {
	options := query.FileExecutionOptions{
		Workers: 1, MemoryBytes: duckDBBenchFileCacheBytes, Workspace: workspace,
	}
	result, stats, err := q.RunFileSnapshot(snapshot, options)
	if err != nil {
		return query.Result{}, query.FileExecutionStats{}, 0, err
	}
	reps = max(reps, 1)
	var best int64
	for rep := 0; rep < reps; rep++ {
		start := time.Now()
		candidate, candidateStats, runErr := q.RunFileSnapshot(snapshot, options)
		ns := time.Since(start).Nanoseconds()
		if runErr != nil {
			return query.Result{}, query.FileExecutionStats{}, 0, runErr
		}
		if rep == 0 || ns < best {
			best, result, stats = ns, candidate, candidateStats
		}
	}
	return result, stats, best, nil
}

func observeFileQueryBuffer(dst *OursFileStore, stats query.FileExecutionStats) {
	peak := max(stats.PeakBatchBytes, stats.BufferedBytes)
	if peak > dst.QueryBufferedBytes {
		dst.QueryBufferedBytes = peak
	}
}

// MeasureFileStoreCorpus measures compact persistence, bounded warm reads,
// synchronous per-key durability, and recovery against the same generator
// truth as the heap Store. Load ends only after the compact generation is
// fenced and resources are closed. After mutations, cardinality is accepted
// only from a newly recovered FileStore.
func MeasureFileStoreCorpus(dir string, m Manifest, reps int) (OursFileStore, error) {
	var v OursFileStore
	base := heapAlloc()
	buf, err := os.ReadFile(filepath.Join(dir, "docs.ndjson"))
	if err != nil {
		return v, err
	}
	maxDocumentBytes, err := maxNDJSONDocumentBytes(buf, m.Docs)
	if err != nil {
		return v, err
	}
	loadOptions := fileStoreBenchOptions(m, maxDocumentBytes, false)
	tempDir, err := os.MkdirTemp("", "simdjson-duckdbbench-*")
	if err != nil {
		return v, err
	}
	defer os.RemoveAll(tempDir)

	var bestPath string
	for rep := 0; rep < max(reps, 1); rep++ {
		path := filepath.Join(tempDir, "store-"+strconv.Itoa(rep)+".db")
		build, buildErr := buildMeasuredFileStore(path, buf, m, loadOptions)
		if buildErr != nil {
			return v, buildErr
		}
		if rep == 0 || build.loadNS < v.LoadNS {
			v.LoadNS, v.FileBytes, bestPath = build.loadNS, build.fileBytes, path
			v.LoadDeviceCommits = build.stats.DeviceCommits
			v.LoadCommittedBatches = build.stats.CommittedBatches
			v.LoadPendingRetiredBytes = build.stats.PendingRetiredBytes
			v.LoadReusableBytes = build.stats.ReusableBytes
		}
	}
	buf = nil

	file, err := os.OpenFile(bestPath, os.O_RDWR, 0)
	if err != nil {
		return v, err
	}
	options := fileStoreBenchOptions(m, maxDocumentBytes, true)
	start := time.Now()
	store, err := simdjson.OpenFileStore(file, options)
	v.ReopenNS = time.Since(start).Nanoseconds()
	if err != nil {
		_ = file.Close()
		return v, err
	}
	storeOpen := true
	fileOpen := true
	defer func() {
		if storeOpen {
			_ = store.Close()
		}
		if fileOpen {
			_ = file.Close()
		}
	}()
	snapshot, err := store.Snapshot()
	if err != nil {
		return v, err
	}
	snapshotOpen := true
	defer func() {
		if snapshotOpen {
			_ = snapshot.Close()
		}
	}()
	v.DocsObserved = int(snapshot.Len())

	project, err := simdjson.CompilePointer("/" + m.ExtractField)
	if err != nil {
		return v, err
	}
	probeKey := keyPrefix + "0"
	var pointRaw []byte
	pointRaw, ok, err := snapshot.AppendRaw(pointRaw[:0], probeKey)
	if err != nil || !ok {
		return v, fmt.Errorf("durable point key %q: found=%v err=%v", probeKey, ok, err)
	}
	entries, err := simdjson.RequiredIndexEntries(pointRaw)
	if err != nil {
		return v, err
	}
	pointTape := make([]simdjson.IndexEntry, entries)
	pointBytes := 0
	v.PointNS = timeMin(reps, func() {
		pointRaw = pointRaw[:0]
		pointRaw, ok, err = snapshot.AppendRaw(pointRaw, probeKey)
		if err != nil || !ok {
			return
		}
		var index simdjson.Index
		index, err = simdjson.BuildIndex(pointRaw, pointTape)
		if err != nil {
			return
		}
		var node simdjson.Node
		node, ok, err = index.PointerCompiled(project)
		if err == nil && ok {
			pointBytes += len(node.Raw().Bytes())
		}
	})
	if err != nil || !ok {
		return v, fmt.Errorf("durable point projection: found=%v err=%v", ok, err)
	}
	runtime.KeepAlive(pointBytes)

	var workspace query.FileExecutionWorkspace
	extract := query.Select(query.Path(m.ExtractField))
	result, stats, _, err := runFileQuery(extract, snapshot, 1, &workspace)
	if err != nil {
		return v, err
	}
	observeFileQueryBuffer(&v, stats)
	if len(result.Columns) != 0 {
		for _, cell := range result.Columns[0].Cells {
			if cell.Kind() != query.KindNull {
				v.ExtractHits++
			}
		}
	}

	if m.ContainKey != "" {
		filter := query.Select(query.Count()).Where(query.Cmp(m.ContainKey, query.Eq, m.ContainValue))
		result, stats, v.FilterNS, err = runFileQuery(filter, snapshot, reps, &workspace)
		if err != nil {
			return v, err
		}
		observeFileQueryBuffer(&v, stats)
		v.FilterCount = int(countValue(result))

		group := query.Select(query.Path(m.ContainKey), query.Count()).GroupBy(m.ContainKey)
		result, stats, v.GroupNS, err = runFileQuery(group, snapshot, reps, &workspace)
		if err != nil {
			return v, err
		}
		observeFileQueryBuffer(&v, stats)
		v.GroupCount = result.RowCount

		needle := fmt.Sprintf(`{%q:%q}`, m.ContainKey, m.ContainValue)
		contain := query.Select(query.Count()).Where(query.Contains("", needle))
		result, stats, v.ContainNS, err = runFileQuery(contain, snapshot, reps, &workspace)
		if err != nil {
			return v, err
		}
		observeFileQueryBuffer(&v, stats)
		v.ContainCount = int(countValue(result))
	}
	if m.SumField != "" {
		sum := query.Select(query.Sum(m.SumField))
		result, stats, v.SumNS, err = runFileQuery(sum, snapshot, reps, &workspace)
		if err != nil {
			return v, err
		}
		observeFileQueryBuffer(&v, stats)
		if f, valid := result.Columns[0].Cells[0].Float64(); valid {
			v.SumObserved = int64(f)
		}
	}

	result = query.Result{}
	workspace.Release()
	pointRaw = nil
	pointTape = nil
	if retained := int64(heapAlloc()) - int64(base); retained > 0 {
		v.HeapBytes = retained
	}
	if profilePath := os.Getenv("DUCKDBBENCH_FILE_HEAP_PROFILE"); profilePath != "" {
		profile, profileErr := os.Create(profilePath)
		if profileErr != nil {
			return v, profileErr
		}
		profileErr = pprof.WriteHeapProfile(profile)
		closeErr := profile.Close()
		if profileErr != nil {
			return v, profileErr
		}
		if closeErr != nil {
			return v, closeErr
		}
	}
	storeStats := store.Stats()
	v.CacheResidentBytes = storeStats.ResidentBytes
	v.CacheCapacityBytes = storeStats.CapacityBytes
	v.CommitCapacityBytes = storeStats.CommitCapacityBytes
	v.ReusableCapacityBytes = storeStats.ReusableCapacityBytes
	v.ReusableExternalBytes = storeStats.ReusableExternalBytes
	v.PageReads = storeStats.PageReads
	v.ReadBytes = storeStats.ReadBytes
	v.CacheHits = storeStats.CacheHits
	v.CacheMisses = storeStats.CacheMisses
	v.Evictions = storeStats.Evictions

	if err := snapshot.Close(); err != nil {
		return v, err
	}
	snapshotOpen = false
	v.MutationOps = min(m.Docs, 256)
	replacement := []byte(`{"bench_mutation":true}`)
	start = time.Now()
	for row := 0; row < v.MutationOps; row++ {
		key := keyPrefix + strconv.Itoa(row)
		created, putErr := store.Put(key, replacement)
		if putErr != nil || created {
			return v, fmt.Errorf("durable update %s: created=%v err=%v", key, created, putErr)
		}
	}
	v.UpdateNSOp = time.Since(start).Nanoseconds() / int64(v.MutationOps)
	start = time.Now()
	for row := 0; row < v.MutationOps; row++ {
		key := keyPrefix + strconv.Itoa(row)
		deleted, deleteErr := store.Delete(key)
		if deleteErr != nil || !deleted {
			return v, fmt.Errorf("durable delete %s: deleted=%v err=%v", key, deleted, deleteErr)
		}
	}
	v.DeleteNSOp = time.Since(start).Nanoseconds() / int64(v.MutationOps)
	storeStats = store.Stats()
	v.ReusableBytes = storeStats.ReusableBytes
	if info, statErr := file.Stat(); statErr != nil {
		return v, statErr
	} else {
		v.PostMutationFileBytes = info.Size()
	}
	if err := store.Close(); err != nil {
		return v, err
	}
	storeOpen = false
	if err := file.Close(); err != nil {
		return v, err
	}
	fileOpen = false

	file, err = os.OpenFile(bestPath, os.O_RDWR, 0)
	if err != nil {
		return v, err
	}
	fileOpen = true
	start = time.Now()
	store, err = simdjson.OpenFileStore(file, options)
	v.MutationReopenNS = time.Since(start).Nanoseconds()
	if err != nil {
		return v, err
	}
	storeOpen = true
	v.AfterDeletes = int(store.Len())
	if v.AfterDeletes != m.Docs-v.MutationOps {
		return v, fmt.Errorf("durable reopen has %d documents, want %d", v.AfterDeletes, m.Docs-v.MutationOps)
	}
	if v.AfterDeletes != 0 {
		raw, found, readErr := store.AppendRaw(nil, keyPrefix+strconv.Itoa(v.MutationOps))
		if readErr != nil || !found || len(raw) == 0 {
			return v, fmt.Errorf("durable reopen survivor: found=%v bytes=%d err=%v", found, len(raw), readErr)
		}
	}
	return v, nil
}

// Verify rejects durable results before storage or transaction ratios are
// rendered. AfterDeletes is recovery-observed, not merely in-memory state.
func (v OursFileStore) Verify(m Manifest) []string {
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
		bad = append(bad, fmt.Sprintf("recovered deletes %d, want %d", v.AfterDeletes, m.Docs-v.MutationOps))
	}
	return bad
}
