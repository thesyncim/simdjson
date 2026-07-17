package simdjson

import "testing"

func BenchmarkCodecMarshalSmall(b *testing.B) {
	value := benchSmall{ID: 1, OK: true, Name: "sim"}
	codec, err := CompileCodec[benchSmall](CodecOptions{})
	if err != nil {
		b.Fatal(err)
	}
	out, err := codec.Marshal(&value)
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(out)))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := codec.Marshal(&value); err != nil {
			b.Fatal(err)
		}
	}
}
