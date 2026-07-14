package simdjson

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// flatNumberArray1024 is the 1024-element flat number array shared by the
// index-iteration benchmarks: "[0,0,...,0]" with 1024 numbers.
func flatNumberArray1024() []byte {
	return []byte("[" + strings.Repeat("0,", 1023) + "0]")
}

// flatObject1024 is the 1024-member flat object shared by the object-cursor
// benchmarks: {"a":0,"a":0,...} with 1024 members.
func flatObject1024() []byte {
	return []byte("{" + strings.Repeat(`"a":0,`, 1023) + `"a":0}`)
}

func BenchmarkValid(b *testing.B) {
	src := benchmarkJSON()
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if !Valid(src) {
			b.Fatal("invalid")
		}
	}
}

func BenchmarkStdlibValid(b *testing.B) {
	src := benchmarkJSON()
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if !json.Valid(src) {
			b.Fatal("invalid")
		}
	}
}

func BenchmarkValidNumber(b *testing.B) {
	src := []byte(`-12.34e+56`)
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if !ValidNumber(src) {
			b.Fatal("invalid")
		}
	}
}

func BenchmarkStdlibValidNumber(b *testing.B) {
	src := []byte(`-12.34e+56`)
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if !json.Valid(src) {
			b.Fatal("invalid")
		}
	}
}

func BenchmarkValidString(b *testing.B) {
	src := []byte(`"plain ascii string"`)
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if !ValidString(src) {
			b.Fatal("invalid")
		}
	}
}

func BenchmarkStdlibValidString(b *testing.B) {
	src := []byte(`"plain ascii string"`)
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if !json.Valid(src) {
			b.Fatal("invalid")
		}
	}
}

func BenchmarkValidSmall(b *testing.B) {
	src := smallJSON()
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if !Valid(src) {
			b.Fatal("invalid")
		}
	}
}

func BenchmarkStdlibValidSmall(b *testing.B) {
	src := smallJSON()
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if !json.Valid(src) {
			b.Fatal("invalid")
		}
	}
}

func BenchmarkValidLongString(b *testing.B) {
	src := longStringJSON()
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if !Valid(src) {
			b.Fatal("invalid")
		}
	}
}

func BenchmarkStdlibValidLongString(b *testing.B) {
	src := longStringJSON()
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if !json.Valid(src) {
			b.Fatal("invalid")
		}
	}
}

func BenchmarkValidLongUnicodeString(b *testing.B) {
	src := longUnicodeStringJSON()
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if !Valid(src) {
			b.Fatal("invalid")
		}
	}
}

func BenchmarkStdlibValidLongUnicodeString(b *testing.B) {
	src := longUnicodeStringJSON()
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if !json.Valid(src) {
			b.Fatal("invalid")
		}
	}
}

func BenchmarkParse(b *testing.B) {
	src := benchmarkJSON()
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := Parse(src); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStdlibUnmarshal(b *testing.B) {
	src := benchmarkJSON()
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var v any
		if err := json.Unmarshal(src, &v); err != nil {
			b.Fatal(err)
		}
		benchmarkSink = v
	}
}

func BenchmarkParseOptionsZeroCopy(b *testing.B) {
	src := benchmarkJSON()
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := parseOptionsZeroCopyForTest(src); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkBuildIndex(b *testing.B) {
	src := benchmarkJSON()
	count, err := RequiredIndexEntries(src)
	if err != nil {
		b.Fatal(err)
	}
	storage := make([]IndexEntry, count)
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		tape, err := BuildIndex(src, storage)
		if err != nil {
			b.Fatal(err)
		}
		indexBenchmarkSink = tape.Len()
	}
}

func BenchmarkBuildIndexPointerCompiled(b *testing.B) {
	src := benchmarkJSON()
	count, err := RequiredIndexEntries(src)
	if err != nil {
		b.Fatal(err)
	}
	storage := make([]IndexEntry, count)
	pointer := MustCompilePointer("/items/2/message")
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		tape, err := BuildIndex(src, storage)
		if err != nil {
			b.Fatal(err)
		}
		value, ok, err := tape.PointerCompiled(pointer)
		if err != nil || !ok {
			b.Fatal("pointer missing")
		}
		indexBenchmarkSink = len(value.Raw().Bytes())
	}
}

