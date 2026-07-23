package query

import (
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
	var corrupt [1]byte
	corruptOffset := int64(posting.Offset) + storeio.PageHeaderSize
	if _, err := file.ReadAt(corrupt[:], corruptOffset); err != nil {
		t.Fatal(err)
	}
	corrupt[0] ^= 0xff
	if _, err := file.WriteAt(corrupt[:], corruptOffset); err != nil {
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
	if !errors.Is(err, storeio.ErrPageCorrupt) {
		t.Fatalf("corrupt index query error = %v, want %v", err, storeio.ErrPageCorrupt)
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
