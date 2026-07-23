package simdjson

import (
	"errors"
	"fmt"
	"math/bits"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/thesyncim/simdjson/internal/storeio"
)

func testFileStoreOptions() FileStoreOptions {
	return FileStoreOptions{
		Store:    StoreOptions{ChunkDocuments: 4},
		PageSize: 4096, MaxPageSize: 64 << 10, ResidentBytes: 4 << 20,
		MaxDocumentBytes: 64 << 10, MaxKeyBytes: 128, InlineValueBytes: 512,
		ReadConcurrency: 2, PrefetchQueue: 8, BufferCount: 64,
		QueueSlots: 4, GroupLimit: 2, Backend: FileStoreBackendPortable,
		MaxSnapshotLeases: 8, MaxRetiredExtents: 256,
		Synchronous: true,
	}
}

func TestFileStoreDirtyBudgetUsesExtentSizes(t *testing.T) {
	options := testFileStoreOptions()
	normalized, err := options.normalized()
	if err != nil {
		t.Fatal(err)
	}
	oldFixedFrameBound := uint64(normalized.maxTransactionPages * normalized.MaxPageSize)
	if normalized.maxTransactionBytes >= oldFixedFrameBound {
		t.Fatalf("packed dirty bound = %d, fixed-frame bound %d", normalized.maxTransactionBytes, oldFixedFrameBound)
	}
	options.ResidentBytes = int64(normalized.maxTransactionBytes)
	if _, err := options.normalized(); err != nil {
		t.Fatalf("exact dirty budget rejected: %v", err)
	}
	options.ResidentBytes--
	if _, err := options.normalized(); err == nil {
		t.Fatal("undersized dirty budget accepted")
	}
	options = testFileStoreOptions()
	options.MaxDocumentBytes = int(^uint(0) >> 1)
	if _, err := options.normalized(); err == nil {
		t.Fatal("overflowing transaction geometry accepted")
	}
	options = testFileStoreOptions()
	options.ReadMode = FileStoreReadMode(255)
	if _, err := options.normalized(); err == nil {
		t.Fatal("invalid direct-read mode accepted")
	}
	options = testFileStoreOptions()
	options.WriteMode = FileStoreWriteMode(255)
	if _, err := options.normalized(); err == nil {
		t.Fatal("invalid direct-write mode accepted")
	}
	options = testFileStoreOptions()
	options.ReadConcurrency = -1
	if _, err := options.normalized(); err == nil {
		t.Fatal("invalid read concurrency accepted")
	}
	options = testFileStoreOptions()
	options.PrefetchQueue = 32769
	if _, err := options.normalized(); err == nil {
		t.Fatal("invalid prefetch queue accepted")
	}
}

func TestFileStoreDirectReadModeAndCallerDescriptorLifetime(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "file-store-direct-*")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	options := testFileStoreOptions()
	options.ReadMode = FileStoreReadDirectTry
	options.WriteMode = FileStoreWriteDirectTry
	store, err := CreateFileStore(file, options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put("direct:key", []byte(`{"mode":"observable"}`)); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenFileStore(file, options)
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := reopened.AppendRaw(make([]byte, 0, 64), "direct:key")
	if err != nil || !ok || string(got) != `{"mode":"observable"}` {
		t.Fatalf("direct-mode read = (%q,%v,%v)", got, ok, err)
	}
	stats := reopened.Stats()
	if stats.PageReads == 0 {
		t.Fatalf("direct-mode reopen performed no cache-miss read: %+v", stats)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	// FileStore owns only independently reopened direct descriptors. Closing
	// them must never close or alter the caller-owned descriptor.
	var magic [8]byte
	if _, err := file.ReadAt(magic[:], 0); err != nil {
		t.Fatalf("caller descriptor after FileStore.Close: %v", err)
	}
}

func TestFileStoreCreateOpenAndSnapshotLifetime(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "file-store-*")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	options := testFileStoreOptions()
	store, err := CreateFileStore(file, options)
	if err != nil {
		t.Fatal(err)
	}
	if store.Len() != 0 || store.Generation() != 1 || store.DurableGeneration() != 1 {
		t.Fatalf("created state = len %d generation %d durable %d", store.Len(), store.Generation(), store.DurableGeneration())
	}
	if got, ok, err := store.AppendRaw(nil, "missing"); err != nil || ok || got != nil {
		t.Fatalf("AppendRaw missing = (%q,%v,%v)", got, ok, err)
	}
	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Len() != 0 || snapshot.Generation() != 1 {
		t.Fatalf("snapshot = len %d generation %d", snapshot.Len(), snapshot.Generation())
	}
	if err := store.Close(); !errors.Is(err, storeio.ErrLeasesActive) {
		t.Fatalf("Close with snapshot = %v, want %v", err, storeio.ErrLeasesActive)
	}
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenFileStore(file, options)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.Len() != 0 || reopened.Generation() != 1 || reopened.DurableGeneration() != 1 {
		t.Fatalf("reopened state = len %d generation %d durable %d", reopened.Len(), reopened.Generation(), reopened.DurableGeneration())
	}
}

