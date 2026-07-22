// Tests cover deterministic generation, strict artifact parsing, report
// accounting, Store correctness, and an opt-in pinned-DuckDB smoke.
package duckdbbench

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestGenerateSyntheticDeterministicSharedNDJSON(t *testing.T) {
	spec := SynthSpec{Name: "tiny", Docs: 64, Shapes: 4, DocBytes: 256, Seed: 42}
	a, b := filepath.Join(t.TempDir(), "a"), filepath.Join(t.TempDir(), "b")
	ma, err := GenerateSynthetic(a, spec)
	if err != nil {
		t.Fatal(err)
	}
	mb, err := GenerateSynthetic(b, spec)
	if err != nil {
		t.Fatal(err)
	}
	if ma != mb {
		t.Fatalf("manifest differs:\n%+v\n%+v", ma, mb)
	}
	adoc, err := os.ReadFile(filepath.Join(a, "docs.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	bdoc, err := os.ReadFile(filepath.Join(b, "docs.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	if string(adoc) != string(bdoc) {
		t.Fatal("same seed did not produce byte-identical NDJSON")
	}
	if ma.KeyBytes <= 0 || ma.SourceBytes <= 0 || ma.LogicalBytes() != ma.KeyBytes+ma.SourceBytes {
		t.Fatalf("bad accounting: %+v", ma)
	}
	lines := strings.Split(strings.TrimSpace(string(adoc)), "\n")
	if len(lines) != spec.Docs {
		t.Fatalf("NDJSON has %d rows, want %d", len(lines), spec.Docs)
	}
	for i, line := range lines {
		if !json.Valid([]byte(line)) {
			t.Fatalf("row %d is invalid JSON", i)
		}
	}
}

func writeDuckDBRun(t *testing.T, dir string, m Manifest) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	facts := "IMAGE duckdb/duckdb:1.5.4@sha256:test\n" +
		"VERSION v1.5.4 test\nPLATFORM linux/arm64\nCORPUS_SHA256 " + m.NDJSONSHA256 + "\n" +
		"THREADS 1\nDOCS 10\nSOURCE_BYTES " + strconv.FormatInt(m.SourceBytes, 10) + "\nKEY_BYTES " + strconv.FormatInt(m.KeyBytes, 10) + "\nDATABASE_BYTES 8192\nWAL_BYTES 0\n" +
		"MUTATION_OPS 2\nWAL_BYTES_AFTER_MUTATIONS 1024\n" +
		"RESULT docs 10\nRESULT extract_hits 10\nRESULT filter 2\n" +
		"RESULT group 3\nRESULT contain 2\nRESULT sum 55\nRESULT after_deletes 8\n"
	if err := os.WriteFile(filepath.Join(dir, "facts.log"), []byte(facts), 0o644); err != nil {
		t.Fatal(err)
	}
	profile := DuckDBProfile{
		QueryName: "SELECT count(*) FROM docs", Latency: 0.0002, CPUTime: 0.0001,
		SystemPeakBufferMemory: 4096, CumulativeRowsScanned: int64(m.Docs),
	}
	js, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{"load", "key_index", "filter_index", "point", "filter", "sum", "group", "contain", "update", "delete"} {
		content := append(append([]byte(nil), js...), '\n')
		if label == "point" {
			profile.Latency = 0.0001
			second, _ := json.Marshal(profile)
			content = append(content, second...)
			content = append(content, '\n')
			profile.Latency = 0.0002
		}
		if err := os.WriteFile(filepath.Join(dir, label+".profiles.jsons"), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestParseDuckDBRunAndVerify(t *testing.T) {
	m := Manifest{Docs: 10, SourceBytes: 1000, KeyBytes: 50, NDJSONSHA256: strings.Repeat("a", 64), ExtractHits: 10, ContainKey: "kind", ContainExpected: 2, GroupExpected: 3, SumField: "n", SumExpected: 55}
	dir := t.TempDir()
	writeDuckDBRun(t, dir, m)
	log, err := ParseDuckDBRun(dir)
	if err != nil {
		t.Fatal(err)
	}
	if bad := log.Verify(m); len(bad) != 0 {
		t.Fatalf("verification: %v", bad)
	}
	if ns, ok := log.QueryNS("point"); !ok || ns != 100_000 {
		t.Fatalf("point = %g, %v; want 100000, true", ns, ok)
	}
	if log.IndexNS() != 400_000 {
		t.Fatalf("index = %g ns, want 400000", log.IndexNS())
	}
	if log.PeakBufferBytes() != 4096 {
		t.Fatalf("peak buffers = %d", log.PeakBufferBytes())
	}
	if got, _ := log.MeanNS("update"); got != 100_000 { // one 200us profile / two operations
		t.Fatalf("update mean = %g, want 100000", got)
	}
}

func TestParseDuckDBRunRejectsMalformedArtifacts(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "facts.log"), []byte("VERSION v1\nDOCS nope\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseDuckDBRun(dir); err == nil {
		t.Fatal("malformed scalar fact accepted")
	}
}

func TestBuildReportKeepsAccountingDomainsSeparate(t *testing.T) {
	m := Manifest{
		Name: "tiny", Class: "clustered", Docs: 10, ShapeCount: 1,
		SourceBytes: 1000, KeyBytes: 50, NDJSONSHA256: strings.Repeat("b", 64), ExtractHits: 10,
		ContainKey: "kind", ContainExpected: 2, GroupExpected: 3,
		SumField: "n", SumExpected: 55,
	}
	dir := t.TempDir()
	writeDuckDBRun(t, dir, m)
	log, err := ParseDuckDBRun(dir)
	if err != nil {
		t.Fatal(err)
	}
	store := OursStore{
		LoadNS: 100_000, IndexBuildNS: 200_000, HeapBytes: 4000, IndexBytes: 256,
		PointNS: 100, FilterNS: 1000, SumNS: 2000, GroupNS: 3000, ContainNS: 4000,
		MutationOps: 2, UpdateNSOp: 500, DeleteNSOp: 300, AfterDeletes: 8,
		DocsObserved: 10, ExtractHits: 10, FilterCount: 2, SumObserved: 55,
		GroupCount: 3, ContainCount: 2,
	}
	report := BuildReport(OursResults{GoVersion: "go-test", GOARCH: "test", Corpora: []OursCorpus{{Manifest: m, Store: store}}}, map[string]DuckDBLog{"tiny": log})
	for _, want := range []string{
		"Store and DuckDB comparison", "Correctness gate", "Store live heap",
		"DuckDB file", "there is no heap/disk cross-ratio", "Per-key mutations", "verified",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("report missing %q:\n%s", want, report)
		}
	}
}

func TestMeasureStoreCorpusSmoke(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tiny")
	m, err := GenerateSynthetic(dir, SynthSpec{Name: "tiny", Docs: 128, Shapes: 4, DocBytes: 256, Seed: 9})
	if err != nil {
		t.Fatal(err)
	}
	result, err := MeasureStoreCorpus(dir, m, 1)
	if err != nil {
		t.Fatal(err)
	}
	if bad := result.Verify(m); len(bad) != 0 {
		t.Fatalf("verification: %v", bad)
	}
	if result.HeapBytes <= 0 || result.PointNS <= 0 || result.UpdateNSOp <= 0 || result.DeleteNSOp <= 0 {
		t.Fatalf("missing measurements: %+v", result)
	}
}

func TestPinnedDuckDBEndToEnd(t *testing.T) {
	if os.Getenv("DUCKDBBENCH") != "1" {
		t.Skip("set DUCKDBBENCH=1 to run the pinned container smoke")
	}
	root := t.TempDir()
	corpus := filepath.Join(root, "tiny")
	m, err := GenerateSynthetic(corpus, SynthSpec{Name: "tiny", Docs: 1024, Shapes: 4, DocBytes: 256, Seed: 11})
	if err != nil {
		t.Fatal(err)
	}
	results := filepath.Join(root, "results")
	cmd := exec.Command("bash", "run-duckdb.sh", corpus)
	cmd.Env = append(os.Environ(), "RESULTS="+results, "REPS=2")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("runner: %v\n%s", err, output)
	}
	log, err := ParseDuckDBRun(filepath.Join(results, "tiny"))
	if err != nil {
		t.Fatal(err)
	}
	if bad := log.Verify(m); len(bad) != 0 {
		t.Fatalf("verification: %v", bad)
	}
}
