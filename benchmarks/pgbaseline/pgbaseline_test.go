// Tests for the phase-0 harness tooling: generator determinism and
// correctness of the recorded expectations, COPY escaping, PostgreSQL log
// parsing, and report assembly. Everything here runs on tiny inputs; the
// real corpus set and measurements are driven manually via cmd/pgbaseline
// and run-pg.sh, and the one end-to-end smoke test is gated behind
// PGBASELINE=1 so the suite stays fast by default.
package pgbaseline

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readCorpus loads a generated corpus's files.
func readCorpus(t *testing.T, dir string) (ndjson, pgcopy []byte, m Manifest) {
	t.Helper()
	ndjson, err := os.ReadFile(filepath.Join(dir, "docs.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	pgcopy, err = os.ReadFile(filepath.Join(dir, "docs.pgcopy"))
	if err != nil {
		t.Fatal(err)
	}
	m, err = ReadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	return ndjson, pgcopy, m
}

// recount independently recomputes the manifest's expected counts from the
// NDJSON bytes with encoding/json, the harness's oracle.
func recount(t *testing.T, ndjson []byte, m Manifest) (extract, exist, contain int) {
	t.Helper()
	for _, line := range bytes.Split(bytes.TrimRight(ndjson, "\n"), []byte("\n")) {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(line, &fields); err != nil {
			t.Fatalf("invalid generated document %q: %v", line, err)
		}
		if _, ok := fields[m.ExtractField]; ok {
			extract++
		}
		if _, ok := fields[m.ExistKey]; ok {
			exist++
		}
		if v, ok := fields[m.ContainKey]; ok && m.ContainKey != "" {
			var s string
			if json.Unmarshal(v, &s) == nil && s == m.ContainValue {
				contain++
			}
		}
	}
	return extract, exist, contain
}

func checkManifestCounts(t *testing.T, ndjson []byte, m Manifest) {
	t.Helper()
	extract, exist, contain := recount(t, ndjson, m)
	if extract != m.ExtractHits || exist != m.ExistExpected || contain != m.ContainExpected {
		t.Errorf("manifest counts (extract=%d exist=%d contain=%d) disagree with recount (%d, %d, %d)",
			m.ExtractHits, m.ExistExpected, m.ContainExpected, extract, exist, contain)
	}
}

func TestSyntheticDeterminismAndCounts(t *testing.T) {
	spec := SynthSpec{Name: "tiny_s4", Docs: 500, Shapes: 4, DocBytes: 300, Seed: 7}
	d1, d2 := filepath.Join(t.TempDir(), "a"), filepath.Join(t.TempDir(), "b")
	m1, err := GenerateSynthetic(d1, spec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := GenerateSynthetic(d2, spec); err != nil {
		t.Fatal(err)
	}
	n1, p1, _ := readCorpus(t, d1)
	n2, p2, m2 := readCorpus(t, d2)
	if !bytes.Equal(n1, n2) || !bytes.Equal(p1, p2) {
		t.Fatal("same spec generated different corpora")
	}
	if m1 != m2 {
		t.Fatalf("same spec generated different manifests: %+v vs %+v", m1, m2)
	}
	if m1.Docs != spec.Docs || m1.ShapeCount != 4 || m1.Class != "clustered" {
		t.Fatalf("bad manifest: %+v", m1)
	}
	if int64(len(n1))-int64(m1.Docs) != m1.SourceBytes {
		t.Fatalf("SourceBytes %d does not match NDJSON size %d minus %d newlines",
			m1.SourceBytes, len(n1), m1.Docs)
	}
	checkManifestCounts(t, n1, m1)
	// Sizes should track the target within the ±10% jitter plus the
	// minimum-filler floor.
	avg := int(m1.SourceBytes) / m1.Docs
	if avg < spec.DocBytes*8/10 || avg > spec.DocBytes*12/10 {
		t.Errorf("average document size %d strays from target %d", avg, spec.DocBytes)
	}
}

func TestHeterogeneousShapes(t *testing.T) {
	spec := SynthSpec{Name: "tiny_hetero", Docs: 50, Hetero: true, DocBytes: 300, Seed: 9}
	dir := filepath.Join(t.TempDir(), "h")
	m, err := GenerateSynthetic(dir, spec)
	if err != nil {
		t.Fatal(err)
	}
	if m.ShapeCount != spec.Docs {
		t.Fatalf("heterogeneous ShapeCount = %d, want %d", m.ShapeCount, spec.Docs)
	}
	ndjson, _, _ := readCorpus(t, dir)
	seen := map[string]bool{}
	for _, line := range bytes.Split(bytes.TrimRight(ndjson, "\n"), []byte("\n")) {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(line, &fields); err != nil {
			t.Fatal(err)
		}
		for k := range fields {
			if seen[k] {
				t.Fatalf("key %q appears in two documents; shapes are not distinct", k)
			}
			seen[k] = true
		}
	}
	checkManifestCounts(t, ndjson, m)
}

func TestRealCorpusGeneration(t *testing.T) {
	spec := RealSpec{
		Name: "tweets_tiny", Corpus: "twitter_status.json.zst",
		RecordsField: "statuses", TargetBytes: 1 << 20,
		ExtractField: "id_str", ExistKey: "possibly_sensitive",
		ContainKey: "lang", ContainValue: "ja",
	}
	dir := filepath.Join(t.TempDir(), "tw")
	m, err := GenerateReal(dir, spec)
	if err != nil {
		t.Fatal(err)
	}
	if m.SourceBytes < spec.TargetBytes || m.Docs == 0 || m.PrettyBytes == 0 {
		t.Fatalf("bad manifest: %+v", m)
	}
	ndjson, _, _ := readCorpus(t, dir)
	checkManifestCounts(t, ndjson, m)
}

func TestEscapePGCopy(t *testing.T) {
	in := []byte(`{"a":"b\\c\td"}` + "\x01")
	got := string(escapePGCopy(nil, in))
	want := `{"a":"b\\\\c\\td"}` + "\x01"
	if got != want {
		t.Fatalf("escapePGCopy = %q, want %q", got, want)
	}
	plain := []byte(`{"a":1}`)
	if !bytes.Equal(escapePGCopy(nil, plain), plain) {
		t.Fatal("escape-free document must pass through unchanged")
	}
}

const sampleLog = `Timing is on.
STEP version
                              version
--------------------------------------------------------------------
 PostgreSQL 18.4 on aarch64-unknown-linux-musl, compiled by gcc
(1 row)

Time: 0.500 ms
STEP setting_shared_buffers
 shared_buffers
----------------
 1GB
(1 row)

Time: 0.200 ms
CREATE TABLE
Time: 1.000 ms
STEP copy
COPY 1000
Time: 2500.000 ms
STEP rowcount
 count
-------
  1000
(1 row)

Time: 10.000 ms
STEP size_table
 pg_table_size
---------------
        524288
(1 row)

Time: 0.300 ms
STEP explain_q_exist_seq
                 QUERY PLAN
--------------------------------------------
 Seq Scan on t  (cost=0.00..1.00 rows=1)
(1 row)

Time: 3.000 ms
STEP q_exist_seq
 count
-------
   250
(1 row)

Time: 40.000 ms
STEP q_exist_seq
 count
-------
   250
(1 row)

Time: 31.000 ms
STEP q_exist_seq
 count
-------
   250
(1 row)

Time: 33.000 ms
`

func TestParsePGLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.log")
	if err := os.WriteFile(path, []byte(sampleLog), 0o644); err != nil {
		t.Fatal(err)
	}
	l, err := ParsePGLog(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(l.Version, "PostgreSQL 18.4") {
		t.Errorf("version = %q", l.Version)
	}
	if l.Settings["shared_buffers"] != "1GB" {
		t.Errorf("shared_buffers = %q", l.Settings["shared_buffers"])
	}
	if ms, ok := l.StepMS("copy"); !ok || ms != 2500 {
		t.Errorf("copy time = %v %v", ms, ok)
	}
	if l.Values["copy"] != 1000 || l.Values["rowcount"] != 1000 {
		t.Errorf("copy/rowcount values = %d/%d", l.Values["copy"], l.Values["rowcount"])
	}
	if l.Values["size_table"] != 524288 {
		t.Errorf("size_table = %d", l.Values["size_table"])
	}
	if l.Values["q_exist_seq"] != 250 {
		t.Errorf("exist count = %d", l.Values["q_exist_seq"])
	}
	// Warm-up discarded, minimum of the rest.
	if ms, ok := l.QueryMS("q_exist_seq"); !ok || ms != 31 {
		t.Errorf("q_exist_seq = %v %v, want 31", ms, ok)
	}
	if !strings.Contains(l.Explains["explain_q_exist_seq"], "Seq Scan on t") {
		t.Errorf("explain not captured: %q", l.Explains["explain_q_exist_seq"])
	}
}

func TestBuildReport(t *testing.T) {
	m := Manifest{
		Name: "tiny", Class: "clustered", Docs: 1000, SourceBytes: 400_000,
		ShapeCount: 4, ExtractField: "s00_f07", ExistKey: "s03_f11",
		ContainKey: "s01_f02", ContainValue: "cat07",
		ExtractHits: 250, ExistExpected: 250, ContainExpected: 8,
	}
	res := OursResults{
		GoVersion: "gotest", GOARCH: "arm64",
		Corpora: []OursCorpus{{
			Manifest: m,
			Variants: []OursVariant{
				{HashKeys: false, IngestNS: 2e6, RetainedBytes: 500_000, Entries: 33000, ModeledBytes: 928_000,
					ExtractPointerNS: 1e5, ExistNS: 1e5, ContainNS: 1e5, SingleDocNS: 100,
					ExtractHits: 250, ExistCount: 250, ContainCount: 8},
				{HashKeys: true, IngestNS: 1e6, RetainedBytes: 600_000, Entries: 33000, ModeledBytes: 928_000,
					ExtractPointerNS: 1e5, ExtractColumnNS: 5e4, ExistNS: 1e5, ContainNS: 2e5, SingleDocNS: 90,
					ExtractHits: 250, ExistCount: 250, ContainCount: 8},
			},
		}},
	}
	logPath := filepath.Join(t.TempDir(), "tiny.log")
	if err := os.WriteFile(logPath, []byte(sampleLog), 0o644); err != nil {
		t.Fatal(err)
	}
	l, err := ParsePGLog(logPath)
	if err != nil {
		t.Fatal(err)
	}
	got := BuildReport(res, map[string]PGLog{"tiny": l})

	for _, want := range []string{
		"PostgreSQL 18.4",
		"| tiny | clustered | 1000 | 4 |",
		// Space ratio: 600000 / 524288 (no path_ops index in the sample
		// log, so sizeGinPath is 0 and the table alone is the divisor).
		"1.14x",
		// Existence: PG best 31 ms vs ours 0.1 ms -> 310x.
		"310x",
		"space-clustered",
		"missed (phase 1 pending)",
		"## Verification",
		"**MISMATCH**", // sample log's exist count 250 matches, rowcount matches, extract hits n/a=0 mismatches
		"## Honesty notes",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report is missing %q\n----\n%s", want, got)
		}
	}
}

// TestSmokeEndToEnd exercises gen + measure on a tiny corpus. It touches
// timers, the GC, and a few megabytes, so it is gated behind PGBASELINE=1
// to keep the default suite lean.
func TestSmokeEndToEnd(t *testing.T) {
	if os.Getenv("PGBASELINE") != "1" {
		t.Skip("set PGBASELINE=1 to run the end-to-end smoke test")
	}
	dir := filepath.Join(t.TempDir(), "smoke")
	spec := SynthSpec{Name: "smoke", Docs: 2000, Shapes: 4, DocBytes: 400, Seed: 3}
	if _, err := GenerateSynthetic(dir, spec); err != nil {
		t.Fatal(err)
	}
	c, err := MeasureDir(dir, 2)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range c.Variants {
		if bad := v.Verify(c.Manifest); len(bad) != 0 {
			t.Errorf("hashkeys=%t: %v", v.HashKeys, bad)
		}
		if v.RetainedBytes < c.Manifest.SourceBytes {
			t.Errorf("hashkeys=%t: retained %d below source bytes %d", v.HashKeys, v.RetainedBytes, c.Manifest.SourceBytes)
		}
	}
}
