package simdjson

import (
	"bytes"
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"
)

// TestFileStoreHundredXResidentSmoke is an explicit storage-pressure gate. It
// builds live FileStore state whose physical image exceeds the exact cache
// budget by 100x, reopens with an empty cache, probes distant keys, then
// exercises update, delete, and mutable TTL while eviction is unavoidable.
//
//	SIMDJSON_FILESTORE_100X=1 go test . -run '^TestFileStoreHundredXResidentSmoke$' -v -count=1
func TestFileStoreHundredXResidentSmoke(t *testing.T) {
	if os.Getenv("SIMDJSON_FILESTORE_100X") != "1" {
		t.Skip("set SIMDJSON_FILESTORE_100X=1 to run the above-RAM storage gate")
	}
	const records = 10_000
	file, err := os.CreateTemp(t.TempDir(), "file-store-100x-*")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	options := fileStoreScaleOptions()
	normalized, err := options.normalized()
	if err != nil {
		t.Fatal(err)
	}
	options.ResidentBytes = int64(normalized.maxTransactionBytes)

	started := time.Now()
	store, err := CreateFileStore(file, options)
	if err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 0, 32)
	document := make([]byte, 0, 3072)
	var sourceBytes uint64
	for row := range records {
		key = fmt.Appendf(key[:0], "row:%08d", row)
		document = appendFileStoreScaleDocument(document[:0], row, false)
		sourceBytes += uint64(len(key) + len(document))
		if created, putErr := store.Put(string(key), document); putErr != nil || !created {
			t.Fatalf("Put(%d) = (%v,%v)", row, created, putErr)
		}
	}
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}
	buildStats := store.Stats()
	if buildStats.FileEnd <= 100*buildStats.CapacityBytes {
		t.Fatalf("file image = %d bytes, need >100x %d-byte cache", buildStats.FileEnd, buildStats.CapacityBytes)
	}
	if sourceBytes <= 100*buildStats.CapacityBytes {
		t.Fatalf("source keys+JSON = %d bytes, need >100x %d-byte cache", sourceBytes, buildStats.CapacityBytes)
	}
	if buildStats.ResidentBytes > buildStats.CapacityBytes || buildStats.DirtyBytes != 0 {
		t.Fatalf("unbounded or dirty cache after Flush: %+v", buildStats)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenFileStore(file, options)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	scanStarted := time.Now()
	scanRows := 0
	var scanBytes uint64
	scanScratch := make([]byte, 0, 3072)
	scanSnapshot, err := reopened.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	scanScratch, err = scanSnapshot.RangeRawReadAheadBuffer(scanScratch, func(key, value []byte) error {
		scanRows++
		scanBytes += uint64(len(key) + len(value))
		return nil
	})
	if closeErr := scanSnapshot.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	scanElapsed := time.Since(scanStarted)
	if scanRows != records || scanBytes != sourceBytes {
		t.Fatalf("read-ahead scan = %d rows/%d bytes, want %d/%d", scanRows, scanBytes, records, sourceBytes)
	}
	scanStats := reopened.Stats()
	if scanStats.PrefetchQueued == 0 || scanStats.PrefetchHits+scanStats.CoalescedReads == 0 {
		t.Fatalf("read-ahead scan performed no overlapping reads: %+v", scanStats)
	}
	readBuffer := make([]byte, 0, 3072)
	for _, row := range []int{records - 1, 0, records / 2, 17, records - 101, 1} {
		key = fmt.Appendf(key[:0], "row:%08d", row)
		want := appendFileStoreScaleDocument(document[:0], row, false)
		got, ok, readErr := reopened.AppendRaw(readBuffer[:0], string(key))
		if readErr != nil || !ok || !bytes.Equal(got, want) {
			t.Fatalf("pressure read %d = (%q,%v,%v)", row, got, ok, readErr)
		}
	}
	updatedRow := records / 2
	updatedKey := fmt.Sprintf("row:%08d", updatedRow)
	updated := appendFileStoreScaleDocument(document[:0], updatedRow, true)
	if created, err := reopened.Put(updatedKey, updated); err != nil || created {
		t.Fatalf("pressure update = (%v,%v)", created, err)
	}
	if deleted, err := reopened.Delete("row:00000017"); err != nil || !deleted {
		t.Fatalf("pressure delete = (%v,%v)", deleted, err)
	}
	if ok, err := reopened.SetTTL("row:00000001", time.Hour); err != nil || !ok {
		t.Fatalf("pressure TTL set = (%v,%v)", ok, err)
	}
	if ok, err := reopened.SetTTL("row:00000001", 2*time.Hour); err != nil || !ok {
		t.Fatalf("pressure TTL change = (%v,%v)", ok, err)
	}
	if err := reopened.Flush(); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := reopened.AppendRaw(readBuffer[:0], updatedKey); err != nil || !ok || !bytes.Equal(got, updated) {
		t.Fatalf("updated pressure read = (%q,%v,%v)", got, ok, err)
	}
	if _, ok, err := reopened.AppendRaw(readBuffer[:0], "row:00000017"); err != nil || ok {
		t.Fatalf("deleted pressure read = (%v,%v)", ok, err)
	}
	stats := reopened.Stats()
	if stats.CapacityBytes != uint64(options.ResidentBytes) || stats.ResidentBytes > stats.CapacityBytes ||
		stats.Evictions == 0 || stats.PageReads == 0 || stats.PinnedPages != 0 || stats.DirtyBytes != 0 {
		t.Fatalf("pressure stats = %+v", stats)
	}
	deadline, hasDeadline, err := reopened.Deadline("row:00000001")
	if err != nil || !hasDeadline {
		t.Fatalf("pressure deadline = (%v,%v,%v)", deadline, hasDeadline, err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	final, err := OpenFileStore(file, options)
	if err != nil {
		t.Fatal(err)
	}
	defer final.Close()
	if got, ok, err := final.AppendRaw(readBuffer[:0], updatedKey); err != nil || !ok || !bytes.Equal(got, updated) {
		t.Fatalf("reopened update = (%q,%v,%v)", got, ok, err)
	}
	if _, ok, err := final.AppendRaw(readBuffer[:0], "row:00000017"); err != nil || ok {
		t.Fatalf("reopened delete = (%v,%v)", ok, err)
	}
	if got, ok, err := final.Deadline("row:00000001"); err != nil || !ok || !got.Equal(deadline) {
		t.Fatalf("reopened deadline = (%v,%v,%v), want %v", got, ok, err, deadline)
	}
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	t.Logf("records=%d source=%d source_ratio=%.1fx file=%d cache=%d file_ratio=%.1fx elapsed=%s scan=%s scan_mib_s=%.1f heap_alloc=%d reads=%d evictions=%d direct=%v",
		records, sourceBytes, float64(sourceBytes)/float64(stats.CapacityBytes), stats.FileEnd, stats.CapacityBytes,
		float64(stats.FileEnd)/float64(stats.CapacityBytes),
		time.Since(started), scanElapsed, float64(sourceBytes)/(1<<20)/scanElapsed.Seconds(),
		memory.HeapAlloc, stats.PageReads, stats.Evictions, stats.DirectReads)
}

func fileStoreScaleOptions() FileStoreOptions {
	return FileStoreOptions{
		Store:    StoreOptions{ChunkDocuments: 1},
		PageSize: 4096, MaxPageSize: 4096, ResidentBytes: 1 << 20,
		MaxDocumentBytes: 3072, MaxKeyBytes: 32, InlineValueBytes: 3072,
		ReadConcurrency: 4, PrefetchQueue: 64, BufferCount: 64,
		QueueSlots: 16, GroupLimit: 8, Backend: FileStoreBackendPortable,
		ReadMode: FileStoreReadDirectTry, MaxSnapshotLeases: 16,
		MaxRetiredExtents: 1 << 15,
	}
}

func appendFileStoreScaleDocument(dst []byte, row int, updated bool) []byte {
	dst = fmt.Appendf(dst, `{"id":%d,"nested":{"group":%d,"state":"s%d"},"payload":"`, row, row%64, row%8)
	for range 2048 {
		dst = append(dst, byte('a'+row%26))
	}
	dst = append(dst, `","updated":`...)
	if updated {
		dst = append(dst, "true}"...)
	} else {
		dst = append(dst, "false}"...)
	}
	return dst
}
