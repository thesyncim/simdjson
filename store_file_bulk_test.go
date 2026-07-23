package simdjson

import (
	"fmt"
	"math/bits"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/thesyncim/simdjson/internal/storeio"
)

func TestWriteFileStoreBulkPreservesDocumentsIndexesTTLAndMutation(t *testing.T) {
	builder, err := NewStoreBuilder(StoreOptions{ChunkDocuments: 7, ShapeTapes: true})
	if err != nil {
		t.Fatal(err)
	}
	const documents = 25
	for i := range documents {
		status := "idle"
		if i%3 == 0 {
			status = "active"
		}
		padding := strings.Repeat("x", i*80)
		if i == documents-1 {
			padding = strings.Repeat("x", 60<<10)
		}
		document := fmt.Sprintf(
			`{"id":%d,"meta":{"tenant":%q,"status":%q},"padding":%q}`,
			i, []string{"acme", "other"}[i&1], status, padding,
		)
		if i == 9 {
			document = `{"id":9,"meta":{"tenant":"other","status":"ac\u0074ive"},"padding":"` +
				strings.Repeat("x", 900) + `"}`
		}
		if err := builder.Append(fmt.Sprintf("k%02d", i), []byte(document)); err != nil {
			t.Fatal(err)
		}
	}
	source, err := builder.Build()
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(24 * time.Hour).Truncate(time.Millisecond)
	if !source.SetDeadline("k04", deadline) {
		t.Fatal("source SetDeadline failed")
	}

	options := testFileStoreOptions()
	options.Store.ChunkDocuments = 4
	options.ResidentBytes = 8 << 20
	options.BufferCount = 128
	options.MaxRetiredExtents = 512
	options.Indexes = []StoreIndexDefinition{
		{Name: "status", Paths: []string{"/meta/status"}},
		{Name: "tenant_status", Paths: []string{"/meta/tenant", "/meta/status"}},
	}
	options.Float64Columns = []string{"/id"}
	file, err := os.CreateTemp(t.TempDir(), "file-store-bulk-*")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	size, err := source.WriteFileStore(file, options)
	if err != nil {
		t.Fatal(err)
	}
	if size <= 0 {
		t.Fatalf("bulk file size = %d", size)
	}

	store, err := OpenFileStore(file, options)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.Stats().DocumentCount; got != documents {
		t.Fatalf("bulk document count = %d, want %d", got, documents)
	}
	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	idAggregate, covered, err := snapshot.ReduceFloat64Path("/id")
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
	if err != nil || !covered ||
		idAggregate != (Float64Aggregate{Count: documents, Sum: 300, Min: 0, Max: documents - 1}) {
		t.Fatalf("bulk id reduction = (%+v,%v,%v)", idAggregate, covered, err)
	}
	for _, row := range []int{0, 9, documents - 1} {
		key := fmt.Sprintf("k%02d", row)
		raw, ok, err := store.AppendRaw(nil, key)
		if err != nil || !ok || !strings.Contains(string(raw), fmt.Sprintf(`"id":%d`, row)) {
			t.Fatalf("bulk %s = (%q,%v,%v)", key, raw, ok, err)
		}
	}
	if got, ok, err := store.Deadline("k04"); err != nil || !ok || !got.Equal(deadline) {
		t.Fatalf("bulk deadline = (%v,%v,%v), want %v", got, ok, err, deadline)
	}

	needle := func(src string) Index {
		t.Helper()
		needed, err := RequiredIndexEntries([]byte(src))
		if err != nil {
			t.Fatal(err)
		}
		index, err := BuildIndex([]byte(src), make([]IndexEntry, needed))
		if err != nil {
			t.Fatal(err)
		}
		return index
	}
	countMasks := func(masks []StoreMask) int {
		count := 0
		for _, mask := range masks {
			count += bits.OnesCount64(mask.Bits)
		}
		return count
	}
	active, acme := needle(`"active"`), needle(`"acme"`)
	indexSnapshot, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	var indexWorkspace FileIndexWorkspace
	masks, err := indexSnapshot.AppendIndexMasksInto(
		nil, &indexWorkspace, "status", active,
	)
	if err != nil || countMasks(masks) != 9 {
		t.Fatalf("bulk active masks = (%+v,%v), count %d", masks, err, countMasks(masks))
	}
	if stats := indexWorkspace.LastProbeStats(); stats.CertificateRows != 9 ||
		stats.DocumentRecheckRows != 0 || stats.PostingPages == 0 ||
		stats.PostingPages >= stats.CandidateChunks {
		t.Fatalf("bulk coalesced status probe stats = %+v", stats)
	}
	masks, err = indexSnapshot.AppendIndexMasksInto(
		masks[:0], &indexWorkspace, "tenant_status", acme, active,
	)
	if err != nil || countMasks(masks) != 5 {
		t.Fatalf("bulk compound masks = (%+v,%v), count %d", masks, err, countMasks(masks))
	}
	if stats := indexWorkspace.LastProbeStats(); stats.CertificateRows != 5 ||
		stats.DocumentRecheckRows != 0 || stats.PostingPages == 0 ||
		stats.PostingPages >= stats.CandidateChunks {
		t.Fatalf("bulk coalesced compound probe stats = %+v", stats)
	}
	if err := indexSnapshot.Close(); err != nil {
		t.Fatal(err)
	}

	if created, err := store.Put("k00", []byte(`{"id":0,"meta":{"tenant":"acme","status":"idle"}}`)); err != nil || created {
		t.Fatalf("bulk update = (%v,%v)", created, err)
	}
	if deleted, err := store.Delete("k03"); err != nil || !deleted {
		t.Fatalf("bulk delete = (%v,%v)", deleted, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenFileStore(file, options)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, ok, err := reopened.AppendRaw(nil, "k03"); err != nil || ok {
		t.Fatalf("reopened deleted k03 = (%v,%v)", ok, err)
	}
	snapshot, err = reopened.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	idAggregate, covered, err = snapshot.ReduceFloat64Path("/id")
	if closeErr := snapshot.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil || !covered ||
		idAggregate != (Float64Aggregate{Count: documents - 1, Sum: 297, Min: 0, Max: documents - 1}) {
		t.Fatalf("reopened bulk id reduction = (%+v,%v,%v)", idAggregate, covered, err)
	}
	masks, err = reopened.AppendIndexMasks(masks[:0], "status", active)
	if err != nil || countMasks(masks) != 7 {
		t.Fatalf("reopened active masks = (%+v,%v), count %d", masks, err, countMasks(masks))
	}
}

func TestWriteFileStoreBulkEmptyAndNonEmptyGuard(t *testing.T) {
	source := NewStore(StoreOptions{})
	options := testFileStoreOptions()
	file, err := os.CreateTemp(t.TempDir(), "file-store-bulk-empty-*")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := source.WriteFileStore(file, options); err != nil {
		t.Fatal(err)
	}
	store, err := OpenFileStore(file, options)
	if err != nil {
		t.Fatal(err)
	}
	if store.Stats().DocumentCount != 0 {
		t.Fatal("empty bulk source opened non-empty")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := source.WriteFileStore(file, options); err != ErrFileStoreNotEmpty {
		t.Fatalf("non-empty guard = %v, want %v", err, ErrFileStoreNotEmpty)
	}
}

func TestWriteFileStoreBulkKeepsPackedIndexBaseLiveThroughChurn(t *testing.T) {
	builder, err := NewStoreBuilder(StoreOptions{ChunkDocuments: 4, ShapeTapes: true})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 8 {
		if err := builder.Append(fmt.Sprintf("k%d", i), []byte(fmt.Sprintf(
			`{"meta":{"status":%q},"n":%d}`, []string{"idle", "active"}[i&1], i,
		))); err != nil {
			t.Fatal(err)
		}
	}
	source, err := builder.Build()
	if err != nil {
		t.Fatal(err)
	}
	options := testFileStoreOptions()
	options.BufferCount = 128
	options.MaxRetiredExtents = 512
	options.Indexes = []StoreIndexDefinition{{Name: "status", Paths: []string{"/meta/status"}}}
	file, err := os.CreateTemp(t.TempDir(), "file-store-bulk-index-churn-*")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := source.WriteFileStore(file, options); err != nil {
		t.Fatal(err)
	}
	store, err := OpenFileStore(file, options)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	idleDocument := []byte(`{"meta":{"status":"idle"}}`)
	needed, err := RequiredIndexEntries(idleDocument)
	if err != nil {
		t.Fatal(err)
	}
	idle, err := BuildIndex(idleDocument, make([]IndexEntry, needed))
	if err != nil {
		t.Fatal(err)
	}
	idleHash, ok, err := fileIndexTupleHash(store.options.indexes[0], idle)
	if err != nil || !ok {
		t.Fatalf("idle index hash = (%x,%v,%v)", idleHash, ok, err)
	}
	state := store.state.Load()
	base, found, err := storeio.LookupIndexTree(store.cache, state.indexRoot, storeio.IndexDirectoryKey{
		IndexID: 0, TupleHash: idleHash, Chunk: 0,
	}, storeio.IndexTreeBounds{
		FileEnd: state.super.FileEnd, NextLogicalID: state.root.NextLogicalID,
		IndexHighWater: uint32(len(store.options.indexes)),
	})
	if err != nil || !found || base.Flags&storeio.IndexPostingImmutableBase == 0 {
		t.Fatalf("packed base posting = (%+v,%v,%v)", base, found, err)
	}

	put := func(version int) {
		t.Helper()
		status := []string{"idle", "active"}[version&1]
		if created, err := store.Put("k0", []byte(fmt.Sprintf(
			`{"meta":{"status":%q},"n":%d}`, status, version,
		))); err != nil || created {
			t.Fatalf("indexed churn %d = (%v,%v)", version, created, err)
		}
	}
	for version := 1; version <= 24; version++ {
		put(version)
	}
	for version := 25; version <= 64; version++ {
		put(version)
	}
	for _, extent := range store.reusable {
		if extent.Offset < base.Page.Offset+uint64(base.Page.Length) &&
			base.Page.Offset < extent.Offset+extent.Length {
			t.Fatalf("live packed base %+v entered reusable extent %+v", base.Page, extent)
		}
	}
	valueNeeded, err := RequiredIndexEntries([]byte(`"idle"`))
	if err != nil {
		t.Fatal(err)
	}
	idleValue, err := BuildIndex([]byte(`"idle"`), make([]IndexEntry, valueNeeded))
	if err != nil {
		t.Fatal(err)
	}
	masks, err := store.AppendIndexMasks(nil, "status", idleValue)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, mask := range masks {
		count += bits.OnesCount64(mask.Bits)
	}
	if count != 4 {
		t.Fatalf("idle rows after indexed churn = %d, want 4", count)
	}
}

func TestWriteFileStoreBulkRecoveryFallsBackToBaseGeneration(t *testing.T) {
	builder, err := NewStoreBuilder(StoreOptions{ChunkDocuments: 4, ShapeTapes: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := builder.Append("stable", []byte(`{"version":1,"meta":{"status":"active"}}`)); err != nil {
		t.Fatal(err)
	}
	source, err := builder.Build()
	if err != nil {
		t.Fatal(err)
	}
	options := testFileStoreOptions()
	options.BufferCount = 128
	options.Indexes = []StoreIndexDefinition{{Name: "status", Paths: []string{"/meta/status"}}}
	file, err := os.CreateTemp(t.TempDir(), "file-store-bulk-recovery-*")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := source.WriteFileStore(file, options); err != nil {
		t.Fatal(err)
	}
	store, err := OpenFileStore(file, options)
	if err != nil {
		t.Fatal(err)
	}
	if created, err := store.Put("stable", []byte(`{"version":2,"meta":{"status":"idle"}}`)); err != nil || created {
		t.Fatalf("update = (%v,%v)", created, err)
	}
	newest := store.state.Load()
	if newest.root.Generation != 2 {
		t.Fatalf("newest generation = %d, want 2", newest.root.Generation)
	}
	newestStateOffset := newest.stateRef.Offset
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	var one [1]byte
	if _, err := file.ReadAt(one[:], int64(newestStateOffset)+storeio.PageHeaderSize); err != nil {
		t.Fatal(err)
	}
	one[0] ^= 0xff
	if _, err := file.WriteAt(one[:], int64(newestStateOffset)+storeio.PageHeaderSize); err != nil {
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenFileStore(file, options)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.Generation() != 1 {
		t.Fatalf("recovered generation = %d, want compact base generation 1", reopened.Generation())
	}
	raw, ok, err := reopened.AppendRaw(nil, "stable")
	if err != nil || !ok || string(raw) != `{"version":1,"meta":{"status":"active"}}` {
		t.Fatalf("recovered bulk value = (%q,%v,%v)", raw, ok, err)
	}
}
