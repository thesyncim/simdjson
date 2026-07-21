package pgbaseline

import (
	"fmt"
	"sort"
	"strings"
)

// This file joins the three phase-0 artifacts — the corpus manifests, our
// measurements (ours.json), and the parsed PostgreSQL session logs — into
// the acceptance report. The report is regenerated, never hand-edited;
// misses stay in it with their causes.

// pgMetrics is one corpus's PostgreSQL numbers, derived from its log.
type pgMetrics struct {
	copyMS, createOps, createOpsNoFu, createPath    float64
	sizeTable, sizeGinOps, sizeGinOpsNoFu           int64
	sizeGinPath                                     int64
	extractSeqMS, ctidMS                            float64
	existSeqMS, existGinOpsMS                       float64
	containSeqMS, containGinOpsMS, containGinPathMS float64
	rowcount, extractHits, existCount, containCount int64
}

func derivePG(l PGLog) pgMetrics {
	var m pgMetrics
	m.copyMS, _ = l.StepMS("copy")
	m.createOps, _ = l.StepMS("create_gin_ops_fastupdate_on")
	m.createOpsNoFu, _ = l.StepMS("create_gin_ops_fastupdate_off")
	m.createPath, _ = l.StepMS("create_gin_path_ops")
	m.sizeTable = l.Values["size_table"]
	m.sizeGinOps = l.Values["size_gin_ops"]
	m.sizeGinOpsNoFu = l.Values["size_gin_ops_fastupdate_off"]
	m.sizeGinPath = l.Values["size_gin_path_ops"]
	m.extractSeqMS, _ = l.QueryMS("q_extract_seq")
	m.ctidMS, _ = l.QueryMS("q_extract_ctid")
	m.existSeqMS, _ = l.QueryMS("q_exist_seq")
	m.existGinOpsMS, _ = l.QueryMS("q_exist_gin_ops")
	m.containSeqMS, _ = l.QueryMS("q_contain_seq")
	m.containGinOpsMS, _ = l.QueryMS("q_contain_gin_ops")
	m.containGinPathMS, _ = l.QueryMS("q_contain_gin_path_ops")
	m.rowcount = l.Values["rowcount"]
	m.extractHits = l.Values["q_extract_seq"]
	m.existCount = l.Values["q_exist_seq"]
	m.containCount = l.Values["q_contain_seq"]
	return m
}

// bestMS returns the smallest positive sample and its label.
func bestMS(pairs ...struct {
	ms    float64
	label string
}) (float64, string) {
	best, label := 0.0, ""
	for _, p := range pairs {
		if p.ms > 0 && (best == 0 || p.ms < best) {
			best, label = p.ms, p.label
		}
	}
	return best, label
}

func msLabel(ms float64, label string) struct {
	ms    float64
	label string
} {
	return struct {
		ms    float64
		label string
	}{ms, label}
}

func (c OursCorpus) variant(hashKeys bool) *OursVariant {
	for i := range c.Variants {
		if c.Variants[i].HashKeys == hashKeys {
			return &c.Variants[i]
		}
	}
	return nil
}

// bestExtractNS is the variant's best whole-corpus extraction pass.
func bestExtractNS(v *OursVariant) (int64, string) {
	if v.ExtractColumnNS > 0 && v.ExtractColumnNS < v.ExtractPointerNS {
		return v.ExtractColumnNS, "shape column"
	}
	return v.ExtractPointerNS, "pointer"
}

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

func fmtMS(ms float64) string {
	if ms <= 0 {
		return "n/a"
	}
	if ms >= 10000 {
		return fmt.Sprintf("%.1f s", ms/1000)
	}
	return fmt.Sprintf("%.1f ms", ms)
}

