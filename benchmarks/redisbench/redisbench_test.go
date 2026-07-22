// Tests for the RedisJSON/RediSearch harness tooling: generator determinism
// and correctness of the recorded expectations, RESP mass-load encoding,
// session-log parsing, and report assembly. Everything here runs on tiny
// inputs; the real corpus set and measurements are driven manually via
// cmd/redisbench and run-redis.sh, and the one end-to-end smoke test is gated
// behind REDISBENCH=1 so the suite stays fast by default.
package redisbench

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// readCorpus loads a generated corpus's files.
func readCorpus(t *testing.T, dir string) (ndjson, resp []byte, m Manifest) {
	t.Helper()
	ndjson, err := os.ReadFile(filepath.Join(dir, "docs.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	resp, err = os.ReadFile(filepath.Join(dir, "docs.resp"))
	if err != nil {
		t.Fatal(err)
	}
	m, err = ReadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	return ndjson, resp, m
}

// recount independently recomputes the manifest's expected results from the
// NDJSON bytes with encoding/json, the harness's oracle.
func recount(t *testing.T, ndjson []byte, m Manifest) (extract, exist, contain, group int, sum int64) {
	t.Helper()
	groups := map[string]bool{}
	total, groupPresent := 0, 0
	for _, line := range bytes.Split(bytes.TrimRight(ndjson, "\n"), []byte("\n")) {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(line, &fields); err != nil {
			t.Fatalf("invalid generated document %q: %v", line, err)
		}
		total++
		if _, ok := fields[m.ExtractField]; ok {
			extract++
		}
		if _, ok := fields[m.ExistKey]; ok {
			exist++
		}
		if v, ok := fields[m.ContainKey]; ok && m.ContainKey != "" {
			var s string
			if json.Unmarshal(v, &s) == nil && s != "" {
				groups[s] = true
				groupPresent++
				if s == m.ContainValue {
					contain++
				}
			}
		}
		if v, ok := fields[m.SumField]; ok && m.SumField != "" {
			var n int64
			if json.Unmarshal(v, &n) == nil {
				sum += n
			}
		}
	}
	if m.ContainKey != "" {
		group = groupCardinality(len(groups), groupPresent, total)
	}
	return extract, exist, contain, group, sum
}

func checkManifestCounts(t *testing.T, ndjson []byte, m Manifest) {
	t.Helper()
	extract, exist, contain, group, sum := recount(t, ndjson, m)
	if extract != m.ExtractHits || exist != m.ExistExpected || contain != m.ContainExpected {
		t.Errorf("manifest counts (extract=%d exist=%d contain=%d) disagree with recount (%d, %d, %d)",
			m.ExtractHits, m.ExistExpected, m.ContainExpected, extract, exist, contain)
	}
	if group != m.GroupExpected {
		t.Errorf("manifest GroupExpected %d disagrees with recount %d", m.GroupExpected, group)
	}
	if sum != m.SumExpected {
		t.Errorf("manifest SumExpected %d disagrees with recount %d", m.SumExpected, sum)
	}
}

// parseRESP decodes docs.resp back into (key, path, document) triples,
// validating the RESP array framing the mass loader emits.
func parseRESP(t *testing.T, resp []byte) []struct{ key, path, doc string } {
	t.Helper()
	var out []struct{ key, path, doc string }
	r := bytes.NewReader(resp)
	readLine := func() string {
		var sb []byte
		for {
			b, err := r.ReadByte()
			if err != nil {
				return string(sb)
			}
			if b == '\r' {
				r.ReadByte() // consume '\n'
				return string(sb)
			}
			sb = append(sb, b)
		}
	}
	bulk := func() string {
		hdr := readLine()
		if len(hdr) == 0 || hdr[0] != '$' {
			t.Fatalf("expected bulk header, got %q", hdr)
		}
		n, err := strconv.Atoi(hdr[1:])
		if err != nil {
			t.Fatalf("bad bulk length %q", hdr)
		}
		buf := make([]byte, n)
		if _, err := r.Read(buf); err != nil {
			t.Fatalf("short bulk read: %v", err)
		}
		r.ReadByte() // '\r'
		r.ReadByte() // '\n'
		return string(buf)
	}
	for r.Len() > 0 {
		arr := readLine()
		if arr == "" {
			break
		}
		if arr != "*4" {
			t.Fatalf("expected *4 command array, got %q", arr)
		}
		cmd, key, path, doc := bulk(), bulk(), bulk(), bulk()
		if cmd != "JSON.SET" {
			t.Fatalf("expected JSON.SET, got %q", cmd)
		}
		out = append(out, struct{ key, path, doc string }{key, path, doc})
	}
	return out
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
	if m1.SumField == "" || m1.SumExpected == 0 || m1.GroupExpected == 0 {
		t.Fatalf("aggregate anchors not populated: %+v", m1)
	}
	if int64(len(n1))-int64(m1.Docs) != m1.SourceBytes {
		t.Fatalf("SourceBytes %d does not match NDJSON size %d minus %d newlines",
			m1.SourceBytes, len(n1), m1.Docs)
	}
	checkManifestCounts(t, n1, m1)
	avg := int(m1.SourceBytes) / m1.Docs
	if avg < spec.DocBytes*8/10 || avg > spec.DocBytes*12/10 {
		t.Errorf("average document size %d strays from target %d", avg, spec.DocBytes)
	}
}

// TestRESPMatchesNDJSON checks that docs.resp is well-formed RESP whose
// JSON.SET documents are byte-identical to the NDJSON lines, under keys the
// FT.CREATE prefix will see.
func TestRESPMatchesNDJSON(t *testing.T) {
	spec := SynthSpec{Name: "tiny_resp", Docs: 200, Shapes: 4, DocBytes: 200, Seed: 11}
	dir := filepath.Join(t.TempDir(), "r")
	m, err := GenerateSynthetic(dir, spec)
	if err != nil {
		t.Fatal(err)
	}
	ndjson, resp, _ := readCorpus(t, dir)
	lines := bytes.Split(bytes.TrimRight(ndjson, "\n"), []byte("\n"))
	cmds := parseRESP(t, resp)
	if len(cmds) != m.Docs || len(cmds) != len(lines) {
		t.Fatalf("resp has %d commands, ndjson %d lines, manifest %d docs", len(cmds), len(lines), m.Docs)
	}
	for i, c := range cmds {
		if want := keyPrefix + strconv.Itoa(i); c.key != want {
			t.Fatalf("command %d key %q, want %q", i, c.key, want)
		}
		if c.path != "$" {
			t.Fatalf("command %d path %q, want $", i, c.path)
		}
		if c.doc != string(lines[i]) {
			t.Fatalf("command %d document mismatch:\n resp: %s\nndjson: %s", i, c.doc, lines[i])
		}
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
		ContainKey: "lang", ContainValue: "ja", SumField: "retweet_count",
	}
	dir := filepath.Join(t.TempDir(), "tw")
	m, err := GenerateReal(dir, spec)
	if err != nil {
		t.Fatal(err)
	}
	if m.SourceBytes < spec.TargetBytes || m.Docs == 0 || m.PrettyBytes == 0 {
		t.Fatalf("bad manifest: %+v", m)
	}
	if m.GroupExpected == 0 {
		t.Fatalf("real corpus produced no groups: %+v", m)
	}
	ndjson, _, _ := readCorpus(t, dir)
	checkManifestCounts(t, ndjson, m)
}

// sampleLog is one run-redis.sh session in the line grammar redislog.go parses.
const sampleLog = `IMAGE redis/redis-stack-server:7.4.0-v3
VERSION 7.4.2
MODULE ReJSON 20808
MODULE search 21015
DOCS 1000
LOAD_NS 2000000
LOAD_REPLIES 1000
USED_MEMORY_BASE 1000000
USED_MEMORY 3000000
USED_MEMORY_INDEXED 3500000
INDEX_NS 5000000
INDEX_NUM_DOCS 1000
SAMPLE projection 3000
SAMPLE projection 1000
SAMPLE projection 1200
SAMPLE filter 500000
SAMPLE filter 400000
SAMPLE filter 420000
RESULT filter 8
SAMPLE sum 600000
SAMPLE sum 550000
RESULT sum 123456
SAMPLE groupby 700000
SAMPLE groupby 650000
RESULT groupby 32
`

func TestParseRedisLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.log")
	if err := os.WriteFile(path, []byte(sampleLog), 0o644); err != nil {
		t.Fatal(err)
	}
	l, err := ParseRedisLog(path)
	if err != nil {
		t.Fatal(err)
	}
	if l.Version != "7.4.2" || l.Image == "" {
		t.Errorf("version/image = %q/%q", l.Version, l.Image)
	}
	if l.Modules["ReJSON"] != "20808" || l.Modules["search"] != "21015" {
		t.Errorf("modules = %v", l.Modules)
	}
	if l.keyspaceBytes() != 2_000_000 {
		t.Errorf("keyspace = %d, want 2000000", l.keyspaceBytes())
	}
	if l.indexBytes() != 500_000 {
		t.Errorf("index bytes = %d, want 500000", l.indexBytes())
	}
	// Warm-up discarded, minimum of the rest.
	if ns, ok := l.QueryNS("projection"); !ok || ns != 1000 {
		t.Errorf("projection = %v %v, want 1000", ns, ok)
	}
	if ns, ok := l.QueryNS("filter"); !ok || ns != 400000 {
		t.Errorf("filter = %v %v, want 400000", ns, ok)
	}
	if l.Results["filter"] != 8 || l.Results["sum"] != 123456 || l.Results["groupby"] != 32 {
		t.Errorf("results = %v", l.Results)
	}
	if l.Values["index_num_docs"] != 1000 {
		t.Errorf("index_num_docs = %d", l.Values["index_num_docs"])
	}
}

func TestBuildReport(t *testing.T) {
	m := Manifest{
		Name: "tiny", Class: "clustered", Docs: 1000, SourceBytes: 400_000,
		ShapeCount: 4, ExtractField: "s00_f07",
		ContainKey: "s01_f02", ContainValue: "cat07", SumField: "s01_f01",
		ExtractHits: 250, ContainExpected: 8, GroupExpected: 32,
		SumExpected: 999, // deliberately disagrees with the log's sum -> MISMATCH
	}
	res := OursResults{
		GoVersion: "gotest", GOARCH: "arm64",
		Corpora: []OursCorpus{{
			Manifest: m,
			Store: &OursStore{
				IngestNS: 1_250_000, IndexBuildNS: 50_000, RetainedBytes: 2_000_000, IndexBytes: 125_000,
				SingleDocNS: 250, FilterNS: 100_000, SumNS: 150_000, GroupNS: 150_000, ContainNS: 800_000,
				DocsObserved: 1000, ExtractHits: 250, FilterCount: 8, SumObserved: 999, GroupCount: 32, ContainCount: 8,
			},
			Variants: []OursVariant{
				{HashKeys: true, ShapeTapes: false, IngestNS: 2e6, RetainedBytes: 2_500_000, Entries: 33000, ModeledBytes: 928_000,
					ProjectPointerNS: 1e5, SingleDocNS: 500, FilterNS: 200_000, SumNS: 300_000, GroupNS: 300_000, ContainNS: 1e6,
					ExtractHits: 250, FilterCount: 8, SumObserved: 999, GroupCount: 32, ContainCount: 8},
				{HashKeys: true, ShapeTapes: true, IngestNS: 1e6, RetainedBytes: 2_400_000, Entries: 16000, ShapeTapedDocs: 1000, ModeledBytes: 800_000,
					ProjectPointerNS: 1e5, ProjectColumnNS: 5e4, SingleDocNS: 500, FilterNS: 200_000, SumNS: 300_000, GroupNS: 300_000, ContainNS: 1e6,
					ExtractHits: 250, FilterCount: 8, SumObserved: 999, GroupCount: 32, ContainCount: 8},
			},
		}},
	}
	logPath := filepath.Join(t.TempDir(), "tiny.log")
	if err := os.WriteFile(logPath, []byte(sampleLog), 0o644); err != nil {
		t.Fatal(err)
	}
	l, err := ParseRedisLog(logPath)
	if err != nil {
		t.Fatal(err)
	}
	got := BuildReport(res, map[string]RedisLog{"tiny": l})

	for _, want := range []string{
		"7.4.2",
		"| tiny | clustered | 1000 | 4 |",
		// Space: Store 2000000 / (keyspace 2000000 + index 500000) = 0.80x.
		"0.80x",
		// Filter: Redis 400000 ns / Store 100000 ns = 4.00x.
		"4.00x",
		"not-expressible",
		"containment @>",
		"## Scenario matrix",
		"competitor expressiveness",
		"## Verification",
		"**MISMATCH**", // manifest SumExpected 999 vs log sum 123456
		"## Honesty notes",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report is missing %q\n----\n%s", want, got)
		}
	}
}

// TestSmokeEndToEnd exercises gen + measure on a tiny corpus. It touches
// timers, the GC, and a few megabytes, so it is gated behind REDISBENCH=1 to
// keep the default suite lean.
func TestSmokeEndToEnd(t *testing.T) {
	if os.Getenv("REDISBENCH") != "1" {
		t.Skip("set REDISBENCH=1 to run the end-to-end smoke test")
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
			t.Errorf("hashkeys=%t shapetapes=%t: %v", v.HashKeys, v.ShapeTapes, bad)
		}
		if v.RetainedBytes < c.Manifest.SourceBytes {
			t.Errorf("hashkeys=%t: retained %d below source bytes %d", v.HashKeys, v.RetainedBytes, c.Manifest.SourceBytes)
		}
	}
	if c.Store == nil {
		t.Fatal("MeasureDir omitted the keyed Store comparison")
	}
	if bad := c.Store.Verify(c.Manifest); len(bad) != 0 {
		t.Errorf("Store: %v", bad)
	}
	if c.Store.RetainedBytes < c.Manifest.SourceBytes {
		t.Errorf("Store retained %d below source bytes %d", c.Store.RetainedBytes, c.Manifest.SourceBytes)
	}
}
