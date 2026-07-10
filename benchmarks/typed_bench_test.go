package benchmarks

import (
	stdjson "encoding/json"
	"testing"

	goccyjson "github.com/goccy/go-json"
	jsoniter "github.com/json-iterator/go"
	segmentjson "github.com/segmentio/encoding/json"
	"github.com/thesyncim/simdjson"
	"github.com/thesyncim/simdjson/benchmarks/easyjsonmodel"
)

var (
	typedBenchmarkOptions = simdjson.DecoderOptions{ZeroCopy: true, CaseSensitive: true}
	typedOwnedOptions     = simdjson.DecoderOptions{CaseSensitive: true}
)

var (
	typedSmallSink    TypedSmall
	typedDocumentSink TypedDocument
	easySmallSink     easyjsonmodel.TypedSmall
	easyDocumentSink  easyjsonmodel.TypedDocument
	typedSmallDecoder = mustTypedDecoder[TypedSmall](typedBenchmarkOptions)
	typedDocDecoder   = mustTypedDecoder[TypedDocument](typedBenchmarkOptions)
	typedSmallOwned   = mustTypedDecoder[TypedSmall](typedOwnedOptions)
	typedDocOwned     = mustTypedDecoder[TypedDocument](typedOwnedOptions)
)

func mustTypedDecoder[T any](opts simdjson.DecoderOptions) simdjson.Decoder[T] {
	decoder, err := simdjson.CompileDecoder[T](opts)
	if err != nil {
		panic(err)
	}
	return decoder
}

func BenchmarkParseTyped(b *testing.B) {
	for _, fixture := range fixtures {
		b.Run(fixture.name, func(b *testing.B) {
			if fixture.name == "small" {
				benchmarkTypedSmall(b, fixture.data)
				return
			}
			benchmarkTypedDocument(b, fixture.data)
		})
	}
}

func benchmarkTypedSmall(b *testing.B, src []byte) {
	b.Run("stdlib", func(b *testing.B) {
		b.SetBytes(int64(len(src)))
		b.ReportAllocs()
		for range b.N {
			var dst TypedSmall
			if err := stdjson.Unmarshal(src, &dst); err != nil {
				b.Fatal(err)
			}
			typedSmallSink = dst
		}
	})
	b.Run("go-json", func(b *testing.B) {
		b.SetBytes(int64(len(src)))
		b.ReportAllocs()
		for range b.N {
			var dst TypedSmall
			if err := goccyjson.Unmarshal(src, &dst); err != nil {
				b.Fatal(err)
			}
			typedSmallSink = dst
		}
	})
	b.Run("Segment", func(b *testing.B) {
		b.SetBytes(int64(len(src)))
		b.ReportAllocs()
		for range b.N {
			var dst TypedSmall
			if err := segmentjson.Unmarshal(src, &dst); err != nil {
				b.Fatal(err)
			}
			typedSmallSink = dst
		}
	})
	b.Run("jsoniter", func(b *testing.B) {
		b.SetBytes(int64(len(src)))
		b.ReportAllocs()
		for range b.N {
			var dst TypedSmall
			if err := jsoniter.Unmarshal(src, &dst); err != nil {
				b.Fatal(err)
			}
			typedSmallSink = dst
		}
	})
	b.Run("easyjson-generated", func(b *testing.B) {
		b.SetBytes(int64(len(src)))
		b.ReportAllocs()
		for range b.N {
			var dst easyjsonmodel.TypedSmall
			if err := dst.UnmarshalJSON(src); err != nil {
				b.Fatal(err)
			}
			easySmallSink = dst
		}
	})
	b.Run("simdjson-Compiled-zero-copy", func(b *testing.B) {
		b.SetBytes(int64(len(src)))
		b.ReportAllocs()
		for range b.N {
			var dst TypedSmall
			if err := typedSmallDecoder.Decode(src, &dst); err != nil {
				b.Fatal(err)
			}
			typedSmallSink = dst
		}
	})
	b.Run("simdjson-Compiled-owned", func(b *testing.B) {
		b.SetBytes(int64(len(src)))
		b.ReportAllocs()
		for range b.N {
			var dst TypedSmall
			if err := typedSmallOwned.Decode(src, &dst); err != nil {
				b.Fatal(err)
			}
			typedSmallSink = dst
		}
	})
}