func TestCreateFileStoreRequiresEmptyFile(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "file-store-nonempty-*")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.Write([]byte("occupied")); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateFileStore(file, testFileStoreOptions()); !errors.Is(err, ErrFileStoreNotEmpty) {
		t.Fatalf("CreateFileStore = %v, want %v", err, ErrFileStoreNotEmpty)
	}
}

func TestFileStoreMutationsOverflowSnapshotAndReopen(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "file-store-mutations-*")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	options := testFileStoreOptions()
	store, err := CreateFileStore(file, options)
	if err != nil {
		t.Fatal(err)
	}
	want := make(map[string]string)
	for i := range 10 {
		key := fmt.Sprintf("key-%02d", i)
		value := fmt.Sprintf(`{"key":%q,"value":%d}`, key, i)
		created, putErr := store.Put(key, []byte(value))
		if putErr != nil || !created {
			t.Fatalf("Put(%q) = (%v,%v)", key, created, putErr)
		}
		want[key] = value
	}
	if store.Len() != uint64(len(want)) {
		t.Fatalf("Len = %d, want %d", store.Len(), len(want))
	}

	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	old := want["key-01"]
	large := `{"payload":"` + strings.Repeat("large-value-", 400) + `"}`
	created, err := store.Put("key-01", []byte(large))
	if err != nil || created {
		t.Fatalf("update = (%v,%v), want existing", created, err)
	}
	want["key-01"] = large
	if got, ok, err := snapshot.AppendRaw(nil, "key-01"); err != nil || !ok || string(got) != old {
		t.Fatalf("old snapshot = (%q,%v,%v), want %q", got, ok, err, old)
	}
	if got, ok, err := store.AppendRaw(nil, "key-01"); err != nil || !ok || string(got) != large {
		t.Fatalf("current overflow = (%d bytes,%v,%v), want %d bytes", len(got), ok, err, len(large))
	}
	deleted, err := store.Delete("key-02")
	if err != nil || !deleted {
		t.Fatalf("Delete existing = (%v,%v)", deleted, err)
	}
	delete(want, "key-02")
	if deleted, err := store.Delete("key-02"); err != nil || deleted {
		t.Fatalf("Delete missing = (%v,%v)", deleted, err)
	}
	if got, ok, err := snapshot.AppendRaw(nil, "key-02"); err != nil || !ok || string(got) == "" {
		t.Fatalf("snapshot deleted key = (%q,%v,%v)", got, ok, err)
	}
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenFileStore(file, options)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.Len() != uint64(len(want)) {
		t.Fatalf("reopened Len = %d, want %d", reopened.Len(), len(want))
	}
	queued, err := reopened.PrefetchKeys([]string{"key-09", "key-00", "missing", "key-05", "key-01"})
	if err != nil || queued == 0 {
		t.Fatalf("PrefetchKeys = (%d,%v)", queued, err)
	}
	if stats := reopened.Stats(); stats.PrefetchQueued < uint64(queued) || stats.CapacityBytes == 0 || stats.DocumentCount != uint64(len(want)) {
		t.Fatalf("Stats after prefetch = %+v", stats)
	}
	for key, value := range want {
		got, ok, getErr := reopened.AppendRaw(nil, key)
		if getErr != nil || !ok || string(got) != value {
			t.Fatalf("reopened %q = (%q,%v,%v), want %q", key, got, ok, getErr, value)
		}
	}
	if got, ok, err := reopened.AppendRaw(nil, "key-02"); err != nil || ok || got != nil {
		t.Fatalf("reopened deleted key = (%q,%v,%v)", got, ok, err)
	}
}

