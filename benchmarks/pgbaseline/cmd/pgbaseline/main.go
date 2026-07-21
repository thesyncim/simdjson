// Command pgbaseline drives the ADR 0002 phase-0 comparison harness.
//
// Usage:
//
//	pgbaseline gen [-dir corpora] [-docs N] [-docbytes N] [-realbytes N] [-only name]
//	pgbaseline ours [-dir corpora] [-out results/ours.json] [-reps N] [-only name]
//	pgbaseline report [-dir corpora] [-pg results/pg] [-ours results/ours.json] [-out results/phase0-report.md]
//
// gen writes the phase-0 corpus set (NDJSON for our side, COPY text format
// for PostgreSQL, and the shared query manifests). run-pg.sh runs the
// PostgreSQL protocol over the generated corpora. ours measures this
// library's side. report joins everything into the acceptance report.
//
// The full corpus set is several gigabytes and a full run takes tens of
// minutes; nothing in this module's tests invokes any of it.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/thesyncim/simdjson/benchmarks/pgbaseline"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: pgbaseline gen|ours|report [flags]")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "gen":
		err = runGen(os.Args[2:])
	case "ours":
		err = runOurs(os.Args[2:])
	case "report":
		err = runReport(os.Args[2:])
	default:
		err = fmt.Errorf("unknown subcommand %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "pgbaseline:", err)
		os.Exit(1)
	}
}

// specs returns the phase-0 corpus set: the shape-clustered synthetics at
// each shape count, the adversarially heterogeneous set, and the
// real-corpus derivations. Seeds are fixed; the corpora are deterministic.
func synthSpecs(docs, docBytes int) []pgbaseline.SynthSpec {
	return []pgbaseline.SynthSpec{
		{Name: "synth_s1", Docs: docs, Shapes: 1, DocBytes: docBytes, Seed: 1},
		{Name: "synth_s4", Docs: docs, Shapes: 4, DocBytes: docBytes, Seed: 4},
		{Name: "synth_s64", Docs: docs, Shapes: 64, DocBytes: docBytes, Seed: 64},
		{Name: "synth_hetero", Docs: docs, Hetero: true, DocBytes: docBytes, Seed: 99},
	}
}

func realSpecs(targetBytes int64) []pgbaseline.RealSpec {
	return []pgbaseline.RealSpec{
		{
			// Individual tweets: the natural document unit of the twitter
			// corpus, a real shape-clustered workload.
			Name: "twitter_tweets", Corpus: "twitter_status.json.zst",
			RecordsField: "statuses", TargetBytes: targetBytes,
			ExtractField: "id_str", ExistKey: "possibly_sensitive",
			ContainKey: "lang", ContainValue: "ja",
		},
		{
			// Individual performances from the CITM catalog.
			Name: "citm_perf", Corpus: "citm_catalog.json.zst",
			RecordsField: "performances", TargetBytes: targetBytes,
			ExtractField: "venueCode", ExistKey: "name",
			ContainKey: "venueCode", ContainValue: "PLEYEL_PLEYEL",
		},
		{
			// The whole twitter document replicated: large-document
			// handling, and on the PostgreSQL side TOAST storage. No
			// containment: the document has no top-level scalar field.
			Name: "twitter_whole", Corpus: "twitter_status.json.zst",
			TargetBytes:  targetBytes,
			ExtractField: "search_metadata", ExistKey: "statuses",
		},
	}
}

func runGen(args []string) error {
	fs := flag.NewFlagSet("gen", flag.ExitOnError)
	dir := fs.String("dir", "corpora", "output directory")
	docs := fs.Int("docs", 1_000_000, "documents per synthetic corpus")
	docBytes := fs.Int("docbytes", 400, "target synthetic document size in bytes")
	realBytes := fs.Int64("realbytes", 128<<20, "target minified bytes per real-derived corpus")
	only := fs.String("only", "", "generate only the named corpus")
	fs.Parse(args)

	for _, spec := range synthSpecs(*docs, *docBytes) {
		if *only != "" && spec.Name != *only {
			continue
		}
		m, err := pgbaseline.GenerateSynthetic(filepath.Join(*dir, spec.Name), spec)
		if err != nil {
			return err
		}
		fmt.Printf("%s: %d docs, %d shapes, %d bytes minified\n", m.Name, m.Docs, m.ShapeCount, m.SourceBytes)
	}
	for _, spec := range realSpecs(*realBytes) {
		if *only != "" && spec.Name != *only {
			continue
		}
		m, err := pgbaseline.GenerateReal(filepath.Join(*dir, spec.Name), spec)
		if err != nil {
			return err
		}
		fmt.Printf("%s: %d docs, %d bytes minified (pretty source %d)\n", m.Name, m.Docs, m.SourceBytes, m.PrettyBytes)
	}
	return nil
}

func corpusDirs(dir, only string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var dirs []string
	for _, e := range entries {
		if !e.IsDir() || (only != "" && e.Name() != only) {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, e.Name(), "manifest.json")); err == nil {
			dirs = append(dirs, filepath.Join(dir, e.Name()))
		}
	}
	if len(dirs) == 0 {
		return nil, fmt.Errorf("no generated corpora under %s (run pgbaseline gen)", dir)
	}
	return dirs, nil
}

func runOurs(args []string) error {
	fs := flag.NewFlagSet("ours", flag.ExitOnError)
	dir := fs.String("dir", "corpora", "corpus directory")
	out := fs.String("out", "results/ours.json", "output file")
	reps := fs.Int("reps", 3, "repetitions per timed section (minimum wins)")
	only := fs.String("only", "", "measure only the named corpus")
	fs.Parse(args)

	dirs, err := corpusDirs(*dir, *only)
	if err != nil {
		return err
	}
	res := pgbaseline.OursResults{GoVersion: runtime.Version(), GOARCH: runtime.GOARCH}
	for _, d := range dirs {
		fmt.Printf("measuring %s...\n", d)
		c, err := pgbaseline.MeasureDir(d, *reps)
		if err != nil {
			return err
		}
		res.Corpora = append(res.Corpora, c)
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		return err
	}
	js, err := json.MarshalIndent(res, "", "\t")
	if err != nil {
		return err
	}
	return os.WriteFile(*out, append(js, '\n'), 0o644)
}

func runReport(args []string) error {
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	dir := fs.String("dir", "corpora", "corpus directory (unused placeholder for symmetry)")
	pgDir := fs.String("pg", "results/pg", "directory of run-pg.sh session logs")
	oursPath := fs.String("ours", "results/ours.json", "ours.json path")
	out := fs.String("out", "results/phase0-report.md", "report output path")
	fs.Parse(args)
	_ = dir

	js, err := os.ReadFile(*oursPath)
	if err != nil {
		return err
	}
	var res pgbaseline.OursResults
	if err := json.Unmarshal(js, &res); err != nil {
		return fmt.Errorf("%s: %v", *oursPath, err)
	}
	logs := map[string]pgbaseline.PGLog{}
	if entries, err := os.ReadDir(*pgDir); err == nil {
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".log") {
				continue
			}
			l, err := pgbaseline.ParsePGLog(filepath.Join(*pgDir, e.Name()))
			if err != nil {
				return err
			}
			logs[strings.TrimSuffix(e.Name(), ".log")] = l
		}
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		return err
	}
	return os.WriteFile(*out, []byte(pgbaseline.BuildReport(res, logs)), 0o644)
}
