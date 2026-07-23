package simdjson

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/thesyncim/simdjson/internal/storeio"
)

var fileStoreBytesSink []byte
var fileStoreMasksSink []StoreMask

func BenchmarkFileStoreUpdateDurability(b *testing.B) {
	cases := []struct {
		name string
		mode FileStoreWriteMode
	}{
		{name: "buffered", mode: FileStoreWriteBuffered},
		{name: "direct", mode: FileStoreWriteDirectTry},
	}
	for _, test := range cases {
		b.Run(test.name, func(b *testing.B) {
			file, err := os.CreateTemp(b.TempDir(), "file-store-update-benchmark-*")
			if err != nil {
				b.Fatal(err)
			}
			defer file.Close()
			options := testFileStoreOptions()
			options.Synchronous = false
			options.Store.ChunkDocuments = 64
			options.WriteMode = test.mode
			options.ResidentBytes = 32 << 20
			options.BufferCount = 512
			options.QueueSlots = 64
			options.GroupLimit = 32
			options.MaxRetiredExtents = 1 << 16
			store, err := CreateFileStore(file, options)
			if err != nil {
				b.Fatal(err)
			}
			defer store.Close()
			if test.mode != FileStoreWriteBuffered && !store.Stats().DirectWrites {
				b.Skip("benchmark filesystem rejected O_DIRECT writes")
			}
			keys := make([]string, 1024)
			value := []byte(`{"version":1,"payload":"` + strings.Repeat("x", 1024) + `"}`)
			for i := range keys {
				keys[i] = fmt.Sprintf("key-%05d", i)
				if _, err := store.Put(keys[i], value); err != nil {
					b.Fatal(err)
				}
			}
			if err := store.Flush(); err != nil {
				b.Fatal(err)
			}
			b.SetBytes(int64(len(value)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := store.Put(keys[i&1023], value); err != nil {
					b.Fatal(err)
				}
			}
			if err := store.Flush(); err != nil {
				b.Fatal(err)
			}
			b.StopTimer()
			stats := store.Stats()
			if stats.DirectWrites {
				b.ReportMetric(1, "direct")
			}
			b.ReportMetric(float64(stats.LargestCommitGroup), "max-group")
		})
	}
}

func BenchmarkFileSnapshotAppendRaw(b *testing.B) {
	file, err := os.CreateTemp(b.TempDir(), "file-store-benchmark-*")
	if err != nil {
		b.Fatal(err)
	}
	defer file.Close()
	options := testFileStoreOptions()
	options.Synchronous = false
	options.Store.ChunkDocuments = 64
	options.MaxRetiredExtents = 1 << 16
	options.ResidentBytes = 32 << 20
	options.BufferCount = 512
	store, err := CreateFileStore(file, options)
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()
	keys := make([]string, 1024)
	valueBytes := int64(0)
	for i := range keys {
		keys[i] = fmt.Sprintf("key-%05d", i)
		doc := []byte(fmt.Sprintf(`{"id":%d,"group":%d,"active":true,"name":"document-%05d"}`, i, i%16, i))
		valueBytes += int64(len(doc))
		if _, err := store.Put(keys[i], doc); err != nil {
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
	dst := make([]byte, 0, 256)
	fileStoreBytesSink, _, err = snapshot.AppendRaw(dst[:0], keys[0])
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(valueBytes / int64(len(keys)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		fileStoreBytesSink, _, err = snapshot.AppendRaw(dst[:0], keys[i&1023])
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkFileSnapshotRangeRaw(b *testing.B) {
	file, err := os.CreateTemp(b.TempDir(), "file-store-range-benchmark-*")
	if err != nil {
		b.Fatal(err)
	}
	defer file.Close()
	options := testFileStoreOptions()
	options.Synchronous = false
	options.Store.ChunkDocuments = 64
	options.MaxRetiredExtents = 1 << 16
	options.ResidentBytes = 32 << 20
	options.BufferCount = 512
	store, err := CreateFileStore(file, options)
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()
	var sourceBytes int64
	for i := range 1024 {
		doc := []byte(fmt.Sprintf(`{"id":%d,"group":%d,"name":"document-%05d"}`, i, i%16, i))
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
	b.SetBytes(sourceBytes)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if err := snapshot.RangeRaw(func(_, value []byte) error {
			fileStoreBytesSink = value
			return nil
		}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkFileSnapshotRangeRawPressure(b *testing.B) {
	const records = 2048
	file, err := os.CreateTemp(b.TempDir(), "file-store-range-pressure-*")
	if err != nil {
		b.Fatal(err)
	}
	defer file.Close()
	options := fileStoreScaleOptions()
	normalized, err := options.normalized()
	if err != nil {
		b.Fatal(err)
	}
	options.ResidentBytes = int64(normalized.maxTransactionBytes)
	store, err := CreateFileStore(file, options)
	if err != nil {
		b.Fatal(err)
	}
	key := make([]byte, 0, 32)
	document := make([]byte, 0, 3072)
	var sourceBytes int64
	for row := range records {
		key = fmt.Appendf(key[:0], "row:%08d", row)
		document = appendFileStoreScaleDocument(document[:0], row, false)
		sourceBytes += int64(len(key) + len(document))
		if _, err := store.Put(string(key), document); err != nil {
			b.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		b.Fatal(err)
	}

	cases := []struct {
		name           string
		backend        FileStoreBackend
		readQueueDepth int
		readAhead      bool
	}{
		{name: "serial", backend: FileStoreBackendPortable},
		{name: "read_ahead/portable/4", backend: FileStoreBackendPortable, readQueueDepth: 4, readAhead: true},
		{name: "read_ahead/io_uring/4", backend: FileStoreBackendIOUring, readQueueDepth: 4, readAhead: true},
		{name: "read_ahead/io_uring/8", backend: FileStoreBackendIOUring, readQueueDepth: 8, readAhead: true},
		{name: "read_ahead/io_uring/16", backend: FileStoreBackendIOUring, readQueueDepth: 16, readAhead: true},
		{name: "read_ahead/io_uring/32", backend: FileStoreBackendIOUring, readQueueDepth: 32, readAhead: true},
		{name: "read_ahead/io_uring/64", backend: FileStoreBackendIOUring, readQueueDepth: 64, readAhead: true},
	}
	for _, test := range cases {
		b.Run(test.name, func(b *testing.B) {
			openOptions := options
			openOptions.Backend = test.backend
			if test.readQueueDepth != 0 {
				openOptions.ReadQueueDepth = test.readQueueDepth
			}
			store, err := OpenFileStore(file, openOptions)
			if test.backend == FileStoreBackendIOUring &&
				(errors.Is(err, storeio.ErrUnavailable) || errors.Is(err, storeio.ErrUnsupported)) {
				b.Skip(err)
			}
			if err != nil {
				b.Fatalf("%v: stats=%+v", err, store.Stats())
			}
			defer store.Close()
			snapshot, err := store.Snapshot()
			if err != nil {
				b.Fatal(err)
			}
			defer snapshot.Close()
			stats := store.Stats()
			if test.readAhead && !stats.DirectReads {
				b.Skip("read-ahead pressure benchmark requires active O_DIRECT")
			}
			if stats.ReadBackend != test.backend {
				b.Fatalf("read backend = %v, want %v", stats.ReadBackend, test.backend)
			}
			visit := func(_, value []byte) error {
				fileStoreBytesSink = value
				return nil
			}
			var scratch []byte
			if test.readAhead {
				scratch, err = snapshot.RangeRawReadAheadBuffer(scratch[:0], visit)
			} else {
				scratch, err = snapshot.RangeRawBuffer(scratch[:0], visit)
			}
			if err != nil {
				b.Fatal(err)
			}
			b.SetBytes(sourceBytes)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if test.readAhead {
					scratch, err = snapshot.RangeRawReadAheadBuffer(scratch[:0], visit)
				} else {
					scratch, err = snapshot.RangeRawBuffer(scratch[:0], visit)
				}
				if err != nil {
					b.Fatalf("%v: stats=%+v", err, store.Stats())
				}
			}
			b.StopTimer()
			stats = store.Stats()
			if test.backend == FileStoreBackendIOUring {
				b.ReportMetric(float64(stats.LargestReadBatch), "max-read-batch")
			}
		})
	}
}

func BenchmarkFileSnapshotAppendIndexMasks(b *testing.B) {
	file, err := os.CreateTemp(b.TempDir(), "file-store-index-benchmark-*")
	if err != nil {
		b.Fatal(err)
	}
	defer file.Close()
	options := testFileStoreOptions()
	options.Synchronous = false
	options.Store.ChunkDocuments = 64
	options.ResidentBytes = 32 << 20
	options.BufferCount = 512
	options.Indexes = []StoreIndexDefinition{{Name: "status", Paths: []string{"/status"}}}
	store, err := CreateFileStore(file, options)
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()
	for i := range 256 {
		status := "idle"
		if i%16 == 0 {
			status = "active"
		}
		doc := []byte(fmt.Sprintf(`{"id":%d,"status":%q,"padding":%q}`, i, status, strings.Repeat("x", i%48)))
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
	entries, err := RequiredIndexEntries([]byte(`"active"`))
	if err != nil {
		b.Fatal(err)
	}
	needle, err := BuildIndex([]byte(`"active"`), make([]IndexEntry, entries))
	if err != nil {
		b.Fatal(err)
	}
	var workspace FileIndexWorkspace
	dst := make([]StoreMask, 0, 4)
	if dst, err = snapshot.AppendIndexMasksInto(dst, &workspace, "status", needle); err != nil {
		b.Fatal(err)
	}
	b.Run("workspace", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			fileStoreMasksSink, err = snapshot.AppendIndexMasksInto(dst[:0], &workspace, "status", needle)
			if err != nil || len(fileStoreMasksSink) != 4 {
				b.Fatal(err)
			}
		}
	})
	b.Run("candidate-workspace", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			fileStoreMasksSink, err = snapshot.AppendIndexCandidateMasksInto(dst[:0], &workspace, "status", needle)
			if err != nil || len(fileStoreMasksSink) != 4 {
				b.Fatal(err)
			}
		}
	})
	b.Run("convenience", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			fileStoreMasksSink, err = snapshot.AppendIndexMasks(dst[:0], "status", needle)
			if err != nil || len(fileStoreMasksSink) != 4 {
				b.Fatal(err)
			}
		}
	})
}
