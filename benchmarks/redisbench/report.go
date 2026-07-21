package redisbench

import (
	"fmt"
	"sort"
	"strings"
)

// This file joins the three artifacts — the corpus manifests, our
// measurements (ours.json), and the parsed RedisJSON/RediSearch session logs
// — into the scoreboard. The report is regenerated, never hand-edited; misses
// stay in it with their causes, and the expressiveness column records where
// RedisJSON needs a workaround or cannot answer at all.

const (
	mib = 1 << 20
	gib = 1 << 30
)

func fmtMiB(b int64) string {
	if b >= gib {
		return fmt.Sprintf("%.2f GiB", float64(b)/gib)
	}
	return fmt.Sprintf("%.1f MiB", float64(b)/mib)
}

// fmtMBps formats throughput for b bytes processed in ns nanoseconds.
func fmtMBps(b int64, ns int64) string {
	if ns <= 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.0f MB/s", float64(b)/float64(ns)*1e3)
}

// fmtNS renders a per-operation nanosecond cost.
func fmtNS(ns float64) string {
	if ns <= 0 {
		return "n/a"
	}
	if ns >= 1e6 {
		return fmt.Sprintf("%.1f ms", ns/1e6)
	}
	if ns >= 1e3 {
		return fmt.Sprintf("%.1f us", ns/1e3)
	}
	return fmt.Sprintf("%.0f ns", ns)
}

func fmtPerDoc(totalNS int64, docs int) string {
	if totalNS <= 0 || docs <= 0 {
		return "n/a"
	}
	return fmtNS(float64(totalNS) / float64(docs))
}

func fmtRatio(r float64) string {
	if r <= 0 {
		return "n/a"
	}
	if r >= 100 {
		return fmt.Sprintf("%.0fx", r)
	}
	return fmt.Sprintf("%.2fx", r)
}

func (c OursCorpus) variant(hashKeys, shapeTapes bool) *OursVariant {
	for i := range c.Variants {
		if c.Variants[i].HashKeys == hashKeys && c.Variants[i].ShapeTapes == shapeTapes {
			return &c.Variants[i]
		}
	}
	return nil
}

// acceptanceVariant is the configuration the scoreboard judges: the
// shape-tape mode over the enriched build when measured, otherwise the
// enriched classic build. One configuration carries every ratio — space,
// ingest, and scenarios are never cherry-picked from different variants.
func (c OursCorpus) acceptanceVariant() *OursVariant {
	if v := c.variant(true, true); v != nil {
		return v
	}
	return c.variant(true, false)
}

// bestProjectNS is the variant's best whole-corpus projection pass and its
// label; the point-read cost is SingleDocNS, reported separately.
func bestProjectNS(v *OursVariant) (int64, string) {
	if v.ProjectColumnNS > 0 && v.ProjectColumnNS < v.ProjectPointerNS {
		return v.ProjectColumnNS, "shape column"
	}
	return v.ProjectPointerNS, "pointer"
}

// redisRatios computes every scoreboard ratio for one corpus; zero means not
// measurable (missing Redis log or a skipped scenario). Speed ratios are
// Redis/ours (higher means ours is faster); space is ours/Redis (lower means
// ours is smaller).
type redisRatios struct {
	space   float64
	ingest  float64 // ours/Redis load throughput, higher better
	project float64
	filter  float64
	sum     float64
	group   float64
}

// redisProjectNS is the per-operation JSON.GET cost — a single point read's
// server-side time, directly comparable to our SingleDocNS.
func redisProjectNS(l RedisLog) float64 {
	ns, ok := l.QueryNS("projection")
	if !ok {
		return 0
	}
	return ns
}

