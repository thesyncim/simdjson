package simdjson

import (
	"bytes"
	"os"
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thesyncim/simdjson/internal/storeio"
)

// TestStoreScaleSmoke is an explicit, non-CI capacity/performance diagnostic.
// STORE_SCALE_SMOKE is a comma-separated record-count list, for example:
//
//	STORE_SCALE_SMOKE=10000,100000,5000000 go test -run TestStoreScaleSmoke -v -count=1
//
// It reports one bulk build with nested single-column and compound indexes,
// post-GC live heap, keyed reads, indexed bitmap queries, updates,
// delete/reinsert churn, and TTL changes. The test deliberately does not hide
// dataset construction in benchmark setup or repeat a multi-million-row build
// until testing.B chooses a duration.
func TestStoreScaleSmoke(t *testing.T) {
	spec := os.Getenv("STORE_SCALE_SMOKE")
	if spec == "" {
		t.Skip("set STORE_SCALE_SMOKE to a comma-separated record-count list")
	}
	counts := parseStoreScaleCounts(t, spec)
	for _, records := range counts {
		t.Run(strconv.Itoa(records), func(t *testing.T) {
			runStoreScaleSmoke(t, records)
		})
	}
}

func parseStoreScaleCounts(t *testing.T, spec string) []int {
	t.Helper()
	parts := strings.Split(spec, ",")
	counts := make([]int, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		count, err := strconv.Atoi(part)
		if err != nil || count <= 0 {
			t.Fatalf("STORE_SCALE_SMOKE entry %q is not a positive integer", part)
		}
		counts = append(counts, count)
	}
	return counts
}

