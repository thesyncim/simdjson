package storeio

import (
	"encoding/binary"
	"errors"
	"math"
	"testing"
)

const (
	testFloat64ScanFileEnd      = uint64(64 * testSuperblockPageSize)
	testFloat64ScanNextLogical  = uint64(32)
	testFloat64ScanChunkHigh    = uint32(64)
	testFloat64ScanCatalogID    = uint64(9)
	testFloat64ScanStripeID     = uint64(11)
	testFloat64ScanCatalogCount = 2
)

func testFloat64ScanRef(offset, logical uint64, kind PageKind) PageRef {
	return PageRef{
		Offset: offset * uint64(testSuperblockPageSize), LogicalID: logical,
		Generation: 3, Length: testSuperblockPageSize, Kind: kind,
	}
}

func TestFloat64CatalogExactRoundTripAndAllocs(t *testing.T) {
	refs := [testFloat64ScanCatalogCount]PageRef{
		testFloat64ScanRef(10, testFloat64ScanStripeID, PageFloat64Stripe),
		testFloat64ScanRef(11, testFloat64ScanStripeID+1, PageFloat64Stripe),
	}
	next := testFloat64ScanRef(12, testFloat64ScanStripeID+2, PageFloat64Catalog)
	header := Float64CatalogHeader{
		StoreID: testStoreID, Generation: 3, LogicalID: testFloat64ScanCatalogID,
		PageSize: testSuperblockPageSize, Next: next,
	}
	page := make([]byte, testSuperblockPageSize)
	page, err := EncodeFloat64Catalog(
		page, header, refs[:], testFloat64ScanFileEnd, testFloat64ScanNextLogical,
		testSuperblockPageSize,
	)
	if err != nil {
		t.Fatal(err)
	}
	view, err := OpenFloat64Catalog(
		page, testFloat64ScanFileEnd, testFloat64ScanNextLogical, testSuperblockPageSize,
	)
	if err != nil {
		t.Fatal(err)
	}
	if view.Header() != header || view.Len() != len(refs) {
		t.Fatalf("catalog = (%+v,%d), want (%+v,%d)", view.Header(), view.Len(), header, len(refs))
	}
	for index, want := range refs {
		got, ok := view.RefAt(index)
		if !ok || got != want {
			t.Fatalf("ref %d = (%+v,%v), want %+v", index, got, ok, want)
		}
	}
	if _, ok := view.RefAt(-1); ok {
		t.Fatal("negative catalog index accepted")
	}
	if _, ok := view.RefAt(len(refs)); ok {
		t.Fatal("past-end catalog index accepted")
	}
	if allocs := testing.AllocsPerRun(100, func() {
		if _, encodeErr := EncodeFloat64Catalog(
			page, header, refs[:], testFloat64ScanFileEnd, testFloat64ScanNextLogical,
			testSuperblockPageSize,
		); encodeErr != nil {
			panic(encodeErr)
		}
		opened, openErr := OpenFloat64Catalog(
			page, testFloat64ScanFileEnd, testFloat64ScanNextLogical,
			testSuperblockPageSize,
		)
		if openErr != nil || opened.Len() != len(refs) {
			panic("catalog open")
		}
	}); allocs != 0 {
		t.Fatalf("catalog warm allocations = %.2f, want zero", allocs)
	}
}

func TestFloat64CatalogRejectsResealedCorruption(t *testing.T) {
	refs := [testFloat64ScanCatalogCount]PageRef{
		testFloat64ScanRef(10, testFloat64ScanStripeID, PageFloat64Stripe),
		testFloat64ScanRef(11, testFloat64ScanStripeID+1, PageFloat64Stripe),
	}
	page := make([]byte, testSuperblockPageSize)
	page, err := EncodeFloat64Catalog(page, Float64CatalogHeader{
		StoreID: testStoreID, Generation: 3, LogicalID: testFloat64ScanCatalogID,
		PageSize: testSuperblockPageSize,
	}, refs[:], testFloat64ScanFileEnd, testFloat64ScanNextLogical, testSuperblockPageSize)
	if err != nil {
		t.Fatal(err)
	}
	firstRef := PageHeaderSize + Float64CatalogPayloadHeaderSize
	secondRef := firstRef + PageRefSize
	tests := []struct {
		name   string
		mutate func([]byte)
	}{
		{
			name: "count",
			mutate: func(corrupt []byte) {
				binary.LittleEndian.PutUint16(corrupt[PageHeaderSize+4:], 3)
			},
		},
		{
			name: "physical order",
			mutate: func(corrupt []byte) {
				binary.LittleEndian.PutUint64(
					corrupt[secondRef:], binary.LittleEndian.Uint64(corrupt[firstRef:]),
				)
			},
		},
		{
			name: "logical order",
			mutate: func(corrupt []byte) {
				binary.LittleEndian.PutUint64(
					corrupt[secondRef+8:], binary.LittleEndian.Uint64(corrupt[firstRef+8:]),
				)
			},
		},
		{
			name: "kind",
			mutate: func(corrupt []byte) {
				corrupt[firstRef+28] = byte(PageDocument)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			corrupt := append([]byte(nil), page...)
			test.mutate(corrupt)
			resealTestPage(corrupt)
			if _, openErr := OpenFloat64Catalog(
				corrupt, testFloat64ScanFileEnd, testFloat64ScanNextLogical,
				testSuperblockPageSize,
			); !errors.Is(openErr, ErrFloat64CatalogCorrupt) {
				t.Fatalf("corruption = %v, want %v", openErr, ErrFloat64CatalogCorrupt)
			}
		})
	}
}

