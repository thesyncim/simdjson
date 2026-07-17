package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type operationSpec struct {
	Label    string
	Contract string
	Group    string
	Impl     string
}

var headlineOperations = []operationSpec{
	{Label: "Strict validation", Contract: "Strict JSON + UTF-8", Group: "valid", Impl: "simdjson"},
	{Label: "Typed owned decode", Contract: "Owned strings", Group: "typed-reused", Impl: "simdjson-owned"},
	{Label: "Dynamic owned decode", Contract: "Owned `any` tree", Group: "dynamic-owned", Impl: "simdjson-owned"},
	{Label: "Owned encode", Contract: "Owned output", Group: "encode", Impl: "simdjson-owned"},
	{Label: "Compiled encode reuse", Contract: "Reused output buffer", Group: "encode", Impl: "simdjson-compiled-reuse"},
	{Label: "Parse + complete walk", Contract: "Complete semantic traversal", Group: "dom", Impl: "simdjson"},
}

var rivalImplementations = map[string]bool{
	"go-json":  true,
	"Segment":  true,
	"jsoniter": true,
	"fastjson": true,
}

func renderPublication(root string, publication Publication) (map[string][]byte, error) {
	mainSummary := renderMainSummary(publication)
	goPublication := renderGoPublication(publication)
	crossLanguage := renderCrossLanguage(publication)
	legacyControl := renderLegacyControl(publication)

	files := make(map[string][]byte)
	for _, replacement := range []struct {
		path string
		id   string
		body string
	}{
		{path: filepath.Join(root, "README.md"), id: "main-summary", body: mainSummary},
		{path: filepath.Join(root, "benchmarks", "README.md"), id: "go-publication", body: goPublication},
		{path: filepath.Join(root, "benchmarks", "crosslang", "README.md"), id: "cross-language", body: crossLanguage},
		{path: filepath.Join(root, "benchmarks", "legacy", "README.md"), id: "legacy-control", body: legacyControl},
	} {
		data, err := os.ReadFile(replacement.path)
		if err != nil {
			return nil, err
		}
		updated, err := replaceGeneratedBlock(data, replacement.id, replacement.body)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", replacement.path, err)
		}
		files[replacement.path] = updated
	}
	files[filepath.Join(root, "benchmarks", "charts", "headline.svg")] = renderHeadlineSVG(publication)
	files[filepath.Join(root, "benchmarks", "charts", "corpus-speedups.svg")] = renderCorpusSVG(publication)
	files[filepath.Join(root, "benchmarks", "charts", "simd-uplift.svg")] = renderSIMDSVG(publication)
	files[filepath.Join(root, "benchmarks", "crosslang", "chart.svg")] = renderCrosslangSVG(publication)
	return files, nil
}

func replaceGeneratedBlock(data []byte, id, body string) ([]byte, error) {
	start := []byte("<!-- benchpublish:" + id + ":start -->")
	end := []byte("<!-- benchpublish:" + id + ":end -->")
	startAt := bytes.Index(data, start)
	endAt := bytes.Index(data, end)
	if startAt < 0 || endAt < 0 || endAt <= startAt {
		return nil, fmt.Errorf("missing generated block %q", id)
	}
	startAt += len(start)
	var out bytes.Buffer
	out.Grow(len(data) + len(body))
	out.Write(data[:startAt])
	out.WriteByte('\n')
	out.WriteString(strings.TrimSpace(body))
	out.WriteByte('\n')
	out.Write(data[endAt:])
	return out.Bytes(), nil
}

