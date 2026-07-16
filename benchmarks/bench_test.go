package benchmarks

import (
	stdjson "encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	sonicast "github.com/bytedance/sonic/ast"
	goccyjson "github.com/goccy/go-json"
	jsoniter "github.com/json-iterator/go"
	simdjsongo "github.com/minio/simdjson-go"
	segmentjson "github.com/segmentio/encoding/json"
	"github.com/thesyncim/simdjson"
	simdkernels "github.com/thesyncim/simdjson/simd"
	"github.com/valyala/fastjson"
)

type fixture struct {
	name string
	data []byte
}

var fixtures = []fixture{
	{name: "small", data: []byte(`{"id":1,"ok":true,"name":"sim"}`)},
	{name: "medium", data: recordsJSON(32)},
	{name: "large", data: recordsJSON(1024)},
}

var (
	boolSink          bool
	anySink           any
	simdjsonValueSink simdjson.Value
	sonicNodeSink     sonicast.Node
	fastValueSink     *fastjson.Value
	simdParsedSink    *simdjsongo.ParsedJson
	intSink           int
)

func recordsJSON(count int) []byte {
	var out strings.Builder
	out.Grow(count * 128)
	out.WriteString(`{"items":[`)
	for i := 0; i < count; i++ {
		if i != 0 {
			out.WriteByte(',')
		}
		out.WriteString(`{"id":`)
		out.WriteString(strconv.Itoa(i))
		out.WriteString(`,"active":`)
		if i&1 == 0 {
			out.WriteString("true")
		} else {
			out.WriteString("false")
		}
		out.WriteString(`,"name":"record-`)
		out.WriteString(strconv.Itoa(i))
		out.WriteString(`","message":"plain ascii payload sized to exercise vector scanners","scores":[1,2.5,-3e4]}`)
	}
	out.WriteString(`],"meta":{"count":`)
	out.WriteString(strconv.Itoa(count))
	out.WriteString(`,"source":"benchmark"}}`)
	return []byte(out.String())
}

func TestValidatorsAcceptFixtures(t *testing.T) {
	for _, f := range fixtures {
		t.Run(f.name, func(t *testing.T) {
			checks := map[string]bool{
				"simdjson": simdjson.Valid(f.data),
				"stdlib":   stdjson.Valid(f.data),
				"Sonic":    sonic.Valid(f.data),
				"go-json":  goccyjson.Valid(f.data),
				"Segment":  segmentjson.Valid(f.data),
				"jsoniter": jsoniter.Valid(f.data),
				"fastjson": fastjson.ValidateBytes(f.data) == nil,
			}
			for name, valid := range checks {
				if !valid {
					t.Errorf("%s rejected fixture", name)
				}
			}
		})
	}
}

func TestFixtureSizes(t *testing.T) {
	want := map[string]int{"small": 31, "medium": 4240, "large": 136586}
	for _, fixture := range fixtures {
		if got := len(fixture.data); got != want[fixture.name] {
			t.Fatalf("%s fixture size = %d, want %d", fixture.name, got, want[fixture.name])
		}
	}
}

func BenchmarkValid(b *testing.B) {
	for _, f := range fixtures {
		b.Run(f.name, func(b *testing.B) {
			benchmarkValid(b, f.data)
		})
	}
}

func BenchmarkValidLateInvalid(b *testing.B) {
	for _, f := range fixtures {
		invalid := append(append([]byte(nil), f.data...), 'x')
		b.Run(f.name, func(b *testing.B) {
			benchmarkInvalid(b, invalid)
		})
	}
}

func benchmarkValid(b *testing.B, src []byte) {
	b.Run("simdjson-"+simdkernels.Current().StringBackend, func(b *testing.B) {
		b.SetBytes(int64(len(src)))
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			boolSink = simdjson.Valid(src)
		}
	})
	b.Run("stdlib", func(b *testing.B) {
		b.SetBytes(int64(len(src)))
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			boolSink = stdjson.Valid(src)
		}
	})
	sonicName := "Sonic"
	if sonic.APIKind == sonic.UseStdJSON {
		sonicName = "Sonic-stdlib-fallback"
	}
	b.Run(sonicName, func(b *testing.B) {
		b.SetBytes(int64(len(src)))
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			boolSink = sonic.Valid(src)
		}
	})
	b.Run("go-json", func(b *testing.B) {
		b.SetBytes(int64(len(src)))
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			boolSink = goccyjson.Valid(src)
		}
	})
	b.Run("Segment", func(b *testing.B) {
		b.SetBytes(int64(len(src)))
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			boolSink = segmentjson.Valid(src)
		}
	})
	b.Run("jsoniter", func(b *testing.B) {
		b.SetBytes(int64(len(src)))
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			boolSink = jsoniter.Valid(src)
		}
	})
	b.Run("fastjson", func(b *testing.B) {
		b.SetBytes(int64(len(src)))
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			boolSink = fastjson.ValidateBytes(src) == nil
		}
	})
}

func benchmarkInvalid(b *testing.B, src []byte) {
	benchmarkValid(b, src)
}

func BenchmarkParseAny(b *testing.B) {
	for _, f := range fixtures {
		b.Run(f.name, func(b *testing.B) {
			benchmarkParseAny(b, f.data)
		})
	}
}

func BenchmarkParseAnyNumbers16(b *testing.B) {
	benchmarkParseAny(b, numbers16JSON(1024))
}

