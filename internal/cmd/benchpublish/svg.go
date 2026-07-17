package main

import (
	"fmt"
	"html"
	"math"
	"strings"
)

const chartStyle = `<style>
svg { color-scheme: light dark; }
text { fill:#24292f; font:13px -apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif; }
.heading { font-size:16px; font-weight:500; } .note { font-size:12px; }
.muted { fill:#57606a; } .grid { stroke:#d0d7de; stroke-width:1; }
.baseline { stroke:#8c959f; stroke-width:2; }
.base { fill:#8c959f; } .s1 { fill:#0969da; } .s2 { fill:#8250df; } .s3 { fill:#1a7f37; }
.h1 { fill:#ddf4ff; } .h2 { fill:#b6e3ff; } .h3 { fill:#80ccff; } .h4 { fill:#54aeff; }
@media (prefers-color-scheme: dark) {
text { fill:#f0f6fc; } .muted { fill:#8c959f; } .grid { stroke:#30363d; } .baseline { stroke:#6e7681; }
.base { fill:#8c959f; } .s1 { fill:#58a6ff; } .s2 { fill:#bc8cff; } .s3 { fill:#3fb950; }
.h1 { fill:#102c44; } .h2 { fill:#123c5a; } .h3 { fill:#15557a; } .h4 { fill:#1f6feb; }
}
</style>`

type corpusTimeChartSpec struct {
	File  string
	Title string
	Op    operationSpec
}

var corpusTimeCharts = []corpusTimeChartSpec{
	{File: "validation-times.svg", Title: "Strict validation time", Op: headlineOperations[0]},
	{File: "typed-decode-times.svg", Title: "Typed owned decode time", Op: headlineOperations[1]},
	{File: "dynamic-decode-times.svg", Title: "Dynamic owned decode time", Op: headlineOperations[2]},
	{File: "owned-encode-times.svg", Title: "Owned encode time", Op: headlineOperations[3]},
	{File: "compiled-encode-times.svg", Title: "Compiled encode reuse time", Op: headlineOperations[4]},
	{File: "walk-times.svg", Title: "Parse and complete-walk time", Op: headlineOperations[5]},
}

