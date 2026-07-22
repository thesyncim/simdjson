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

// comparisonNumbers selects one internally coherent engine row. New results
// use Store for space, ingest, point reads, and query scenarios; older result
// artifacts without a Store row retain the historical DocSet fallback so the
// report format remains readable during rollout.
type comparisonNumbers struct {
	retained int64
	ingest   int64
	project  int64
	filter   int64
	sum      int64
	group    int64
	contain  int64
}

func (c OursCorpus) comparisonNumbers() comparisonNumbers {
	if s := c.Store; s != nil {
		return comparisonNumbers{
			retained: s.RetainedBytes,
			ingest:   s.IngestNS,
			project:  s.SingleDocNS,
			filter:   s.FilterNS,
			sum:      s.SumNS,
			group:    s.GroupNS,
			contain:  s.ContainNS,
		}
	}
	if v := c.acceptanceVariant(); v != nil {
		return comparisonNumbers{
			retained: v.RetainedBytes,
			ingest:   v.IngestNS,
			project:  v.SingleDocNS,
			filter:   v.FilterNS,
			sum:      v.SumNS,
			group:    v.GroupNS,
			contain:  v.ContainNS,
		}
	}
	return comparisonNumbers{}
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
	v := c.comparisonNumbers()
	if v.retained == 0 || l == nil {
		return r
	}
	if redisSpace := l.keyspaceBytes() + l.indexBytes(); redisSpace > 0 {
		r.space = float64(v.retained) / float64(redisSpace)
	}
	if loadNS := l.Values["load_ns"]; loadNS > 0 && v.ingest > 0 {
		// Throughput ratio over the same source bytes: Redis load time over
		// ours, so >1 means ours ingests faster.
		r.ingest = float64(loadNS) / float64(v.ingest)
	}
	if rp := redisProjectNS(*l); rp > 0 && v.project > 0 {
		r.project = rp / float64(v.project)
	}
	if rf, ok := l.QueryNS("filter"); ok && rf > 0 && v.filter > 0 {
		r.filter = rf / float64(v.filter)
	}
	if rs, ok := l.QueryNS("sum"); ok && rs > 0 && v.sum > 0 {
		r.sum = rs / float64(v.sum)
	}
	if rg, ok := l.QueryNS("groupby"); ok && rg > 0 && v.group > 0 {
		r.group = rg / float64(v.group)
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
	w("The comparison row is the measured live-heap delta of the keyed Store,\nincluding immutable chunks, key HAMT, snapshot metadata, and the declared exact\nindex used by the filter. The DocSet column remains a representation diagnostic,\nnot the numerator. RedisJSON keyspace is used_memory attributable to the loaded\nkeys; RediSearch index is the subsequent used_memory delta.\n\n")
	w("| corpus | keyed Store + exact index | exact index modeled | DocSet shape-dedup | tape cut | RedisJSON keyspace | RediSearch index | Store/(keyspace+index) |\n")
	w("|---|---:|---:|---:|---:|---:|---:|---:|\n")
	for _, c := range corpora {
		m := c.Manifest
		vh, vd := c.variant(true, false), c.variant(true, true)
		l := logFor(m.Name)
		row := deriveRedisRatios(c, l)
		storeBytes, indexBytes := "n/a", "n/a"
		if c.Store != nil {
			storeBytes = fmtMiB(c.Store.RetainedBytes)
			indexBytes = fmtMiB(int64(c.Store.IndexBytes))
		}
		dedup, cut := "n/a", "n/a"
		if vd != nil {
			dedup = fmtMiB(vd.RetainedBytes)
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
			m.Name, storeBytes, indexBytes, dedup, cut, keyspace, index, ratio)
	}
	w("\n")

	w("## Ingest\n\n")
	w("Single core both sides. Ours is a fresh keyed Store Put pass followed by\nCreateIndex plus full BackfillIndex; Redis is redis-cli --pipe JSON.SET followed\nby FT.CREATE and indexing drain. Index-build times are separate. The load ratio\nis Redis load time over Store load (>1x means Store is faster).\n\n")
	w("| corpus | Store load | Redis JSON.SET load | Store/Redis | Store exact-index build | RediSearch index build |\n")
	w("|---|---:|---:|---:|---:|---:|\n")
	for _, c := range corpora {
		m := c.Manifest
		v := c.comparisonNumbers()
		l := logFor(m.Name)
		oursLoad, oursIndex := "n/a", "n/a"
		if v.ingest > 0 {
			oursLoad = fmtMBps(m.SourceBytes, v.ingest)
		}
		if c.Store != nil && c.Store.IndexBuildNS > 0 {
			oursIndex = fmtNS(float64(c.Store.IndexBuildNS))
		}
		redisLoad, ratio, indexBuild := "n/a", "n/a", "n/a"
		if l != nil {
			row := deriveRedisRatios(c, l)
			redisLoad = fmtMBps(m.SourceBytes, l.Values["load_ns"])
			ratio = fmtRatio(row.ingest)
			indexBuild = fmtNS(float64(l.Values["index_ns"]))
		}
		w("| %s | %s | %s | %s | %s | %s |\n", m.Name, oursLoad, redisLoad, ratio, oursIndex, indexBuild)
	}
	w("\n")

	// The scenario matrix: speed and expressiveness, one row per scenario per
	// corpus. Expressiveness records the competitor's capability, not ours.
	w("## Scenario matrix\n\n")
	w("Speed is single-core. The ratio is Redis/ours, so >1x means Store runs the\nscenario in less time. Point projection is a warmed Snapshot.Get plus compiled\npointer. The filter uses the declared exact index matching RediSearch's declared\nTAG field and still rechecks exact JSON equality. Sum, group, and containment\nrun through RunSnapshotInto; the DocSet corpus projection remains a schema-free\ncapability row.\n\n")
	w("| corpus | scenario | RedisJSON/RediSearch | ours | ratio (Redis/ours) | competitor expressiveness |\n")
	w("|---|---|---:|---:|---:|---|\n")
	for _, c := range corpora {
		m := c.Manifest
		v := c.comparisonNumbers()
		if v.project == 0 {
			continue
		}
		l := logFor(m.Name)
		row := deriveRedisRatios(c, l)

		// projection (point read).
		redisProj, oursProj := "not run", fmtNS(float64(v.project))
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
		if dv := c.acceptanceVariant(); dv != nil {
			wcNS, wcLabel := bestProjectNS(dv)
			w("| %s | corpus projection | %s | %s | %s | workaround (FT.SEARCH RETURN, schema) |\n",
				m.Name, "not-native", fmt.Sprintf("%s (%s)", fmtPerDoc(wcNS, m.Docs), wcLabel), "capability")
		}

		if m.ContainKey != "" {
			redisFilter := redisScenarioCell(l, "filter")
			w("| %s | indexed filter | %s | %s | %s | native (FT.SEARCH TAG, schema) |\n",
				m.Name, redisFilter, fmtNS(float64(v.filter)), ratioCell(l, row.filter))

			redisGroup := redisScenarioCell(l, "groupby")
			w("| %s | group-by aggregate | %s | %s | %s | native (FT.AGGREGATE GROUPBY, schema) |\n",
				m.Name, redisGroup, fmtNS(float64(v.group)), ratioCell(l, row.group))

			w("| %s | containment @> | %s | %s | %s | **not-expressible** (no operator) |\n",
				m.Name, "not-expressible", fmtNS(float64(v.contain)), "capability")
		}

		if m.SumField != "" {
			redisSum := redisScenarioCell(l, "sum")
			w("| %s | scalar aggregate SUM | %s | %s | %s | native (FT.AGGREGATE SUM, schema) |\n",
				m.Name, redisSum, fmtNS(float64(v.sum)), ratioCell(l, row.sum))
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
	w("  queryable. The head-to-head filter declares the same field in our exact\n")
	w("  index, then performs an exact JSON scalar recheck. Our aggregate and DocSet\n")
	w("  column paths need no declaration and can reach nested RFC 6901 paths.\n")
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
