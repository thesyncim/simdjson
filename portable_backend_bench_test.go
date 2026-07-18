package simdjson

import (
	"strconv"
	"strings"
	"testing"
)

func portableBackendJSON() []byte {
	var b strings.Builder
	b.Grow(512 << 10)
	b.WriteString("{\n")
	for i := 0; i < 6000; i++ {
		b.WriteString("  \"field")
		b.WriteString(strconv.Itoa(i))
		b.WriteString("\": \"abcdefghijklmnopqrstuvwxyz0123456789abcdefghijklmnopqrstuvwxyz\"")
		if i != 5999 {
			b.WriteByte(',')
		}
		b.WriteByte('\n')
	}
	b.WriteByte('}')
	return []byte(b.String())
}

func BenchmarkPortableBackendValid(b *testing.B) {
	src := portableBackendJSON()
	b.Run("recursive", func(b *testing.B) {
		old := stage1ValidatorEnabled
		stage1ValidatorEnabled = false
		defer func() { stage1ValidatorEnabled = old }()
		b.SetBytes(int64(len(src)))
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if !Valid(src) {
				b.Fatal("invalid")
			}
		}
	})
	b.Run("portable-stage12", func(b *testing.B) {
		old := stage1ValidatorEnabled
		stage1ValidatorEnabled = true
		defer func() { stage1ValidatorEnabled = old }()
		b.SetBytes(int64(len(src)))
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if !Valid(src) {
				b.Fatal("invalid")
			}
		}
	})
}

func BenchmarkPortableBackendBuildIndex(b *testing.B) {
	src := portableBackendJSON()
	count, err := RequiredIndexEntries(src)
	if err != nil {
		b.Fatal(err)
	}
	storage := make([]IndexEntry, count)
	b.Run("tape-builder", func(b *testing.B) {
		old := stage2IndexPositionEnabled
		stage2IndexPositionEnabled = false
		defer func() { stage2IndexPositionEnabled = old }()
		b.SetBytes(int64(len(src)))
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			index, err := BuildIndex(src, storage)
			if err != nil {
				b.Fatal(err)
			}
			indexBenchmarkSink = index.Len()
		}
	})
	b.Run("portable-stage12", func(b *testing.B) {
		old := stage2IndexPositionEnabled
		stage2IndexPositionEnabled = true
		defer func() { stage2IndexPositionEnabled = old }()
		b.SetBytes(int64(len(src)))
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			index, err := BuildIndex(src, storage)
			if err != nil {
				b.Fatal(err)
			}
			indexBenchmarkSink = index.Len()
		}
	})
}
