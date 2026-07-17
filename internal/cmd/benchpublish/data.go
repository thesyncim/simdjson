package main

import (
	"fmt"
	"math"
	"slices"
)

type Publication struct {
	Metadata  Metadata          `json:"metadata"`
	Results   []BenchmarkResult `json:"results"`
	Crosslang []CrosslangResult `json:"cross_language"`
}

type Metadata struct {
	Commit        string `json:"commit"`
	Dirty         bool   `json:"dirty"`
	GoVersion     string `json:"go_version"`
	GoCommit      string `json:"go_commit"`
	LegacyVersion string `json:"legacy_go_version"`
	Machine       string `json:"machine"`
	OS            string `json:"os"`
	Arch          string `json:"arch"`
	Samples       int    `json:"samples"`
	BenchTime     string `json:"bench_time"`
	CXXVersion    string `json:"cxx_version"`
	CXXLibrary    string `json:"cxx_library"`
	CXXCommit     string `json:"cxx_commit"`
	CXXImpl       string `json:"cxx_implementation"`
	GoExperiment  string `json:"go_experiment"`
}

type BenchmarkResult struct {
	Variant     string    `json:"variant"`
	Name        string    `json:"name"`
	NsPerOp     []float64 `json:"ns_per_op"`
	MBPerSec    []float64 `json:"mb_per_sec,omitempty"`
	BytesPerOp  []float64 `json:"bytes_per_op,omitempty"`
	AllocsPerOp []float64 `json:"allocs_per_op,omitempty"`
}

type CrosslangResult struct {
	Implementation string  `json:"implementation"`
	Corpus         string  `json:"corpus"`
	Digest         string  `json:"digest"`
	NsPerOp        float64 `json:"ns_per_op"`
}

type Metrics struct {
	NsPerOp     float64
	MBPerSec    float64
	BytesPerOp  float64
	AllocsPerOp float64
}

func (p Publication) validate() error {
	if len(p.Metadata.Commit) != 40 || p.Metadata.Dirty {
		return fmt.Errorf("publication must identify one clean 40-character commit")
	}
	if p.Metadata.Samples <= 0 || p.Metadata.BenchTime == "" {
		return fmt.Errorf("missing sample contract")
	}
	if len(p.Results) == 0 || len(p.Crosslang) != 14 {
		return fmt.Errorf("incomplete result sets: Go=%d cross-language=%d", len(p.Results), len(p.Crosslang))
	}
	seen := make(map[string]bool, len(p.Results))
	for _, result := range p.Results {
		key := result.Variant + "\x00" + result.Name
		if seen[key] || len(result.NsPerOp) != p.Metadata.Samples {
			return fmt.Errorf("invalid samples for %s/%s: %d", result.Variant, result.Name, len(result.NsPerOp))
		}
		seen[key] = true
	}
	for _, corpus := range corpusOrder {
		cpp, cppOK := p.crosslang("cpp", corpus)
		goResult, goOK := p.crosslang("go", corpus)
		if !cppOK || !goOK || cpp.Digest != goResult.Digest {
			return fmt.Errorf("missing or mismatched cross-language result for %s", corpus)
		}
	}
	return nil
}

func (p Publication) metric(variant, name string) (Metrics, bool) {
	for _, result := range p.Results {
		if result.Variant == variant && result.Name == name {
			return Metrics{
				NsPerOp:     median(result.NsPerOp),
				MBPerSec:    median(result.MBPerSec),
				BytesPerOp:  median(result.BytesPerOp),
				AllocsPerOp: median(result.AllocsPerOp),
			}, true
		}
	}
	return Metrics{}, false
}

func (p Publication) mustMetric(variant, name string) Metrics {
	metric, ok := p.metric(variant, name)
	if !ok {
		panic(fmt.Sprintf("missing benchmark %s/%s", variant, name))
	}
	return metric
}

func (p Publication) crosslang(implementation, corpus string) (CrosslangResult, bool) {
	for _, result := range p.Crosslang {
		if result.Implementation == implementation && result.Corpus == corpus {
			return result, true
		}
	}
	return CrosslangResult{}, false
}

func median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	copyValues := slices.Clone(values)
	slices.Sort(copyValues)
	middle := len(copyValues) / 2
	if len(copyValues)%2 != 0 {
		return copyValues[middle]
	}
	return (copyValues[middle-1] + copyValues[middle]) / 2
}

func geomean(values []float64) float64 {
	var sum float64
	for _, value := range values {
		if value <= 0 {
			return 0
		}
		sum += math.Log(value)
	}
	return math.Exp(sum / float64(len(values)))
}

var corpusOrder = []string{
	"canada_geometry",
	"citm_catalog",
	"golang_source",
	"string_escaped",
	"string_unicode",
	"synthea_fhir",
	"twitter_status",
}

var corpusLabels = map[string]string{
	"canada_geometry": "Canada geometry",
	"citm_catalog":    "CITM catalog",
	"golang_source":   "Go source",
	"string_escaped":  "Escaped strings",
	"string_unicode":  "Unicode strings",
	"synthea_fhir":    "Synthea FHIR",
	"twitter_status":  "Twitter status",
}
