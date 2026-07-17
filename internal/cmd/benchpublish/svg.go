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
.s1 { fill:#0969da; } .s2 { fill:#8250df; } .s3 { fill:#1a7f37; }
.h1 { fill:#ddf4ff; } .h2 { fill:#b6e3ff; } .h3 { fill:#80ccff; } .h4 { fill:#54aeff; }
@media (prefers-color-scheme: dark) {
text { fill:#f0f6fc; } .muted { fill:#8c959f; } .grid { stroke:#30363d; } .baseline { stroke:#6e7681; }
.s1 { fill:#58a6ff; } .s2 { fill:#bc8cff; } .s3 { fill:#3fb950; }
.h1 { fill:#102c44; } .h2 { fill:#123c5a; } .h3 { fill:#15557a; } .h4 { fill:#1f6feb; }
}
</style>`

func renderHeadlineSVG(p Publication) []byte {
	const width, left, right = 1000, 210, 90
	height := 116 + len(headlineOperations)*50
	values := make([][2]float64, len(headlineOperations))
	maxValue := 1.0
	for i, spec := range headlineOperations {
		values[i] = [2]float64{operationGeomean(p, spec, "stdlib"), operationGeomean(p, spec, "rival")}
		for _, value := range values[i] {
			maxValue = math.Max(maxValue, value)
		}
	}
	maxValue = math.Ceil(maxValue)
	plotWidth := float64(width - left - right)
	var out strings.Builder
	fmt.Fprintf(&out, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" role="img" aria-labelledby="title desc">`, width, height, width, height)
	out.WriteString(`<title id="title">Headline benchmark speedups</title><desc id="desc">Geometric mean speedups over encoding/json and the fastest compatible Go rival. One times is equal performance; larger values mean simdjson is faster.</desc>`)
	out.WriteString(chartStyle)
	out.WriteString(`<text class="heading" x="0" y="20">Geometric-mean speedup</text><text class="muted note" x="0" y="40">Baseline time ÷ simdjson time · 1× is equal · higher is faster</text>`)
	for tick := 0; tick <= int(maxValue); tick++ {
		x := float64(left) + float64(tick)/maxValue*plotWidth
		class := "grid"
		label := fmt.Sprintf("%d×", tick)
		if tick == 1 {
			class = "baseline"
			label = "1× equal"
		}
		fmt.Fprintf(&out, `<line class="%s" x1="%.1f" y1="86" x2="%.1f" y2="%d"/><text class="muted note" x="%.1f" y="78" text-anchor="middle">%s</text>`, class, x, x, height-18, x, label)
	}
	legend := []struct{ label, class string }{{"vs encoding/json", "s1"}, {"vs fastest compatible Go rival", "s2"}}
	for i, item := range legend {
		x := left + i*260
		fmt.Fprintf(&out, `<rect class="%s" x="%d" y="50" width="12" height="12" rx="2"/><text x="%d" y="61">%s</text>`, item.class, x, x+18, item.label)
	}
	classes := []string{"s1", "s2"}
	for row, spec := range headlineOperations {
		y := 96 + row*50
		fmt.Fprintf(&out, `<text x="%d" y="%d" text-anchor="end">%s</text>`, left-12, y+21, html.EscapeString(spec.Label))
		for series, value := range values[row] {
			if value == 0 {
				continue
			}
			barWidth := value / maxValue * plotWidth
			barY := y + series*17
			fmt.Fprintf(&out, `<rect class="%s" x="%d" y="%d" width="%.1f" height="11" rx="2"/><text x="%.1f" y="%d">%.2f×</text>`, classes[series], left, barY, barWidth, float64(left)+barWidth+6, barY+10, value)
		}
	}
	out.WriteString(`</svg>`)
	return []byte(out.String())
}

func renderCorpusSVG(p Publication) []byte {
	const width, left, top, cellWidth, cellHeight = 1000, 205, 104, 110, 46
	height := top + len(headlineOperations)*cellHeight + 28
	var out strings.Builder
	fmt.Fprintf(&out, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" role="img" aria-labelledby="title desc">`, width, height, width, height)
	out.WriteString(`<title id="title">Per-corpus speedup over encoding/json</title><desc id="desc">Every cell is the encoding/json time divided by the simdjson time for one operation and corpus. One times is equal performance; larger values mean simdjson is faster.</desc>`)
	out.WriteString(chartStyle)
	out.WriteString(`<text class="heading" x="0" y="20">Per-corpus speedup over encoding/json</text><text class="muted note" x="0" y="40">encoding/json time ÷ simdjson time · 1× is equal · higher is faster</text>`)
	for column, corpus := range corpusOrder {
		x := left + column*cellWidth + cellWidth/2
		label := strings.ReplaceAll(corpusLabels[corpus], " ", "\n")
		parts := strings.SplitN(label, "\n", 2)
		fmt.Fprintf(&out, `<text x="%d" y="62" text-anchor="middle">%s</text>`, x, html.EscapeString(parts[0]))
		if len(parts) == 2 {
			fmt.Fprintf(&out, `<text class="muted" x="%d" y="79" text-anchor="middle">%s</text>`, x, html.EscapeString(parts[1]))
		}
	}
	for row, spec := range headlineOperations {
		y := top + row*cellHeight
		fmt.Fprintf(&out, `<text x="%d" y="%d" text-anchor="end">%s</text>`, left-12, y+28, html.EscapeString(spec.Label))
		for column, corpus := range corpusOrder {
			stdlib := metricFor(p, "simd", corpus, spec.Group, "encoding-json").NsPerOp
			ours := metricFor(p, "simd", corpus, spec.Group, spec.Impl).NsPerOp
			ratio := stdlib / ours
			class := "h1"
			switch {
			case ratio >= 8:
				class = "h4"
			case ratio >= 4:
				class = "h3"
			case ratio >= 2:
				class = "h2"
			}
			x := left + column*cellWidth
			fmt.Fprintf(&out, `<rect class="%s" x="%d" y="%d" width="%d" height="%d" rx="3"/><text x="%d" y="%d" text-anchor="middle">%.2f×</text>`, class, x+2, y+2, cellWidth-5, cellHeight-5, x+cellWidth/2, y+29, ratio)
		}
	}
	out.WriteString(`</svg>`)
	return []byte(out.String())
}

func renderSIMDSVG(p Publication) []byte {
	const width, left, right = 1000, 220, 160
	height := 104 + len(simdControls)*36
	values := make([]float64, len(simdControls))
	wins := make([]int, len(simdControls))
	maxValue := 1.0
	for i, spec := range simdControls {
		wins[i], values[i] = simdControl(p, spec)
		maxValue = math.Max(maxValue, values[i])
	}
	maxValue = math.Ceil(maxValue*2) / 2
	plotWidth := float64(width - left - right)
	var out strings.Builder
	fmt.Fprintf(&out, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" role="img" aria-labelledby="title desc">`, width, height, width, height)
	out.WriteString(`<title id="title">SIMD uplift over portable Go</title><desc id="desc">Geometric mean portable-Go time divided by SIMD time, with the number of corpus wins labeled on every bar. One times is equal performance; larger values mean SIMD is faster.</desc>`)
	out.WriteString(chartStyle)
	out.WriteString(`<text class="heading" x="0" y="20">SIMD uplift over portable Go</text><text class="muted note" x="0" y="40">portable-Go time ÷ SIMD time · 1× is equal · higher is faster</text>`)
	for tick := 0.0; tick <= maxValue+0.001; tick += 0.5 {
		x := float64(left) + tick/maxValue*plotWidth
		class := "grid"
		label := fmt.Sprintf("%.1f×", tick)
		if math.Abs(tick-1) < 0.001 {
			class = "baseline"
			label = "1× equal"
		}
		fmt.Fprintf(&out, `<line class="%s" x1="%.1f" y1="70" x2="%.1f" y2="%d"/><text class="muted note" x="%.1f" y="62" text-anchor="middle">%s</text>`, class, x, x, height-20, x, label)
	}
	for row, spec := range simdControls {
		y := 81 + row*36
		barWidth := values[row] / maxValue * plotWidth
		fmt.Fprintf(&out, `<text x="%d" y="%d" text-anchor="end">%s</text><rect class="s1" x="%d" y="%d" width="%.1f" height="18" rx="3"/><text x="%.1f" y="%d">%.3f× · wins %d of 7</text>`, left-12, y+14, html.EscapeString(spec.Label), left, y, barWidth, float64(left)+barWidth+7, y+14, values[row], wins[row])
	}
	out.WriteString(`</svg>`)
	return []byte(out.String())
}

func renderCrosslangSVG(p Publication) []byte {
	const width, left, right = 1000, 180, 360
	height := 114 + len(corpusOrder)*46
	ratios := make([]float64, len(corpusOrder))
	maxValue := 1.0
	for i, corpus := range corpusOrder {
		cpp, _ := p.crosslang("cpp", corpus)
		goResult, _ := p.crosslang("go", corpus)
		ratios[i] = cpp.NsPerOp / goResult.NsPerOp
		maxValue = math.Max(maxValue, ratios[i])
	}
	maxValue = math.Ceil(maxValue*2) / 2
	plotWidth := float64(width - left - right)
	var out strings.Builder
	fmt.Fprintf(&out, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" role="img" aria-labelledby="title desc">`, width, height, width, height)
	out.WriteString(`<title id="title">Go speedup over C++ semantic traversal</title><desc id="desc">C++ time divided by Go time for the identical parse and semantic digest contract. One times is equal performance; larger values mean Go is faster. Both absolute times are labeled.</desc>`)
	out.WriteString(chartStyle)
	out.WriteString(`<text class="heading" x="0" y="20">Go speedup over C++</text><text class="muted note" x="0" y="40">C++ time ÷ Go time · 1× is equal · higher is faster</text>`)
	for tick := 0.0; tick <= maxValue+0.001; tick += 0.5 {
		x := float64(left) + tick/maxValue*plotWidth
		class := "grid"
		label := fmt.Sprintf("%.1f×", tick)
		if math.Abs(tick-1) < 0.001 {
			class = "baseline"
			label = "1× equal"
		}
		fmt.Fprintf(&out, `<line class="%s" x1="%.1f" y1="70" x2="%.1f" y2="%d"/><text class="muted note" x="%.1f" y="62" text-anchor="middle">%s</text>`, class, x, x, height-18, x, label)
	}
	for row, corpus := range corpusOrder {
		cpp, _ := p.crosslang("cpp", corpus)
		goResult, _ := p.crosslang("go", corpus)
		y := 81 + row*46
		barWidth := ratios[row] / maxValue * plotWidth
		class := "s1"
		if ratios[row] < 1 {
			class = "s2"
		}
		fmt.Fprintf(&out, `<text x="%d" y="%d" text-anchor="end">%s</text><rect class="%s" x="%d" y="%d" width="%.1f" height="20" rx="3"/><text x="%.1f" y="%d">%.3f× · Go %s · C++ %s</text>`, left-12, y+15, html.EscapeString(corpusLabels[corpus]), class, left, y, barWidth, float64(left)+barWidth+7, y+15, ratios[row], formatCrossDuration(goResult.NsPerOp), formatCrossDuration(cpp.NsPerOp))
	}
	out.WriteString(`</svg>`)
	return []byte(out.String())
}