func numbers16JSON(count int) []byte {
	var out strings.Builder
	out.Grow(count*17 + 2)
	out.WriteByte('[')
	for i := 0; i < count; i++ {
		if i != 0 {
			out.WriteByte(',')
		}
		out.WriteString(strconv.FormatInt(1_000_000_000_000_000+int64(i), 10))
	}
	out.WriteByte(']')
	return []byte(out.String())
}

func benchmarkParseAny(b *testing.B, src []byte) {
	b.Run("stdlib", func(b *testing.B) {
		b.SetBytes(int64(len(src)))
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			var value any
			if err := stdjson.Unmarshal(src, &value); err != nil {
				b.Fatal(err)
			}
			anySink = value
		}
	})
	sonicName := "Sonic"
	if sonic.APIKind == sonic.UseStdJSON {
		sonicName = "Sonic-stdlib-fallback"
	}
	b.Run(sonicName, func(b *testing.B) {
		b.SetBytes(int64(len(src)))
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			var value any
			if err := sonic.Unmarshal(src, &value); err != nil {
				b.Fatal(err)
			}
			anySink = value
		}
	})
	b.Run("go-json", func(b *testing.B) {
		b.SetBytes(int64(len(src)))
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			var value any
			if err := goccyjson.Unmarshal(src, &value); err != nil {
				b.Fatal(err)
			}
			anySink = value
		}
	})
	b.Run("Segment", func(b *testing.B) {
		b.SetBytes(int64(len(src)))
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			var value any
			if err := segmentjson.Unmarshal(src, &value); err != nil {
				b.Fatal(err)
			}
			anySink = value
		}
	})
	b.Run("jsoniter", func(b *testing.B) {
		b.SetBytes(int64(len(src)))
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			var value any
			if err := jsoniter.Unmarshal(src, &value); err != nil {
				b.Fatal(err)
			}
			anySink = value
		}
	})
	b.Run("simdjson-Parse+Any", func(b *testing.B) {
		b.SetBytes(int64(len(src)))
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			value, err := simdjson.ParseOptions(src, simdjson.Options{ZeroCopy: true})
			if err != nil {
				b.Fatal(err)
			}
			anySink = value.Any()
		}
	})
	b.Run("simdjson-Unmarshal-any", func(b *testing.B) {
		b.SetBytes(int64(len(src)))
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			var value any
			if err := simdjson.Unmarshal(src, &value); err != nil {
				b.Fatal(err)
			}
			anySink = value
		}
	})
	zeroCopyDecoder, err := simdjson.CompileDecoder[any](simdjson.DecoderOptions{ZeroCopy: true})
	if err != nil {
		b.Fatal(err)
	}
	b.Run("simdjson-Unmarshal-any-zero-copy", func(b *testing.B) {
		b.SetBytes(int64(len(src)))
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			var value any
			if err := zeroCopyDecoder.Decode(src, &value); err != nil {
				b.Fatal(err)
			}
			anySink = value
		}
	})
}

func BenchmarkParseNative(b *testing.B) {
	for _, f := range fixtures {
		b.Run(f.name, func(b *testing.B) {
			benchmarkParseNative(b, f.data)
		})
	}
}

func benchmarkParseNative(b *testing.B, src []byte) {
	b.Run("simdjson-AST", func(b *testing.B) {
		b.SetBytes(int64(len(src)))
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			value, err := simdjson.ParseOptions(src, simdjson.Options{ZeroCopy: true})
			if err != nil {
				b.Fatal(err)
			}
			simdjsonValueSink = value
		}
	})
	b.Run("simdjson-Index", func(b *testing.B) {
		count, err := simdjson.RequiredIndexEntries(src)
		if err != nil {
			b.Fatal(err)
		}
		storage := make([]simdjson.IndexEntry, count)
		b.SetBytes(int64(len(src)))
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			tape, err := simdjson.BuildIndex(src, storage)
			if err != nil {
				b.Fatal(err)
			}
			intSink = tape.Len()
		}
	})
	b.Run("Sonic-AST", func(b *testing.B) {
		if sonic.APIKind == sonic.UseStdJSON {
			b.Skip("Sonic v1.15.2 native implementation does not support Go 1.27 tip")
		}
		text := string(src)
		b.SetBytes(int64(len(src)))
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			parser := sonicast.NewParserObj(text)
			node, code := parser.Parse()
			if code != 0 {
				b.Fatalf("parse code %d", code)
			}
			sonicNodeSink = node
		}
	})
	b.Run("fastjson-reuse", func(b *testing.B) {
		var parser fastjson.Parser
		b.SetBytes(int64(len(src)))
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			value, err := parser.ParseBytes(src)
			if err != nil {
				b.Fatal(err)
			}
			fastValueSink = value
		}
	})
	b.Run("simdjson-go-reuse", func(b *testing.B) {
		if !simdjsongo.SupportedCPU() {
			b.Skip("simdjson-go v0.4.5 supports amd64 AVX2+CLMUL only")
		}
		var reuse *simdjsongo.ParsedJson
		b.SetBytes(int64(len(src)))
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			parsed, err := simdjsongo.Parse(src, reuse)
			if err != nil {
				b.Fatal(err)
			}
			reuse = parsed
			simdParsedSink = parsed
		}
	})
}
