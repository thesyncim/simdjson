package benchmarks

import (
	"bytes"
	stdjson "encoding/json"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	goccyjson "github.com/goccy/go-json"
	jsoniter "github.com/json-iterator/go"
	segmentjson "github.com/segmentio/encoding/json"
	"github.com/thesyncim/simdjson"
	stdlibcorpus "github.com/thesyncim/simdjson/tests/stdlib"
	"github.com/valyala/fastjson"
)

var corpusBytesSink []byte

func BenchmarkStdlibCorpus(b *testing.B) {
	for _, name := range stdlibcorpus.Names {
		src, err := stdlibcorpus.Read(name)
		if err != nil {
			b.Fatal(err)
		}
		label := strings.TrimSuffix(name, ".json.zst")
		b.Run(label, func(b *testing.B) {
			b.Run("valid", func(b *testing.B) {
				benchmarkCorpusValid(b, src)
			})
			b.Run("dynamic-owned", func(b *testing.B) {
				benchmarkCorpusDynamic(b, src)
			})
			b.Run("typed-reused", func(b *testing.B) {
				benchmarkCorpusTypedByName(b, name, src)
			})
			b.Run("encode", func(b *testing.B) {
				benchmarkCorpusEncodeByName(b, name, src)
			})
		})
	}
}

func benchmarkCorpusTypedByName(b *testing.B, name string, src []byte) {
	switch name {
	case "canada_geometry.json.zst":
		benchmarkCorpusTyped[stdlibcorpus.CanadaRoot](b, src)
	case "citm_catalog.json.zst":
		benchmarkCorpusTyped[stdlibcorpus.CITMRoot](b, src)
	case "golang_source.json.zst":
		benchmarkCorpusTyped[stdlibcorpus.GolangRoot](b, src)
	case "string_escaped.json.zst", "string_unicode.json.zst":
		benchmarkCorpusTyped[stdlibcorpus.StringRoot](b, src)
	case "synthea_fhir.json.zst":
		benchmarkCorpusTyped[stdlibcorpus.SyntheaRoot](b, src)
	case "twitter_status.json.zst":
		benchmarkCorpusTyped[stdlibcorpus.TwitterRoot](b, src)
	default:
		b.Fatalf("missing typed corpus model for %s", name)
	}
}

func benchmarkCorpusEncodeByName(b *testing.B, name string, src []byte) {
	switch name {
	case "canada_geometry.json.zst":
		benchmarkCorpusEncode[stdlibcorpus.CanadaRoot](b, src)
	case "citm_catalog.json.zst":
		benchmarkCorpusEncode[stdlibcorpus.CITMRoot](b, src)
	case "golang_source.json.zst":
		benchmarkCorpusEncode[stdlibcorpus.GolangRoot](b, src)
	case "string_escaped.json.zst", "string_unicode.json.zst":
		benchmarkCorpusEncode[stdlibcorpus.StringRoot](b, src)
	case "synthea_fhir.json.zst":
		benchmarkCorpusEncode[stdlibcorpus.SyntheaRoot](b, src)
	case "twitter_status.json.zst":
		benchmarkCorpusEncode[stdlibcorpus.TwitterRoot](b, src)
	default:
		b.Fatalf("missing encoder corpus model for %s", name)
	}
}

func benchmarkCorpusValid(b *testing.B, src []byte) {
	validators := []struct {
		name string
		fn   func([]byte) bool
	}{
		{"encoding-json", stdjson.Valid},
		{"go-json", goccyjson.Valid},
		{"Segment", segmentjson.Valid},
		{"jsoniter", jsoniter.Valid},
		{"fastjson", func(src []byte) bool { return fastjson.ValidateBytes(src) == nil }},
		{"simdjson", simdjson.Valid},
	}
	if sonic.APIKind != sonic.UseStdJSON {
		validators = append(validators, struct {
			name string
			fn   func([]byte) bool
		}{"Sonic", sonic.Valid})
	}
	for _, validator := range validators {
		if !validator.fn(src) {
			b.Fatalf("%s rejected corpus input", validator.name)
		}
		b.Run(validator.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(src)))
			for b.Loop() {
				boolSink = validator.fn(src)
			}
		})
	}
}

func benchmarkCorpusDynamic(b *testing.B, src []byte) {
	var want any
	if err := stdjson.Unmarshal(src, &want); err != nil {
		b.Fatal(err)
	}
	decoders := []struct {
		name string
		fn   func([]byte, *any) error
	}{
		{"encoding-json", func(src []byte, dst *any) error { return stdjson.Unmarshal(src, dst) }},
		{"go-json", func(src []byte, dst *any) error { return goccyjson.Unmarshal(src, dst) }},
		{"Segment", func(src []byte, dst *any) error { return segmentjson.Unmarshal(src, dst) }},
		{"jsoniter", func(src []byte, dst *any) error { return jsoniter.Unmarshal(src, dst) }},
	}
	if sonic.APIKind != sonic.UseStdJSON {
		decoders = append(decoders, struct {
			name string
			fn   func([]byte, *any) error
		}{"Sonic", func(src []byte, dst *any) error { return sonic.Unmarshal(src, dst) }})
	}
	for _, decoder := range decoders {
		var got any
		if err := decoder.fn(src, &got); err != nil {
			b.Fatalf("%s: %v", decoder.name, err)
		}
		if !reflect.DeepEqual(got, want) {
			b.Fatalf("%s dynamic result differs from encoding/json", decoder.name)
		}
		b.Run(decoder.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(src)))
			for b.Loop() {
				var dst any
				if err := decoder.fn(src, &dst); err != nil {
					b.Fatal(err)
				}
				anySink = dst
			}
		})
	}
	for _, tc := range []struct {
		name string
		opts simdjson.AnyOptions
	}{
		{"simdjson-owned", simdjson.AnyOptions{}},
		{"simdjson-zero-copy", simdjson.AnyOptions{ZeroCopy: true}},
	} {
		got, err := simdjson.ParseAnyOptions(src, tc.opts)
		if err != nil {
			b.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			b.Fatalf("%s dynamic result differs from encoding/json", tc.name)
		}
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(src)))
			for b.Loop() {
				value, err := simdjson.ParseAnyOptions(src, tc.opts)
				if err != nil {
					b.Fatal(err)
				}
				anySink = value
			}
		})
	}
}