func deriveRedisRatios(c OursCorpus, l *RedisLog) redisRatios {
	var r redisRatios
	v := c.acceptanceVariant()
	if v == nil || l == nil {
		return r
	}
	if redisSpace := l.keyspaceBytes() + l.indexBytes(); redisSpace > 0 && v.RetainedBytes > 0 {
		r.space = float64(v.RetainedBytes) / float64(redisSpace)
	}
	if loadNS := l.Values["load_ns"]; loadNS > 0 && v.IngestNS > 0 {
		// Throughput ratio over the same source bytes: Redis load time over
		// ours, so >1 means ours ingests faster.
		r.ingest = float64(loadNS) / float64(v.IngestNS)
	}
	if rp := redisProjectNS(*l); rp > 0 && v.SingleDocNS > 0 {
		r.project = rp / float64(v.SingleDocNS)
	}
	if rf, ok := l.QueryNS("filter"); ok && rf > 0 && v.FilterNS > 0 {
		r.filter = rf / float64(v.FilterNS)
	}
	if rs, ok := l.QueryNS("sum"); ok && rs > 0 && v.SumNS > 0 {
		r.sum = rs / float64(v.SumNS)
	}
	if rg, ok := l.QueryNS("groupby"); ok && rg > 0 && v.GroupNS > 0 {
		r.group = rg / float64(v.GroupNS)
	}
	return r
}

