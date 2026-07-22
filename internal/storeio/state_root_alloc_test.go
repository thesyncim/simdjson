package storeio

import "testing"

var benchmarkStateRoot StateRoot

func TestStateRootCodecSteadyAllocation(t *testing.T) {
	root, fileEnd := testStateRoot(9)
	page := make([]byte, testSuperblockPageSize)
	if _, err := EncodeStateRootPage(page, root, fileEnd); err != nil {
		t.Fatal(err)
	}
	if allocs := testing.AllocsPerRun(1000, func() {
		if _, err := EncodeStateRootPage(page, root, fileEnd); err != nil {
			panic(err)
		}
		if _, err := DecodeStateRootPage(page, fileEnd); err != nil {
			panic(err)
		}
	}); allocs != 0 {
		t.Fatalf("state-root codec allocations = %g, want 0", allocs)
	}
}

func BenchmarkStateRootCodec(b *testing.B) {
	root, fileEnd := testStateRoot(9)
	page := make([]byte, testSuperblockPageSize)
	if _, err := EncodeStateRootPage(page, root, fileEnd); err != nil {
		b.Fatal(err)
	}
	b.Run("encode-4KiB", func(b *testing.B) {
		b.SetBytes(int64(len(page)))
		b.ReportAllocs()
		for range b.N {
			if _, err := EncodeStateRootPage(page, root, fileEnd); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("decode-4KiB", func(b *testing.B) {
		b.SetBytes(int64(len(page)))
		b.ReportAllocs()
		for range b.N {
			decoded, err := DecodeStateRootPage(page, fileEnd)
			if err != nil {
				b.Fatal(err)
			}
			benchmarkStateRoot = decoded
		}
	})
}