func runStoreScaleSmoke(t *testing.T, records int) {
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	builder, err := NewStoreBuilder(StoreOptions{ChunkDocuments: 64, ShapeTapes: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, definition := range []StoreIndexDefinition{
		{Name: "group", Paths: []string{"/group"}},
		{Name: "tenant_state", Paths: []string{"/profile/tenant", "/state"}},
	} {
		if err := builder.CreateIndex(definition); err != nil {
			t.Fatal(err)
		}
	}

	started := time.Now()
	var keyScratch [32]byte
	var documentScratch [192]byte
	var sourceBytes uint64
	var documentExtentBytes uint64
	chunkDataBytes := 0
	chunkRows := 0
	for i := 0; i < records; i++ {
		key := appendStoreScaleKey(keyScratch[:0], i)
		document := appendStoreScaleDocument(documentScratch[:0], i, false)
		sourceBytes += uint64(len(key) + len(document))
		chunkDataBytes += len(key) + len(document)
		chunkRows++
		if err := builder.Append(string(key), document); err != nil {
			t.Fatalf("Append row %d: %v", i, err)
		}
		if chunkRows == 64 {
			documentExtentBytes += storeScaleDocumentExtent(chunkRows, chunkDataBytes)
			chunkDataBytes = 0
			chunkRows = 0
		}
	}
	if chunkRows != 0 {
		documentExtentBytes += storeScaleDocumentExtent(chunkRows, chunkDataBytes)
	}
	store, err := builder.Build()
	if err != nil {
		t.Fatal(err)
	}
	buildDuration := time.Since(started)
	if store.Len() != records {
		t.Fatalf("Len = %d, want %d", store.Len(), records)
	}
	if os.Getenv("STORE_SCALE_REOPEN") != "" {
		var image bytes.Buffer
		if _, err := store.WriteTo(&image); err != nil {
			t.Fatal(err)
		}
		store, err = OpenStore(image.Bytes())
		if err != nil {
			t.Fatal(err)
		}
	}

	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	liveHeap := positiveDelta(after.HeapAlloc, before.HeapAlloc)
	heapObjects := positiveDelta(after.HeapObjects, before.HeapObjects)
	storeStats := store.Stats()
	externalBytes := storeStats.ExternalKeyBytes + storeStats.ExternalDocumentBytes + storeStats.ExternalIndexBytes
	accountedBytes := liveHeap + externalBytes
	pause := time.Duration(positiveDelta(after.PauseTotalNs, before.PauseTotalNs))
	gcCycles := positiveDelta(uint64(after.NumGC), uint64(before.NumGC))
	if profilePath := os.Getenv("STORE_SCALE_HEAP_PROFILE"); profilePath != "" {
		profile, profileErr := os.Create(profilePath)
		if profileErr != nil {
			t.Fatal(profileErr)
		}
		if profileErr = pprof.WriteHeapProfile(profile); profileErr != nil {
			_ = profile.Close()
			t.Fatal(profileErr)
		}
		if profileErr = profile.Close(); profileErr != nil {
			t.Fatal(profileErr)
		}
	}

	snapshot := store.Snapshot()
	keys := make([]string, min(records, 4096))
	for i := range keys {
		row := int((uint64(i) * 11400714819323198485) % uint64(records))
		keys[i] = string(appendStoreScaleKey(nil, row))
	}
	pointRuns, pointDuration := storeScaleMeasure(250*time.Millisecond, func(iteration int) {
		storeRawSink, storeBoolSink = snapshot.GetRaw(keys[iteration%len(keys)])
	})
	if !storeBoolSink {
		t.Fatal("point-read smoke ended on a miss")
	}

	groupNeedle := storeScaleScalar(t, `7`)
	tenantNeedle := storeScaleScalar(t, `"t07"`)
	stateNeedle := storeScaleScalar(t, `"s3"`)
	masks := make([]StoreMask, 0, (records+63)/64)
	queryRuns, queryDuration := storeScaleMeasure(250*time.Millisecond, func(_ int) {
		masks, err = snapshot.AppendIndexMasks(masks[:0], "tenant_state", tenantNeedle, stateNeedle)
		if err != nil {
			panic(err)
		}
	})
	compoundMasks := len(masks)
	groupRuns, groupDuration := storeScaleMeasure(250*time.Millisecond, func(_ int) {
		masks, err = snapshot.AppendIndexMasks(masks[:0], "group", groupNeedle)
		if err != nil {
			panic(err)
		}
	})
	groupMasks := len(masks)
	groupPostingExtentBytes, compoundPostingExtentBytes := storeScalePostingExtents(t, snapshot, records)
	postingExtentBytes := groupPostingExtentBytes + compoundPostingExtentBytes

	operationCount := min(records, 4096)
	started = time.Now()
	for i := 0; i < operationCount; i++ {
		row := i % records
		key := appendStoreScaleKey(keyScratch[:0], row)
		document := appendStoreScaleDocument(documentScratch[:0], row, i&1 != 0)
		if _, err := store.Put(string(key), document); err != nil {
			t.Fatalf("update row %d: %v", row, err)
		}
	}
	updateDuration := time.Since(started)

	churnCount := min(records, 1024)
	started = time.Now()
	for i := 0; i < churnCount; i++ {
		row := i % records
		keyBytes := appendStoreScaleKey(keyScratch[:0], row)
		key := string(keyBytes)
		if !store.Delete(key) {
			t.Fatalf("delete row %d missed", row)
		}
		document := appendStoreScaleDocument(documentScratch[:0], row, false)
		if _, err := store.Put(key, document); err != nil {
			t.Fatalf("reinsert row %d: %v", row, err)
		}
	}
	churnDuration := time.Since(started)

	ttlCount := 4096
	ttlKey := keys[0]
	deadline := time.Unix(4_000_000_000, 0)
	started = time.Now()
	for i := 0; i < ttlCount; i++ {
		if !store.SetDeadline(ttlKey, deadline.Add(time.Duration(i)*time.Second)) {
			t.Fatal("TTL change missed")
		}
	}
	ttlDuration := time.Since(started)

	var indexBytes uint64
	for _, name := range []string{"group", "tenant_state"} {
		stats, statsErr := snapshot.IndexStats(name)
		if statsErr != nil {
			t.Fatal(statsErr)
		}
		indexBytes += stats.EstimatedBytes
	}

	t.Logf("STORE_SCALE records=%d build=%s build_docs_s=%.0f source_bytes=%d source_B_doc=%.1f live_heap=%d heap_B_doc=%.1f heap_source_ratio=%.2f external_bytes=%d external_B_doc=%.1f external_key_bytes=%d external_document_bytes=%d external_index_bytes=%d accounted_bytes=%d accounted_B_doc=%.1f accounted_source_ratio=%.2f heap_objects=%d objects_doc=%.3f gc_cycles=%d gc_pause=%s document_extent_bytes=%d document_extent_B_doc=%.1f extent_source_ratio=%.2f index_bytes=%d index_B_doc=%.2f posting_extent_bytes=%d posting_extent_B_doc=%.2f group_posting_extent_bytes=%d compound_posting_extent_bytes=%d posting_vs_heap_index=%.3f point=%s point_runs=%d compound=%s compound_runs=%d compound_masks=%d group=%s group_runs=%d group_masks=%d update=%s delete_insert=%s ttl_change=%s",
		records,
		buildDuration,
		float64(records)/buildDuration.Seconds(),
		sourceBytes,
		float64(sourceBytes)/float64(records),
		liveHeap,
		float64(liveHeap)/float64(records),
		float64(liveHeap)/float64(sourceBytes),
		externalBytes,
		float64(externalBytes)/float64(records),
		storeStats.ExternalKeyBytes,
		storeStats.ExternalDocumentBytes,
		storeStats.ExternalIndexBytes,
		accountedBytes,
		float64(accountedBytes)/float64(records),
		float64(accountedBytes)/float64(sourceBytes),
		heapObjects,
		float64(heapObjects)/float64(records),
		gcCycles,
		pause,
		documentExtentBytes,
		float64(documentExtentBytes)/float64(records),
		float64(documentExtentBytes)/float64(sourceBytes),
		indexBytes,
		float64(indexBytes)/float64(records),
		postingExtentBytes,
		float64(postingExtentBytes)/float64(records),
		groupPostingExtentBytes,
		compoundPostingExtentBytes,
		float64(postingExtentBytes)/float64(indexBytes),
		pointDuration/time.Duration(pointRuns),
		pointRuns,
		queryDuration/time.Duration(queryRuns),
		queryRuns,
		compoundMasks,
		groupDuration/time.Duration(groupRuns),
		groupRuns,
		groupMasks,
		updateDuration/time.Duration(operationCount),
		churnDuration/time.Duration(churnCount),
		ttlDuration/time.Duration(ttlCount),
	)
	runtime.KeepAlive(store)
}

func storeScalePostingExtents(t *testing.T, snapshot Snapshot, records int) (group, compound uint64) {
	t.Helper()
	const payloadCapacity = 4096 - storeio.PageHeaderSize - storeio.PageTrailerSize
	masks := make([]StoreMask, 0, (records+63)/64)
	postings := make([]storeio.PostingEntry, 0, cap(masks))
	var extentBytes uint64
	pageUsed := storeio.PostingPagePayloadHeaderSize
	flushPage := func() {
		if pageUsed == storeio.PostingPagePayloadHeaderSize {
			return
		}
		extentBytes += 4096
		pageUsed = storeio.PostingPagePayloadHeaderSize
	}
	appendStream := func(name string, values ...Index) {
		var err error
		masks, err = snapshot.AppendIndexMasks(masks[:0], name, values...)
		if err != nil {
			t.Fatal(err)
		}
		if len(masks) == 0 {
			return
		}
		postings = postings[:0]
		for _, mask := range masks {
			postings = append(postings, storeio.PostingEntry{Chunk: mask.Chunk, Bits: mask.Bits})
		}
		for len(postings) != 0 {
			encodedCapacity := payloadCapacity - pageUsed - storeio.PostingSegmentHeaderSize
			if encodedCapacity <= 0 {
				flushPage()
				continue
			}
			count, encodedBytes, prefixErr := storeio.PostingPagePrefix(postings, encodedCapacity)
			if prefixErr != nil {
				if pageUsed != storeio.PostingPagePayloadHeaderSize {
					flushPage()
					continue
				}
				t.Fatal(prefixErr)
			}
			pageUsed += storeio.PostingSegmentHeaderSize + encodedBytes
			postings = postings[count:]
			if len(postings) != 0 {
				// A stream appears at most once in a page. Its continuation
				// starts in the next page and is linked by logical id/rank.
				flushPage()
			}
		}
	}
	for groupValue := 0; groupValue < 128; groupValue++ {
		appendStream("group", storeScaleScalar(t, strconv.Itoa(groupValue)))
	}
	flushPage()
	group = extentBytes
	for tenant := 0; tenant < 64; tenant++ {
		var tenantJSON = [5]byte{'"', 't', byte('0' + tenant/10), byte('0' + tenant%10), '"'}
		tenantValue := storeScaleScalar(t, string(tenantJSON[:]))
		for state := 0; state < 4; state++ {
			var stateJSON = [4]byte{'"', 's', byte('0' + state), '"'}
			appendStream("tenant_state", tenantValue, storeScaleScalar(t, string(stateJSON[:])))
		}
	}
	flushPage()
	return group, extentBytes - group
}

func appendStoreScaleKey(dst []byte, row int) []byte {
	dst = append(dst, "account:"...)
	return strconv.AppendInt(dst, int64(row), 10)
}

func appendStoreScaleDocument(dst []byte, row int, replacement bool) []byte {
	dst = append(dst, `{"id":`...)
	dst = strconv.AppendInt(dst, int64(row), 10)
	dst = append(dst, `,"profile":{"tenant":"t`...)
	if tenant := row & 63; tenant < 10 {
		dst = append(dst, '0')
	}
	dst = strconv.AppendInt(dst, int64(row&63), 10)
	dst = append(dst, `"},"state":"s`...)
	dst = strconv.AppendInt(dst, int64((row>>6)&3), 10)
	dst = append(dst, `","group":`...)
	dst = strconv.AppendInt(dst, int64(row&127), 10)
	dst = append(dst, `,"payload":"`...)
	if replacement {
		dst = append(dst, "replacement"...)
	} else {
		dst = append(dst, "shared"...)
	}
	return append(dst, `"}`...)
}

func storeScaleDocumentExtent(rows, dataBytes int) uint64 {
	needed := storeio.PageHeaderSize + storeio.DocumentPagePayloadHeaderSize +
		rows*storeio.DocumentPageRecordSize + dataBytes + storeio.PageTrailerSize
	extent := uint64(4096)
	for extent < uint64(needed) {
		extent <<= 1
	}
	return extent
}

func storeScaleScalar(t *testing.T, src string) Index {
	t.Helper()
	var storage [8]IndexEntry
	index, err := BuildIndex([]byte(src), storage[:])
	if err != nil {
		t.Fatal(err)
	}
	return index
}

func storeScaleMeasure(minimum time.Duration, operation func(int)) (int, time.Duration) {
	runs := 1
	for {
		started := time.Now()
		for i := 0; i < runs; i++ {
			operation(i)
		}
		duration := time.Since(started)
		if duration >= minimum {
			return runs, duration
		}
		if duration == 0 {
			runs *= 100
		} else {
			scale := int(minimum/duration) + 1
			if scale > 100 {
				scale = 100
			}
			runs *= scale
		}
	}
}

func positiveDelta(after, before uint64) uint64 {
	if after <= before {
		return 0
	}
	return after - before
}