// BuildReport renders the RedisJSON/RediSearch scoreboard. logs maps corpus
// name to its parsed session; corpora without a log get "n/a" Redis columns,
// and the header says the run was protocol-only.
func BuildReport(res OursResults, logs map[string]RedisLog) string {
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }

	corpora := append([]OursCorpus(nil), res.Corpora...)
	sort.Slice(corpora, func(i, j int) bool { return corpora[i].Manifest.Name < corpora[j].Manifest.Name })

	var anyLog *RedisLog
	for name := range logs {
		l := logs[name]
		anyLog = &l
	}
	logFor := func(name string) *RedisLog {
		if l, ok := logs[name]; ok {
			return &l
		}
		return nil
	}

	w("# ADR 0003: RedisJSON + RediSearch scoreboard\n\n")
	w("Regenerate with `redisbench gen`, `run-redis.sh`, `redisbench ours`, and\n")
	w("`redisbench report`; see redis-methodology.md. The framing is single-core\n")
	w("parity on the scenarios RediSearch can express, with containment as a\n")
	w("predicate it has no operator for. Misses stay listed with causes.\n\n")

	w("## Environment\n\n")
	w("- ours: %s, %s, single goroutine, native process\n", res.GoVersion, res.GOARCH)
	if anyLog != nil {
		w("- RedisJSON/RediSearch: %s (image %s)\n", anyLog.Version, anyLog.Image)
		mods := make([]string, 0, len(anyLog.Modules))
		for name, ver := range anyLog.Modules {
			mods = append(mods, fmt.Sprintf("%s %s", name, ver))
		}
		sort.Strings(mods)
		for _, mod := range mods {
			w("- module: %s\n", mod)
		}
		w("- Redis is single-threaded for command execution; a single instance over a\n")
		w("  single connection is the fair single-core comparison. It runs in a Linux\n")
		w("  container (Docker); ours runs natively on the host. Same hardware.\n")
	} else {
		w("- RedisJSON/RediSearch: **no session logs found** — this is a protocol-only\n")
		w("  report; the Redis columns are n/a. Run run-redis.sh (docker) and regenerate.\n")
	}
	w("\n")

	w("## Corpora\n\n")
	w("Byte accounting is minified: source bytes are the exact bytes of every\ndocument, no separators, pretty-printing removed.\n\n")
	w("| corpus | class | docs | shapes | source | avg doc |\n")
	w("|---|---|---:|---:|---:|---:|\n")
	for _, c := range corpora {
		m := c.Manifest
		w("| %s | %s | %d | %d | %s | %d B |\n",
			m.Name, m.Class, m.Docs, m.ShapeCount, fmtMiB(m.SourceBytes), m.SourceBytes/int64(max(m.Docs, 1)))
	}
	w("\n")

	w("## Space at rest\n\n")
	w("Ours is the measured live-heap delta of the DocSet (arenas, headers, slack)\nunder the shape-tape mode; modeled is source + 16 B/entry (+16 B/header per\nshape-taped document). RedisJSON keyspace is used_memory attributable to the\nloaded keys; the RediSearch index is FT.INFO's reported size. The ratio is\nours vs RedisJSON keyspace + RediSearch index.\n\n")
	w("| corpus | ours (dedup) | tape cut | dedup docs | modeled | RedisJSON keyspace | RediSearch index | ours/(keyspace+index) |\n")
	w("|---|---:|---:|---:|---:|---:|---:|---:|\n")
	for _, c := range corpora {
		m := c.Manifest
		vh, vd := c.variant(true, false), c.variant(true, true)
		l := logFor(m.Name)
		row := deriveRedisRatios(c, l)
		dedup, cut, dedupDocs, dedupModeled := "n/a", "n/a", "n/a", "n/a"
		if vd != nil {
			dedup = fmtMiB(vd.RetainedBytes)
			dedupModeled = fmtMiB(vd.ModeledBytes)
			dedupDocs = fmt.Sprintf("%d%%", 100*vd.ShapeTapedDocs/int64(max(m.Docs, 1)))
			if vh != nil && vh.Entries > 0 {
				classicTape := vh.Entries * 16
				dedupTape := vd.Entries*16 + vd.ShapeTapedDocs*16
				cut = fmt.Sprintf("%.0f%%", 100*(1-float64(dedupTape)/float64(classicTape)))
			}
		}
		keyspace, index, ratio := "n/a", "n/a", "n/a"
		if l != nil {
			keyspace, index = fmtMiB(l.keyspaceBytes()), fmtMiB(l.indexBytes())
			ratio = fmtRatio(row.space)
		}
		w("| %s | %s | %s | %s | %s | %s | %s | %s |\n",
			m.Name, dedup, cut, dedupDocs, dedupModeled, keyspace, index, ratio)
	}
	w("\n")

	w("## Ingest\n\n")
	w("Single core both sides. Ours: ReadFrom (validate + index + arena copy) in\nthe shape-tape mode. Redis: redis-cli --pipe mass JSON.SET, then FT.CREATE\nplus the indexing drain. The ratio is Redis load time over ours (>1x means\nours ingests the same bytes faster); Redis also pays the RESP protocol and\nRedisJSON re-encoding that an in-process build does not.\n\n")
	w("| corpus | ours load | Redis JSON.SET load | ours/Redis | RediSearch index build |\n")
	w("|---|---:|---:|---:|---:|\n")
	for _, c := range corpora {
		m := c.Manifest
		v := c.acceptanceVariant()
		l := logFor(m.Name)
		oursLoad := "n/a"
		if v != nil {
			oursLoad = fmtMBps(m.SourceBytes, v.IngestNS)
		}
		redisLoad, ratio, indexBuild := "n/a", "n/a", "n/a"
		if l != nil {
			row := deriveRedisRatios(c, l)
			redisLoad = fmtMBps(m.SourceBytes, l.Values["load_ns"])
			ratio = fmtRatio(row.ingest)
			indexBuild = fmtNS(float64(l.Values["index_ns"]))
		}
		w("| %s | %s | %s | %s | %s |\n", m.Name, oursLoad, redisLoad, ratio, indexBuild)
	}
	w("\n")

	// The scenario matrix: speed and expressiveness, one row per scenario per
	// corpus. Expressiveness records the competitor's capability, not ours.
	w("## Scenario matrix\n\n")
	w("Speed is single-core. The ratio is Redis/ours, so >1x means ours runs the\nscenario in less time. The expressiveness column is RedisJSON/RediSearch's\ncapability: native, native but requiring a pre-declared FT.CREATE schema\n(we declare none), or not-expressible. Point projection compares our\nDoc+PointerCompiled against JSON.GET; the whole-corpus AppendField column\nprojection has no single JSON.GET analogue. Filter, sum, and group-by are\nwhole-corpus. Containment is the object {ContainKey: ContainValue} against\nevery document — RedisJSON and RediSearch have no containment operator.\n\n")
	w("| corpus | scenario | RedisJSON/RediSearch | ours | ratio (Redis/ours) | competitor expressiveness |\n")
	w("|---|---|---:|---:|---:|---|\n")
	for _, c := range corpora {
		m := c.Manifest
		v := c.acceptanceVariant()
		if v == nil {
			continue
		}
		l := logFor(m.Name)
		row := deriveRedisRatios(c, l)

		// projection (point read).
		redisProj, oursProj := "not run", fmtNS(float64(v.SingleDocNS))
		if l != nil {
			if rp := redisProjectNS(*l); rp > 0 {
				redisProj = fmtNS(rp)
			} else {
				redisProj = "n/a"
			}
		}
		w("| %s | point projection | %s | %s | %s | native (JSON.GET, point) |\n",
			m.Name, redisProj, oursProj, ratioCell(l, row.project))

		// whole-corpus projection (our AppendField; Redis workaround).
		wcNS, wcLabel := bestProjectNS(v)
		w("| %s | corpus projection | %s | %s | %s | workaround (FT.SEARCH RETURN, schema) |\n",
			m.Name, "not-native", fmt.Sprintf("%s (%s)", fmtPerDoc(wcNS, m.Docs), wcLabel), "capability")

		if m.ContainKey != "" {
			redisFilter := redisScenarioCell(l, "filter")
			w("| %s | filtered scan | %s | %s | %s | native (FT.SEARCH TAG, schema) |\n",
				m.Name, redisFilter, fmtNS(float64(v.FilterNS)), ratioCell(l, row.filter))

			redisGroup := redisScenarioCell(l, "groupby")
			w("| %s | group-by aggregate | %s | %s | %s | native (FT.AGGREGATE GROUPBY, schema) |\n",
				m.Name, redisGroup, fmtNS(float64(v.GroupNS)), ratioCell(l, row.group))

			w("| %s | containment @> | %s | %s | %s | **not-expressible** (no operator) |\n",
				m.Name, "not-expressible", fmtNS(float64(v.ContainNS)), "capability")
		}

		if m.SumField != "" {
			redisSum := redisScenarioCell(l, "sum")
			w("| %s | scalar aggregate SUM | %s | %s | %s | native (FT.AGGREGATE SUM, schema) |\n",
				m.Name, redisSum, fmtNS(float64(v.SumNS)), ratioCell(l, row.sum))
		}
	}
	w("\n")

	buildAcceptance(w, corpora, logs)
	buildVerification(w, corpora, logs)
	buildHonesty(w, corpora, logs)
	return b.String()
}