func TestFileStoreRejectsInvalidMutationWithoutPublishing(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "file-store-invalid-*")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	store, err := CreateFileStore(file, testFileStoreOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	generation := store.Generation()
	if _, err := store.Put("bad", []byte(`{"unterminated":`)); err == nil {
		t.Fatal("Put invalid JSON succeeded")
	}
	if store.Generation() != generation || store.Len() != 0 {
		t.Fatalf("invalid Put published generation %d len %d", store.Generation(), store.Len())
	}
	if _, err := store.Put(strings.Repeat("k", store.options.MaxKeyBytes+1), []byte(`null`)); !errors.Is(err, ErrFileStoreKeyTooLarge) {
		t.Fatalf("oversize key = %v, want %v", err, ErrFileStoreKeyTooLarge)
	}
}

func TestFileStoreReusesExtentsWithoutViolatingSnapshots(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "file-store-reuse-*")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	options := testFileStoreOptions()
	options.MaxRetiredExtents = 512
	store, err := CreateFileStore(file, options)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Put("hot", []byte(`{"version":0}`)); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	beforePinned := store.state.Load().super.FileEnd
	for version := 1; version <= 20; version++ {
		if _, err := store.Put("hot", []byte(fmt.Sprintf(`{"version":%d}`, version))); err != nil {
			t.Fatal(err)
		}
	}
	afterPinned := store.state.Load().super.FileEnd
	if afterPinned <= beforePinned {
		t.Fatalf("active snapshot did not fence reuse: fileEnd %d -> %d", beforePinned, afterPinned)
	}
	if got, ok, err := snapshot.AppendRaw(nil, "hot"); err != nil || !ok || string(got) != `{"version":0}` {
		t.Fatalf("pinned value after churn = (%q,%v,%v)", got, ok, err)
	}
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}

	for version := 21; version <= 40; version++ {
		if _, err := store.Put("hot", []byte(fmt.Sprintf(`{"version":%d}`, version))); err != nil {
			t.Fatal(err)
		}
	}
	plateau := store.state.Load().super.FileEnd
	for version := 41; version <= 80; version++ {
		if _, err := store.Put("hot", []byte(fmt.Sprintf(`{"version":%d}`, version))); err != nil {
			t.Fatal(err)
		}
	}
	if got := store.state.Load().super.FileEnd; got != plateau {
		t.Fatalf("copy-on-write file did not plateau: %d -> %d", plateau, got)
	}
	if got, ok, err := store.AppendRaw(nil, "hot"); err != nil || !ok || string(got) != `{"version":80}` {
		t.Fatalf("latest value = (%q,%v,%v)", got, ok, err)
	}
}

