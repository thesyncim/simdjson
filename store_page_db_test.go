package simdjson

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/thesyncim/simdjson/internal/storeio"
)

func openPortableStorePageDB(t testing.TB, path string, maximum uint32) *StorePageDB {
	t.Helper()
	db, err := OpenStorePageDB(path, StorePageDBOptions{
		Open: StorePageOpenOptions{
			ResidentBytes: 8 * int64(maximum), MaxDocumentPageBytes: maximum,
		},
		CommitBackend: StorePageCommitPortable,
	})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestStorePageDBUpdateDeleteRecovery(t *testing.T) {
	store, original := buildStorePageTestData(t, 10, 4)
	path, initialBytes := writeStorePageTestFile(t, store, StorePageWriteOptions{MaxDocumentPageBytes: 8192})
	db := openPortableStorePageDB(t, path, 8192)
	initialGeneration := db.Generation()

	key := "account:00000003"
	large := `{"id":3,"payload":"` + strings.Repeat("x", 5000) + `"}`
	created, err := db.Put(key, []byte(large))
	if err != nil || created {
		t.Fatalf("Put update = (%v,%v)", created, err)
	}
	if db.Generation() != initialGeneration+1 || db.DurableGeneration() != db.Generation() {
		t.Fatalf("generation = visible %d durable %d, started %d",
			db.Generation(), db.DurableGeneration(), initialGeneration)
	}
	buffer := make([]byte, 0, len(large))
	got, ok, err := db.AppendRaw(buffer, key)
	if err != nil || !ok || string(got) != large {
		t.Fatalf("updated read = (%d bytes,%v,%v)", len(got), ok, err)
	}

	unchangedGeneration := db.Generation()
	if _, err := db.Put(key, []byte(large)); err != nil || db.Generation() != unchangedGeneration {
		t.Fatalf("idempotent update = generation %d, err %v", db.Generation(), err)
	}
	if _, err := db.Put("missing", []byte(`{"v":1}`)); !errors.Is(err, ErrStorePageInsertUnsupported) {
		t.Fatalf("missing Put = %v", err)
	}
	if _, err := db.Put(key, []byte(`{"broken":`)); !errors.Is(err, ErrStorePageInvalidJSON) {
		t.Fatalf("invalid Put = %v", err)
	}
	if db.Generation() != unchangedGeneration {
		t.Fatalf("failed Put changed generation to %d", db.Generation())
	}

	deletedKey := "account:00000004"
	deleted, err := db.Delete(deletedKey)
	if err != nil || !deleted {
		t.Fatalf("Delete = (%v,%v)", deleted, err)
	}
	if deleted, err := db.Delete(deletedKey); err != nil || deleted {
		t.Fatalf("second Delete = (%v,%v)", deleted, err)
	}
	if _, ok, err := db.AppendRaw(buffer[:0], deletedKey); err != nil || ok {
		t.Fatalf("deleted read = (%v,%v)", ok, err)
	}
	stats := db.Stats()
	if stats.Documents != uint64(len(original)-1) || stats.Generation != initialGeneration+2 ||
		stats.DurableGeneration != stats.Generation || stats.FileBytes <= uint64(initialBytes) ||
		stats.CommitBackend != StorePageCommitPortable || stats.DeviceCommits != 2 ||
		stats.Cache.ResidentBytes > stats.Cache.CapacityBytes {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.AppendRaw(nil, key); !errors.Is(err, ErrStorePageClosed) {
		t.Fatalf("read after Close = %v", err)
	}

	reader, err := OpenStorePageReader(path, StorePageOpenOptions{
		ResidentBytes: 4 * 8192, MaxDocumentPageBytes: 8192,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	got, ok, err = reader.AppendRaw(buffer[:0], key)
	if err != nil || !ok || string(got) != large || reader.Generation() != initialGeneration+2 {
		t.Fatalf("reopened update = (%d bytes,%v,%v), generation %d", len(got), ok, err, reader.Generation())
	}
	if _, ok, err := reader.AppendRaw(nil, deletedKey); err != nil || ok {
		t.Fatalf("reopened delete = (%v,%v)", ok, err)
	}
}

func TestStorePageDBDeletesLastChunkAndDatabase(t *testing.T) {
	store, _ := buildStorePageTestData(t, 3, 2)
	path, _ := writeStorePageTestFile(t, store, StorePageWriteOptions{MaxDocumentPageBytes: 4096})
	db := openPortableStorePageDB(t, path, 4096)
	for i := range 3 {
		key := fmt.Sprintf("account:%08d", i)
		deleted, err := db.Delete(key)
		if err != nil || !deleted {
			t.Fatalf("Delete(%q) = (%v,%v)", key, deleted, err)
		}
	}
	if db.Len() != 0 {
		t.Fatalf("Len = %d, want 0", db.Len())
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reader, err := OpenStorePageReader(path, StorePageOpenOptions{
		ResidentBytes: 2 * 4096, MaxDocumentPageBytes: 4096,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if reader.Len() != 0 {
		t.Fatalf("reopened Len = %d, want 0", reader.Len())
	}
}

func TestStorePageDBDeleteCopiesMultiLevelKeyPath(t *testing.T) {
	store, want := buildStorePageTestData(t, 512, 16)
	path, initialBytes := writeStorePageTestFile(t, store, StorePageWriteOptions{MaxDocumentPageBytes: 4096})
	db := openPortableStorePageDB(t, path, 4096)
	deletedKey := "account:00000246"
	if deleted, err := db.Delete(deletedKey); err != nil || !deleted {
		t.Fatalf("Delete = (%v,%v)", deleted, err)
	}
	stats := db.Stats()
	const wantGrowth = uint64(5 * storePageQuantum) // document, chunk radix, key leaf+branch, state
	if growth := stats.FileBytes - uint64(initialBytes); growth != wantGrowth {
		t.Fatalf("delete COW growth = %d bytes, want %d", growth, wantGrowth)
	}
	for _, key := range []string{"account:00000100", "account:00000400"} {
		if deleted, err := db.Delete(key); err != nil || !deleted {
			t.Fatalf("Delete(%q) = (%v,%v)", key, deleted, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reader, err := OpenStorePageReader(path, StorePageOpenOptions{
		ResidentBytes: 4 * 4096, MaxDocumentPageBytes: 4096,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	for _, key := range []string{deletedKey, "account:00000100", "account:00000400"} {
		if _, ok, err := reader.AppendRaw(nil, key); err != nil || ok {
			t.Fatalf("deleted key %q = (%v,%v)", key, ok, err)
		}
	}
	for _, key := range []string{"account:00000245", "account:00000247", "account:00000511"} {
		got, ok, err := reader.AppendRaw(nil, key)
		if err != nil || !ok || string(got) != want[key] {
			t.Fatalf("neighbor %q = (%q,%v,%v)", key, got, ok, err)
		}
	}
}

func TestStorePageDBRootFallback(t *testing.T) {
	store, original := buildStorePageTestData(t, 4, 4)
	path, _ := writeStorePageTestFile(t, store, StorePageWriteOptions{MaxDocumentPageBytes: 4096})
	db := openPortableStorePageDB(t, path, 4096)
	key := "account:00000001"
	if _, err := db.Put(key, []byte(`{"changed":true}`)); err != nil {
		t.Fatal(err)
	}
	newGeneration := db.Generation()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	offset, err := storeio.SuperblockOffset(newGeneration, storePageQuantum)
	if err != nil {
		t.Fatal(err)
	}
	var torn [storeio.SuperblockSize]byte
	if _, err := file.WriteAt(torn[:], offset); err != nil {
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	reader, err := OpenStorePageReader(path, StorePageOpenOptions{
		ResidentBytes: 2 * 4096, MaxDocumentPageBytes: 4096,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	got, ok, err := reader.AppendRaw(nil, key)
	if err != nil || !ok || string(got) != original[key] || reader.Generation() != newGeneration-1 {
		t.Fatalf("fallback read = (%q,%v,%v), generation %d", got, ok, err, reader.Generation())
	}
}

func TestStorePageDBEnforcesSingleWriter(t *testing.T) {
	store, _ := buildStorePageTestData(t, 4, 4)
	path, _ := writeStorePageTestFile(t, store, StorePageWriteOptions{MaxDocumentPageBytes: 4096})
	first := openPortableStorePageDB(t, path, 4096)
	if second, err := OpenStorePageDB(path, StorePageDBOptions{
		Open: StorePageOpenOptions{
			ResidentBytes: 2 * 4096, MaxDocumentPageBytes: 4096,
		},
		CommitBackend: StorePageCommitPortable,
	}); !errors.Is(err, ErrStorePageWriterLocked) {
		if second != nil {
			_ = second.Close()
		}
		t.Fatalf("second writer = %v, want %v", err, ErrStorePageWriterLocked)
	}
	reader, err := OpenStorePageReader(path, StorePageOpenOptions{
		ResidentBytes: 2 * 4096, MaxDocumentPageBytes: 4096,
	})
	if err != nil {
		t.Fatalf("reader alongside writer: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second := openPortableStorePageDB(t, path, 4096)
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStorePageDBConcurrentReadersAndWriter(t *testing.T) {
	store, _ := buildStorePageTestData(t, 128, 16)
	path, _ := writeStorePageTestFile(t, store, StorePageWriteOptions{MaxDocumentPageBytes: 4096})
	db := openPortableStorePageDB(t, path, 4096)
	defer db.Close()

	var stop atomic.Bool
	var failed atomic.Pointer[error]
	var readers sync.WaitGroup
	for worker := 0; worker < min(runtime.GOMAXPROCS(0), 8); worker++ {
		readers.Add(1)
		go func(id int) {
			defer readers.Done()
			key := fmt.Sprintf("account:%08d", id)
			buffer := make([]byte, 0, 128)
			for !stop.Load() {
				got, ok, err := db.AppendRaw(buffer[:0], key)
				if err != nil || !ok || !Valid(got) {
					failure := fmt.Errorf("reader %d = (%q,%v,%v)", id, got, ok, err)
					failed.CompareAndSwap(nil, &failure)
					return
				}
			}
		}(worker)
	}
	key := "account:00000000"
	for i := range 32 {
		doc := []byte(fmt.Sprintf(`{"id":0,"version":%d}`, i))
		if _, err := db.Put(key, doc); err != nil {
			t.Fatal(err)
		}
	}
	stop.Store(true)
	readers.Wait()
	if failure := failed.Load(); failure != nil {
		t.Fatal(*failure)
	}
}

func TestStorePageDBAppendRawSteadyAllocation(t *testing.T) {
	store, want := buildStorePageTestData(t, 64, 16)
	path, _ := writeStorePageTestFile(t, store, StorePageWriteOptions{MaxDocumentPageBytes: 4096})
	db := openPortableStorePageDB(t, path, 4096)
	defer db.Close()
	key := "account:00000007"
	buffer := make([]byte, 0, 256)
	if _, ok, err := db.AppendRaw(buffer[:0], key); err != nil || !ok {
		t.Fatalf("warm read = (%v,%v)", ok, err)
	}
	allocs := testing.AllocsPerRun(1000, func() {
		got, ok, err := db.AppendRaw(buffer[:0], key)
		if err != nil || !ok || !bytes.Equal(got, []byte(want[key])) {
			panic("unexpected resident read")
		}
	})
	if allocs != 0 {
		t.Fatalf("AppendRaw allocations = %g, want 0", allocs)
	}
}

func TestStorePageDBMoreThanHundredTimesResidentBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("large mutable bounded-residency smoke")
	}
	store, _ := buildStorePageTestData(t, 4096, 16)
	path, size := writeStorePageTestFile(t, store, StorePageWriteOptions{MaxDocumentPageBytes: 4096})
	const resident = int64(2 * 4096)
	if size <= 100*resident {
		t.Fatalf("test image = %d bytes, need >100x %d-byte cache", size, resident)
	}
	db, err := OpenStorePageDB(path, StorePageDBOptions{
		Open: StorePageOpenOptions{
			ResidentBytes: resident, MaxDocumentPageBytes: 4096,
		},
		CommitBackend: StorePageCommitPortable,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	key := "account:00002048"
	want := []byte(`{"id":2048,"mutable":true}`)
	if _, err := db.Put(key, want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := db.AppendRaw(make([]byte, 0, len(want)), key)
	if err != nil || !ok || !bytes.Equal(got, want) {
		t.Fatalf("updated pressure read = (%q,%v,%v)", got, ok, err)
	}
	stats := db.Stats()
	if stats.Cache.CapacityBytes != uint64(resident) || stats.Cache.ResidentBytes > uint64(resident) ||
		stats.FileBytes <= uint64(size) {
		t.Fatalf("bounded mutable stats = %+v", stats)
	}
	const wantGrowth = uint64(4 * storePageQuantum) // document, two radix nodes, state
	if growth := stats.FileBytes - uint64(size); growth != wantGrowth {
		t.Fatalf("one-row COW growth = %d bytes, want %d", growth, wantGrowth)
	}
}

func BenchmarkStorePageDBResidentRead(b *testing.B) {
	store, _ := buildStorePageTestData(b, 1024, 16)
	path, _ := writeStorePageTestFile(b, store, StorePageWriteOptions{MaxDocumentPageBytes: 4096})
	db := openPortableStorePageDB(b, path, 4096)
	defer db.Close()
	key := "account:00000512"
	buffer := make([]byte, 0, 256)
	if _, ok, err := db.AppendRaw(buffer[:0], key); err != nil || !ok {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, ok, err := db.AppendRaw(buffer[:0], key); err != nil || !ok {
			b.Fatal(err)
		}
	}
}

func BenchmarkStorePageDBResidentCompiledRead(b *testing.B) {
	store, _ := buildStorePageTestData(b, 1024, 16)
	path, _ := writeStorePageTestFile(b, store, StorePageWriteOptions{MaxDocumentPageBytes: 4096})
	db := openPortableStorePageDB(b, path, 4096)
	defer db.Close()
	key := db.CompileKey("account:00000512")
	buffer := make([]byte, 0, 256)
	if _, ok, err := db.AppendRawKey(buffer[:0], key); err != nil || !ok {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, ok, err := db.AppendRawKey(buffer[:0], key); err != nil || !ok {
			b.Fatal(err)
		}
	}
}

func BenchmarkStorePageDBDurableUpdatePortable(b *testing.B) {
	benchmarkStorePageDBDurableUpdate(b, StorePageCommitPortable)
}

func BenchmarkStorePageDBDurableUpdateIOUring(b *testing.B) {
	if runtime.GOOS != "linux" {
		b.Skip("io_uring is Linux-only")
	}
	benchmarkStorePageDBDurableUpdate(b, StorePageCommitIOUring)
}

func benchmarkStorePageDBDurableUpdate(b *testing.B, backend StorePageCommitBackend) {
	store, _ := buildStorePageTestData(b, 1024, 16)
	path, _ := writeStorePageTestFile(b, store, StorePageWriteOptions{MaxDocumentPageBytes: 4096})
	db, err := OpenStorePageDB(path, StorePageDBOptions{
		Open: StorePageOpenOptions{
			ResidentBytes: 8 * 4096, MaxDocumentPageBytes: 4096,
		},
		CommitBackend: backend,
	})
	if err != nil {
		b.Skipf("commit backend %s unavailable: %v", backend, err)
	}
	defer db.Close()
	key := "account:00000512"
	documents := [2][]byte{[]byte(`{"id":512,"version":0}`), []byte(`{"id":512,"version":1}`)}
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		if _, err := db.Put(key, documents[i&1]); err != nil {
			b.Fatal(err)
		}
	}
}