// ratioCell renders a speed ratio, "n/a" when there is no Redis run.
func ratioCell(l *RedisLog, r float64) string {
	if l == nil {
		return "n/a"
	}
	return fmtRatio(r)
}

// redisScenarioCell renders the per-operation Redis time for a whole-corpus
// scenario, "not run" when the log is absent.
func redisScenarioCell(l *RedisLog, label string) string {
	if l == nil {
		return "not run"
	}
	if ns, ok := l.QueryNS(label); ok {
		return fmtNS(ns)
	}
	return "n/a"
}

func buildAcceptance(w func(string, ...any), corpora []OursCorpus, logs map[string]RedisLog) {
	w("## Acceptance table\n\n")
	w("| target | corpus | measured | bound | status |\n")
	w("|---|---|---:|---:|---|\n")
	for _, t := range Targets {
		for _, c := range corpora {
			m := c.Manifest
			if t.Class != "" && t.Class != m.Class {
				continue
			}
			l, ok := logs[m.Name]
			var measured float64
			bound, dir := t.Bound, ">="
			if ok {
				row := deriveRedisRatios(c, &l)
				switch t.Kind {
				case TargetSpace:
					measured, dir = row.space, "<="
				case TargetProject:
					measured = row.project
				case TargetFilter:
					measured = row.filter
				case TargetSum:
					measured = row.sum
				case TargetGroup:
					measured = row.group
				}
			}
			status := "no redis run"
			if measured > 0 {
				met := measured >= bound
				if dir == "<=" {
					met = measured <= bound
				}
				if met {
					status = "met"
				} else {
					status = "missed"
				}
			} else if ok {
				status = "n/a"
			}
			w("| %s | %s | %s | %s %s | %s |\n",
				t.ID, m.Name, fmtRatio(measured), dir, fmtRatio(bound), status)
		}
	}
	w("\n")
	w("Target notes:\n\n")
	for _, t := range Targets {
		w("- **%s**: %s.\n", t.ID, t.Note)
	}
	w("- **containment**: RedisJSON and RediSearch have no containment operator; it is\n")
	w("  a capability we hold and they lack, reported as a scenario without a ratio.\n")
	w("\n")
}