func BenchmarkIndexArrayIter4(b *testing.B) {
	src := rawArrayJSON()
	storage := make([]IndexEntry, len(src))
	tape, err := BuildIndex(src, storage)
	if err != nil {
		b.Fatal(err)
	}
	root := tape.Root()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		iter, ok := root.ArrayIter()
		if !ok {
			b.Fatal("root is not array")
		}
		total := 0
		for {
			value, ok := iter.Next()
			if !ok {
				break
			}
			total += int(value.Kind())
		}
		indexBenchmarkSink = total
	}
}

func BenchmarkIndexArrayIter1024(b *testing.B) {
	src := flatNumberArray1024()
	storage := make([]IndexEntry, 1025)
	tape, err := BuildIndex(src, storage)
	if err != nil {
		b.Fatal(err)
	}
	root := tape.Root()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		iter, ok := root.ArrayIter()
		if !ok {
			b.Fatal("root is not array")
		}
		total := 0
		for {
			value, ok := iter.Next()
			if !ok {
				break
			}
			total += int(value.Kind())
		}
		indexBenchmarkSink = total
	}
}

func BenchmarkIndexArrayIterKind1024(b *testing.B) {
	src := flatNumberArray1024()
	storage := make([]IndexEntry, 1025)
	tape, err := BuildIndex(src, storage)
	if err != nil {
		b.Fatal(err)
	}
	root := tape.Root()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		iter, ok := root.ArrayIter()
		if !ok {
			b.Fatal("root is not array")
		}
		total := 0
		for {
			kind, ok := iter.NextKind()
			if !ok {
				break
			}
			total += int(kind)
		}
		indexBenchmarkSink = total
	}
}

func BenchmarkIndexArrayCursorKind1024(b *testing.B) {
	src := flatNumberArray1024()
	storage := make([]IndexEntry, 1025)
	tape, err := BuildIndex(src, storage)
	if err != nil {
		b.Fatal(err)
	}
	root := tape.Root()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		iter, ok := root.ArrayIter()
		if !ok {
			b.Fatal("root is not an array")
		}
		total := 0
		for iter.Valid() {
			total += int(iter.CurrentKind())
			iter = iter.Advance()
		}
		indexBenchmarkSink = total
	}
}

func BenchmarkIndexFlatArrayIterKind1024(b *testing.B) {
	src := flatNumberArray1024()
	storage := make([]IndexEntry, 1025)
	tape, err := BuildIndex(src, storage)
	if err != nil {
		b.Fatal(err)
	}
	root := tape.Root()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		iter, ok := root.FlatArrayIter()
		if !ok {
			b.Fatal("root is not a flat array")
		}
		total := 0
		for {
			kind, ok := iter.NextKind()
			if !ok {
				break
			}
			total += int(kind)
		}
		indexBenchmarkSink = total
	}
}

func BenchmarkIndexFlatArrayCursorKind1024(b *testing.B) {
	src := flatNumberArray1024()
	storage := make([]IndexEntry, 1025)
	tape, err := BuildIndex(src, storage)
	if err != nil {
		b.Fatal(err)
	}
	root := tape.Root()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		iter, ok := root.FlatArrayIter()
		if !ok {
			b.Fatal("root is not a flat array")
		}
		total := 0
		for iter.Valid() {
			total += int(iter.CurrentKind())
			iter = iter.Advance()
		}
		indexBenchmarkSink = total
	}
}

func BenchmarkIndexArrayIterRaw1024(b *testing.B) {
	src := flatNumberArray1024()
	storage := make([]IndexEntry, 1025)
	tape, err := BuildIndex(src, storage)
	if err != nil {
		b.Fatal(err)
	}
	root := tape.Root()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		iter, ok := root.ArrayIter()
		if !ok {
			b.Fatal("root is not array")
		}
		total := 0
		for {
			raw, ok := iter.NextRaw()
			if !ok {
				break
			}
			total += len(raw.Bytes())
		}
		indexBenchmarkSink = total
	}
}

