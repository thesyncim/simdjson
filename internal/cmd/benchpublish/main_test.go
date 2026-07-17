package main

import (
	"strings"
	"testing"
)

func TestParseBenchmarkLine(t *testing.T) {
	line := "BenchmarkStdlibCorpus/canada_geometry/valid/simdjson-1  123  456.5 ns/op  789.25 MB/s  16 B/op  2 allocs/op"
	name, metric, ok := parseBenchmarkLine(line)
	if !ok || name != "BenchmarkStdlibCorpus/canada_geometry/valid/simdjson" {
		t.Fatalf("parse name: %q, %v", name, ok)
	}
	if metric != (Metrics{NsPerOp: 456.5, MBPerSec: 789.25, BytesPerOp: 16, AllocsPerOp: 2}) {
		t.Fatalf("metrics = %+v", metric)
	}
}

func TestParseCrosslangLine(t *testing.T) {
	line := "canada_geometry size=  270000 contract=parse+semantic-digest digest=99bfa84117bedba4 time=    330125ns ( 0.82 GB/s)"
	result, err := parseCrosslangLine(line)
	if err != nil {
		t.Fatal(err)
	}
	if result.Corpus != "canada_geometry" || result.Digest != "99bfa84117bedba4" || result.NsPerOp != 330125 {
		t.Fatalf("result = %+v", result)
	}
}

func TestMedianUsesMiddlePair(t *testing.T) {
	if got := median([]float64{6, 1, 4, 2, 5, 3}); got != 3.5 {
		t.Fatalf("median = %v", got)
	}
}

func TestPublicationRendersEverySurface(t *testing.T) {
	p := testPublication()
	if err := p.validate(); err != nil {
		t.Fatal(err)
	}
	if rendered := renderMainSummary(p); !strings.Contains(rendered, "vs fastest rival") {
		t.Fatal("main summary omitted headline comparison")
	}
	for name, rendered := range map[string]string{
		"go":        renderGoPublication(p),
		"crosslang": renderCrossLanguage(p),
		"legacy":    renderLegacyControl(p),
	} {
		if !strings.Contains(rendered, "Canada geometry") {
			t.Fatalf("%s output omitted corpus rows", name)
		}
	}
	for name, rendered := range map[string][]byte{
		"headline":  renderHeadlineSVG(p),
		"corpus":    renderCorpusSVG(p),
		"simd":      renderSIMDSVG(p),
		"crosslang": renderCrosslangSVG(p),
	} {
		text := string(rendered)
		if !strings.HasPrefix(text, "<svg ") || !strings.Contains(text, "<title") || strings.Contains(text, `\"`) {
			t.Fatalf("%s is not clean accessible SVG", name)
		}
	}
}

func testPublication() Publication {
	p := Publication{Metadata: Metadata{
		Commit:        strings.Repeat("a", 40),
		GoVersion:     "go1.27-devel_test",
		GoCommit:      strings.Repeat("b", 40),
		LegacyVersion: "go1.26.4 darwin/arm64",
		Machine:       "Test CPU",
		OS:            "darwin",
		Arch:          "arm64",
		Samples:       2,
		BenchTime:     "300ms",
		CXXVersion:    "clang version 21",
		CXXLibrary:    "simdjson 4.6.4",
		CXXCommit:     strings.Repeat("c", 40),
		CXXImpl:       "arm64",
		GoExperiment:  "simd",
	}}
	add := func(variant, name string, ns float64) {
		p.Results = append(p.Results, BenchmarkResult{
			Variant: variant, Name: name,
			NsPerOp: []float64{ns, ns}, MBPerSec: []float64{2000, 2000},
			BytesPerOp: []float64{16, 16}, AllocsPerOp: []float64{1, 1},
		})
	}
	seen := make(map[string]bool)
	for _, corpus := range corpusOrder {
		for _, spec := range headlineOperations {
			for _, impl := range []string{"encoding-json", spec.Impl, "go-json"} {
				name := benchmarkName("BenchmarkStdlibCorpus", corpus, spec.Group, impl)
				key := "simd\x00" + name
				if seen[key] {
					continue
				}
				seen[key] = true
				ns := 100.0
				if impl == "encoding-json" {
					ns = 300
				} else if impl == "go-json" {
					ns = 200
				}
				add("simd", name, ns)
			}
			pureName := benchmarkName("BenchmarkStdlibCorpus", corpus, spec.Group, spec.Impl)
			pureKey := "pure\x00" + pureName
			if !seen[pureKey] {
				seen[pureKey] = true
				add("pure", pureName, 150)
			}
		}
		for _, spec := range []struct{ group, impl string }{
			{"dynamic-owned", "simdjson-zero-copy"},
			{"typed-reused", "simdjson-zero-copy"},
		} {
			name := benchmarkName("BenchmarkStdlibCorpus", corpus, spec.group, spec.impl)
			add("simd", name, 90)
			add("pure", name, 140)
		}
		indexName := benchmarkName("BenchmarkStdlibCorpusNativeParse", corpus, "", "simdjson-index-reused")
		add("index-simd", indexName, 80)
		add("index-pure", indexName, 120)
		for _, group := range []string{"valid", "dynamic-owned", "typed-reused", "encode"} {
			add("sonic", benchmarkName("BenchmarkStdlibCorpusNativeSonic", corpus, group, "Sonic-native"), 180)
		}
		for _, group := range []string{"dynamic-owned", "typed-reused", "encode"} {
			add("jsonv2", benchmarkName("BenchmarkStdlibCorpusJSONV2", corpus, group, "jsonv2"), 250)
		}
		for _, impl := range []string{"cpp", "go"} {
			p.Crosslang = append(p.Crosslang, CrosslangResult{Implementation: impl, Corpus: corpus, Digest: "0123456789abcdef", NsPerOp: map[string]float64{"cpp": 120, "go": 100}[impl]})
		}
	}
	for _, benchmark := range []string{"BenchmarkHookDecodeSmall", "BenchmarkHookDecodeLarge", "BenchmarkHookEncodeSmall", "BenchmarkHookEncodeLarge"} {
		add("hooks", benchmark+"/interpreter", 100)
		add("hooks", benchmark+"/hook", 125)
	}
	return p
}
