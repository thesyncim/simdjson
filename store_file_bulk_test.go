package simdjson

import (
	"bytes"
	"encoding/binary"
	"errors"
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

func TestWriteFileStoreBulkGroupsExactDocumentsAndPeelsMutations(t *testing.T) {
	const documents = 1024
	options := testFileStoreOptions()
	options.Store = StoreOptions{ChunkDocuments: 8, ShapeTapes: true}
	options.MaxPageSize = 64 << 10
	options.ResidentBytes = 8 << 20
	options.Float64Columns = []string{"/score"}
	builder, err := NewStoreBuilder(options.Store)
	if err != nil {
		t.Fatal(err)
	}
	for row := range documents {
		document := fmt.Appendf(
			nil,
			`{"tenant":"t%d","status":"s%d","score":%d,"active":%t,"payload":"%s"}`,
			row&3, row%11, row, row&1 == 0, strings.Repeat("x", 96+(row&7)),
		)
		if err := builder.Append(fmt.Sprintf("doc:%04d", row), document); err != nil {
			t.Fatal(err)
		}
	}
	source, err := builder.Build()
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.CreateTemp(t.TempDir(), "file-store-groups-*")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	size, err := source.WriteFileStore(file, options)
	if err != nil {
		t.Fatal(err)
	}
	if size <= 0 {
		t.Fatalf("grouped file size = %d", size)
	}
	store, err := OpenFileStore(file, options)
	if err != nil {
		t.Fatal(err)
	}
	state := store.state.Load()
	groupRef, ok, err := storeio.LookupChunkTree(store.cache, state.chunkRoot, 0, storeio.ChunkTreeBounds{
		FileEnd: state.super.FileEnd, NextLogicalID: state.root.NextLogicalID,
	})
	if err != nil || !ok || groupRef.Kind != storeio.PageDocumentGroup {
		t.Fatalf("chunk zero ref = (%+v,%v,%v)", groupRef, ok, err)
	}
	if groupRef.Length >= uint32(16*options.PageSize) {
		t.Fatalf("group extent = %d, did not beat sixteen independent pages", groupRef.Length)
	}
	sameGroup, ok, err := storeio.LookupChunkTree(store.cache, state.chunkRoot, 1, storeio.ChunkTreeBounds{
		FileEnd: state.super.FileEnd, NextLogicalID: state.root.NextLogicalID,
	})
	if err != nil || !ok || sameGroup != groupRef {
		t.Fatalf("adjacent chunk did not share group: (%+v,%v,%v)", sameGroup, ok, err)
	}
	buffer := make([]byte, 0, 256)
	buffer, ok, err = store.AppendRaw(buffer[:0], "doc:0003")
	if err != nil || !ok || !bytes.Contains(buffer, []byte(`"score":3`)) {
		t.Fatalf("group point = (%q,%v,%v)", buffer, ok, err)
	}
	pointSnapshot, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	allocs := testing.AllocsPerRun(100, func() {
		var found bool
		buffer, found, err = pointSnapshot.AppendRaw(buffer[:0], "doc:0003")
		if err != nil || !found {
			panic("group point failed")
		}
	})
	if err := pointSnapshot.Close(); err != nil {
		t.Fatal(err)
	}
	if allocs != 0 {
		t.Fatalf("group point allocations = %.2f, want zero with caller buffer", allocs)
	}

	replacement := []byte(`{"tenant":"new","status":"updated","score":3000,"active":true}`)
	if created, err := store.Put("doc:0003", replacement); err != nil || created {
		t.Fatalf("group update = (%v,%v)", created, err)
	}
	if deleted, err := store.Delete("doc:0004"); err != nil || !deleted {
		t.Fatalf("group delete = (%v,%v)", deleted, err)
	}
	state = store.state.Load()
	peeled, ok, err := storeio.LookupChunkTree(store.cache, state.chunkRoot, 0, storeio.ChunkTreeBounds{
		FileEnd: state.super.FileEnd, NextLogicalID: state.root.NextLogicalID,
	})
	if err != nil || !ok || peeled.Kind != storeio.PageDocument || peeled == groupRef {
		t.Fatalf("mutated chunk was not peeled to a private page: (%+v,%v,%v)", peeled, ok, err)
	}
	untouched, ok, err := storeio.LookupChunkTree(store.cache, state.chunkRoot, 1, storeio.ChunkTreeBounds{
		FileEnd: state.super.FileEnd, NextLogicalID: state.root.NextLogicalID,
	})
	if err != nil || !ok || untouched != groupRef {
		t.Fatalf("untouched chunk lost shared base: (%+v,%v,%v)", untouched, ok, err)
	}
	groupLease, err := store.cache.Acquire(groupRef)
	if err != nil {
		t.Fatal(err)
	}
	groupHeader := storeio.AdmittedDocumentGroup(groupLease.Page()).Header()
	groupLease.Release()
	for ordinal := uint16(1); ordinal < groupHeader.ChunkCount; ordinal++ {
		row := int(ordinal) * options.Store.ChunkDocuments
		key := fmt.Sprintf("doc:%04d", row)
		replacement := fmt.Appendf(nil, `{"peeled":%d,"score":%d}`, ordinal, row)
		if created, putErr := store.Put(key, replacement); putErr != nil || created {
			t.Fatalf("peel group chunk %d = (%v,%v)", ordinal, created, putErr)
		}
		retiredGroup := false
		for _, extent := range store.retireScratch {
			if extent.Offset == groupRef.Offset && extent.Length == uint64(groupRef.Length) {
				retiredGroup = true
				break
			}
		}
		wantRetired := ordinal+1 == groupHeader.ChunkCount
		if retiredGroup != wantRetired {
			t.Fatalf("group retirement after chunk %d = %v, want %v", ordinal, retiredGroup, wantRetired)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenFileStore(file, options)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, ok, err := reopened.AppendRaw(buffer[:0], "doc:0003")
	if err != nil || !ok || !bytes.Equal(got, replacement) {
		t.Fatalf("reopened update = (%q,%v,%v)", got, ok, err)
	}
	if _, ok, err := reopened.AppendRaw(buffer[:0], "doc:0004"); err != nil || ok {
		t.Fatalf("reopened delete = (%v,%v)", ok, err)
	}
	got, ok, err = reopened.AppendRaw(buffer[:0], "doc:0010")
	if err != nil || !ok || !bytes.Contains(got, []byte(`"score":10`)) {
		t.Fatalf("reopened untouched group = (%q,%v,%v)", got, ok, err)
	}
}

func TestWriteFileStoreBulkGroupRejectsResealedInvalidJSON(t *testing.T) {
	const documents = 128
	options := testFileStoreOptions()
	options.Store = StoreOptions{ChunkDocuments: 8, ShapeTapes: true}
	options.MaxPageSize = 64 << 10
	options.ResidentBytes = 8 << 20
	builder, err := NewStoreBuilder(options.Store)
	if err != nil {
		t.Fatal(err)
	}
	for row := range documents {
		document := fmt.Appendf(
			nil, `{"id":%d,"payload":"%s"}`, row, strings.Repeat("x", 96+(row&7)),
		)
		if err := builder.Append(fmt.Sprintf("doc:%04d", row), document); err != nil {
			t.Fatal(err)
		}
	}
	source, err := builder.Build()
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.CreateTemp(t.TempDir(), "file-store-group-corrupt-*")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := source.WriteFileStore(file, options); err != nil {
		t.Fatal(err)
	}

	lastChunk := uint32(documents/options.Store.ChunkDocuments - 1)
	ref := recoveredFileDocumentRef(t, file, options, lastChunk)
	if ref.Kind != storeio.PageDocumentGroup {
		t.Fatalf("last chunk ref = %+v, want document group", ref)
	}
	page := make([]byte, ref.Length)
	if _, err := file.ReadAt(page, int64(ref.Offset)); err != nil {
		t.Fatal(err)
	}
	payload := page[storeio.PageHeaderSize:]
	templateCount := int(binary.LittleEndian.Uint16(payload[12:14]))
	templateStart := storeio.PageHeaderSize + storeio.DocumentGroupPayloadHeaderSize
	for offset := 16; offset < 32; offset += 4 {
		templateStart += int(binary.LittleEndian.Uint32(payload[offset : offset+4]))
	}
	entry := templateStart + templateCount*4
	values := int(binary.LittleEndian.Uint16(page[entry : entry+2]))
	staticStart := entry + 8 + (values+1)*4
	if staticStart >= len(page) {
		t.Fatalf("first template static byte offset %d is outside the page", staticStart)
	}
	if page[staticStart] != '{' {
		t.Fatalf("first template static byte at %d = %q", staticStart, page[staticStart])
	}
	page[staticStart] = 'x'
	if _, err := storeio.SealPage(page); err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt(page, int64(ref.Offset)); err != nil {
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenFileStore(file, options)
	if reopened != nil {
		_ = reopened.Close()
		t.Fatal("OpenFileStore returned a store for invalid grouped JSON")
	}
	if !errors.Is(err, storeio.ErrDocumentGroupCorrupt) {
		t.Fatalf("OpenFileStore invalid grouped JSON = %v, want document-group corruption", err)
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
