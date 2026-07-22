package simdjson

import (
	"errors"
	"os"
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
