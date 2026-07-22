package storeio

import "testing"

var benchmarkDocumentRecord DocumentRecord
var benchmarkDocumentJSON []byte

func testBenchmarkDocumentRows() [64]DocumentRecord {
	var rows [64]DocumentRecord
	for slot := range rows {
		rows[slot] = DocumentRecord{
			Slot: uint8(slot),
			Key:  []byte("session:00"),
			JSON: []byte(`{"state":"open"}`),
		}
	}
	return rows
}

func TestDocumentPageSteadyAllocation(t *testing.T) {
	header := testDocumentPageHeader(^uint64(0))
	rows := testBenchmarkDocumentRows()
	page := make([]byte, testSuperblockPageSize)
	if _, err := EncodeDocumentPage(page, header, rows[:], testDocumentNextLogicalID); err != nil {
		t.Fatal(err)
	}
	view, err := OpenDocumentPage(page, header.ChunkID+1, testDocumentNextLogicalID)
	if err != nil {
		t.Fatal(err)
	}
	if allocs := testing.AllocsPerRun(1000, func() {
		if _, err := EncodeDocumentPage(page, header, rows[:], testDocumentNextLogicalID); err != nil {
			panic(err)
		}
		opened, err := OpenDocumentPage(page, header.ChunkID+1, testDocumentNextLogicalID)
		if err != nil {
			panic(err)
		}
		if _, ok := opened.Lookup(63); !ok {
			panic("document lookup miss")
		}
		if _, ok := opened.LookupKey(63, rows[63].Key); !ok {
			panic("document key lookup miss")
		}
		if _, ok := opened.LookupString(63, "session:00"); !ok {
			panic("document string-key lookup miss")
		}
		if _, ok := view.RecordAt(63); !ok {
			panic("document rank miss")
		}
	}); allocs != 0 {
		t.Fatalf("document-page codec and lookup allocations = %g, want 0", allocs)
	}
}

func TestDocumentPageOverflowSteadyAllocation(t *testing.T) {
	header := testDocumentPageHeader(1)
	fileEnd := uint64(32 * testSuperblockPageSize)
	rows := []DocumentRecord{{
		Slot: 0, Key: []byte("large"), Overflow: testOverflowRef(10, 20, header.Generation), JSONLength: 1 << 20,
	}}
	page := make([]byte, testSuperblockPageSize)
	if allocs := testing.AllocsPerRun(1000, func() {
		if _, err := EncodeDocumentPageWithOverflow(page, header, rows, testDocumentNextLogicalID, fileEnd, testSuperblockPageSize); err != nil {
			panic(err)
		}
		view, err := OpenDocumentPageWithOverflow(page, header.ChunkID+1, testDocumentNextLogicalID, fileEnd, testSuperblockPageSize)
		if err != nil {
			panic(err)
		}
		value, ok := view.LookupStringValue(0, "large")
		if !ok || value.Overflow != rows[0].Overflow || value.Length != rows[0].JSONLength {
			panic("overflow lookup")
		}
	}); allocs != 0 {
		t.Fatalf("document overflow codec allocations = %g, want 0", allocs)
	}
}

func BenchmarkDocumentPage(b *testing.B) {
	header := testDocumentPageHeader(^uint64(0))
	rows := testBenchmarkDocumentRows()
	page := make([]byte, testSuperblockPageSize)
	if _, err := EncodeDocumentPage(page, header, rows[:], testDocumentNextLogicalID); err != nil {
		b.Fatal(err)
	}
	view, err := OpenDocumentPage(page, header.ChunkID+1, testDocumentNextLogicalID)
	if err != nil {
		b.Fatal(err)
	}
	b.Run("encode-64-row-4KiB", func(b *testing.B) {
		b.SetBytes(int64(len(page)))
		b.ReportAllocs()
		for range b.N {
			if _, err := EncodeDocumentPage(page, header, rows[:], testDocumentNextLogicalID); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("open-64-row-4KiB", func(b *testing.B) {
		b.SetBytes(int64(len(page)))
		b.ReportAllocs()
		for range b.N {
			opened, err := OpenDocumentPage(page, header.ChunkID+1, testDocumentNextLogicalID)
			if err != nil {
				b.Fatal(err)
			}
			benchmarkDocumentRecord, _ = opened.Lookup(63)
		}
	})
	b.Run("lookup-hit", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			benchmarkDocumentRecord, _ = view.Lookup(63)
		}
	})
	b.Run("lookup-json", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			benchmarkDocumentJSON, _ = view.LookupJSON(63)
		}
	})
	b.Run("lookup-key-verified", func(b *testing.B) {
		b.ReportAllocs()
		key := rows[63].Key
		for range b.N {
			benchmarkDocumentJSON, _ = view.LookupKey(63, key)
		}
	})
	b.Run("lookup-string-verified", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			benchmarkDocumentJSON, _ = view.LookupString(63, "session:00")
		}
	})
	b.Run("lookup-miss", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			benchmarkDocumentJSON, _ = view.LookupJSON(64)
		}
	})
}
