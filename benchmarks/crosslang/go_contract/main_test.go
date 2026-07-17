package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/thesyncim/simdjson"
)

func hashBytesReference(hash uint64, value []byte) uint64 {
	hash = hashUint64(hash, uint64(len(value)))
	for _, b := range value {
		hash = hashByte(hash, b)
	}
	return hash
}

func hashStringReference(hash uint64, value string) uint64 {
	hash = hashUint64(hash, uint64(len(value)))
	for i := range len(value) {
		hash = hashByte(hash, value[i])
	}
	return hash
}

func TestOptimizedHashMatchesReference(t *testing.T) {
	// BuildIndex rejects documents at 2^32 bytes, so these boundaries cover
	// every non-zero length byte reachable by the benchmark contract.
	for _, size := range []int{0, 1, 7, 8, 9, 254, 255, 256, 257, 65534, 65535, 65536, 65537, 1<<24 - 1, 1 << 24} {
		value := make([]byte, size)
		for i := range value {
			value[i] = byte(i*131 + 17)
		}
		for _, seed := range []uint64{0, 14695981039346656037, ^uint64(0)} {
			if got, want := hashBytes(seed, value), hashBytesReference(seed, value); got != want {
				t.Fatalf("hashBytes(seed=%#x, len=%d) = %#x, want %#x", seed, size, got, want)
			}
			text := string(value)
			if got, want := hashString(seed, text), hashStringReference(seed, text); got != want {
				t.Fatalf("hashString(seed=%#x, len=%d) = %#x, want %#x", seed, size, got, want)
			}
		}
	}
}

func BenchmarkCanadaContract(b *testing.B) {
	dir := os.Getenv("SIMDJSON_CORPUS")
	if dir == "" {
		b.Skip("set SIMDJSON_CORPUS to the cross-language corpus directory")
	}
	src, err := os.ReadFile(filepath.Join(dir, "canada_geometry.json"))
	if err != nil {
		b.Fatal(err)
	}
	entries, err := simdjson.RequiredIndexEntries(src)
	if err != nil {
		b.Fatal(err)
	}
	storage := make([]simdjson.IndexEntry, entries)
	textScratch := make([]byte, 0, 256)
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		index, err := simdjson.BuildIndex(src, storage)
		if err != nil {
			b.Fatal(err)
		}
		digestSink = semanticDigest(index.Root(), &textScratch)
	}
}