func encodeTestFloat64Stripe(t *testing.T) ([]byte, []Float64StripeColumn) {
	t.Helper()
	u16 := make([]byte, 4)
	binary.LittleEndian.PutUint16(u16[0:2], 300)
	binary.LittleEndian.PutUint16(u16[2:4], 600)
	u32 := make([]byte, 4)
	binary.LittleEndian.PutUint32(u32, 70000)
	f64 := make([]byte, 16)
	binary.LittleEndian.PutUint64(f64[0:8], math.Float64bits(-1.25))
	binary.LittleEndian.PutUint64(f64[8:16], math.Float64bits(3.5))
	columns := []Float64StripeColumn{
		{Encoding: Float64GroupUint8, Values: []byte{1, 2, 255}},
		{Encoding: Float64GroupUint16, Values: u16},
		{Encoding: Float64GroupUint32, Values: u32},
		{Encoding: Float64GroupFloat64LE, Values: f64},
		{Encoding: Float64GroupUint8},
	}
	page := make([]byte, testSuperblockPageSize)
	page, err := EncodeFloat64Stripe(page, Float64StripeHeader{
		StoreID: testStoreID, Generation: 3, LogicalID: testFloat64ScanStripeID,
		PageSize: testSuperblockPageSize, FirstChunk: 7, ChunkCount: 4,
		RowCount: 19, ColumnCount: uint16(len(columns)),
	}, columns, testFloat64ScanNextLogical)
	if err != nil {
		t.Fatal(err)
	}
	return page, columns
}

func TestFloat64StripeExactAdaptiveRoundTripAndAllocs(t *testing.T) {
	page, columns := encodeTestFloat64Stripe(t)
	view, err := OpenFloat64Stripe(page, testFloat64ScanChunkHigh, testFloat64ScanNextLogical)
	if err != nil {
		t.Fatal(err)
	}
	header := view.Header()
	if header.FirstChunk != 7 || header.ChunkCount != 4 || header.RowCount != 19 ||
		int(header.ColumnCount) != len(columns) {
		t.Fatalf("stripe header = %+v", header)
	}
	for ordinal, want := range columns {
		values, encoding, ok := view.ColumnValues(ordinal)
		if !ok || encoding != want.Encoding || len(values) != len(want.Values) {
			t.Fatalf(
				"column %d = (%x,%v,%v), want (%x,%v,true)",
				ordinal, values, encoding, ok, want.Values, want.Encoding,
			)
		}
		for index := range values {
			if values[index] != want.Values[index] {
				t.Fatalf("column %d byte %d = %x, want %x", ordinal, index, values, want.Values)
			}
		}
	}
	if _, _, ok := view.ColumnValues(-1); ok {
		t.Fatal("negative stripe column accepted")
	}
	if _, _, ok := view.ColumnValues(len(columns)); ok {
		t.Fatal("past-end stripe column accepted")
	}
	reuse := make([]byte, testSuperblockPageSize)
	if allocs := testing.AllocsPerRun(100, func() {
		if _, encodeErr := EncodeFloat64Stripe(reuse, header, columns, testFloat64ScanNextLogical); encodeErr != nil {
			panic(encodeErr)
		}
		opened, openErr := OpenFloat64Stripe(
			reuse, testFloat64ScanChunkHigh, testFloat64ScanNextLogical,
		)
		if openErr != nil || opened.Header().RowCount != header.RowCount {
			panic("stripe open")
		}
	}); allocs != 0 {
		t.Fatalf("stripe warm allocations = %.2f, want zero", allocs)
	}
}

func TestFloat64StripeRejectsResealedCorruption(t *testing.T) {
	page, columns := encodeTestFloat64Stripe(t)
	directoryStart := PageHeaderSize + Float64StripePayloadHeaderSize
	dataStart := directoryStart + len(columns)*Float64StripeColumnSize
	f64Start := dataStart + len(columns[0].Values) + len(columns[1].Values) + len(columns[2].Values)
	emptyEntry := directoryStart + 4*Float64StripeColumnSize
	tests := []struct {
		name   string
		mutate func([]byte)
	}{
		{
			name: "row count",
			mutate: func(corrupt []byte) {
				binary.LittleEndian.PutUint32(corrupt[PageHeaderSize+12:], 0)
			},
		},
		{
			name: "column count exceeds rows",
			mutate: func(corrupt []byte) {
				binary.LittleEndian.PutUint32(corrupt[PageHeaderSize+12:], 2)
			},
		},
		{
			name: "column end",
			mutate: func(corrupt []byte) {
				binary.LittleEndian.PutUint32(corrupt[directoryStart:], math.MaxUint32)
			},
		},
		{
			name: "empty invalid encoding",
			mutate: func(corrupt []byte) {
				corrupt[emptyEntry+8] = 0xff
			},
		},
		{
			name: "non-finite float64",
			mutate: func(corrupt []byte) {
				binary.LittleEndian.PutUint64(corrupt[f64Start:], math.Float64bits(math.Inf(1)))
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			corrupt := append([]byte(nil), page...)
			test.mutate(corrupt)
			resealTestPage(corrupt)
			if _, openErr := OpenFloat64Stripe(
				corrupt, testFloat64ScanChunkHigh, testFloat64ScanNextLogical,
			); !errors.Is(openErr, ErrFloat64StripeCorrupt) {
				t.Fatalf("corruption = %v, want %v", openErr, ErrFloat64StripeCorrupt)
			}
		})
	}
}