func buildVerification(w func(string, ...any), corpora []OursCorpus, logs map[string]RedisLog) {
	w("## Verification\n\n")
	w("Both engines must agree with the generator's expected results before any\nratio is meaningful. Ours-side results were verified during measurement; this\ntable cross-checks RediSearch's returned counts and aggregates.\n\n")
	w("| corpus | check | expected | RediSearch | status |\n")
	w("|---|---|---:|---:|---|\n")
	verify := func(name, check string, want, got int64, ok bool) {
		if !ok {
			w("| %s | %s | %d | n/a | no redis run |\n", name, check, want)
			return
		}
		status := "ok"
		if want != got {
			status = "**MISMATCH**"
		}
		w("| %s | %s | %d | %d | %s |\n", name, check, want, got, status)
	}
	for _, c := range corpora {
		m := c.Manifest
		l, ok := logs[m.Name]
		verify(m.Name, "index num_docs", int64(m.Docs), l.Values["index_num_docs"], ok)
		if m.ContainKey != "" {
			verify(m.Name, "filter count", int64(m.ContainExpected), l.Results["filter"], ok)
			verify(m.Name, "group cardinality", int64(m.GroupExpected), l.Results["groupby"], ok)
		}
		if m.SumField != "" {
			verify(m.Name, "sum", m.SumExpected, l.Results["sum"], ok)
		}
	}
	w("\n")
}

func buildHonesty(w func(string, ...any), corpora []OursCorpus, logs map[string]RedisLog) {
	w("## Honesty notes\n\n")
	w("- The comparison is an embedded library vs a networked server. The scenario\n")
	w("  times are read from the Redis SLOWLOG: the server's own command execution\n")
	w("  time in microseconds, with the client round trip and process spawn\n")
	w("  excluded — Redis's fair single-core compute. Ours is in-process ns. The\n")
	w("  SLOWLOG's microsecond granularity floors sub-microsecond point reads, so\n")
	w("  the projection ratio is a lower bound on our margin. Ingest and index\n")
	w("  build, being seconds-scale, are wall-clocked instead.\n")
	w("- RediSearch needs a pre-declared FT.CREATE schema: every field a scenario\n")
	w("  touches had to be named and typed before load, and only declared fields are\n")
	w("  queryable. Our column scans need no schema and reach any path. The schema\n")
	w("  requirement is why the whole-corpus projection and every filter/aggregate\n")
	w("  row is marked as needing a declared schema.\n")
	w("- Containment (@>) has no RedisJSON or RediSearch operator. It is reported as a\n")
	w("  scenario we answer and they cannot, not as a speed ratio.\n")
	w("- RedisJSON stores an uncompressed re-encoded object per key and RediSearch a\n")
	w("  separate inverted index; the space column charges both. Our dedup mode\n")
	w("  stores value entries with keys deduplicated into the shape cache.\n")
	for _, c := range corpora {
		m := c.Manifest
		if l, ok := logs[m.Name]; ok {
			if ks := l.keyspaceBytes(); ks > 0 && ks < m.SourceBytes {
				w("- %s: RedisJSON keyspace (%s) is below the minified source (%s) — the\n  in-memory object form can undercut raw JSON on some shapes; we store exact\n  source bytes plus structure.\n",
					m.Name, fmtMiB(ks), fmtMiB(m.SourceBytes))
			}
		}
	}
}