func TestFileStorePersistsReusableExtentsAcrossReopen(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "file-store-free-tree-*")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	options := testFileStoreOptions()
	options.MaxRetiredExtents = 512
	store, err := CreateFileStore(file, options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put("hot", []byte(`0`)); err != nil {
		t.Fatal(err)
	}
	for version := 1; version <= 30; version++ {
		if _, err := store.Put("hot", []byte(fmt.Sprintf(`%d`, version))); err != nil {
			t.Fatal(err)
		}
	}
	if store.state.Load().freeRoot == (storeio.PageRef{}) || store.state.Load().super.FreeLength == 0 {
		t.Fatal("churn did not publish a durable free-tree root")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenFileStore(file, options)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.freeLoaded {
		t.Fatal("OpenFileStore eagerly walked the free tree")
	}
	if _, err := reopened.Put("hot", []byte(`31`)); err != nil {
		t.Fatal(err)
	}
	if !reopened.freeLoaded {
		t.Fatal("first mutation did not lazily load the bounded free tree")
	}
	for version := 32; version <= 50; version++ {
		if _, err := reopened.Put("hot", []byte(fmt.Sprintf(`%d`, version))); err != nil {
			t.Fatal(err)
		}
	}
	plateau := reopened.Stats().FileEnd
	for version := 51; version <= 80; version++ {
		if _, err := reopened.Put("hot", []byte(fmt.Sprintf(`%d`, version))); err != nil {
			t.Fatal(err)
		}
	}
	if got := reopened.Stats().FileEnd; got != plateau {
		t.Fatalf("reopened allocator did not plateau: %d -> %d", plateau, got)
	}
}

func TestFileStoreTTLPersistsAndExpiresThroughSnapshots(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "file-store-ttl-*")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	options := testFileStoreOptions()
	options.MaxRetiredExtents = 512
	store, err := CreateFileStore(file, options)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"a", "b", "c"} {
		if _, err := store.Put(key, []byte(fmt.Sprintf(`{"key":%q}`, key))); err != nil {
			t.Fatal(err)
		}
	}
	beforeTTL, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(24 * time.Hour).Truncate(time.Millisecond)
	deadlineA, deadlineB := base.Add(time.Hour), base.Add(2*time.Hour)
	if ok, err := store.SetDeadline("a", deadlineA); err != nil || !ok {
		t.Fatalf("SetDeadline(a) = (%v,%v)", ok, err)
	}
	if ok, err := store.SetDeadline("b", deadlineB); err != nil || !ok {
		t.Fatalf("SetDeadline(b) = (%v,%v)", ok, err)
	}
	if _, ok, err := beforeTTL.Deadline("a"); err != nil || ok {
		t.Fatalf("old snapshot deadline = (%v,%v)", ok, err)
	}
	if got, ok, err := store.Deadline("a"); err != nil || !ok || !got.Equal(deadlineA) {
		t.Fatalf("Deadline(a) = (%v,%v,%v), want %v", got, ok, err, deadlineA)
	}
	if _, err := store.Put("a", []byte(`{"key":"a","updated":true}`)); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := store.Deadline("a"); err != nil || !ok || !got.Equal(deadlineA) {
		t.Fatalf("Put did not preserve deadline = (%v,%v,%v)", got, ok, err)
	}
	if ok, err := store.Persist("b"); err != nil || !ok {
		t.Fatalf("Persist(b) = (%v,%v)", ok, err)
	}
	if _, ok, err := store.Deadline("b"); err != nil || ok {
		t.Fatalf("Deadline persisted b = (%v,%v)", ok, err)
	}
	if ok, err := store.SetDeadline("b", deadlineB); err != nil || !ok {
		t.Fatalf("restore Deadline(b) = (%v,%v)", ok, err)
	}
	if err := beforeTTL.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenFileStore(file, options)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.state.Load().root.TTLCount != 2 || reopened.state.Load().ttlRoot == (storeio.PageRef{}) {
		t.Fatalf("reopened TTL state = %+v", reopened.state.Load().root)
	}
	for key, want := range map[string]time.Time{"a": deadlineA, "b": deadlineB} {
		got, ok, err := reopened.Deadline(key)
		if err != nil || !ok || !got.Equal(want) {
			t.Fatalf("reopened Deadline(%s) = (%v,%v,%v), want %v", key, got, ok, err, want)
		}
	}
	beforeExpiry, err := reopened.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	expired, err := reopened.ExpireDue(base.Add(90*time.Minute), 0)
	if err != nil || expired != 1 {
		t.Fatalf("ExpireDue = (%d,%v), want (1,nil)", expired, err)
	}
	if _, ok, err := reopened.AppendRaw(nil, "a"); err != nil || ok {
		t.Fatalf("current expired a = (%v,%v)", ok, err)
	}
	if got, ok, err := beforeExpiry.AppendRaw(nil, "a"); err != nil || !ok || len(got) == 0 {
		t.Fatalf("old snapshot expired a = (%q,%v,%v)", got, ok, err)
	}
	if err := beforeExpiry.Close(); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := reopened.Deadline("b"); err != nil || !ok || !got.Equal(deadlineB) {
		t.Fatalf("unexpired b = (%v,%v,%v)", got, ok, err)
	}
	if ok, err := reopened.SetTTL("c", 0); err != nil || !ok {
		t.Fatalf("SetTTL(c,0) = (%v,%v)", ok, err)
	}
	if ok, err := reopened.SetDeadline("missing", deadlineB); err != nil || ok {
		t.Fatalf("SetDeadline(missing) = (%v,%v)", ok, err)
	}
	if ok, err := reopened.SetDeadline("b", time.Date(2500, 1, 1, 0, 0, 0, 0, time.UTC)); !errors.Is(err, ErrFileStoreDeadlineRange) || ok {
		t.Fatalf("out-of-range deadline = (%v,%v)", ok, err)
	}
}

