package query

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/thesyncim/simdjson"
	"github.com/thesyncim/simdjson/internal/storeio"
)

func TestRunFileSnapshotParallelSpillDifferential(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "query-file-store-*")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	store, err := simdjson.CreateFileStore(file, simdjson.FileStoreOptions{
		Store: simdjson.StoreOptions{ChunkDocuments: 8}, Synchronous: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	set := &simdjson.DocSet{ShapeTapes: true, Postings: true}
	for i := range 448 {
		label := fmt.Sprintf("group-%03d-%s", i, strings.Repeat(string(rune('a'+i%26)), 1024))
		doc := []byte(fmt.Sprintf(`{"id":%d,"bucket":%d,"score":%d,"label":%q,"active":%t}`,
			i, i%17, i*3, label, i%3 != 0))
		if _, err := set.Append(doc); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Put(fmt.Sprintf("key-%04d", i), doc); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()

	queries := []*Query{
		Select(Path("id"), Path("label")).Where(Cmp("active", Eq, true)).OrderBy("label", Desc).Limit(73),
		Select(Count(), Sum("score"), Avg("score"), Min("score"), Max("score")).Where(Cmp("bucket", Ge, 4)),
		Select(Path("bucket"), Count(), Sum("score"), Avg("score")).GroupBy("bucket").OrderBy("bucket", Desc),
		Select(Path("label"), Count(), Sum("score")).GroupBy("label").OrderBy("label", Asc).Limit(91),
		Select(Path("id")).Where(Cmp("id", Ge, 20)).Limit(19),
	}
	spillDir := t.TempDir()
	for i, q := range queries {
		want, err := q.Run(set)
		if err != nil {
			t.Fatalf("query %d baseline: %v", i, err)
		}
		got, stats, err := q.RunFileSnapshot(snapshot, FileExecutionOptions{
			Workers: 4, BatchRows: 11, BatchBytes: 4 << 10,
			MemoryBytes: 64 << 10, SpillDirectory: spillDir,
		})
		if err != nil {
			t.Fatalf("query %d file execution: %v", i, err)
		}
		if gotKey, wantKey := resultKey(got), resultKey(want); gotKey != wantKey {
			t.Fatalf("query %d mismatch:\n got: %s\nwant: %s", i, gotKey, wantKey)
		}
		if stats.RowsScanned != uint64(set.Len()) || stats.Batches < 2 || stats.Workers != 4 {
			t.Fatalf("query %d stats = %+v", i, stats)
		}
		if i == 0 || i == 3 {
			if stats.SpillRuns <= maxSpillFanIn || stats.SpilledBytes == 0 {
				t.Fatalf("query %d did not exercise bounded fan-in spill: %+v", i, stats)
			}
		}
	}
	entries, err := os.ReadDir(spillDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("spill directory retained %d files", len(entries))
	}
}

func TestRunFileSnapshotOptions(t *testing.T) {
	q := Select(Count())
	if _, _, err := q.RunFileSnapshot(nil, FileExecutionOptions{}); err == nil {
		t.Fatal("nil snapshot accepted")
	}
	if _, _, err := q.RunFileSnapshot(nil, FileExecutionOptions{Workers: -1}); err == nil {
		t.Fatal("negative worker count accepted")
	}
	if _, _, err := q.RunFileSnapshot(nil, FileExecutionOptions{MemoryBytes: 1024}); err == nil {
		t.Fatal("undersized memory target accepted")
	}
}

func TestRunFileSnapshotPersistentFloat64CoveringAggregates(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "query-file-cover-*")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	store, err := simdjson.CreateFileStore(file, simdjson.FileStoreOptions{
		Store:          simdjson.StoreOptions{ChunkDocuments: 4},
		Float64Columns: []string{"/score", "/nested/value"},
		Synchronous:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	set := &simdjson.DocSet{ShapeTapes: true}
	for row, document := range []string{
		`{"score":1.5,"nested":{"value":10}}`,
		`{"score":2,"nested":{"value":"text"}}`,
		`{"score":"text","nested":{"value":-3}}`,
		`{"other":7}`,
		`{"score":-1,"nested":{"value":8}}`,
	} {
		raw := []byte(document)
		if _, err := set.Append(raw); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Put(fmt.Sprintf("k%d", row), raw); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	run := func(q *Query) (Result, FileExecutionStats) {
		t.Helper()
		want, err := q.Run(set)
		if err != nil {
			t.Fatal(err)
		}
		got, stats, err := q.RunFileSnapshot(snapshot, FileExecutionOptions{
			Workers: 3, BatchRows: 2, MemoryBytes: 64 << 10,
		})
		if err != nil {
			t.Fatal(err)
		}
		if gotKey, wantKey := resultKey(got), resultKey(want); gotKey != wantKey {
			t.Fatalf("covering aggregate mismatch:\n got: %s\nwant: %s", gotKey, wantKey)
		}
		return got, stats
	}

	_, stats := run(Select(
		Count(), Sum("score"), Avg("score"), Min("score"), Max("score"),
		Sum("nested.value"), Max("nested.value"),
	))
	if stats.CoveringColumns != 2 || stats.RowsTotal != uint64(set.Len()) ||
		stats.RowsScanned != 0 || stats.Batches != 0 || stats.BufferedBytes != 0 {
		t.Fatalf("covering aggregate stats = %+v", stats)
	}
	_, stats = run(Select(Sum("score"), Max("score")))
	if stats.CoveringColumns != 1 || stats.RowsScanned != 0 {
		t.Fatalf("duplicate covering aggregate stats = %+v", stats)
	}
	result, stats := run(Select(Sum("score")).Limit(0))
	if result.RowCount != 0 || stats.CoveringColumns != 0 || stats.RowsScanned != 0 {
		t.Fatalf("LIMIT 0 covering result = rows %d stats %+v", result.RowCount, stats)
	}

	// COUNT(path) includes present non-numeric values, and an unconfigured
	// numeric path has no cover. Both shapes must remain on the JSON executor.
	_, stats = run(Select(Count("score")))
	if stats.CoveringColumns != 0 || stats.RowsScanned != uint64(set.Len()) {
		t.Fatalf("COUNT(path) incorrectly used numeric cover: %+v", stats)
	}
	_, stats = run(Select(Sum("other")))
	if stats.CoveringColumns != 0 || stats.RowsScanned != uint64(set.Len()) {
		t.Fatalf("unconfigured SUM incorrectly used cover: %+v", stats)
	}
	_, stats = run(Select(Sum("score")).Where(Cmp("other", Ge, 0)))
	if stats.CoveringColumns != 0 || stats.RowsScanned != uint64(set.Len()) {
		t.Fatalf("filtered SUM incorrectly used unfiltered cover: %+v", stats)
	}
}

func TestRunFileSnapshotPersistentCompoundIndexPushdown(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "query-file-index-*")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	options := simdjson.FileStoreOptions{
		Store: simdjson.StoreOptions{ChunkDocuments: 8, ShapeTapes: true},
		Indexes: []simdjson.StoreIndexDefinition{
			{Name: "tenant_country", Paths: []string{"/tenant", "/profile/geo/country"}},
			{Name: "tenant", Paths: []string{"/tenant"}},
			{Name: "country", Paths: []string{"/profile/geo/country"}},
		},
		Synchronous: false,
	}
	store, err := simdjson.CreateFileStore(file, options)
	if err != nil {
		t.Fatal(err)
	}
	set := &simdjson.DocSet{ShapeTapes: true, Postings: true}
	for i := range 512 {
		tenant := "other"
		if i%8 == 0 {
			tenant = "acme"
		}
		country := "US"
		if i%16 == 0 {
			country = "PT"
		}
		padding := ""
		if i%17 == 0 {
			padding = strings.Repeat("x", 900)
		}
		doc := []byte(fmt.Sprintf(
			`{"id":%d,"tenant":%q,"profile":{"geo":{"country":%q}},"padding":%q}`,
			i, tenant, country, padding,
		))
		if _, err := set.Append(doc); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Put(fmt.Sprintf("key-%04d", i), doc); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = simdjson.OpenFileStore(file, options)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()

	run := func(q *Query) (Result, FileExecutionStats) {
		t.Helper()
		want, err := q.Run(set)
		if err != nil {
			t.Fatal(err)
		}
		got, stats, err := q.RunFileSnapshot(snapshot, FileExecutionOptions{
			Workers: 3, BatchRows: 7, BatchBytes: 16 << 10, MemoryBytes: 64 << 10,
		})
		if err != nil {
			t.Fatal(err)
		}
		if gotKey, wantKey := resultKey(got), resultKey(want); gotKey != wantKey {
			t.Fatalf("indexed file result mismatch:\n got: %s\nwant: %s", gotKey, wantKey)
		}
		return got, stats
	}

	_, stats := run(
		Select(Path("id"), Path("profile.geo.country")).Where(And(
			Cmp("tenant", Eq, "acme"),
			Cmp("profile.geo.country", Eq, "PT"),
		)),
	)
	if !stats.IndexBounded || stats.IndexLookups != 1 ||
		stats.RowsTotal != 512 || stats.CandidateRows != 32 ||
		stats.RowsScanned != 32 || stats.CandidateChunks != 32 {
		t.Fatalf("compound pushdown stats = %+v", stats)
	}

	_, stats = run(
		Select(Count()).Where(And(
			Cmp("tenant", Eq, "absent"),
			Cmp("profile.geo.country", Eq, "PT"),
		)),
	)
	if !stats.IndexBounded || stats.IndexLookups != 1 ||
		stats.CandidateRows != 0 || stats.RowsScanned != 0 || stats.Batches != 0 {
		t.Fatalf("empty compound pushdown stats = %+v", stats)
	}

	result, stats := run(Select(Count()).Where(Cmp("tenant", Eq, "acme")))
	if result.RowCount != 1 || !countIs(result.Columns[0].Cells[0], 64) ||
		!stats.IndexBounded || stats.IndexLookups != 1 ||
		stats.CandidateRows != 64 || stats.CandidateChunks != 64 ||
		stats.IndexPostingPages == 0 ||
		stats.IndexCertificateRows != 64 || stats.IndexRecheckRows != 0 ||
		stats.RowsScanned != 0 || stats.Batches != 0 {
		t.Fatalf("direct exact count = result %s stats %+v", resultKey(result), stats)
	}

	result, stats = run(Select(Count()).Where(Contains("", `{"tenant":"acme"}`)))
	if result.RowCount != 1 || !countIs(result.Columns[0].Cells[0], 64) ||
		!stats.IndexBounded || stats.IndexLookups != 1 ||
		stats.CandidateRows != 64 || stats.IndexCertificateRows != 64 ||
		stats.IndexRecheckRows != 0 ||
		stats.RowsScanned != 0 || stats.Batches != 0 {
		t.Fatalf("direct root containment count = result %s stats %+v", resultKey(result), stats)
	}

	result, stats = run(Select(Count()).Where(Contains("profile.geo", `{"country":"PT"}`)))
	if result.RowCount != 1 || !countIs(result.Columns[0].Cells[0], 32) ||
		!stats.IndexBounded || stats.IndexLookups != 1 ||
		stats.CandidateRows != 32 || stats.IndexCertificateRows != 32 ||
		stats.IndexRecheckRows != 0 ||
		stats.RowsScanned != 0 || stats.Batches != 0 {
		t.Fatalf("direct nested containment count = result %s stats %+v", resultKey(result), stats)
	}

	result, stats = run(Select(Count()).Where(Contains(
		"", `{"tenant":"acme","profile":{"geo":{"country":"PT"}}}`,
	)))
	if result.RowCount != 1 || !countIs(result.Columns[0].Cells[0], 32) ||
		!stats.IndexBounded || stats.IndexLookups != 1 ||
		stats.CandidateRows != 32 || stats.IndexCertificateRows != 32 ||
		stats.IndexRecheckRows != 0 || stats.RowsScanned != 0 {
		t.Fatalf("compound containment count = result %s stats %+v", resultKey(result), stats)
	}

	result, stats = run(Select(Count()).Where(Contains(
		"", `{"tenant":"absent","tenant":"acme"}`,
	)))
	if result.RowCount != 1 || !countIs(result.Columns[0].Cells[0], 64) ||
		!stats.IndexBounded || stats.IndexLookups != 1 ||
		stats.IndexCertificateRows != 64 || stats.IndexRecheckRows != 0 ||
		stats.RowsScanned != 0 {
		t.Fatalf("last-wins containment count = result %s stats %+v", resultKey(result), stats)
	}

	_, stats = run(Select(Count()).Where(Contains(
		"", `{"profile":{"geo":{}}}`,
	)))
	if stats.IndexBounded || stats.IndexLookups != 0 ||
		stats.IndexCertificateRows != 0 || stats.IndexRecheckRows != 0 ||
		stats.RowsScanned != 512 {
		t.Fatalf("empty-object containment incorrectly flattened: %+v", stats)
	}

	_, stats = run(
		Select(Path("id")).Where(Or(
			Cmp("tenant", Eq, "acme"),
			Cmp("profile.geo.country", Eq, "PT"),
		)),
	)
	if !stats.IndexBounded || stats.IndexLookups != 2 ||
		stats.CandidateRows != 64 || stats.RowsScanned != 64 || stats.CandidateChunks != 64 {
		t.Fatalf("bounded OR pushdown stats = %+v", stats)
	}

	_, stats = run(
		Select(Path("id")).Where(Or(
			Cmp("tenant", Eq, "acme"),
			Cmp("id", Ge, 500),
		)),
	)
	if stats.IndexBounded || stats.IndexLookups != 0 || stats.RowsScanned != 512 {
		t.Fatalf("mixed OR fallback stats = %+v", stats)
	}

	_, stats = run(Select(Count()).Where(Not(Cmp("tenant", Eq, "acme"))))
	if stats.IndexBounded || stats.IndexLookups != 0 || stats.RowsScanned != 512 {
		t.Fatalf("durable NOT fallback stats = %+v", stats)
	}

	_, stats = run(Select(Path("id")).Where(Cmp("id", Ge, 500)))
	if stats.IndexBounded || stats.IndexLookups != 0 ||
		stats.RowsTotal != 512 || stats.RowsScanned != 512 {
		t.Fatalf("unindexed fallback stats = %+v", stats)
	}
}

func TestRunFileSnapshotIndexCorruptionFailsClosed(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "query-file-index-corrupt-*")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	options := simdjson.FileStoreOptions{
		Store: simdjson.StoreOptions{ChunkDocuments: 8},
		Indexes: []simdjson.StoreIndexDefinition{{
			Name: "status", Paths: []string{"/status"},
		}},
		Synchronous: true,
	}
	store, err := simdjson.CreateFileStore(file, options)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 16 {
		if _, err := store.Put(
			fmt.Sprintf("key-%02d", i),
			[]byte(fmt.Sprintf(`{"id":%d,"status":"active"}`, i)),
		); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	var rootScratch [4096]byte
	super, root, _, err := storeio.RecoverStateRoot(file, 4096, rootScratch[:])
	if err != nil {
		t.Fatal(err)
	}
	ref := root.IndexDirectory
	var posting storeio.PageRef
	for {
		page := make([]byte, ref.Length)
		if _, err := file.ReadAt(page, int64(ref.Offset)); err != nil {
			t.Fatal(err)
		}
		view, err := storeio.OpenIndexDirectoryPage(
			page, super.FileEnd, root.NextLogicalID, root.IndexCount,
		)
		if err != nil {
			t.Fatal(err)
		}
		if view.Header().Level == 0 {
			entry, ok := view.EntryAt(0)
			if !ok {
				t.Fatal("current index leaf is empty")
			}
			posting = entry.Posting.Page
			break
		}
		child, ok := view.ChildAt(0)
		if !ok {
			t.Fatal("current index branch is empty")
		}
		ref = child.Ref
	}
	corrupt := make([]byte, posting.Length)
	if _, err := file.ReadAt(corrupt, int64(posting.Offset)); err != nil {
		t.Fatal(err)
	}
	certificate := []byte(`"active"`)
	position := bytes.Index(corrupt, certificate)
	if position < 0 {
		t.Fatal("posting certificate is absent")
	}
	copy(corrupt[position:], `xxxxxxxx`)
	if _, err := storeio.SealPage(corrupt); err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt(corrupt, int64(posting.Offset)); err != nil {
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		t.Fatal(err)
	}

	store, err = simdjson.OpenFileStore(file, options)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	_, stats, err := Select(Count()).Where(Cmp("status", Eq, "active")).RunFileSnapshot(
		snapshot, FileExecutionOptions{Workers: 1},
	)
	if !errors.Is(err, storeio.ErrPostingPageCorrupt) {
		t.Fatalf("corrupt index query error = %v, want %v", err, storeio.ErrPostingPageCorrupt)
	}
	if stats.RowsScanned != 0 {
		t.Fatalf("corrupt index silently scanned %d rows", stats.RowsScanned)
	}
}

func BenchmarkRunFileSnapshotParallelAggregate(b *testing.B) {
	file, err := os.CreateTemp(b.TempDir(), "query-file-benchmark-*")
	if err != nil {
		b.Fatal(err)
	}
	defer file.Close()
	store, err := simdjson.CreateFileStore(file, simdjson.FileStoreOptions{
		Store: simdjson.StoreOptions{ChunkDocuments: 64}, Synchronous: false,
	})
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()
	var sourceBytes int64
	for i := range 1024 {
		doc := []byte(fmt.Sprintf(`{"id":%d,"bucket":%d,"score":%d,"active":%t,"padding":%q}`,
			i, i%16, i*7, i%3 != 0, strings.Repeat("x", i%96)))
		sourceBytes += int64(len(doc))
		if _, err := store.Put(fmt.Sprintf("key-%05d", i), doc); err != nil {
			b.Fatal(err)
		}
	}
	if err := store.Flush(); err != nil {
		b.Fatal(err)
	}
	snapshot, err := store.Snapshot()
	if err != nil {
		b.Fatal(err)
	}
	defer snapshot.Close()
	q := Select(Count(), Sum("score"), Avg("score")).Where(And(Cmp("active", Eq, true), Cmp("bucket", Ge, 4)))
	for _, workers := range []int{1, 4} {
		b.Run(fmt.Sprintf("workers-%d", workers), func(b *testing.B) {
			opts := FileExecutionOptions{Workers: workers, BatchRows: 128, BatchBytes: 1 << 20, MemoryBytes: 64 << 20}
			if _, _, err := q.RunFileSnapshot(snapshot, opts); err != nil {
				b.Fatal(err)
			}
			b.SetBytes(sourceBytes)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				result, _, err := q.RunFileSnapshot(snapshot, opts)
				if err != nil || result.RowCount != 1 {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkRunFileSnapshotPersistentIndexPushdown(b *testing.B) {
	file, err := os.CreateTemp(b.TempDir(), "query-file-index-benchmark-*")
	if err != nil {
		b.Fatal(err)
	}
	defer file.Close()
	store, err := simdjson.CreateFileStore(file, simdjson.FileStoreOptions{
		Store: simdjson.StoreOptions{ChunkDocuments: 64},
		Indexes: []simdjson.StoreIndexDefinition{{
			Name: "tenant_country", Paths: []string{"/tenant", "/profile/country"},
		}},
		Synchronous: false,
	})
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()
	var sourceBytes int64
	for i := range 1024 {
		tenant := fmt.Sprintf("t%d", i%8)
		country := fmt.Sprintf("c%d", (i/8)%8)
		doc := []byte(fmt.Sprintf(
			`{"id":%d,"tenant":%q,"profile":{"country":%q},"scan":{"tenant":%q,"country":%q},"padding":%q}`,
			i, tenant, country, tenant, country, strings.Repeat("x", i%48),
		))
		sourceBytes += int64(len(doc))
		if _, err := store.Put(fmt.Sprintf("key-%05d", i), doc); err != nil {
			b.Fatal(err)
		}
	}
	if err := store.Flush(); err != nil {
		b.Fatal(err)
	}
	snapshot, err := store.Snapshot()
	if err != nil {
		b.Fatal(err)
	}
	defer snapshot.Close()
	var workspace FileExecutionWorkspace
	opts := FileExecutionOptions{
		Workers: 1, BatchRows: 128, BatchBytes: 1 << 20, MemoryBytes: 64 << 20,
		Workspace: &workspace,
	}
	for _, test := range []struct {
		name string
		q    *Query
	}{
		{
			name: "compound-index-1of64",
			q: Select(Path("id")).Where(And(
				Cmp("tenant", Eq, "t0"),
				Cmp("profile.country", Eq, "c0"),
			)),
		},
		{
			name: "same-predicate-scan",
			q: Select(Path("id")).Where(And(
				Cmp("scan.tenant", Eq, "t0"),
				Cmp("scan.country", Eq, "c0"),
			)),
		},
	} {
		b.Run(test.name, func(b *testing.B) {
			result, stats, err := test.q.RunFileSnapshot(snapshot, opts)
			if err != nil || result.RowCount != 16 {
				b.Fatalf("warm run = rows %d, stats %+v, err %v", result.RowCount, stats, err)
			}
			b.SetBytes(sourceBytes)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				result, _, err = test.q.RunFileSnapshot(snapshot, opts)
				if err != nil || result.RowCount != 16 {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(stats.RowsScanned), "rows_scanned/op")
		})
	}
}

// BenchmarkRunFileSnapshotIndexSelectivityCrossover keeps the indexed and
// unindexed predicates semantically identical while varying only posting
// density. It supplies evidence for deciding whether the planner should gain a
// sparse-read cutoff: above the crossover, walking many index-selected extents
// loses to the ordered full scan even though the index is logically usable.
func BenchmarkRunFileSnapshotIndexSelectivityCrossover(b *testing.B) {
	file, err := os.CreateTemp(b.TempDir(), "query-file-index-selectivity-*")
	if err != nil {
		b.Fatal(err)
	}
	defer file.Close()
	storeOptions := simdjson.FileStoreOptions{
		Store: simdjson.StoreOptions{ChunkDocuments: 64, ShapeTapes: true},
		Indexes: []simdjson.StoreIndexDefinition{
			{Name: "class", Paths: []string{"/indexed"}},
			{Name: "p75", Paths: []string{"/p75_indexed"}},
			{Name: "p88", Paths: []string{"/p88_indexed"}},
			{Name: "p94", Paths: []string{"/p94_indexed"}},
			{Name: "p97", Paths: []string{"/p97_indexed"}},
			{Name: "all", Paths: []string{"/all_indexed"}},
		},
		Synchronous: false, ResidentBytes: 64 << 20,
	}
	if os.Getenv("SIMDJSON_BENCH_DIRECT") == "1" {
		storeOptions.ReadMode = simdjson.FileStoreReadDirectRequire
		storeOptions.ReadConcurrency = 4
		storeOptions.PrefetchQueue = 64
	}
	store, err := simdjson.CreateFileStore(file, storeOptions)
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()
	const rows = 2048
	counts := map[string]int{}
	var sourceBytes int64
	for i := range rows {
		remainder := i & 127
		class := "p53"
		switch {
		case remainder < 1:
			class = "p01"
		case remainder < 5:
			class = "p03"
		case remainder < 13:
			class = "p06"
		case remainder < 29:
			class = "p13"
		case remainder < 61:
			class = "p25"
		}
		counts[class]++
		p75 := remainder < 96
		p88 := remainder < 112
		p94 := remainder < 120
		p97 := remainder < 124
		doc := []byte(fmt.Sprintf(
			`{"id":%d,"indexed":%q,"scan":%q,"p75_indexed":%t,"p75_scan":%t,"p88_indexed":%t,"p88_scan":%t,"p94_indexed":%t,"p94_scan":%t,"p97_indexed":%t,"p97_scan":%t,"all_indexed":"all","all_scan":"all","padding":%q}`,
			i, class, class, p75, p75, p88, p88, p94, p94, p97, p97, strings.Repeat("x", i&63),
		))
		sourceBytes += int64(len(doc))
		if _, err := store.Put(fmt.Sprintf("key-%05d", i), doc); err != nil {
			b.Fatal(err)
		}
	}
	if err := store.Flush(); err != nil {
		b.Fatal(err)
	}
	snapshot, err := store.Snapshot()
	if err != nil {
		b.Fatal(err)
	}
	defer snapshot.Close()
	var workspace FileExecutionWorkspace
	opts := FileExecutionOptions{
		Workers: 1, BatchRows: 128, BatchBytes: 1 << 20,
		MemoryBytes: 64 << 20, Workspace: &workspace,
	}
	for _, selection := range []struct {
		name        string
		literal     any
		indexedPath string
		scanPath    string
		count       int
	}{
		{name: "p01", literal: "p01", indexedPath: "indexed", scanPath: "scan", count: counts["p01"]},
		{name: "p03", literal: "p03", indexedPath: "indexed", scanPath: "scan", count: counts["p03"]},
		{name: "p06", literal: "p06", indexedPath: "indexed", scanPath: "scan", count: counts["p06"]},
		{name: "p13", literal: "p13", indexedPath: "indexed", scanPath: "scan", count: counts["p13"]},
		{name: "p25", literal: "p25", indexedPath: "indexed", scanPath: "scan", count: counts["p25"]},
		{name: "p53", literal: "p53", indexedPath: "indexed", scanPath: "scan", count: counts["p53"]},
		{name: "p75", literal: true, indexedPath: "p75_indexed", scanPath: "p75_scan", count: rows * 3 / 4},
		{name: "p88", literal: true, indexedPath: "p88_indexed", scanPath: "p88_scan", count: rows * 7 / 8},
		{name: "p94", literal: true, indexedPath: "p94_indexed", scanPath: "p94_scan", count: rows * 15 / 16},
		{name: "p97", literal: true, indexedPath: "p97_indexed", scanPath: "p97_scan", count: rows * 31 / 32},
		{name: "p100", literal: "all", indexedPath: "all_indexed", scanPath: "all_scan", count: rows},
	} {
		for _, test := range []struct {
			name string
			path string
		}{
			{name: "index", path: selection.indexedPath},
			{name: "scan", path: selection.scanPath},
		} {
			q := Select(Count()).Where(Cmp(test.path, Eq, selection.literal))
			name := fmt.Sprintf("%s/%s", selection.name, test.name)
			b.Run(name, func(b *testing.B) {
				result, stats, err := q.RunFileSnapshot(snapshot, opts)
				if err != nil || result.RowCount != 1 || len(result.Columns) != 1 || len(result.Columns[0].Cells) != 1 {
					b.Fatalf("warm run = result %+v, stats %+v, err %v", result, stats, err)
				}
				count, countOK := result.Columns[0].Cells[0].Int64()
				if !countOK || count != int64(selection.count) {
					b.Fatalf("warm run = result %+v, stats %+v, err %v", result, stats, err)
				}
				b.SetBytes(sourceBytes)
				b.ReportAllocs()
				b.ReportMetric(float64(stats.RowsScanned), "rows_scanned/op")
				b.ResetTimer()
				for range b.N {
					result, _, err = q.RunFileSnapshot(snapshot, opts)
					if err != nil || result.RowCount != 1 {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