func writeFiles(files map[string][]byte) error {
	for path, data := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func checkFiles(files map[string][]byte) error {
	for path, want := range files {
		got, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !bytes.Equal(got, want) {
			return fmt.Errorf("%s differs", path)
		}
	}
	return nil
}

func renderMainSummary(p Publication) string {
	var out strings.Builder
	fmt.Fprintf(&out, "The current publication is measured from clean library revision\n`%s`. Measurements use an %s and one CPU. Each row reports the median of\n%s; the pinned Go revision is `%s`. Each contract runs in a fresh process so\nallocator-heavy dynamic decode cannot perturb later groups. Lower time is\nbetter; speedups are geometric means across the seven exact 6.33 MiB Go\n`encoding/json` corpus payloads.\n\n",
		p.Metadata.Commit, p.Metadata.Machine, sampleContract(p.Metadata), p.Metadata.GoCommit)
	out.WriteString("| Operation | Contract | vs stdlib | vs fastest rival | vs native Sonic | SIMD vs pure Go |\n")
	out.WriteString("|---|---|---:|---:|---:|---:|\n")
	for _, spec := range headlineOperations {
		stdlib := operationGeomean(p, spec, "stdlib")
		rival := operationGeomean(p, spec, "rival")
		sonic := operationGeomean(p, spec, "sonic")
		simd := operationGeomean(p, spec, "simd")
		label := spec.Label
		if label == "Strict validation" {
			label = "Validate"
		}
		fmt.Fprintf(&out, "| %s | %s | **%.2fx** | %s | %s | **%.3fx** |\n",
			label, spec.Contract, stdlib, formatRatioCell(rival, 2), formatRatioCell(sonic, 2), simd)
	}
	out.WriteString("\n![Geometric-mean performance comparison](benchmarks/charts/headline.svg)\n\n")
	out.WriteString("Every plotted value is baseline time divided by simdjson time: `1x` is equal\nperformance, and larger values mean simdjson is faster. SIMD uplift stays in\nthe table above and has a dedicated chart in the detailed results.\n\n")
	out.WriteString("The fastest-rival column chooses the best compatible result per payload from\ngo-json, Segment, jsoniter, and fastjson, all built with the pinned Go tip.\nNative Sonic uses its stable supported toolchain in an isolated module; its\nsyntax-only `Valid` result is context rather than a strict-validation peer.\n\n")
	fmt.Fprintf(&out, "The same corpus puts `encoding/json/v2` behind by %.2fx on typed decode, %.2fx\non dynamic decode, and %.2fx on owned encode. Reusable structural-index\nconstruction is part of the regular benchmark gate and remains zero-allocation.\n\n",
		variantGeomean(p, "jsonv2", "BenchmarkStdlibCorpusJSONV2", "typed-reused", "jsonv2", headlineOperations[1]),
		variantGeomean(p, "jsonv2", "BenchmarkStdlibCorpusJSONV2", "dynamic-owned", "jsonv2", headlineOperations[2]),
		variantGeomean(p, "jsonv2", "BenchmarkStdlibCorpusJSONV2", "encode", "jsonv2", headlineOperations[3]))
	out.WriteString("[Current per-corpus results, allocations, hook cost, SIMD uplift, charts, and exact commands](benchmarks/README.md).\n")
	out.WriteString("The [cross-language benchmark](benchmarks/crosslang/README.md) publishes only\nthe enforced parse-plus-semantic-digest contract as a direct comparison.\n")
	return out.String()
}

func renderGoPublication(p Publication) string {
	var out strings.Builder
	fmt.Fprintf(&out, "Every table in this document is generated from one clean publication record:\n\n| Component | Revision |\n|---|---|\n| simdjson | `%s` (`dirty=false`) |\n| Go | `%s`, commit `%s` |\n| Machine | %s, `%s/%s`, one CPU |\n| Samples | %s, median reported |\n\n",
		p.Metadata.Commit, escapePipes(p.Metadata.GoVersion), p.Metadata.GoCommit, p.Metadata.Machine, p.Metadata.OS, p.Metadata.Arch, sampleContract(p.Metadata))
	out.WriteString("Each `valid`, `dynamic-owned`, `dom`, `typed-reused`, and `encode`\ncontract runs in a fresh process. Compilation, plan creation, fixture decode,\ncapacity preparation, and correctness checks happen before the timer.\n\n")
	out.WriteString("## Headline geomeans\n\n")
	out.WriteString("| Operation | vs `encoding/json` | vs fastest compatible rival | SIMD vs pure Go |\n|---|---:|---:|---:|\n")
	for _, spec := range headlineOperations {
		fmt.Fprintf(&out, "| %s | **%.3fx** | %s | **%.3fx** |\n", spec.Label,
			operationGeomean(p, spec, "stdlib"), formatRatioCell(operationGeomean(p, spec, "rival"), 3), operationGeomean(p, spec, "simd"))
	}
	out.WriteString("\n![Headline geometric-mean speedups](charts/headline.svg)\n\n")
	out.WriteString("Read each bar as baseline time divided by simdjson time. The `1x` line is\nequal performance; longer bars are faster.\n\n")
	out.WriteString("The rival is the fastest compatible per-payload result from go-json, Segment,\njsoniter, or fastjson. Aggregate leads do not imply a win on every payload.\n\n")
	out.WriteString("## Per-corpus results\n\n### Strict validation\n\n")
	renderComparisonTable(&out, p, headlineOperations[0])
	out.WriteString("\nValid input allocates zero bytes and zero objects.\n\n### Typed owned decode\n\n")
	renderComparisonTable(&out, p, headlineOperations[1])
	out.WriteString("\n### Dynamic owned decode\n\n")
	renderComparisonTable(&out, p, headlineOperations[2])
	out.WriteString("\nDynamic `any` values use ordinary Go interface construction. The current\nallocation profile is:\n\n| Corpus | Bytes/op | Allocs/op |\n|---|---:|---:|\n")
	for _, corpus := range corpusOrder {
		metric := p.mustMetric("simd", benchmarkName("BenchmarkStdlibCorpus", corpus, "dynamic-owned", "simdjson-owned"))
		fmt.Fprintf(&out, "| %s | %s | %s |\n", corpusLabels[corpus], formatInteger(metric.BytesPerOp), formatInteger(metric.AllocsPerOp))
	}
	out.WriteString("\n### Encode\n\n| Corpus | stdlib | Owned | Compiled reuse | Rival | Rival time |\n|---|---:|---:|---:|---|---:|\n")
	for _, corpus := range corpusOrder {
		stdlib := metricFor(p, "simd", corpus, "encode", "encoding-json")
		owned := metricFor(p, "simd", corpus, "encode", "simdjson-owned")
		reuse := metricFor(p, "simd", corpus, "encode", "simdjson-compiled-reuse")
		rivalName, rival := fastestRival(p, corpus, "encode")
		ownedCell := formatDuration(owned.NsPerOp)
		rivalCell := formatDuration(rival.NsPerOp)
		if owned.NsPerOp <= rival.NsPerOp {
			ownedCell = bold(ownedCell)
		} else {
			rivalCell = bold(rivalCell)
		}
		fmt.Fprintf(&out, "| %s | %s | %s | **%s** | %s | %s |\n", corpusLabels[corpus], formatDuration(stdlib.NsPerOp), ownedCell, formatDuration(reuse.NsPerOp), rivalName, rivalCell)
	}
	out.WriteString("\n### Parse and complete walk\n\n| Corpus | stdlib `any` + walk | simdjson parse + walk | Lead |\n|---|---:|---:|---:|\n")
	for _, corpus := range corpusOrder {
		stdlib := metricFor(p, "simd", corpus, "dom", "encoding-json")
		ours := metricFor(p, "simd", corpus, "dom", "simdjson")
		fmt.Fprintf(&out, "| %s | %s | **%s** | **%.2fx** |\n", corpusLabels[corpus], formatDuration(stdlib.NsPerOp), formatDuration(ours.NsPerOp), stdlib.NsPerOp/ours.NsPerOp)
	}
	out.WriteString("\n![Per-corpus speedup over encoding/json](charts/corpus-speedups.svg)\n\n")
	out.WriteString("Each cell is `encoding/json` time divided by simdjson time for that exact\noperation and payload. `1x` is equal performance; larger values are faster.\n\n")
	out.WriteString("### Reusable structural index\n\n`BuildIndex` validates the input and builds a caller-owned navigable tape.\nCorrectly sized storage is reused; every row allocates zero bytes and objects.\n\n| Corpus | Time | Throughput |\n|---|---:|---:|\n")
	for _, corpus := range corpusOrder {
		metric := p.mustMetric("index-simd", benchmarkName("BenchmarkStdlibCorpusNativeParse", corpus, "", "simdjson-index-reused"))
		fmt.Fprintf(&out, "| %s | **%s** | **%.2f GB/s** |\n", corpusLabels[corpus], formatDuration(metric.NsPerOp), metric.MBPerSec/1000)
	}
	out.WriteString("\n## Native hook cost\n\nHooks keep the public API composable without weakening default ownership.\nDecode uses retainable receiver state; encode passes ordinary GC-visible\nreceivers.\n\n| Case | Interpreter | Native hook | Hook / interpreter | Bytes/op | Allocs/op |\n|---|---:|---:|---:|---:|---:|\n")
	renderHookRow(&out, p, "Decode small", "BenchmarkHookDecodeSmall")
	renderHookRow(&out, p, "Decode 1,024 records", "BenchmarkHookDecodeLarge")
	renderHookRow(&out, p, "Encode small", "BenchmarkHookEncodeSmall")
	renderHookRow(&out, p, "Encode 1,024 records", "BenchmarkHookEncodeLarge")
	out.WriteString("\n## SIMD controls\n\nBoth binaries use the same candidate, compiler, corpus, isolated-process\ncontract, and one CPU.\n\n| Path | SIMD wins | Geomean uplift |\n|---|---:|---:|\n")
	for _, control := range simdControls {
		wins, uplift := simdControl(p, control)
		fmt.Fprintf(&out, "| %s | %d/7 | **%.3fx** |\n", control.Label, wins, uplift)
	}
	out.WriteString("\n![SIMD uplift by path](charts/simd-uplift.svg)\n\n")
	out.WriteString("Each bar is portable-Go time divided by SIMD time. `1x` is equal\nperformance; values above `1x` are a SIMD win.\n\n## Additional Go context\n\n")
	fmt.Fprintf(&out, "`encoding/json/v2` is built from the pinned Go tip. Its time divided by\nsimdjson time is %.3fx for typed owned decode, %.3fx for dynamic owned decode,\nand %.3fx for owned encode.\n\n",
		variantGeomean(p, "jsonv2", "BenchmarkStdlibCorpusJSONV2", "typed-reused", "jsonv2", headlineOperations[1]),
		variantGeomean(p, "jsonv2", "BenchmarkStdlibCorpusJSONV2", "dynamic-owned", "jsonv2", headlineOperations[2]),
		variantGeomean(p, "jsonv2", "BenchmarkStdlibCorpusJSONV2", "encode", "jsonv2", headlineOperations[3]))
	fmt.Fprintf(&out, "Sonic is measured with `%s` because its native path does not support\nthe pinned Go tip. Sonic time divided by simdjson time is %.3fx for typed\nowned decode, %.3fx for dynamic owned decode, and %.3fx for owned encode. Its\nsyntax-only validation result (%.3fx) is context, not a strict-UTF-8 peer.\n",
		p.Metadata.LegacyVersion,
		operationGeomean(p, headlineOperations[1], "sonic"), operationGeomean(p, headlineOperations[2], "sonic"), operationGeomean(p, headlineOperations[3], "sonic"), operationGeomean(p, headlineOperations[0], "sonic"))
	return out.String()
}

func renderComparisonTable(out *strings.Builder, p Publication, spec operationSpec) {
	out.WriteString("| Corpus | `encoding/json` | simdjson | Rival | Rival time | vs stdlib | vs rival |\n|---|---:|---:|---|---:|---:|---:|\n")
	for _, corpus := range corpusOrder {
		stdlib := metricFor(p, "simd", corpus, spec.Group, "encoding-json")
		ours := metricFor(p, "simd", corpus, spec.Group, spec.Impl)
		rivalName, rival := fastestRival(p, corpus, spec.Group)
		fmt.Fprintf(out, "| %s | %s | **%s** | %s | %s | **%.2fx** | **%.2fx** |\n",
			corpusLabels[corpus], formatDuration(stdlib.NsPerOp), formatDuration(ours.NsPerOp), rivalName, formatDuration(rival.NsPerOp), stdlib.NsPerOp/ours.NsPerOp, rival.NsPerOp/ours.NsPerOp)
	}
}

func renderHookRow(out *strings.Builder, p Publication, label, benchmark string) {
	interpreter := p.mustMetric("hooks", benchmark+"/interpreter")
	hook := p.mustMetric("hooks", benchmark+"/hook")
	fmt.Fprintf(out, "| %s | %s | %s | %.2fx | %s | %s |\n", label, formatDuration(interpreter.NsPerOp), formatDuration(hook.NsPerOp), hook.NsPerOp/interpreter.NsPerOp, formatInteger(hook.BytesPerOp), formatInteger(hook.AllocsPerOp))
}

func renderCrossLanguage(p Publication) string {
	var out strings.Builder
	fmt.Fprintf(&out, "| Component | Revision |\n|---|---|\n| Go simdjson | `%s` (`dirty=false`) |\n| Go compiler | `%s`, `GOEXPERIMENT=%s` |\n| C++ simdjson | %s, commit `%s`, %s implementation |\n| C++ compiler | %s |\n| Machine | %s, single thread |\n\n",
		p.Metadata.Commit, escapePipes(p.Metadata.GoVersion), p.Metadata.GoExperiment, p.Metadata.CXXLibrary, p.Metadata.CXXCommit, p.Metadata.CXXImpl, escapePipes(p.Metadata.CXXVersion), p.Metadata.Machine)
	fmt.Fprintf(&out, "%s are taken per operation; the median is reported.\n\n| Corpus | Digest | C++ | Go | Go speedup |\n|---|---|---:|---:|---:|\n", strings.ToUpper(sampleContract(p.Metadata)[:1])+sampleContract(p.Metadata)[1:])
	for _, corpus := range corpusOrder {
		cpp, _ := p.crosslang("cpp", corpus)
		goResult, _ := p.crosslang("go", corpus)
		cppCell, goCell := formatCrossDuration(cpp.NsPerOp), formatCrossDuration(goResult.NsPerOp)
		if cpp.NsPerOp <= goResult.NsPerOp {
			cppCell = bold(cppCell)
		} else {
			goCell = bold(goCell)
		}
		fmt.Fprintf(&out, "| %s | `%s` | %s | %s | **%.3fx** |\n", corpusLabels[corpus], cpp.Digest, cppCell, goCell, cpp.NsPerOp/goResult.NsPerOp)
	}
	out.WriteString("\n![Go speedup over C++ semantic traversal](chart.svg)\n\n")
	out.WriteString("`Go speedup` is C++ time divided by Go time. `1x` is equal performance;\nvalues above `1x` mean Go is faster. The raw medians remain in the adjacent\ncolumns. The identical digest for the two string fixtures is expected: they\ndecode to the same semantic value even though one source uses escapes and the\nother uses literal Unicode.\n")
	return out.String()
}

func renderLegacyControl(p Publication) string {
	var out strings.Builder
	fmt.Fprintf(&out, "%s, one CPU, `%s`, %s per row, median reported:\n\n| Corpus | Typed owned | Dynamic owned | Owned encode | Syntax-only `Valid` |\n|---|---:|---:|---:|---:|\n", p.Metadata.Machine, p.Metadata.LegacyVersion, sampleContract(p.Metadata))
	for _, corpus := range corpusOrder {
		fmt.Fprintf(&out, "| %s | %s | %s | %s | %s |\n", corpusLabels[corpus],
			formatDuration(legacyMetric(p, corpus, "typed-reused").NsPerOp),
			formatDuration(legacyMetric(p, corpus, "dynamic-owned").NsPerOp),
			formatDuration(legacyMetric(p, corpus, "encode").NsPerOp),
			formatDuration(legacyMetric(p, corpus, "valid").NsPerOp))
	}
	out.WriteString("\nSonic's `Valid` accepts invalid UTF-8, so that column is implementation\ncontext rather than a strict-validation comparison. Compiler and standard-\nlibrary revisions also differ; the main tables exclude these rows from\nsame-toolchain fastest-rival selection.\n")
	return out.String()
}

func benchmarkName(root, corpus, group, impl string) string {
	name := root + "/" + corpus
	if group != "" {
		name += "/" + group
	}
	if impl != "" {
		name += "/" + impl
	}
	return name
}

func metricFor(p Publication, variant, corpus, group, impl string) Metrics {
	return p.mustMetric(variant, benchmarkName("BenchmarkStdlibCorpus", corpus, group, impl))
}

func legacyMetric(p Publication, corpus, group string) Metrics {
	return p.mustMetric("sonic", benchmarkName("BenchmarkStdlibCorpusNativeSonic", corpus, group, sonicImplementation(group)))
}

func sonicImplementation(group string) string {
	if group == "typed-reused" {
		return "Sonic-native-owned"
	}
	return "Sonic-native"
}

func fastestRival(p Publication, corpus, group string) (string, Metrics) {
	prefix := benchmarkName("BenchmarkStdlibCorpus", corpus, group, "") + "/"
	name := ""
	best := Metrics{NsPerOp: 1e300}
	for _, result := range p.Results {
		if result.Variant != "simd" || !strings.HasPrefix(result.Name, prefix) {
			continue
		}
		implementation := strings.TrimPrefix(result.Name, prefix)
		if rivalImplementations[implementation] {
			metric := Metrics{NsPerOp: median(result.NsPerOp)}
			if metric.NsPerOp < best.NsPerOp {
				name, best = implementation, metric
			}
		}
	}
	if name == "" {
		return "—", Metrics{}
	}
	return name, best
}

func operationGeomean(p Publication, spec operationSpec, comparison string) float64 {
	var ratios []float64
	for _, corpus := range corpusOrder {
		ours := metricFor(p, "simd", corpus, spec.Group, spec.Impl).NsPerOp
		var other float64
		switch comparison {
		case "stdlib":
			other = metricFor(p, "simd", corpus, spec.Group, "encoding-json").NsPerOp
		case "rival":
			if spec.Group == "dom" {
				return 0
			}
			_, otherMetric := fastestRival(p, corpus, spec.Group)
			other = otherMetric.NsPerOp
		case "simd":
			other = metricFor(p, "pure", corpus, spec.Group, spec.Impl).NsPerOp
		case "sonic":
			if spec.Impl == "simdjson-compiled-reuse" || spec.Group == "dom" {
				return 0
			}
			other = legacyMetric(p, corpus, spec.Group).NsPerOp
		}
		ratios = append(ratios, other/ours)
	}
	return geomean(ratios)
}

func variantGeomean(p Publication, variant, root, group, impl string, oursSpec operationSpec) float64 {
	var ratios []float64
	for _, corpus := range corpusOrder {
		other := p.mustMetric(variant, benchmarkName(root, corpus, group, impl)).NsPerOp
		ours := metricFor(p, "simd", corpus, oursSpec.Group, oursSpec.Impl).NsPerOp
		ratios = append(ratios, other/ours)
	}
	return geomean(ratios)
}

type simdControlSpec struct {
	Label string
	Group string
	Impl  string
}

var simdControls = []simdControlSpec{
	{Label: "Validation", Group: "valid", Impl: "simdjson"},
	{Label: "Dynamic owned", Group: "dynamic-owned", Impl: "simdjson-owned"},
	{Label: "Dynamic zero-copy", Group: "dynamic-owned", Impl: "simdjson-zero-copy"},
	{Label: "Parse + complete walk", Group: "dom", Impl: "simdjson"},
	{Label: "Typed owned", Group: "typed-reused", Impl: "simdjson-owned"},
	{Label: "Typed zero-copy", Group: "typed-reused", Impl: "simdjson-zero-copy"},
	{Label: "Encode owned", Group: "encode", Impl: "simdjson-owned"},
	{Label: "Encode compiled reuse", Group: "encode", Impl: "simdjson-compiled-reuse"},
	{Label: "Reusable structural index", Impl: "simdjson-index-reused"},
}

func simdControl(p Publication, spec simdControlSpec) (int, float64) {
	var ratios []float64
	wins := 0
	for _, corpus := range corpusOrder {
		var simd, pure Metrics
		if spec.Group == "" {
			name := benchmarkName("BenchmarkStdlibCorpusNativeParse", corpus, "", spec.Impl)
			simd = p.mustMetric("index-simd", name)
			pure = p.mustMetric("index-pure", name)
		} else {
			simd = metricFor(p, "simd", corpus, spec.Group, spec.Impl)
			pure = metricFor(p, "pure", corpus, spec.Group, spec.Impl)
		}
		if simd.NsPerOp < pure.NsPerOp {
			wins++
		}
		ratios = append(ratios, pure.NsPerOp/simd.NsPerOp)
	}
	return wins, geomean(ratios)
}

func formatDuration(ns float64) string {
	switch {
	case ns < 1000:
		return fmt.Sprintf("%.1f ns", ns)
	case ns < 1e6:
		return fmt.Sprintf("%.1f us", ns/1e3)
	default:
		return fmt.Sprintf("%.3f ms", ns/1e6)
	}
}

func formatCrossDuration(ns float64) string {
	if ns < 1e6 {
		return fmt.Sprintf("%.3f us", ns/1e3)
	}
	return fmt.Sprintf("%.6f ms", ns/1e6)
}

func formatInteger(value float64) string {
	n := int64(value + 0.5)
	s := strconv.FormatInt(n, 10)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func formatRatioCell(value float64, precision int) string {
	if value == 0 {
		return "—"
	}
	return fmt.Sprintf("**%.*fx**", precision, value)
}

func sampleContract(metadata Metadata) string {
	return fmt.Sprintf("%s approximately %s samples", countWord(metadata.Samples), formatBenchTime(metadata.BenchTime))
}

func countWord(count int) string {
	words := [...]string{"zero", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten"}
	if count >= 0 && count < len(words) {
		return words[count]
	}
	return strconv.Itoa(count)
}

func formatBenchTime(value string) string {
	for i, r := range value {
		if r < '0' || r > '9' {
			return value[:i] + " " + value[i:]
		}
	}
	return value
}

func bold(value string) string { return "**" + value + "**" }

func escapePipes(value string) string { return strings.ReplaceAll(value, "|", "\\|") }