func BenchmarkIndexObjectIter(b *testing.B) {
	src := rawObjectJSON()
	storage := make([]IndexEntry, len(src))
	tape, err := BuildIndex(src, storage)
	if err != nil {
		b.Fatal(err)
	}
	root := tape.Root()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		iter, ok := root.ObjectIter()
		if !ok {
			b.Fatal("root is not object")
		}
		total := 0
		for {
			key, value, ok := iter.Next()
			if !ok {
				break
			}
			total += int(key.Kind()) + int(value.Kind())
		}
		indexBenchmarkSink = total
	}
}

func BenchmarkIndexObjectCursor1024(b *testing.B) {
	src := flatObject1024()
	storage := make([]IndexEntry, 2049)
	tape, err := BuildIndex(src, storage)
	if err != nil {
		b.Fatal(err)
	}
	root := tape.Root()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		iter, ok := root.ObjectIter()
		if !ok {
			b.Fatal("root is not an object")
		}
		total := 0
		for iter.Valid() {
			key, value := iter.Current()
			total += int(key.Kind()) + int(value.Kind())
			iter = iter.Advance()
		}
		indexBenchmarkSink = total
	}
}

func BenchmarkIndexFlatObjectCursor1024(b *testing.B) {
	src := flatObject1024()
	storage := make([]IndexEntry, 2049)
	tape, err := BuildIndex(src, storage)
	if err != nil {
		b.Fatal(err)
	}
	root := tape.Root()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		iter, ok := root.FlatObjectIter()
		if !ok {
			b.Fatal("root is not a flat object")
		}
		total := 0
		for iter.Valid() {
			key, value := iter.Current()
			total += int(key.Kind()) + int(value.Kind())
			iter = iter.Advance()
		}
		indexBenchmarkSink = total
	}
}

func BenchmarkParseOptionsZeroCopySmall(b *testing.B) {
	src := smallJSON()
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := parseOptionsZeroCopyForTest(src); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStdlibUnmarshalSmall(b *testing.B) {
	src := smallJSON()
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var v any
		if err := json.Unmarshal(src, &v); err != nil {
			b.Fatal(err)
		}
		benchmarkSink = v
	}
}

func BenchmarkPointer(b *testing.B) {
	src := benchmarkJSON()
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		v, ok, err := Get(src, "/items/2/message")
		if err != nil || !ok || v.kind != String {
			b.Fatal(v, ok, err)
		}
	}
}

func BenchmarkPointerZeroCopy(b *testing.B) {
	src := benchmarkJSON()
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		v, ok, err := GetOptions(src, "/items/2/message", Options{ZeroCopy: true})
		if err != nil || !ok || v.kind != String {
			b.Fatal(v, ok, err)
		}
	}
}

func BenchmarkGetRaw(b *testing.B) {
	src := benchmarkJSON()
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		raw, ok, err := GetRaw(src, "/items/2/message")
		if err != nil || !ok || raw.Kind() != String {
			b.Fatal(raw, ok, err)
		}
	}
}

func BenchmarkScanRaw(b *testing.B) {
	src := benchmarkJSON()
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		raw, ok, err := ScanRaw(src, "/items/2/message")
		if err != nil || !ok || raw.Kind() != String {
			b.Fatal(raw, ok, err)
		}
	}
}

func BenchmarkGetRawCompiled(b *testing.B) {
	src := benchmarkJSON()
	ptr := MustCompilePointer("/items/2/message")
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		raw, ok, err := ptr.GetRaw(src)
		if err != nil || !ok || raw.Kind() != String {
			b.Fatal(raw, ok, err)
		}
	}
}