func benchmarkCorpusTyped[T any](b *testing.B, src []byte) {
	var want T
	if err := stdjson.Unmarshal(src, &want); err != nil {
		b.Fatal(err)
	}
	decoders := []struct {
		name string
		fn   func([]byte, *T) error
	}{
		{"encoding-json", func(src []byte, dst *T) error { return stdjson.Unmarshal(src, dst) }},
		{"go-json", func(src []byte, dst *T) error { return goccyjson.Unmarshal(src, dst) }},
		{"Segment", func(src []byte, dst *T) error { return segmentjson.Unmarshal(src, dst) }},
		{"jsoniter", func(src []byte, dst *T) error { return jsoniter.Unmarshal(src, dst) }},
	}
	if sonic.APIKind != sonic.UseStdJSON {
		decoders = append(decoders, struct {
			name string
			fn   func([]byte, *T) error
		}{"Sonic", func(src []byte, dst *T) error { return sonic.Unmarshal(src, dst) }})
	}
	for _, decoder := range decoders {
		var got T
		if err := decoder.fn(src, &got); err != nil {
			b.Fatalf("%s: %v", decoder.name, err)
		}
		if !reflect.DeepEqual(got, want) {
			b.Fatalf("%s typed result differs from encoding/json", decoder.name)
		}
		b.Run(decoder.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(src)))
			var dst T
			for b.Loop() {
				if err := decoder.fn(src, &dst); err != nil {
					b.Fatal(err)
				}
			}
			runtime.KeepAlive(dst)
		})
	}
	for _, opts := range []struct {
		name string
		opts simdjson.DecoderOptions
	}{
		{"simdjson-owned", simdjson.DecoderOptions{}},
		{"simdjson-zero-copy", simdjson.DecoderOptions{ZeroCopy: true}},
	} {
		decoder, err := simdjson.CompileDecoder[T](opts.opts)
		if err != nil {
			b.Fatal(err)
		}
		var got T
		if err := decoder.Decode(src, &got); err != nil {
			b.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			b.Fatalf("%s typed result differs from encoding/json", opts.name)
		}
		b.Run(opts.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(src)))
			var dst T
			for b.Loop() {
				if err := decoder.Decode(src, &dst); err != nil {
					b.Fatal(err)
				}
			}
			runtime.KeepAlive(dst)
		})
	}
}

func benchmarkCorpusEncode[T any](b *testing.B, src []byte) {
	var value T
	if err := stdjson.Unmarshal(src, &value); err != nil {
		b.Fatal(err)
	}
	want, err := stdjson.Marshal(&value)
	if err != nil {
		b.Fatal(err)
	}
	encoders := []struct {
		name string
		fn   func(*T) ([]byte, error)
	}{
		{"encoding-json", func(src *T) ([]byte, error) { return stdjson.Marshal(src) }},
		{"go-json", func(src *T) ([]byte, error) { return goccyjson.Marshal(src) }},
		{"Segment", func(src *T) ([]byte, error) { return segmentjson.Marshal(src) }},
		{"jsoniter", func(src *T) ([]byte, error) { return jsoniter.Marshal(src) }},
		{"simdjson-owned", func(src *T) ([]byte, error) { return simdjson.Marshal(src) }},
	}
	if sonic.APIKind != sonic.UseStdJSON {
		encoders = append(encoders, struct {
			name string
			fn   func(*T) ([]byte, error)
		}{"Sonic", func(src *T) ([]byte, error) { return sonic.Marshal(src) }})
	}
	for _, encoder := range encoders {
		got, err := encoder.fn(&value)
		if err != nil {
			b.Fatalf("%s: %v", encoder.name, err)
		}
		if err := equivalentJSON(want, got); err != nil {
			b.Fatalf("%s: %v", encoder.name, err)
		}
		b.Run(encoder.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(want)))
			for b.Loop() {
				out, err := encoder.fn(&value)
				if err != nil {
					b.Fatal(err)
				}
				corpusBytesSink = out
			}
		})
	}
	compiled, err := simdjson.CompileEncoder[T](simdjson.EncoderOptions{})
	if err != nil {
		b.Fatal(err)
	}
	b.Run("simdjson-compiled-reuse", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(want)))
		out := make([]byte, 0, len(want))
		for b.Loop() {
			out, err = compiled.AppendJSON(out[:0], &value)
			if err != nil {
				b.Fatal(err)
			}
		}
		corpusBytesSink = out
	})
}

func equivalentJSON(want, got []byte) error {
	if bytes.Equal(want, got) {
		return nil
	}
	var wantValue, gotValue any
	if err := stdjson.Unmarshal(want, &wantValue); err != nil {
		return err
	}
	if err := stdjson.Unmarshal(got, &gotValue); err != nil {
		return err
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		return fmt.Errorf("encoded value differs from encoding/json")
	}
	return nil
}
