package storeio

import "testing"

var benchmarkPageChecksum uint32

func TestSuperblockCodecSteadyAllocation(t *testing.T) {
	state := []byte("allocation-free state root")
	root := testSuperblock(1, 2*uint64(testSuperblockPageSize), state)
	var first, second [SuperblockSize]byte
	if _, err := EncodeSuperblock(first[:], root); err != nil {
		t.Fatal(err)
	}
	root.Generation = 2
	root.StateOffset += uint64(root.PageSize)
	root.FileEnd += uint64(root.PageSize)
	if _, err := EncodeSuperblock(second[:], root); err != nil {
		t.Fatal(err)
	}
	if allocs := testing.AllocsPerRun(1000, func() {
		if _, err := EncodeSuperblock(second[:], root); err != nil {
			panic(err)
		}
		if _, err := DecodeSuperblock(second[:]); err != nil {
			panic(err)
		}
		if _, _, err := SelectSuperblock(first[:], second[:]); err != nil {
			panic(err)
		}
		_ = PageChecksum(state)
	}); allocs != 0 {
		t.Fatalf("superblock codec allocations = %g, want 0", allocs)
	}
}

func BenchmarkPageChecksum(b *testing.B) {
	for _, test := range []struct {
		name string
		size int
	}{
		{"root-record", SuperblockSize - 8},
		{"page-4KiB", 4 << 10},
		{"page-64KiB", 64 << 10},
	} {
		data := make([]byte, test.size)
		b.Run(test.name, func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			b.ReportAllocs()
			var checksum uint32
			for range b.N {
				checksum = PageChecksum(data)
			}
			benchmarkPageChecksum = checksum
		})
	}
}
