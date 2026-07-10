package legacy

import (
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	sonicast "github.com/bytedance/sonic/ast"
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
	nodeSink          sonicast.Node
	typedSmallSink    typedSmall
	typedDocumentSink typedDocument
)

type typedSmall struct {
	ID   int    `json:"id"`
	OK   bool   `json:"ok"`
	Name string `json:"name"`
}

type typedRecord struct {
	ID      int        `json:"id"`
	Active  bool       `json:"active"`
	Name    string     `json:"name"`
	Message string     `json:"message"`
	Scores  [3]float64 `json:"scores"`
}

type typedMeta struct {
	Count  int    `json:"count"`
	Source string `json:"source"`
}

type typedDocument struct {
	Items []typedRecord `json:"items"`
	Meta  typedMeta     `json:"meta"`
}

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

func TestNativeSonicFixtures(t *testing.T) {
	if sonic.APIKind != sonic.UseSonicJSON {
		t.Fatalf("Sonic native API unavailable under %s", runtime.Version())
	}
	for _, f := range fixtures {
		t.Run(f.name, func(t *testing.T) {
			if !sonic.Valid(f.data) {
				t.Fatal("Sonic rejected valid fixture")
			}
			parser := sonicast.NewParserObj(string(f.data))
			node, code := parser.Parse()
			if code != 0 {
				t.Fatalf("Sonic AST parse code %d", code)
			}
			if err := node.LoadAll(); err != nil {
				t.Fatalf("Sonic AST LoadAll: %v", err)
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

func BenchmarkSonicNativeValid(b *testing.B) {
	assertNativeSonic(b)
	for _, f := range fixtures {
		b.Run(f.name, func(b *testing.B) {
			b.SetBytes(int64(len(f.data)))
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				boolSink = sonic.Valid(f.data)
			}
		})
	}
}

func BenchmarkSonicNativeParseAny(b *testing.B) {
	assertNativeSonic(b)
	for _, f := range fixtures {
		b.Run(f.name, func(b *testing.B) {
			b.SetBytes(int64(len(f.data)))
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				var value any
				if err := sonic.Unmarshal(f.data, &value); err != nil {
					b.Fatal(err)
				}
				anySink = value
			}
		})
	}
}

func BenchmarkSonicNativeParseAnyNumbers16(b *testing.B) {
	assertNativeSonic(b)
	src := numbers16JSON(1024)
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var value any
		if err := sonic.Unmarshal(src, &value); err != nil {
			b.Fatal(err)
		}
		anySink = value
	}
}

func BenchmarkSonicNativeParseTyped(b *testing.B) {
	assertNativeSonic(b)
	benchmarkSonicTyped(b, sonic.ConfigDefault)
}

func BenchmarkSonicNativeParseTypedFastest(b *testing.B) {
	assertNativeSonic(b)
	benchmarkSonicTyped(b, sonic.ConfigFastest)
}

func BenchmarkSonicNativeParseTypedStd(b *testing.B) {
	assertNativeSonic(b)
	benchmarkSonicTyped(b, sonic.ConfigStd)
}

func benchmarkSonicTyped(b *testing.B, api sonic.API) {
	for _, fixture := range fixtures {
		b.Run(fixture.name, func(b *testing.B) {
			b.SetBytes(int64(len(fixture.data)))
			b.ReportAllocs()
			if fixture.name == "small" {
				for range b.N {
					var dst typedSmall
					if err := api.Unmarshal(fixture.data, &dst); err != nil {
						b.Fatal(err)
					}
					typedSmallSink = dst
				}
				return
			}
			for range b.N {
				var dst typedDocument
				if err := api.Unmarshal(fixture.data, &dst); err != nil {
					b.Fatal(err)
				}
				typedDocumentSink = dst
			}
		})
	}
}

func BenchmarkSonicNativeParseTypedReused(b *testing.B) {
	assertNativeSonic(b)
	benchmarkSonicTypedReused(b, sonic.ConfigDefault)
}

func BenchmarkSonicNativeParseTypedReusedFastest(b *testing.B) {
	assertNativeSonic(b)
	benchmarkSonicTypedReused(b, sonic.ConfigFastest)
}

func BenchmarkSonicNativeParseTypedReusedStd(b *testing.B) {
	assertNativeSonic(b)
	benchmarkSonicTypedReused(b, sonic.ConfigStd)
}

func benchmarkSonicTypedReused(b *testing.B, api sonic.API) {
	for _, fixture := range fixtures[1:] {
		b.Run(fixture.name, func(b *testing.B) {
			dst := typedDocument{Items: make([]typedRecord, 0, 1024)}
			b.SetBytes(int64(len(fixture.data)))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if err := api.Unmarshal(fixture.data, &dst); err != nil {
					b.Fatal(err)
				}
				typedDocumentSink = dst
			}
		})
	}
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

func BenchmarkSonicNativeLoadAll(b *testing.B) {
	assertNativeSonic(b)
	for _, f := range fixtures {
		b.Run(f.name, func(b *testing.B) {
			text := string(f.data)
			b.SetBytes(int64(len(f.data)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				parser := sonicast.NewParserObj(text)
				node, code := parser.Parse()
				if code != 0 {
					b.Fatalf("parse code %d", code)
				}
				if err := node.LoadAll(); err != nil {
					b.Fatal(err)
				}
				nodeSink = node
			}
		})
	}
}

func assertNativeSonic(tb testing.TB) {
	tb.Helper()
	if sonic.APIKind != sonic.UseSonicJSON {
		tb.Fatalf("Sonic native API unavailable under %s", runtime.Version())
	}
}
