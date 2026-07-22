package simdjson

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

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