func benchmarkTypedDocument(b *testing.B, src []byte) {
	b.Run("stdlib", func(b *testing.B) {
		b.SetBytes(int64(len(src)))
		b.ReportAllocs()
		for range b.N {
			var dst TypedDocument
			if err := stdjson.Unmarshal(src, &dst); err != nil {
				b.Fatal(err)
			}
			typedDocumentSink = dst
		}
	})
	b.Run("go-json", func(b *testing.B) {
		b.SetBytes(int64(len(src)))
		b.ReportAllocs()
		for range b.N {
			var dst TypedDocument
			if err := goccyjson.Unmarshal(src, &dst); err != nil {
				b.Fatal(err)
			}
			typedDocumentSink = dst
		}
	})
	b.Run("Segment", func(b *testing.B) {
		b.SetBytes(int64(len(src)))
		b.ReportAllocs()
		for range b.N {
			var dst TypedDocument
			if err := segmentjson.Unmarshal(src, &dst); err != nil {
				b.Fatal(err)
			}
			typedDocumentSink = dst
		}
	})
	b.Run("jsoniter", func(b *testing.B) {
		b.SetBytes(int64(len(src)))
		b.ReportAllocs()
		for range b.N {
			var dst TypedDocument
			if err := jsoniter.Unmarshal(src, &dst); err != nil {
				b.Fatal(err)
			}
			typedDocumentSink = dst
		}
	})
	b.Run("easyjson-generated", func(b *testing.B) {
		b.SetBytes(int64(len(src)))
		b.ReportAllocs()
		for range b.N {
			var dst easyjsonmodel.TypedDocument
			if err := dst.UnmarshalJSON(src); err != nil {
				b.Fatal(err)
			}
			easyDocumentSink = dst
		}
	})
	b.Run("simdjson-Compiled-zero-copy", func(b *testing.B) {
		b.SetBytes(int64(len(src)))
		b.ReportAllocs()
		for range b.N {
			var dst TypedDocument
			if err := typedDocDecoder.Decode(src, &dst); err != nil {
				b.Fatal(err)
			}
			typedDocumentSink = dst
		}
	})
	b.Run("simdjson-Compiled-owned", func(b *testing.B) {
		b.SetBytes(int64(len(src)))
		b.ReportAllocs()
		for range b.N {
			var dst TypedDocument
			if err := typedDocOwned.Decode(src, &dst); err != nil {
				b.Fatal(err)
			}
			typedDocumentSink = dst
		}
	})
	b.Run("stdlib-reused", func(b *testing.B) {
		dst := TypedDocument{Items: make([]TypedRecord, 0, 1024)}
		b.SetBytes(int64(len(src)))
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			if err := stdjson.Unmarshal(src, &dst); err != nil {
				b.Fatal(err)
			}
			typedDocumentSink = dst
		}
	})
	b.Run("easyjson-generated-reused", func(b *testing.B) {
		dst := easyjsonmodel.TypedDocument{Items: make([]easyjsonmodel.TypedRecord, 0, 1024)}
		b.SetBytes(int64(len(src)))
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			if err := dst.UnmarshalJSON(src); err != nil {
				b.Fatal(err)
			}
			easyDocumentSink = dst
		}
	})
	b.Run("simdjson-Compiled-zero-copy-reused", func(b *testing.B) {
		dst := TypedDocument{Items: make([]TypedRecord, 0, 1024)}
		b.SetBytes(int64(len(src)))
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			if err := typedDocDecoder.Decode(src, &dst); err != nil {
				b.Fatal(err)
			}
			typedDocumentSink = dst
		}
	})
	b.Run("simdjson-Compiled-owned-reused", func(b *testing.B) {
		dst := TypedDocument{Items: make([]TypedRecord, 0, 1024)}
		b.SetBytes(int64(len(src)))
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			if err := typedDocOwned.Decode(src, &dst); err != nil {
				b.Fatal(err)
			}
			typedDocumentSink = dst
		}
	})
}

var (
	typedDocEncoder  = mustTypedEncoder[TypedDocument]()
	encodeOutSink    []byte
)

func mustTypedEncoder[T any]() simdjson.Encoder[T] {
	encoder, err := simdjson.CompileEncoder[T]()
	if err != nil {
		panic(err)
	}
	return encoder
}

func BenchmarkEncodeTyped(b *testing.B) {
	src := recordsJSON(1024)
	var doc TypedDocument
	if err := stdjson.Unmarshal(src, &doc); err != nil {
		b.Fatal(err)
	}
	out, err := typedDocEncoder.AppendJSON(nil, &doc)
	if err != nil {
		b.Fatal(err)
	}
	size := int64(len(out))

	b.Run("stdlib", func(b *testing.B) {
		b.SetBytes(size)
		b.ReportAllocs()
		for range b.N {
			result, err := stdjson.Marshal(&doc)
			if err != nil {
				b.Fatal(err)
			}
			encodeOutSink = result
		}
	})
	b.Run("go-json", func(b *testing.B) {
		b.SetBytes(size)
		b.ReportAllocs()
		for range b.N {
			result, err := goccyjson.Marshal(&doc)
			if err != nil {
				b.Fatal(err)
			}
			encodeOutSink = result
		}
	})
	b.Run("Segment", func(b *testing.B) {
		b.SetBytes(size)
		b.ReportAllocs()
		for range b.N {
			result, err := segmentjson.Marshal(&doc)
			if err != nil {
				b.Fatal(err)
			}
			encodeOutSink = result
		}
	})
	b.Run("jsoniter", func(b *testing.B) {
		b.SetBytes(size)
		b.ReportAllocs()
		for range b.N {
			result, err := jsoniter.Marshal(&doc)
			if err != nil {
				b.Fatal(err)
			}
			encodeOutSink = result
		}
	})
	b.Run("simdjson-Marshal", func(b *testing.B) {
		b.SetBytes(size)
		b.ReportAllocs()
		for range b.N {
			result, err := simdjson.Marshal(&doc)
			if err != nil {
				b.Fatal(err)
			}
			encodeOutSink = result
		}
	})
	b.Run("simdjson-AppendJSON-reused", func(b *testing.B) {
		buffer := make([]byte, 0, len(out))
		b.SetBytes(size)
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			result, err := typedDocEncoder.AppendJSON(buffer[:0], &doc)
			if err != nil {
				b.Fatal(err)
			}
			buffer = result
		}
	})
}