func TestFileStoreExactIndexesMaintainProbeAndReopen(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "file-store-index-*")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	options := testFileStoreOptions()
	options.ResidentBytes = 8 << 20
	options.BufferCount = 128
	options.MaxRetiredExtents = 512
	options.Indexes = []StoreIndexDefinition{
		{Name: "status", Paths: []string{"/status"}},
		{Name: "tenant_status", Paths: []string{"/tenant", "/status"}},
	}
	store, err := CreateFileStore(file, options)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 12 {
		status := "idle"
		if i%3 == 0 {
			status = "active"
		}
		tenant := "other"
		if i%2 == 0 {
			tenant = "acme"
		}
		doc := fmt.Sprintf(`{"id":%d,"tenant":%q,"status":%q,"padding":%q}`, i, tenant, status, strings.Repeat("x", i*70))
		if i == 9 {
			doc = fmt.Sprintf(`{"id":%d,"tenant":%q,"status":"ac\u0074ive","padding":%q}`, i, tenant, strings.Repeat("x", 900))
		}
		if _, err := store.Put(fmt.Sprintf("k%02d", i), []byte(doc)); err != nil {
			t.Fatal(err)
		}
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
	active := needle(`"active"`)
	acme := needle(`"acme"`)
	countMasks := func(masks []StoreMask) int {
		count := 0
		for _, mask := range masks {
			count += bits.OnesCount64(mask.Bits)
		}
		return count
	}
	masks, err := store.AppendIndexMasks(nil, "status", active)
	if err != nil || countMasks(masks) != 4 {
		t.Fatalf("active masks = (%+v,%v), count %d", masks, err, countMasks(masks))
	}
	compound, err := store.AppendIndexMasks(nil, "tenant_status", acme, active)
	if err != nil || countMasks(compound) != 2 {
		t.Fatalf("compound masks = (%+v,%v), count %d", compound, err, countMasks(compound))
	}
	old, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	var indexWorkspace FileIndexWorkspace
	bufferedMasks := make([]StoreMask, 0, 4)
	bufferedMasks, err = old.AppendIndexMasksInto(
		bufferedMasks[:0], &indexWorkspace, "tenant_status", acme, active,
	)
	if err != nil || countMasks(bufferedMasks) != 2 {
		t.Fatalf("buffered compound masks = (%+v,%v)", bufferedMasks, err)
	}
	bufferedMasks, err = old.AppendIndexCandidateMasksInto(
		bufferedMasks[:0], &indexWorkspace, "tenant_status", acme, active,
	)
	if err != nil || countMasks(bufferedMasks) != 2 {
		t.Fatalf("buffered compound candidates = (%+v,%v)", bufferedMasks, err)
	}
	if _, err := store.Put("k00", []byte(`{"id":0,"tenant":"acme","status":"idle"}`)); err != nil {
		t.Fatal(err)
	}
	if ok, err := store.Delete("k06"); err != nil || !ok {
		t.Fatalf("Delete indexed row = (%v,%v)", ok, err)
	}
	masks, err = store.AppendIndexMasks(masks[:0], "status", active)
	if err != nil || countMasks(masks) != 2 {
		t.Fatalf("updated active masks = (%+v,%v), count %d", masks, err, countMasks(masks))
	}
	oldMasks, err := old.AppendIndexMasks(nil, "status", active)
	if err != nil || countMasks(oldMasks) != 4 {
		t.Fatalf("old snapshot masks = (%+v,%v), count %d", oldMasks, err, countMasks(oldMasks))
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenFileStore(file, options)
	if err != nil {
		t.Fatal(err)
	}
	masks, err = reopened.AppendIndexMasks(nil, "status", active)
	if err != nil || countMasks(masks) != 2 {
		t.Fatalf("reopened active masks = (%+v,%v), count %d", masks, err, countMasks(masks))
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	wrong := options
	wrong.Indexes = []StoreIndexDefinition{{Name: "status", Paths: []string{"/tenant"}}, options.Indexes[1]}
	if _, err := OpenFileStore(file, wrong); err == nil {
		t.Fatal("OpenFileStore accepted a mismatched index catalog")
	}
}

func TestFileSnapshotRangeMasksRawOrderedAndBuffered(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "file-store-mask-range-*")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	store, err := CreateFileStore(file, testFileStoreOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for i := range 10 {
		padding := ""
		if i == 9 {
			padding = strings.Repeat("x", 1024)
		}
		doc := []byte(fmt.Sprintf(`{"id":%d,"padding":%q}`, i, padding))
		if _, err := store.Put(fmt.Sprintf("k%02d", i), doc); err != nil {
			t.Fatal(err)
		}
	}
	if deleted, err := store.Delete("k01"); err != nil || !deleted {
		t.Fatalf("Delete(k01) = (%v,%v)", deleted, err)
	}
	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()

	masks := []StoreMask{
		{Chunk: 0, Bits: 1<<0 | 1<<1 | 1<<3 | 1<<63},
		{Chunk: 2, Bits: 1 << 1},
	}
	var keys []string
	scratch := make([]byte, 0, 2048)
	scratch, err = snapshot.RangeMasksRawBuffer(masks, scratch, func(key, value []byte) error {
		keys = append(keys, string(key))
		if len(value) == 0 {
			t.Fatal("empty value")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(keys, ","), "k00,k03,k09"; got != want {
		t.Fatalf("masked key order = %q, want %q", got, want)
	}
	if cap(scratch) < 1024 {
		t.Fatalf("caller overflow scratch capacity = %d, want at least 1024", cap(scratch))
	}

	var serialKeys []string
	scratch, err = snapshot.RangeRawBuffer(scratch[:0], func(key, _ []byte) error {
		serialKeys = append(serialKeys, string(key))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	beforeReadAhead := store.Stats()
	var readAheadKeys []string
	scratch, err = snapshot.RangeRawReadAheadBuffer(scratch[:0], func(key, _ []byte) error {
		readAheadKeys = append(readAheadKeys, string(key))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(readAheadKeys, ","), strings.Join(serialKeys, ","); got != want {
		t.Fatalf("read-ahead order = %q, want %q", got, want)
	}
	if after := store.Stats(); after.PrefetchQueued != beforeReadAhead.PrefetchQueued {
		t.Fatalf("buffered read-ahead should use the serial kernel-readahead lane: before=%+v after=%+v", beforeReadAhead, after)
	}
	if err := snapshot.RangeMasksRaw(
		[]StoreMask{{Chunk: 2, Bits: 1}, {Chunk: 2, Bits: 2}},
		func(_, _ []byte) error { return nil },
	); !errors.Is(err, ErrStoreMaskOrder) {
		t.Fatalf("duplicate chunk error = %v, want %v", err, ErrStoreMaskOrder)
	}
	if err := snapshot.RangeMasksRaw(
		[]StoreMask{{Chunk: 99, Bits: 1}},
		func(_, _ []byte) error { return nil },
	); !errors.Is(err, ErrStoreMaskChunk) {
		t.Fatalf("unknown chunk error = %v, want %v", err, ErrStoreMaskChunk)
	}

	steady := []StoreMask{{Chunk: 0, Bits: 1<<0 | 1<<3}, {Chunk: 2, Bits: 1 << 1}}
	visitBytes := 0
	visit := func(key, value []byte) error {
		visitBytes += len(key) + len(value)
		return nil
	}
	scratch, err = snapshot.RangeMasksRawBuffer(steady, scratch[:0], visit)
	if err != nil {
		t.Fatal(err)
	}
	if cap(scratch) < 2048 || visitBytes == 0 {
		t.Fatalf("masked steady scan returned scratch capacity %d and visited %d bytes", cap(scratch), visitBytes)
	}
}

func TestFileStoreExactIndexWorkspaceAllocations(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "file-store-index-alloc-*")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	options := testFileStoreOptions()
	options.BufferCount = 128
	options.Indexes = []StoreIndexDefinition{
		{Name: "tenant_status", Paths: []string{"/tenant", "/status"}},
	}
	store, err := CreateFileStore(file, options)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for row := range 8 {
		document := fmt.Appendf(nil, `{"tenant":"acme","status":"active","row":%d}`, row)
		if _, err := store.Put(fmt.Sprintf("k%d", row), document); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	needle := func(src string) Index {
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
	acme, active := needle(`"acme"`), needle(`"active"`)
	var workspace FileIndexWorkspace
	masks := make([]StoreMask, 0, 2)
	masks, err = snapshot.AppendIndexMasksInto(masks, &workspace, "tenant_status", acme, active)
	if err != nil || len(masks) == 0 {
		t.Fatalf("warm exact probe = (%+v,%v)", masks, err)
	}
	allocs := testing.AllocsPerRun(100, func() {
		var runErr error
		masks, runErr = snapshot.AppendIndexMasksInto(masks[:0], &workspace, "tenant_status", acme, active)
		if runErr != nil || len(masks) == 0 {
			panic("exact probe failed")
		}
	})
	if allocs != 0 {
		t.Fatalf("warmed AppendIndexMasksInto allocated %.2f times, want 0", allocs)
	}
	masks, err = snapshot.AppendIndexCandidateMasksInto(masks[:0], &workspace, "tenant_status", acme, active)
	if err != nil || len(masks) == 0 {
		t.Fatalf("warm candidate probe = (%+v,%v)", masks, err)
	}
	allocs = testing.AllocsPerRun(100, func() {
		var runErr error
		masks, runErr = snapshot.AppendIndexCandidateMasksInto(masks[:0], &workspace, "tenant_status", acme, active)
		if runErr != nil || len(masks) == 0 {
			panic("candidate probe failed")
		}
	})
	if allocs != 0 {
		t.Fatalf("warmed AppendIndexCandidateMasksInto allocated %.2f times, want 0", allocs)
	}
}

func TestFileSnapshotRangeBufferAllocations(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "file-store-range-alloc-*")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	store, err := CreateFileStore(file, testFileStoreOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for row := range 10 {
		padding := ""
		if row == 9 {
			padding = strings.Repeat("x", 1024)
		}
		document := fmt.Appendf(nil, `{"row":%d,"padding":%q}`, row, padding)
		if _, err := store.Put(fmt.Sprintf("k%02d", row), document); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	masks := []StoreMask{{Chunk: 0, Bits: 1<<0 | 1<<3}, {Chunk: 2, Bits: 1 << 1}}
	scratch := make([]byte, 0, 2048)
	visitBytes := 0
	visit := func(key, value []byte) error {
		visitBytes += len(key) + len(value)
		return nil
	}
	scratch, err = snapshot.RangeMasksRawBuffer(masks, scratch, visit)
	if err != nil {
		t.Fatal(err)
	}
	allocs := testing.AllocsPerRun(100, func() {
		visitBytes = 0
		var runErr error
		scratch, runErr = snapshot.RangeMasksRawBuffer(masks, scratch[:0], visit)
		if runErr != nil || visitBytes == 0 {
			panic("masked range failed")
		}
	})
	if allocs != 0 {
		t.Fatalf("warmed RangeMasksRawBuffer allocated %.2f times, want 0", allocs)
	}
	allocs = testing.AllocsPerRun(100, func() {
		visitBytes = 0
		var runErr error
		scratch, runErr = snapshot.RangeRawReadAheadBuffer(scratch[:0], visit)
		if runErr != nil || visitBytes == 0 {
			panic("read-ahead range failed")
		}
	})
	if allocs != 0 {
		t.Fatalf("warmed RangeRawReadAheadBuffer allocated %.2f times, want 0", allocs)
	}
}
