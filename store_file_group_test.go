package simdjson

import (
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/thesyncim/simdjson/document"
	"github.com/thesyncim/simdjson/internal/storeio"
)

func TestFileSnapshotIndexScalarGroupsAndResidual(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "file-index-groups-*")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	store, err := CreateFileStore(file, FileStoreOptions{
		Store: StoreOptions{ChunkDocuments: 4},
		Indexes: []StoreIndexDefinition{{
			Name: "kind", Paths: []string{"/kind"},
		}},
		Synchronous: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	documents := []string{
		`{"kind":"a"}`,
		`{"kind":"b"}`,
		`{"kind":"a"}`,
		`{"missing":true}`,
		`{"kind":null}`,
		`{"kind":{"nested":true}}`,
		`{"kind":1}`,
		`{"kind":1.0}`,
	}
	for row, document := range documents {
		if _, err := store.Put(fmt.Sprintf("k%d", row), []byte(document)); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()

	var workspace FileIndexWorkspace
	groups, residual, ok, err := snapshot.AppendIndexScalarGroupsInto(
		nil, nil, &workspace, "kind",
	)
	if err != nil || !ok {
		t.Fatalf("index groups = ok %v err %v", ok, err)
	}
	if got := workspace.LastProbeStats(); got.CertificateRows != 6 ||
		got.CandidateRows != 6 || got.MatchedRows != 6 ||
		got.DocumentRecheckRows != 0 || got.PostingPages == 0 {
		t.Fatalf("index group stats = %+v", got)
	}
	counts := make(map[string]uint64, len(groups))
	for _, group := range groups {
		key := string(group.Value.Bytes())
		if _, numeric := group.Value.NumberBytes(); numeric {
			key = "number"
		}
		counts[key] += group.Count
	}
	want := map[string]uint64{`"a"`: 2, `"b"`: 1, "null": 1, "number": 2}
	if len(counts) != len(want) {
		t.Fatalf("certified groups = %v, want %v", counts, want)
	}
	for key, count := range want {
		if counts[key] != count {
			t.Fatalf("certified groups = %v, want %v", counts, want)
		}
	}
	var rows []StoreRow
	var scratch []byte
	scratch, err = snapshot.RangeMasksRawRowsBuffer(
		residual, scratch,
		func(row StoreRow, _, _ []byte) error {
			rows = append(rows, row)
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(rows) != fmt.Sprint([]StoreRow{{Chunk: 0, Slot: 3}, {Chunk: 1, Slot: 1}}) {
		t.Fatalf("residual rows = %v", rows)
	}
	rowAllocs := testing.AllocsPerRun(100, func() {
		var callErr error
		scratch, callErr = snapshot.RangeMasksRawRowsBuffer(
			residual, scratch[:0],
			func(StoreRow, []byte, []byte) error { return nil },
		)
		if callErr != nil {
			panic(callErr)
		}
	})
	if rowAllocs != 0 {
		t.Fatalf("warmed row-address scan allocated %.2f times", rowAllocs)
	}

	reuseGroups := make([]FileIndexScalarGroup, 0, len(groups))
	reuseResidual := make([]StoreMask, 0, len(residual))
	reuseGroups, reuseResidual, _, err = snapshot.AppendIndexScalarGroupsInto(
		reuseGroups, reuseResidual, &workspace, "kind",
	)
	if err != nil {
		t.Fatal(err)
	}
	allocs := testing.AllocsPerRun(100, func() {
		var callErr error
		reuseGroups, reuseResidual, _, callErr =
			snapshot.AppendIndexScalarGroupsInto(
				reuseGroups[:0], reuseResidual[:0], &workspace, "kind",
			)
		if callErr != nil {
			panic(callErr)
		}
	})
	if allocs != 0 {
		t.Fatalf("warmed index group scan allocated %.2f times", allocs)
	}
}

func TestFileSnapshotIndexGroupCatalogRejectsInvalidScalar(t *testing.T) {
	builder, err := NewStoreBuilder(StoreOptions{ChunkDocuments: 4})
	if err != nil {
		t.Fatal(err)
	}
	if err := builder.Append("k0", []byte(`{"kind":"a"}`)); err != nil {
		t.Fatal(err)
	}
	source, err := builder.Build()
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.CreateTemp(t.TempDir(), "file-index-group-corrupt-*")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	options := FileStoreOptions{
		Store: StoreOptions{ChunkDocuments: 4},
		Indexes: []StoreIndexDefinition{{
			Name: "kind", Paths: []string{"/kind"},
		}},
		PageSize: 4096, MaxPageSize: 64 << 10, Synchronous: true,
	}
	if _, err := source.WriteFileStore(file, options); err != nil {
		t.Fatal(err)
	}
	store, err := OpenFileStore(file, options)
	if err != nil {
		t.Fatal(err)
	}
	head := store.state.Load().root.IndexGroupHead
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	page := make([]byte, head.Length)
	if n, err := file.ReadAt(page, int64(head.Offset)); err != nil || n != len(page) {
		t.Fatalf("read catalog = (%d,%v)", n, err)
	}
	valueAt := storeio.PageHeaderSize +
		storeio.IndexGroupCatalogPayloadHeaderSize +
		storeio.IndexGroupCatalogEntryHeaderSize
	page[valueAt] = '{'
	if _, err := storeio.SealPage(page); err != nil {
		t.Fatal(err)
	}
	if n, err := file.WriteAt(page, int64(head.Offset)); err != nil || n != len(page) {
		t.Fatalf("write catalog = (%d,%v)", n, err)
	}
	reopened, err := OpenFileStore(file, options)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	snapshot, err := reopened.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	if _, _, ok, err := snapshot.AppendIndexScalarGroupsInto(
		nil, nil, nil, "kind",
	); !ok || !errors.Is(err, storeio.ErrIndexGroupCatalogCorrupt) {
		t.Fatalf("corrupt catalog = ok %v err %v", ok, err)
	}
}

func TestFileSnapshotBulkIndexScalarGroupCatalog(t *testing.T) {
	builder, err := NewStoreBuilder(StoreOptions{ChunkDocuments: 4, ShapeTapes: true})
	if err != nil {
		t.Fatal(err)
	}
	documents := []string{
		`{"kind":"a"}`,
		`{"kind":"\u0061"}`,
		`{"missing":true}`,
		`{"kind":null}`,
		`{"kind":1}`,
		`{"kind":1.0}`,
	}
	for row, document := range documents {
		if err := builder.Append(fmt.Sprintf("k%d", row), []byte(document)); err != nil {
			t.Fatal(err)
		}
	}
	source, err := builder.Build()
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.CreateTemp(t.TempDir(), "file-index-group-catalog-*")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	options := FileStoreOptions{
		Store: StoreOptions{ChunkDocuments: 4, ShapeTapes: true},
		Indexes: []StoreIndexDefinition{{
			Name: "kind", Paths: []string{"/kind"},
		}},
		PageSize: 4096, MaxPageSize: 64 << 10, Synchronous: true,
	}
	if _, err := source.WriteFileStore(file, options); err != nil {
		t.Fatal(err)
	}
	store, err := OpenFileStore(file, options)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	head := store.state.Load().root.IndexGroupHead
	if head.Kind != storeio.PageIndexGroupCatalog {
		t.Fatalf("index group head = %+v", head)
	}
	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	var workspace FileIndexWorkspace
	groups, residual, ok, err := snapshot.AppendIndexScalarGroupsInto(
		nil, nil, &workspace, "kind",
	)
	if err != nil || !ok {
		t.Fatalf("catalog groups = ok %v err %v", ok, err)
	}
	if len(residual) != 0 || len(groups) != 3 {
		t.Fatalf("catalog groups = %d residual = %v", len(groups), residual)
	}
	if got := workspace.LastProbeStats(); got.CertificateRows != 6 ||
		got.MatchedRows != 6 || got.CandidateRows != 0 || got.PostingPages != 0 {
		t.Fatalf("catalog stats = %+v", got)
	}
	counts := make(map[string]uint64, len(groups))
	for _, group := range groups {
		key := string(group.Value.Bytes())
		if _, numeric := group.Value.NumberBytes(); numeric {
			key = "number"
		}
		if group.Value.Kind() == document.String {
			key = "string"
		}
		counts[key] += group.Count
	}
	if len(counts) != 3 || counts["string"] != 2 ||
		counts["null"] != 2 || counts["number"] != 2 {
		t.Fatalf("catalog counts = %v", counts)
	}
	reuseGroups := make([]FileIndexScalarGroup, 0, len(groups))
	reuseGroups, _, _, err = snapshot.AppendIndexScalarGroupsInto(
		reuseGroups, nil, &workspace, "kind",
	)
	if err != nil {
		t.Fatal(err)
	}
	allocs := testing.AllocsPerRun(100, func() {
		var callErr error
		reuseGroups, _, _, callErr = snapshot.AppendIndexScalarGroupsInto(
			reuseGroups[:0], nil, &workspace, "kind",
		)
		if callErr != nil {
			panic(callErr)
		}
	})
	if allocs != 0 {
		t.Fatalf("warmed catalog scan allocated %.2f times", allocs)
	}
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}

	if updated, err := store.SetTTL("k0", time.Hour); err != nil || !updated {
		t.Fatalf("SetTTL = (%v,%v)", updated, err)
	}
	if got := store.state.Load().root.IndexGroupHead; got != head {
		t.Fatalf("TTL changed index group head: got %+v want %+v", got, head)
	}
	if created, err := store.Put("k0", []byte(`{"kind":"b"}`)); err != nil || created {
		t.Fatalf("Put = (%v,%v)", created, err)
	}
	if got := store.state.Load().root.IndexGroupHead; got != (storeio.PageRef{}) {
		t.Fatalf("document mutation retained index group head %+v", got)
	}
	retired := false
	for _, extent := range store.retireScratch {
		if extent.Offset == head.Offset && extent.Length == uint64(head.Length) {
			retired = true
			break
		}
	}
	if !retired {
		t.Fatalf("document mutation did not retire index group head %+v", head)
	}
}
