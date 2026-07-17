package main

import (
	"fmt"
	"html"
	"math"
	"strings"
)

const chartStyle = `<style>
:root { color-scheme: light dark; --fg:#24292f; --muted:#57606a; --grid:#d0d7de; --s1:#0969da; --s2:#8250df; --s3:#1a7f37; --h1:#ddf4ff; --h2:#b6e3ff; --h3:#80ccff; --h4:#54aeff; }
@media (prefers-color-scheme: dark) { :root { --fg:#f0f6fc; --muted:#8c959f; --grid:#30363d; --s1:#58a6ff; --s2:#bc8cff; --s3:#3fb950; --h1:#102c44; --h2:#123c5a; --h3:#15557a; --h4:#1f6feb; } }
text { fill:var(--fg); font:13px -apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif; }
.muted { fill:var(--muted); } .grid { stroke:var(--grid); stroke-width:1; }
.s1 { fill:var(--s1); } .s2 { fill:var(--s2); } .s3 { fill:var(--s3); }
.h1 { fill:var(--h1); } .h2 { fill:var(--h2); } .h3 { fill:var(--h3); } .h4 { fill:var(--h4); }
</style>`

func renderHeadlineSVG(p Publication) []byte {
	const width, left, right = 1000, 210, 90
	height := 90 + len(headlineOperations)*54
	values := make([][3]float64, len(headlineOperations))
	maxValue := 1.0
	for i, spec := range headlineOperations {
		values[i] = [3]float64{operationGeomean(p, spec, "stdlib"), operationGeomean(p, spec, "rival"), operationGeomean(p, spec, "simd")}
		for _, value := range values[i] {
			maxValue = math.Max(maxValue, value)
		}
	}
	maxValue = math.Ceil(maxValue)
	plotWidth := float64(width - left - right)
	var out strings.Builder
	fmt.Fprintf(&out, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" role="img" aria-labelledby="title desc">`, width, height, width, height)
	out.WriteString(`<title id="title">Headline benchmark speedups</title><desc id="desc">Geometric mean speedups over encoding/json, the fastest compatible Go rival, and pure Go. Larger bars are faster.</desc>`)
	out.WriteString(chartStyle)
	for tick := 0; tick <= int(maxValue); tick++ {
		x := float64(left) + float64(tick)/maxValue*plotWidth
		fmt.Fprintf(&out, `<line class="grid" x1="%.1f" y1="58" x2="%.1f" y2="%d"/><text class="muted" x="%.1f" y="48" text-anchor="middle">%dx</text>`, x, x, height-24, x, tick)
	}
	legend := []struct{ label, class string }{{"vs encoding/json", "s1"}, {"vs fastest rival", "s2"}, {"SIMD vs pure Go", "s3"}}
	for i, item := range legend {
		x := left + i*190
		fmt.Fprintf(&out, `<rect class="%s" x="%d" y="12" width="12" height="12" rx="2"/><text x="%d" y="23">%s</text>`, item.class, x, x+18, item.label)
	}
	classes := []string{"s1", "s2", "s3"}
	for row, spec := range headlineOperations {
		y := 68 + row*54
		fmt.Fprintf(&out, `<text x="%d" y="%d" text-anchor="end">%s</text>`, left-12, y+23, html.EscapeString(spec.Label))
		for series, value := range values[row] {
			if value == 0 {
				continue
			}
			barWidth := value / maxValue * plotWidth
			barY := y + series*13
			fmt.Fprintf(&out, `<rect class="%s" x="%d" y="%d" width="%.1f" height="9" rx="2"/><text x="%.1f" y="%d">%.3fx</text>`, classes[series], left, barY, barWidth, float64(left)+barWidth+6, barY+9, value)
		}
	}
	out.WriteString(`</svg>`)
	return []byte(out.String())
}

func renderCorpusSVG(p Publication) []byte {
	const width, left, top, cellWidth, cellHeight = 1000, 205, 78, 110, 46
	height := top + len(headlineOperations)*cellHeight + 44
	var out strings.Builder
	fmt.Fprintf(&out, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" role="img" aria-labelledby="title desc">`, width, height, width, height)
	out.WriteString(`<title id="title">Per-corpus speedup over encoding/json</title><desc id="desc">Every cell is the encoding/json time divided by the simdjson time for one operation and corpus. Values above one are faster.</desc>`)
	out.WriteString(chartStyle)
	for column, corpus := range corpusOrder {
		x := left + column*cellWidth + cellWidth/2
		label := strings.ReplaceAll(corpusLabels[corpus], " ", "\n")
		parts := strings.SplitN(label, "\n", 2)
		fmt.Fprintf(&out, `<text x="%d" y="28" text-anchor="middle">%s</text>`, x, html.EscapeString(parts[0]))
		if len(parts) == 2 {
			fmt.Fprintf(&out, `<text class="muted" x="%d" y="45" text-anchor="middle">%s</text>`, x, html.EscapeString(parts[1]))
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
			fmt.Fprintf(&out, `<rect class="%s" x="%d" y="%d" width="%d" height="%d" rx="3"/><text x="%d" y="%d" text-anchor="middle">%.2fx</text>`, class, x+2, y+2, cellWidth-5, cellHeight-5, x+cellWidth/2, y+29, ratio)
		}
	}
	out.WriteString(`</svg>`)
	return []byte(out.String())
}

func renderSIMDSVG(p Publication) []byte {
	const width, left, right = 1000, 220, 160
	height := 66 + len(simdControls)*36
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
	out.WriteString(`<title id="title">SIMD uplift by path</title><desc id="desc">Geometric mean pure-Go time divided by SIMD time, with the number of corpus wins labeled on every bar.</desc>`)
	out.WriteString(chartStyle)
	for tick := 0.0; tick <= maxValue+0.001; tick += 0.5 {
		x := float64(left) + tick/maxValue*plotWidth
		fmt.Fprintf(&out, `<line class="grid" x1="%.1f" y1="32" x2="%.1f" y2="%d"/><text class="muted" x="%.1f" y="23" text-anchor="middle">%.1fx</text>`, x, x, height-20, x, tick)
	}
	for row, spec := range simdControls {
		y := 43 + row*36
		barWidth := values[row] / maxValue * plotWidth
		fmt.Fprintf(&out, `<text x="%d" y="%d" text-anchor="end">%s</text><rect class="s1" x="%d" y="%d" width="%.1f" height="18" rx="3"/><text x="%.1f" y="%d">%.3fx · %d/7 wins</text>`, left-12, y+14, html.EscapeString(spec.Label), left, y, barWidth, float64(left)+barWidth+7, y+14, values[row], wins[row])
	}
	out.WriteString(`</svg>`)
	return []byte(out.String())
}

func renderCrosslangSVG(p Publication) []byte {
	const width, left, right = 1000, 180, 290
	height := 70 + len(corpusOrder)*46
	ratios := make([]float64, len(corpusOrder))
	maxValue := 1.0
	for i, corpus := range corpusOrder {
		cpp, _ := p.crosslang("cpp", corpus)
		goResult, _ := p.crosslang("go", corpus)
		ratios[i] = goResult.NsPerOp / cpp.NsPerOp
		maxValue = math.Max(maxValue, ratios[i])
	}
	maxValue = math.Ceil(maxValue*5) / 5
	plotWidth := float64(width - left - right)
	var out strings.Builder
	fmt.Fprintf(&out, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" role="img" aria-labelledby="title desc">`, width, height, width, height)
	out.WriteString(`<title id="title">Go versus C++ semantic traversal</title><desc id="desc">Go time divided by C++ time for the identical parse and semantic digest contract. Values below one mean Go is faster. Both absolute times are labeled.</desc>`)
	out.WriteString(chartStyle)
	baseline := float64(left) + 1/maxValue*plotWidth
	fmt.Fprintf(&out, `<line class="grid" x1="%.1f" y1="30" x2="%.1f" y2="%d"/><text class="muted" x="%.1f" y="21" text-anchor="middle">equal time</text>`, baseline, baseline, height-18, baseline)
	for row, corpus := range corpusOrder {
		cpp, _ := p.crosslang("cpp", corpus)
		goResult, _ := p.crosslang("go", corpus)
		y := 43 + row*46
		barWidth := ratios[row] / maxValue * plotWidth
		class := "s3"
		if ratios[row] > 1 {
			class = "s2"
		}
		fmt.Fprintf(&out, `<text x="%d" y="%d" text-anchor="end">%s</text><rect class="%s" x="%d" y="%d" width="%.1f" height="20" rx="3"/><text x="%.1f" y="%d">%.3fx · C++ %s · Go %s</text>`, left-12, y+15, html.EscapeString(corpusLabels[corpus]), class, left, y, barWidth, float64(left)+barWidth+7, y+15, ratios[row], formatCrossDuration(cpp.NsPerOp), formatCrossDuration(goResult.NsPerOp))
	}
	out.WriteString(`</svg>`)
	return []byte(out.String())
}
