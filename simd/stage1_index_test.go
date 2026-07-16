//go:build goexperiment.simd && arm64

package simd

import (
	"math/bits"
	"math/rand"
	"reflect"
	"testing"
)

func TestStage1IndexBlocksMatchesRecordPipeline(t *testing.T) {
	rng := rand.New(rand.NewSource(0x51a9e1))
	for trial := 0; trial < 200; trial++ {
		blocks := 1 + rng.Intn(67)
		src := make([]byte, blocks*64)
		if _, err := rng.Read(src); err != nil {
			t.Fatal(err)
		}
		// Bias the random stream toward JSON syntax, strings, escapes, and
		// whitespace so every carried state is exercised frequently.
		alphabet := []byte("{}[],:\"\\ truefalsenull0123456789\n\t\rabcdefghijklmnopqrstuvwxyz")
		for i := range src {
			if rng.Intn(4) != 0 {
				src[i] = alphabet[rng.Intn(len(alphabet))]
			}
		}

		var direct, directValid Stage1IndexStream
		var records Stage1Stream
		var previousIn uint64
		var bad bool
		var nonASCII bool
		var hasEscapes bool
		var got, want, gotValid, wantValid []uint32
		for block := 0; block < blocks; {
			count := 1 + rng.Intn(Stage1ChunkBlocks)
			if count > blocks-block {
				count = blocks - block
			}
			out := make([]uint32, count*64+64)
			written := Stage1IndexBlocks(&src[block*64], count, uint32(block*64), &direct, out)
			got = append(got, out[:written]...)
			validOut := make([]uint32, count*64+64)
			var validMeta Stage1ValidMeta
			validWritten := Stage1ValidBlocks(&src[block*64], count, uint32(block*64), &directValid, validOut, &validMeta)
			gotValid = append(gotValid, validOut[:validWritten]...)

			for recordBlock := 0; recordBlock < count; {
				recordCount := count - recordBlock
				if recordCount > Stage1ChunkBlocks {
					recordCount = Stage1ChunkBlocks
				}
				var recs [Stage1ChunkBlocks]Stage1Rec
				Stage1BlocksGP(&src[(block+recordBlock)*64], recordCount, &records, &recs)
				for i := 0; i < recordCount; i++ {
					rec := &recs[i]
					bad = bad || rec.Bad
					nonASCII = nonASCII || rec.NonASCII
					closers := (rec.InStr<<1 | previousIn) &^ rec.InStr
					if rec.EscInStr != 0 {
						hasEscapes = true
					}
					mask := rec.Emit | closers
					base := uint32((block + recordBlock + i) * 64)
					for validMask := rec.Emit; validMask != 0; validMask &= validMask - 1 {
						wantValid = append(wantValid, base+uint32(bits.TrailingZeros64(validMask)))
					}
					for mask != 0 {
						position := bits.TrailingZeros64(mask)
						entry := base + uint32(position)
						want = append(want, entry)
						mask &= mask - 1
					}
					previousIn = rec.InStr >> 63
				}
				recordBlock += recordCount
			}
			block += count
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("trial %d index mismatch\ngot  %v\nwant %v", trial, got, want)
		}
		if !reflect.DeepEqual(gotValid, wantValid) {
			t.Fatalf("trial %d validation stream mismatch\ngot  %v\nwant %v", trial, gotValid, wantValid)
		}
		if direct.Bad != bad || direct.Carry != records.Carry || direct.Follows != records.Follows ||
			direct.PreviousIn != previousIn || direct.NonASCII != nonASCII || direct.Escaped != hasEscapes {
			t.Fatalf("trial %d state mismatch: direct=%+v records=%+v previous=%x escaped=%v bad=%v",
				trial, direct, records, previousIn, hasEscapes, bad)
		}
		if directValid.Bad != bad || directValid.Carry != records.Carry || directValid.Follows != records.Follows ||
			directValid.PreviousIn != previousIn || directValid.NonASCII != nonASCII || directValid.Escaped != hasEscapes {
			t.Fatalf("trial %d validation state mismatch: direct=%+v records=%+v previous=%x escaped=%v bad=%v",
				trial, directValid, records, previousIn, hasEscapes, bad)
		}
	}
}

func TestStage1CursorBlocksOmitsOnlyColons(t *testing.T) {
	rng := rand.New(rand.NewSource(0xc01051))
	alphabet := []byte("{}[],:\"\\ truefalsenull0123456789\n\t\rabcdefghijklmnopqrstuvwxyz")
	for trial := 0; trial < 200; trial++ {
		blocks := 1 + rng.Intn(67)
		src := make([]byte, blocks*64)
		if _, err := rng.Read(src); err != nil {
			t.Fatal(err)
		}
		for i := range src {
			if rng.Intn(4) != 0 {
				src[i] = alphabet[rng.Intn(len(alphabet))]
			}
		}

		var fullState, compactState Stage1IndexStream
		var full, compact []uint32
		for block := 0; block < blocks; {
			count := 1 + rng.Intn(Stage1ChunkBlocks)
			if count > blocks-block {
				count = blocks - block
			}
			fullOut := make([]uint32, count*64+64)
			compactOut := make([]uint32, count*64+64)
			base := uint32(block * 64)
			fullN := Stage1IndexBlocks(&src[block*64], count, base, &fullState, fullOut)
			compactN := Stage1CursorBlocks(&src[block*64], count, base, &compactState, compactOut)
			full = append(full, fullOut[:fullN]...)
			compact = append(compact, compactOut[:compactN]...)
			block += count
		}

		filtered := full[:0]
		for _, position := range full {
			if src[position] != ':' {
				filtered = append(filtered, position)
			}
		}
		if !reflect.DeepEqual(compact, filtered) {
			t.Fatalf("trial %d compact index mismatch\ngot  %v\nwant %v", trial, compact, filtered)
		}
		if compactState != fullState {
			t.Fatalf("trial %d state mismatch: compact=%+v full=%+v", trial, compactState, fullState)
		}
	}
}