func BenchmarkScanRawCompiled(b *testing.B) {
	src := benchmarkJSON()
	ptr := MustCompilePointer("/items/2/message")
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		raw, ok, err := ptr.ScanRaw(src)
		if err != nil || !ok || raw.Kind() != String {
			b.Fatal(raw, ok, err)
		}
	}
}

func BenchmarkGetRawEarly(b *testing.B) {
	src := benchmarkJSON()
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		raw, ok, err := GetRaw(src, "/items/0/id")
		if err != nil || !ok || raw.Kind() != Number {
			b.Fatal(raw, ok, err)
		}
	}
}

func BenchmarkScanRawEarly(b *testing.B) {
	src := benchmarkJSON()
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		raw, ok, err := ScanRaw(src, "/items/0/id")
		if err != nil || !ok || raw.Kind() != Number {
			b.Fatal(raw, ok, err)
		}
	}
}

func BenchmarkScanRawEarlyCompiled(b *testing.B) {
	src := benchmarkJSON()
	ptr := MustCompilePointer("/items/0/id")
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		raw, ok, err := ptr.ScanRaw(src)
		if err != nil || !ok || raw.Kind() != Number {
			b.Fatal(raw, ok, err)
		}
	}
}

func BenchmarkGetRawLongString(b *testing.B) {
	src := longStringJSON()
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		raw, ok, err := GetRaw(src, "/s")
		if err != nil || !ok || raw.Kind() != String {
			b.Fatal(raw, ok, err)
		}
	}
}

func BenchmarkScanRawLongString(b *testing.B) {
	src := longStringJSON()
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		raw, ok, err := ScanRaw(src, "/s")
		if err != nil || !ok || raw.Kind() != String {
			b.Fatal(raw, ok, err)
		}
	}
}

func BenchmarkScanRawLongStringCompiled(b *testing.B) {
	src := longStringJSON()
	ptr := MustCompilePointer("/s")
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		raw, ok, err := ptr.ScanRaw(src)
		if err != nil || !ok || raw.Kind() != String {
			b.Fatal(raw, ok, err)
		}
	}
}

func BenchmarkAppendCompact(b *testing.B) {
	src := benchmarkJSON()
	dst := make([]byte, 0, len(src))
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		out, err := AppendCompact(dst[:0], src)
		if err != nil {
			b.Fatal(err)
		}
		dst = out[:0]
	}
}

func BenchmarkStdlibCompact(b *testing.B) {
	src := benchmarkJSON()
	var dst bytes.Buffer
	dst.Grow(len(src))
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		dst.Reset()
		if err := json.Compact(&dst, src); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEachArrayRaw(b *testing.B) {
	src := rawArrayJSON()
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	total := 0
	for i := 0; i < b.N; i++ {
		if err := EachArray(src, func(_ int, value RawValue) error {
			total += len(value.Bytes())
			return nil
		}); err != nil {
			b.Fatal(err)
		}
	}
	benchmarkSink = total
}

func BenchmarkStdlibArrayRawMessages(b *testing.B) {
	src := rawArrayJSON()
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	var total int
	for i := 0; i < b.N; i++ {
		var values []json.RawMessage
		if err := json.Unmarshal(src, &values); err != nil {
			b.Fatal(err)
		}
		for _, value := range values {
			total += len(value)
		}
	}
	benchmarkSink = total
}

func BenchmarkEachObjectRaw(b *testing.B) {
	src := rawObjectJSON()
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	total := 0
	for i := 0; i < b.N; i++ {
		if err := EachObject(src, func(key string, value RawValue) error {
			total += len(key) + len(value.Bytes())
			return nil
		}); err != nil {
			b.Fatal(err)
		}
	}
	benchmarkSink = total
}

func BenchmarkStdlibObjectRawMessages(b *testing.B) {
	src := rawObjectJSON()
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	var total int
	for i := 0; i < b.N; i++ {
		var values map[string]json.RawMessage
		if err := json.Unmarshal(src, &values); err != nil {
			b.Fatal(err)
		}
		for key, value := range values {
			total += len(key) + len(value)
		}
	}
	benchmarkSink = total
}