func fmtNSPerDoc(totalNS int64, docs int) string {
	if totalNS <= 0 || docs <= 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.0f ns", float64(totalNS)/float64(docs))
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

// ratios computes every acceptance-relevant ratio for one corpus; zero
// means not measurable (missing PG log or skipped query).
type ratios struct {
	space   float64 // ours/PG, lower better
	ingest  float64 // ours/PG throughput, higher better
	extract float64 // PG/ours per doc, higher better
	exist   float64 // PG/ours whole corpus, higher better
	contain float64 // PG/ours whole corpus, higher better
}

func deriveRatios(c OursCorpus, pg *pgMetrics) ratios {
	var r ratios
	v := c.variant(true)
	if v == nil || pg == nil {
		return r
	}
	docs := int64(c.Manifest.Docs)
	if pgSpace := pg.sizeTable + pg.sizeGinPath; pgSpace > 0 && v.RetainedBytes > 0 {
		r.space = float64(v.RetainedBytes) / float64(pgSpace)
	}
	if pgIngestMS := pg.copyMS + pg.createPath; pgIngestMS > 0 && v.IngestNS > 0 {
		r.ingest = pgIngestMS * 1e6 / float64(v.IngestNS)
	}
	if ours, _ := bestExtractNS(v); pg.extractSeqMS > 0 && ours > 0 && docs > 0 {
		r.extract = pg.extractSeqMS * 1e6 / float64(ours)
	}
	if pgBest, _ := bestMS(msLabel(pg.existSeqMS, "seq"), msLabel(pg.existGinOpsMS, "gin jsonb_ops")); pgBest > 0 && v.ExistNS > 0 {
		r.exist = pgBest * 1e6 / float64(v.ExistNS)
	}
	if pgBest, _ := bestMS(msLabel(pg.containSeqMS, "seq"), msLabel(pg.containGinOpsMS, "gin jsonb_ops"), msLabel(pg.containGinPathMS, "gin jsonb_path_ops")); pgBest > 0 && v.ContainNS > 0 {
		r.contain = pgBest * 1e6 / float64(v.ContainNS)
	}
	return r
}

// BuildReport renders the phase-0 acceptance report. logs maps corpus name
// to its parsed PostgreSQL session; corpora without a log get "n/a" PG
// columns, and the header says so.
func BuildReport(res OursResults, logs map[string]PGLog) string {
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }

	corpora := append([]OursCorpus(nil), res.Corpora...)
	sort.Slice(corpora, func(i, j int) bool { return corpora[i].Manifest.Name < corpora[j].Manifest.Name })

	pgm := map[string]pgMetrics{}
	var anyLog *PGLog
	for name := range logs {
		l := logs[name]
		pgm[name] = derivePG(l)
		anyLog = &l
	}

	w("# ADR 0002 phase 0: PostgreSQL comparison baseline\n\n")
	w("Regenerate with `pgbaseline gen`, `run-pg.sh`, `pgbaseline ours`, and\n")
	w("`pgbaseline report`; see METHODOLOGY.md. Targets come from ADR 0002 and\n")
	w("bind the design, not this report: misses stay listed with causes.\n\n")

	w("## Environment\n\n")
	w("- ours: %s, %s, single goroutine, native process\n", res.GoVersion, res.GOARCH)
	if anyLog != nil {
		w("- PostgreSQL: %s\n", anyLog.Version)
		keys := make([]string, 0, len(anyLog.Settings))
		for k := range anyLog.Settings {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			w("- PG setting: %s = %s\n", k, anyLog.Settings[k])
		}
		w("- PG runs in a Linux container (Docker); ours runs natively on the host. Same hardware, different kernels.\n")
	} else {
		w("- PostgreSQL: **no session logs found** — PG columns are n/a; run run-pg.sh and regenerate\n")
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
	w("Ours is the measured live-heap delta of the DocSet (arenas, headers,\nslack); modeled is source + 16 B/entry, the analytic floor. PG sizes are\nafter VACUUM ANALYZE.\n\n")
	w("| corpus | ours (hash) | ours (nohash) | modeled | PG table | PG gin path_ops | PG gin ops | ours/PG(table+path) |\n")
	w("|---|---:|---:|---:|---:|---:|---:|---:|\n")
	for _, c := range corpora {
		m := c.Manifest
		vh, vn := c.variant(true), c.variant(false)
		pg, ok := pgm[m.Name]
		row := deriveRatios(c, &pg)
		pgTable, pgPath, pgOps, ratio := "n/a", "n/a", "n/a", "n/a"
		if ok {
			pgTable, pgPath, pgOps = fmtMiB(pg.sizeTable), fmtMiB(pg.sizeGinPath), fmtMiB(pg.sizeGinOps)
			ratio = fmtRatio(row.space)
		}
		w("| %s | %s | %s | %s | %s | %s | %s | %s |\n",
			m.Name, fmtMiB(vh.RetainedBytes), fmtMiB(vn.RetainedBytes), fmtMiB(vh.ModeledBytes),
			pgTable, pgPath, pgOps, ratio)
	}
	w("\n")

	w("## Ingest\n\n")
	w("Single core both sides. Ours: ReadFrom (validate + index + arena copy;\nno key postings yet). PG: COPY, then each CREATE INDEX separately.\n\n")
	w("| corpus | ours (hash) | ours (nohash) | PG COPY | + gin path_ops build | gin ops build (fu=on) | (fu=off) | ours/(COPY+path) |\n")
	w("|---|---:|---:|---:|---:|---:|---:|---:|\n")
	for _, c := range corpora {
		m := c.Manifest
		vh, vn := c.variant(true), c.variant(false)
		pg, ok := pgm[m.Name]
		row := deriveRatios(c, &pg)
		copyC, pathC, opsC, opsNoFuC, ratio := "n/a", "n/a", "n/a", "n/a", "n/a"
		if ok {
			copyC = fmtMS(pg.copyMS)
			pathC = fmtMS(pg.createPath)
			opsC = fmtMS(pg.createOps)
			opsNoFuC = fmtMS(pg.createOpsNoFu)
			ratio = fmtRatio(row.ingest)
		}
		w("| %s | %s | %s | %s | %s | %s | %s | %s |\n",
			m.Name, fmtMBps(m.SourceBytes, vh.IngestNS), fmtMBps(m.SourceBytes, vn.IngestNS),
			copyC, pathC, opsC, opsNoFuC, ratio)
	}
	w("\n")

	w("## Point extraction\n\n")
	w("Full scan of one top-level field, per document. PG: `SELECT\ncount(doc->>'f') FROM t` over N rows. Ours: AppendPointer and, on\nclustered corpora, the ShapeCache column path. Single row: PG by ctid vs\nours Doc(i)+PointerCompiled.\n\n")
	w("| corpus | ours pointer | ours column | PG seq per row | PG/ours | PG ctid row | ours single doc |\n")
	w("|---|---:|---:|---:|---:|---:|---:|\n")
	for _, c := range corpora {
		m := c.Manifest
		v := c.variant(true)
		pg, ok := pgm[m.Name]
		row := deriveRatios(c, &pg)
		colNS := "n/a"
		if v.ExtractColumnNS > 0 {
			colNS = fmtNSPerDoc(v.ExtractColumnNS, m.Docs)
		}
		pgRow, ratio, ctid := "n/a", "n/a", "n/a"
		if ok {
			pgRow = fmtNSPerDoc(int64(pg.extractSeqMS*1e6), m.Docs)
			ratio = fmtRatio(row.extract)
			ctid = fmtMS(pg.ctidMS)
		}
		w("| %s | %s | %s | %s | %s | %s | %d ns |\n",
			m.Name, fmtNSPerDoc(v.ExtractPointerNS, m.Docs), colNS, pgRow, ratio, ctid, v.SingleDocNS)
	}
	w("\n")

	w("## Existence and containment\n\n")
	w("Whole-corpus counts. Ours is a full column scan (the pre-phase-4\nbaseline: no postings, no pruning). PG existence `doc ? 'k'` can use gin\njsonb_ops; containment `doc @> '{\"k\":\"v\"}'` can use either gin index.\n\n")
	w("| corpus | ours exist | PG exist seq | PG exist gin | PG/ours | ours contain | PG contain seq | PG gin ops | PG gin path | PG/ours |\n")
	w("|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|\n")
	for _, c := range corpora {
		m := c.Manifest
		v := c.variant(true)
		pg, ok := pgm[m.Name]
		row := deriveRatios(c, &pg)
		cols := []string{
			fmtMS(float64(v.ExistNS) / 1e6), "n/a", "n/a", "n/a",
			"n/a", "n/a", "n/a", "n/a", "n/a",
		}
		if v.ContainNS > 0 {
			cols[4] = fmtMS(float64(v.ContainNS) / 1e6)
		}
		if ok {
			cols[1], cols[2], cols[3] = fmtMS(pg.existSeqMS), fmtMS(pg.existGinOpsMS), fmtRatio(row.exist)
			cols[5], cols[6], cols[7] = fmtMS(pg.containSeqMS), fmtMS(pg.containGinOpsMS), fmtMS(pg.containGinPathMS)
			cols[8] = fmtRatio(row.contain)
		}
		w("| %s | %s |\n", m.Name, strings.Join(cols, " | "))
	}
	w("\n")

	w("## Acceptance table\n\n")
	w("| target | corpus | measured | bound | status |\n")
	w("|---|---|---:|---:|---|\n")
	for _, t := range Targets {
		for _, c := range corpora {
			m := c.Manifest
			if t.Class != "" && t.Class != m.Class {
				continue
			}
			pg, ok := pgm[m.Name]
			var measured float64
			bound, dir := t.Bound, ">="
			if ok {
				row := deriveRatios(c, &pg)
				switch t.Kind {
				case TargetSpace:
					measured, dir = row.space, "<="
				case TargetIngest:
					measured = row.ingest
				case TargetExtract:
					measured = row.extract
				case TargetExist:
					measured = row.exist
				case TargetContain:
					measured = row.contain
				}
			}
			status := "no PG run"
			if measured > 0 {
				met := measured >= bound
				if dir == "<=" {
					met = measured <= bound
				}
				switch {
				case met:
					status = "met"
				case t.Phase > 0:
					status = fmt.Sprintf("missed (phase %d pending)", t.Phase)
				default:
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
	w("\n")

	w("## Verification\n\n")
	w("Both engines must agree with the generator's expected counts before any\nratio is meaningful. Ours-side counts were verified during measurement;\nthis table cross-checks PostgreSQL's query results.\n\n")
	w("| corpus | check | expected | PG | status |\n")
	w("|---|---|---:|---:|---|\n")
	verify := func(name, check string, want, got int64, ok bool) {
		if !ok {
			w("| %s | %s | %d | n/a | no PG run |\n", name, check, want)
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
		pg, ok := pgm[m.Name]
		verify(m.Name, "rowcount", int64(m.Docs), pg.rowcount, ok)
		verify(m.Name, "extract hits", int64(m.ExtractHits), pg.extractHits, ok)
		verify(m.Name, "exist count", int64(m.ExistExpected), pg.existCount, ok)
		if m.ContainKey != "" {
			verify(m.Name, "contain count", int64(m.ContainExpected), pg.containCount, ok)
		}
	}
	w("\n")

	w("## Honesty notes\n\n")
	w("- The comparison is library vs full DBMS: PostgreSQL pays for tuple\n")
	w("  headers, buffer management, WAL, and a client protocol that we do not.\n")
	w("  The consuming engine must spend our margin on those and still win.\n")
	w("- Our ingest builds the structural tape (and optional key hashes) but no\n")
	w("  key postings; PostgreSQL's CREATE INDEX builds a queryable GIN. The\n")
	w("  existence/containment rows show what each side bought: PG existence\n")
	w("  with gin jsonb_ops beats our full scan wherever selectivity is low —\n")
	w("  until phase 4 lands postings, that loss is structural.\n")
	w("- Space today is the classic tape: 16 B/entry on top of the source, so\n")
	w("  the ADR predicts we lose the space rows at phase 0. That starting\n")
	w("  point is the point of this report.\n")
	for _, c := range corpora {
		m := c.Manifest
		if pg, ok := pgm[m.Name]; ok && pg.sizeTable > 0 && pg.sizeTable < m.SourceBytes {
			w("- %s: PG's table (%s) is smaller than the minified source (%s) —\n  TOAST compresses large documents; we store exact source bytes.\n",
				m.Name, fmtMiB(pg.sizeTable), fmtMiB(m.SourceBytes))
		}
	}
	w("- PostgreSQL runs single-backend with parallelism disabled\n")
	w("  (max_parallel_workers_per_gather=0, max_parallel_maintenance_workers=0)\n")
	w("  per the single-core comparison rule; the planner is otherwise free, and\n")
	w("  the EXPLAIN captures in the session logs show which plan actually ran.\n")
	return b.String()
}