func renderHeadlineSVG(p Publication) []byte {
	const width, height = 1000, 500
	const left, right, top, bottom = 80, 20, 105, 70
	values := make([][2]float64, len(headlineOperations))
	maxValue := 0.0
	for i, spec := range headlineOperations {
		totals := operationTotals(p, spec)
		values[i] = [2]float64{totals[0], totals[1]}
		for _, value := range values[i] {
			maxValue = math.Max(maxValue, value)
		}
	}
	axisMax := niceCeiling(maxValue)
	plotWidth := float64(width - left - right)
	plotHeight := float64(height - top - bottom)
	var out strings.Builder
	fmt.Fprintf(&out, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" role="img" aria-labelledby="title desc">`, width, height, width, height)
	out.WriteString(`<title id="title">Absolute time for one complete corpus pass</title><desc id="desc">Absolute median time to process all seven corpus files once with encoding/json and simdjson. Lower bars are faster.</desc>`)
	out.WriteString(chartStyle)
	out.WriteString(`<text class="heading" x="0" y="20">Time to process all seven corpus files once</text><text class="muted note" x="0" y="40">6.33 MiB total · per-file medians summed · lower is better</text>`)
	renderLegend(&out, left, 51, []legendItem{{"encoding/json", "base"}, {"simdjson", "s1"}})
	for tick := 0; tick <= 5; tick++ {
		value := axisMax * float64(tick) / 5
		y := float64(top) + (1-float64(tick)/5)*plotHeight
		fmt.Fprintf(&out, `<line class="grid" x1="%d" y1="%.1f" x2="%d" y2="%.1f"/><text class="muted note" x="%d" y="%.1f" text-anchor="end">%s</text>`, left, y, width-right, y, left-8, y+4, formatAxisDuration(value))
	}
	groupWidth := plotWidth / float64(len(headlineOperations))
	classes := [...]string{"base", "s1"}
	for row, spec := range headlineOperations {
		center := float64(left) + (float64(row)+0.5)*groupWidth
		for series, value := range values[row] {
			barHeight := value / axisMax * plotHeight
			x := center - 32 + float64(series)*36
			y := float64(top) + plotHeight - barHeight
			labelX, anchor := center-3, "end"
			if series == 1 {
				labelX, anchor = center+3, "start"
			}
			fmt.Fprintf(&out, `<rect class="%s" x="%.1f" y="%.1f" width="28" height="%.1f" rx="2"/><text class="note" x="%.1f" y="%.1f" text-anchor="%s">%s</text>`, classes[series], x, y, barHeight, labelX, y-5, anchor, formatCompactDuration(value))
		}
		renderTwoLineLabel(&out, center, height-bottom+22, spec.Label)
	}
	out.WriteString(`</svg>`)
	return []byte(out.String())
}

func renderCorpusTimesSVG(p Publication, chart corpusTimeChartSpec) []byte {
	const width, height = 1000, 360
	const plotTop, baseline, barWidth = 102, 282, 28
	facetWidth := float64(width) / float64(len(corpusOrder))
	var out strings.Builder
	fmt.Fprintf(&out, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" role="img" aria-labelledby="title desc">`, width, height, width, height)
	fmt.Fprintf(&out, `<title id="title">%s by corpus</title><desc id="desc">Absolute median completion time for each corpus. Every vertical pair has its own scale so the difference remains visible for small inputs. Labels contain the measured time; lower bars are faster.</desc>`, html.EscapeString(chart.Title))
	out.WriteString(chartStyle)
	fmt.Fprintf(&out, `<text class="heading" x="0" y="20">%s by corpus</text>`, html.EscapeString(chart.Title))
	out.WriteString(`<text class="muted note" x="0" y="40">Each corpus has its own vertical scale · labels are absolute median time · lower is better</text>`)
	renderLegend(&out, 0, 51, []legendItem{{"encoding/json", "base"}, {"simdjson", "s1"}})
	for row, corpus := range corpusOrder {
		values := [2]float64{
			metricFor(p, "simd", corpus, chart.Op.Group, "encoding-json").NsPerOp,
			metricFor(p, "simd", corpus, chart.Op.Group, chart.Op.Impl).NsPerOp,
		}
		facetMax := math.Max(values[0], values[1])
		center := (float64(row) + 0.5) * facetWidth
		fmt.Fprintf(&out, `<line class="grid" x1="%.1f" y1="%d" x2="%.1f" y2="%d"/>`, center-facetWidth/2+8, baseline, center+facetWidth/2-8, baseline)
		for series, value := range values {
			barHeight := value / facetMax * float64(baseline-plotTop)
			x := center - 35 + float64(series)*42
			y := float64(baseline) - barHeight
			class := "base"
			if series == 1 {
				class = "s1"
			}
			fmt.Fprintf(&out, `<rect class="%s" x="%.1f" y="%.1f" width="%d" height="%.1f" rx="2"/><text class="note" x="%.1f" y="%.1f" text-anchor="middle">%s</text>`, class, x, y, barWidth, barHeight, x+barWidth/2, y-5, formatCompactDuration(value))
		}
		renderTwoLineLabel(&out, center, baseline+22, corpusLabels[corpus])
	}
	out.WriteString(`</svg>`)
	return []byte(out.String())
}

func renderSIMDSVG(p Publication) []byte {
	const width, height = 1000, 660
	const columns, facetWidth, facetHeight = 3, 333, 180
	const chartTop, plotTop, baseline, barWidth = 82, 28, 128, 34
	var out strings.Builder
	fmt.Fprintf(&out, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" role="img" aria-labelledby="title desc">`, width, height, width, height)
	out.WriteString(`<title id="title">Absolute SIMD and portable-Go completion time</title><desc id="desc">Absolute time for one pass over all seven payloads with the SIMD and portable-Go paths. Every vertical pair has its own scale; labels contain the measured time and lower bars are faster.</desc>`)
	out.WriteString(chartStyle)
	out.WriteString(`<text class="heading" x="0" y="20">SIMD and portable-Go completion time</text><text class="muted note" x="0" y="40">One pass over all seven payloads · each pair has its own vertical scale · lower is better</text>`)
	renderLegend(&out, 0, 51, []legendItem{{"portable Go", "base"}, {"SIMD", "s1"}})
	for index, spec := range simdControls {
		pure, simd := simdTotals(p, spec)
		facetMax := math.Max(pure, simd)
		column, row := index%columns, index/columns
		originX := float64(column * facetWidth)
		originY := float64(chartTop + row*facetHeight)
		center := originX + facetWidth/2
		baseY := originY + baseline
		fmt.Fprintf(&out, `<line class="grid" x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f"/>`, originX+54, baseY, originX+facetWidth-54, baseY)
		for series, item := range []struct {
			class string
			value float64
		}{{"base", pure}, {"s1", simd}} {
			barHeight := item.value / facetMax * float64(baseline-plotTop)
			x := center - 43 + float64(series)*52
			y := baseY - barHeight
			fmt.Fprintf(&out, `<rect class="%s" x="%.1f" y="%.1f" width="%d" height="%.1f" rx="2"/><text class="note" x="%.1f" y="%.1f" text-anchor="middle">%s</text>`, item.class, x, y, barWidth, barHeight, x+barWidth/2, y-5, formatCompactDuration(item.value))
		}
		renderTwoLineLabel(&out, center, int(baseY)+22, spec.Label)
	}
	out.WriteString(`</svg>`)
	return []byte(out.String())
}

func renderCrosslangSVG(p Publication) []byte {
	const width, height = 1000, 360
	const plotTop, baseline, barWidth = 102, 282, 28
	facetWidth := float64(width) / float64(len(corpusOrder))
	var out strings.Builder
	fmt.Fprintf(&out, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" role="img" aria-labelledby="title desc">`, width, height, width, height)
	out.WriteString(`<title id="title">C++ and Go absolute semantic-traversal time</title><desc id="desc">Absolute median completion time for the identical C++ and Go parse plus semantic digest task. Every vertical pair has its own scale; labels contain the measured time and lower bars are faster.</desc>`)
	out.WriteString(chartStyle)
	out.WriteString(`<text class="heading" x="0" y="20">C++ and Go absolute completion time</text><text class="muted note" x="0" y="40">Identical parse + semantic digest task · each corpus has its own vertical scale · lower is better</text>`)
	renderLegend(&out, 0, 51, []legendItem{{"C++ simdjson", "base"}, {"Go simdjson", "s1"}})
	for row, corpus := range corpusOrder {
		cpp, _ := p.crosslang("cpp", corpus)
		goResult, _ := p.crosslang("go", corpus)
		facetMax := math.Max(cpp.NsPerOp, goResult.NsPerOp)
		center := (float64(row) + 0.5) * facetWidth
		fmt.Fprintf(&out, `<line class="grid" x1="%.1f" y1="%d" x2="%.1f" y2="%d"/>`, center-facetWidth/2+8, baseline, center+facetWidth/2-8, baseline)
		for series, item := range []struct {
			class string
			value float64
		}{{"base", cpp.NsPerOp}, {"s1", goResult.NsPerOp}} {
			barHeight := item.value / facetMax * float64(baseline-plotTop)
			x := center - 35 + float64(series)*42
			y := float64(baseline) - barHeight
			fmt.Fprintf(&out, `<rect class="%s" x="%.1f" y="%.1f" width="%d" height="%.1f" rx="2"/><text class="note" x="%.1f" y="%.1f" text-anchor="middle">%s</text>`, item.class, x, y, barWidth, barHeight, x+barWidth/2, y-5, formatCompactDuration(item.value))
		}
		renderTwoLineLabel(&out, center, baseline+22, corpusLabels[corpus])
	}
	out.WriteString(`</svg>`)
	return []byte(out.String())
}

type legendItem struct {
	label string
	class string
}

func renderLegend(out *strings.Builder, x, y int, items []legendItem) {
	for i, item := range items {
		itemX := x + i*220
		fmt.Fprintf(out, `<rect class="%s" x="%d" y="%d" width="12" height="12" rx="2"/><text x="%d" y="%d">%s</text>`, item.class, itemX, y, itemX+18, y+11, html.EscapeString(item.label))
	}
}

func renderTwoLineLabel(out *strings.Builder, x float64, y int, label string) {
	words := strings.Fields(label)
	if len(words) < 2 {
		fmt.Fprintf(out, `<text x="%.1f" y="%d" text-anchor="middle">%s</text>`, x, y+7, html.EscapeString(label))
		return
	}
	split := (len(words) + 1) / 2
	fmt.Fprintf(out, `<text x="%.1f" y="%d" text-anchor="middle">%s</text><text class="muted" x="%.1f" y="%d" text-anchor="middle">%s</text>`,
		x, y, html.EscapeString(strings.Join(words[:split], " ")), x, y+16, html.EscapeString(strings.Join(words[split:], " ")))
}

func operationTotals(p Publication, spec operationSpec) [3]float64 {
	var totals [3]float64
	for _, corpus := range corpusOrder {
		totals[0] += metricFor(p, "simd", corpus, spec.Group, "encoding-json").NsPerOp
		totals[1] += metricFor(p, "simd", corpus, spec.Group, spec.Impl).NsPerOp
		if spec.Group != "dom" {
			_, rival := fastestRival(p, corpus, spec.Group)
			totals[2] += rival.NsPerOp
		}
	}
	return totals
}

func simdTotals(p Publication, spec simdControlSpec) (pure, simd float64) {
	for _, corpus := range corpusOrder {
		if spec.Group == "" {
			name := benchmarkName("BenchmarkStdlibCorpusNativeParse", corpus, "", spec.Impl)
			pure += p.mustMetric("index-pure", name).NsPerOp
			simd += p.mustMetric("index-simd", name).NsPerOp
			continue
		}
		pure += metricFor(p, "pure", corpus, spec.Group, spec.Impl).NsPerOp
		simd += metricFor(p, "simd", corpus, spec.Group, spec.Impl).NsPerOp
	}
	return pure, simd
}

func niceCeiling(value float64) float64 {
	if value <= 0 {
		return 1
	}
	power := math.Pow(10, math.Floor(math.Log10(value)))
	fraction := value / power
	switch {
	case fraction <= 1:
		return power
	case fraction <= 2:
		return 2 * power
	case fraction <= 5:
		return 5 * power
	default:
		return 10 * power
	}
}

func formatAxisDuration(ns float64) string {
	if ns == 0 {
		return "0"
	}
	if ns >= 1e6 {
		return fmt.Sprintf("%.0f ms", ns/1e6)
	}
	return formatCompactDuration(ns)
}

func formatCompactDuration(ns float64) string {
	switch {
	case ns < 1000:
		return fmt.Sprintf("%.0f ns", ns)
	case ns < 1e5:
		return fmt.Sprintf("%.1f us", ns/1e3)
	case ns < 1e6:
		return fmt.Sprintf("%.0f us", ns/1e3)
	case ns < 1e7:
		return fmt.Sprintf("%.2f ms", ns/1e6)
	default:
		return fmt.Sprintf("%.1f ms", ns/1e6)
	}
}
