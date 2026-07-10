//go:build goexperiment.jsonv2

package benchmarks

import (
	jsonv2 "encoding/json/v2"
	"testing"
)

func BenchmarkParseTypedJSONV2(b *testing.B) {
	for _, fixture := range fixtures {
		b.Run(fixture.name, func(b *testing.B) {
			b.SetBytes(int64(len(fixture.data)))
			b.ReportAllocs()
			if fixture.name == "small" {
				for range b.N {
					var dst TypedSmall
					if err := jsonv2.Unmarshal(fixture.data, &dst); err != nil {
						b.Fatal(err)
					}
					typedSmallSink = dst
				}
				return
			}
			for range b.N {
				var dst TypedDocument
				if err := jsonv2.Unmarshal(fixture.data, &dst); err != nil {
					b.Fatal(err)
				}
				typedDocumentSink = dst
			}
		})
	}
}

func BenchmarkParseTypedJSONV2Reused(b *testing.B) {
	for _, fixture := range fixtures[1:] {
		b.Run(fixture.name, func(b *testing.B) {
			dst := TypedDocument{Items: make([]TypedRecord, 0, 1024)}
			b.SetBytes(int64(len(fixture.data)))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if err := jsonv2.Unmarshal(fixture.data, &dst); err != nil {
					b.Fatal(err)
				}
				typedDocumentSink = dst
			}
		})
	}
}
