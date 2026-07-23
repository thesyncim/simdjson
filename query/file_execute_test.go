package query

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/thesyncim/simdjson"
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
