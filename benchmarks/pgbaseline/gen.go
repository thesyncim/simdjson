// Package pgbaseline is the ADR 0002 phase-0 comparison harness: it
// generates the shared corpus set, measures this library's side of the
// baseline (space and single-core operation costs on a DocSet), parses the
// PostgreSQL protocol logs produced by run-pg.sh, and emits the acceptance
// report that later phases must regenerate.
//
// Everything here is methodology: the corpus definitions, the byte
// accounting rules, and the acceptance targets are code so that later
// phases cannot move the goalposts. See METHODOLOGY.md in this directory.
package pgbaseline

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/thesyncim/simdjson"
	stdlibcorpus "github.com/thesyncim/simdjson/tests/stdlib"
)

// Manifest describes one generated corpus. It is written as manifest.json
// (read by the Go tools) and manifest.env (read by run-pg.sh), and carries
// the query parameters plus the expected result counts, so both sides of
// the comparison run the same queries and can be cross-checked against the
// same expectations.
type Manifest struct {
	Name  string `json:"name"`
	Class string `json:"class"` // "clustered", "heterogeneous", or "real"
	Docs  int    `json:"docs"`

	// SourceBytes is the minified byte count: the sum of the exact bytes of
	// every document with no separators. This is the denominator for all
	// space and throughput accounting on both sides.
	SourceBytes int64 `json:"source_bytes"`
	// PrettyBytes is the size of the pretty-printed original for real
	// corpora (0 for synthetic ones). It is recorded for provenance only;
	// no ratio uses it.
	PrettyBytes int64 `json:"pretty_bytes,omitempty"`

	// ShapeCount is the number of distinct key sets: 1, 4, or 64 for the
	// clustered synthetics, Docs for the heterogeneous one, and 0 for real
	// corpora (natural, not controlled).
	ShapeCount int `json:"shape_count"`

	// The query parameters. All keys and values are alphanumeric (plus
	// underscore) by construction so they can be spliced into SQL literals
	// verbatim; Generate fails otherwise. ContainKey may be empty, in which
	// case the containment rows are skipped for this corpus.
	ExtractField string `json:"extract_field"`
	ExistKey     string `json:"exist_key"`
	ContainKey   string `json:"contain_key,omitempty"`
	ContainValue string `json:"contain_value,omitempty"`

	// Expected results, computed during generation. ExtractHits counts
	// documents where ExtractField is present; ExistExpected counts
	// documents containing ExistKey; ContainExpected counts documents whose
	// ContainKey value equals the JSON string ContainValue.
	ExtractHits     int `json:"extract_hits"`
	ExistExpected   int `json:"exist_expected"`
	ContainExpected int `json:"contain_expected"`
}

// spliceSafe is the alphabet permitted in query keys and values; it is what
// makes the literal splicing in run-pg.sh safe.
var spliceSafe = regexp.MustCompile(`^[A-Za-z0-9_]*$`)

// corpusWriter streams a corpus to docs.ndjson (one minified document per
// line, our side's input) and docs.pgcopy (the same documents in COPY text
// format, PostgreSQL's input), accumulating the manifest as it goes.
type corpusWriter struct {
	dir     string
	ndjson  *bufio.Writer
	pgcopy  *bufio.Writer
	nf, pf  *os.File
	m       Manifest
	scratch []byte
}

func newCorpusWriter(dir string, m Manifest) (*corpusWriter, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	nf, err := os.Create(filepath.Join(dir, "docs.ndjson"))
	if err != nil {
		return nil, err
	}
	pf, err := os.Create(filepath.Join(dir, "docs.pgcopy"))
	if err != nil {
		nf.Close()
		return nil, err
	}
	return &corpusWriter{
		dir:    dir,
		ndjson: bufio.NewWriterSize(nf, 1<<20),
		pgcopy: bufio.NewWriterSize(pf, 1<<20),
		nf:     nf,
		pf:     pf,
		m:      m,
	}, nil
}

// escapePGCopy appends doc in COPY text format: backslash, tab, newline,
// and carriage return are the only metacharacters. Minified JSON contains
// no raw control characters (they are escaped inside strings), so in
// practice only backslash doubling fires, but all four are handled.
func escapePGCopy(dst, doc []byte) []byte {
	for _, c := range doc {
		switch c {
		case '\\':
			dst = append(dst, '\\', '\\')
		case '\t':
			dst = append(dst, '\\', 't')
		case '\n':
			dst = append(dst, '\\', 'n')
		case '\r':
			dst = append(dst, '\\', 'r')
		default:
			dst = append(dst, c)
		}
	}
	return dst
}

// add writes one minified document to both outputs and counts its bytes.
func (w *corpusWriter) add(doc []byte) error {
	if _, err := w.ndjson.Write(doc); err != nil {
		return err
	}
	if err := w.ndjson.WriteByte('\n'); err != nil {
		return err
	}
	w.scratch = escapePGCopy(w.scratch[:0], doc)
	if _, err := w.pgcopy.Write(w.scratch); err != nil {
		return err
	}
	if err := w.pgcopy.WriteByte('\n'); err != nil {
		return err
	}
	w.m.Docs++
	w.m.SourceBytes += int64(len(doc))
	return nil
}

// close flushes both outputs and writes manifest.json and manifest.env.
func (w *corpusWriter) close() (Manifest, error) {
	if err := w.ndjson.Flush(); err != nil {
		return Manifest{}, err
	}
	if err := w.pgcopy.Flush(); err != nil {
		return Manifest{}, err
	}
	if err := w.nf.Close(); err != nil {
		return Manifest{}, err
	}
	if err := w.pf.Close(); err != nil {
		return Manifest{}, err
	}
	for _, s := range []string{w.m.ExtractField, w.m.ExistKey, w.m.ContainKey, w.m.ContainValue} {
		if !spliceSafe.MatchString(s) {
			return Manifest{}, fmt.Errorf("corpus %s: %q is not SQL-splice safe", w.m.Name, s)
		}
	}
	js, err := json.MarshalIndent(w.m, "", "\t")
	if err != nil {
		return Manifest{}, err
	}
	if err := os.WriteFile(filepath.Join(w.dir, "manifest.json"), append(js, '\n'), 0o644); err != nil {
		return Manifest{}, err
	}
	env := fmt.Sprintf("NAME=%s\nDOCS=%d\nEXTRACT_FIELD=%s\nEXIST_KEY=%s\nCONTAIN_KEY=%s\nCONTAIN_VALUE=%s\n",
		w.m.Name, w.m.Docs, w.m.ExtractField, w.m.ExistKey, w.m.ContainKey, w.m.ContainValue)
	if err := os.WriteFile(filepath.Join(w.dir, "manifest.env"), []byte(env), 0o644); err != nil {
		return Manifest{}, err
	}
	return w.m, nil
}

// SynthSpec configures one synthetic corpus. Documents are flat objects
// with sixteen fields. In clustered mode there are Shapes distinct key
// sets: document n has shape n%Shapes and its keys are named sSS_fFF, so
// distinct shapes share no keys. In heterogeneous mode every document is
// its own shape: keys are named dNNNNNNN_fFF with the document ordinal
// baked in.
type SynthSpec struct {
	Name     string
	Docs     int
	Shapes   int  // ignored when Hetero
	Hetero   bool // every document a distinct shape
	DocBytes int  // target document size; actual sizes get ±10% jitter
	Seed     uint64
}

// The synthetic value alphabets. Everything is alphanumeric so containment
// values are splice-safe and string comparison equals JSONB equality.
const synthToken = "abcdefghijklmnopqrstuvwxyz0123456789"

func appendToken(dst []byte, r *rand.Rand, n int) []byte {
	for range n {
		dst = append(dst, synthToken[r.IntN(len(synthToken))])
	}
	return dst
}

// synthDoc appends document n of the corpus. prefix is the shape's key
// prefix ("s03" or "d0000042"). The categorical field f02 draws from 32
// values, the existence anchor f11 from 4, and f15 is filler padding the
// document to the target size.
func synthDoc(dst []byte, prefix string, n int, r *rand.Rand, target int) (doc []byte, f02 string) {
	f02 = fmt.Sprintf("cat%02d", r.IntN(32))
	dst = fmt.Appendf(dst, `{"%s_f00":%d`, prefix, n)
	dst = fmt.Appendf(dst, `,"%s_f01":%d`, prefix, r.IntN(1000))
	dst = fmt.Appendf(dst, `,"%s_f02":"%s"`, prefix, f02)
	dst = fmt.Appendf(dst, `,"%s_f03":%d.%02d`, prefix, r.IntN(10000), r.IntN(100))
	dst = fmt.Appendf(dst, `,"%s_f04":%t`, prefix, r.IntN(2) == 0)
	dst = fmt.Appendf(dst, `,"%s_f05":%d`, prefix, 1700000000+r.IntN(1<<28))
	dst = fmt.Appendf(dst, `,"%s_f06":"status%d"`, prefix, r.IntN(8))
	dst = fmt.Appendf(dst, `,"%s_f07":"`, prefix)
	dst = appendToken(dst, r, 12)
	dst = fmt.Appendf(dst, `","%s_f08":%d`, prefix, r.IntN(1_000_000_000))
	dst = fmt.Appendf(dst, `,"%s_f09":%d.%04d`, prefix, r.IntN(1000), r.IntN(10000))
	dst = fmt.Appendf(dst, `,"%s_f10":%t`, prefix, r.IntN(2) == 0)
	dst = fmt.Appendf(dst, `,"%s_f11":"flag%d"`, prefix, r.IntN(4))
	dst = fmt.Appendf(dst, `,"%s_f12":"`, prefix)
	dst = appendToken(dst, r, 16)
	dst = fmt.Appendf(dst, `","%s_f13":%d`, prefix, r.IntN(1_000_000))
	dst = fmt.Appendf(dst, `,"%s_f14":%d`, prefix, r.IntN(100))
	dst = fmt.Appendf(dst, `,"%s_f15":"`, prefix)
	// Pad with filler to the jittered target; at least one filler byte.
	jitter := target/10 - r.IntN(target/5+1)
	fill := target + jitter - len(dst) - 2
	if fill < 1 {
		fill = 1
	}
	dst = appendToken(dst, r, fill)
	dst = append(dst, '"', '}')
	return dst, f02
}

// GenerateSynthetic writes a synthetic corpus into dir. The generator is
// deterministic: math/rand/v2's PCG is seeded from spec.Seed only, so a
// given spec reproduces byte-identical corpora on any platform.
func GenerateSynthetic(dir string, spec SynthSpec) (Manifest, error) {
	shapes := spec.Shapes
	if spec.Hetero {
		shapes = spec.Docs
	}
	if shapes < 1 || spec.Docs < 1 || spec.DocBytes < 100 {
		return Manifest{}, fmt.Errorf("corpus %s: bad spec %+v", spec.Name, spec)
	}
	class := "clustered"
	if spec.Hetero {
		class = "heterogeneous"
	}
	m := Manifest{Name: spec.Name, Class: class, ShapeCount: shapes}

	// Query anchors. The extraction field lives in shape 0, the existence
	// key in a later shape (selectivity 1/Shapes; every document when
	// Shapes==1), and containment matches one categorical value in shape
	// existShape's neighbor. Heterogeneous corpora anchor on document 0's
	// shape: selectivity 1/Docs, the adversarial extreme.
	prefixFor := func(n int) string {
		if spec.Hetero {
			return fmt.Sprintf("d%07d", n)
		}
		return fmt.Sprintf("s%02d", n%shapes)
	}
	existShape := min(3, shapes-1)
	containShape := min(1, shapes-1)
	m.ExtractField = prefixFor(0) + "_f07"
	m.ExistKey = prefixFor(existShape) + "_f11"
	m.ContainKey = prefixFor(containShape) + "_f02"
	m.ContainValue = "cat07"

	w, err := newCorpusWriter(dir, m)
	if err != nil {
		return Manifest{}, err
	}
	r := rand.New(rand.NewPCG(spec.Seed, 0x7067626173656c6e)) // "pgbaseln"
	var doc []byte
	for n := range spec.Docs {
		var f02 string
		doc, f02 = synthDoc(doc[:0], prefixFor(n), n, r, spec.DocBytes)
		shape := n % shapes
		if spec.Hetero {
			shape = n
		}
		if shape == 0 {
			w.m.ExtractHits++
		}
		if shape == existShape {
			w.m.ExistExpected++
		}
		if shape == containShape && f02 == w.m.ContainValue {
			w.m.ContainExpected++
		}
		if err := w.add(doc); err != nil {
			return Manifest{}, err
		}
	}
	return w.close()
}

// RealSpec configures a corpus derived from one of the repository's real
// corpora (tests/stdlib). When RecordsField is set, the top-level array
// under that key is split into per-record documents — the natural document
// unit — and the records are cycled in order until TargetBytes of minified
// source have been written. When RecordsField is empty the whole corpus is
// one document, replicated to TargetBytes; this exercises large-document
// handling (and, on the PostgreSQL side, TOAST compression).
type RealSpec struct {
	Name         string
	Corpus       string // stdlibcorpus name, e.g. "twitter_status.json.zst"
	RecordsField string
	TargetBytes  int64
	ExtractField string
	ExistKey     string
	ContainKey   string // empty skips containment for this corpus
	ContainValue string
}

// realRecord is one distinct source record with its precomputed query
// facts, so expectations scale exactly with replication.
type realRecord struct {
	doc          []byte
	extract      bool
	exist        bool
	containMatch bool
}

// GenerateReal writes a real-derived corpus into dir. Records are minified
// with simdjson.Compact, so the byte accounting matches the "corpora are
// measured minified" rule; the pretty-printed original's size is recorded
// in the manifest for provenance.
func GenerateReal(dir string, spec RealSpec) (Manifest, error) {
	pretty, err := stdlibcorpus.Read(spec.Corpus)
	if err != nil {
		return Manifest{}, err
	}
	var raws []json.RawMessage
	if spec.RecordsField == "" {
		raws = []json.RawMessage{json.RawMessage(pretty)}
	} else {
		var top map[string]json.RawMessage
		if err := json.Unmarshal(pretty, &top); err != nil {
			return Manifest{}, fmt.Errorf("corpus %s: %v", spec.Corpus, err)
		}
		arr, ok := top[spec.RecordsField]
		if !ok {
			return Manifest{}, fmt.Errorf("corpus %s: no top-level %q", spec.Corpus, spec.RecordsField)
		}
		if err := json.Unmarshal(arr, &raws); err != nil {
			return Manifest{}, fmt.Errorf("corpus %s/%s: %v", spec.Corpus, spec.RecordsField, err)
		}
	}
	quoted := []byte(strconv.Quote(spec.ContainValue))
	records := make([]realRecord, 0, len(raws))
	for _, raw := range raws {
		doc, err := simdjson.Compact(raw)
		if err != nil {
			return Manifest{}, err
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(doc, &fields); err != nil {
			return Manifest{}, err
		}
		rec := realRecord{doc: doc}
		_, rec.extract = fields[spec.ExtractField]
		_, rec.exist = fields[spec.ExistKey]
		if v, ok := fields[spec.ContainKey]; ok && spec.ContainKey != "" {
			c, err := simdjson.Compact(v)
			if err != nil {
				return Manifest{}, err
			}
			rec.containMatch = bytes.Equal(c, quoted)
		}
		records = append(records, rec)
	}

	m := Manifest{
		Name:         spec.Name,
		Class:        "real",
		PrettyBytes:  int64(len(pretty)),
		ExtractField: spec.ExtractField,
		ExistKey:     spec.ExistKey,
		ContainKey:   spec.ContainKey,
		ContainValue: spec.ContainValue,
	}
	w, err := newCorpusWriter(dir, m)
	if err != nil {
		return Manifest{}, err
	}
	for i := 0; w.m.SourceBytes < spec.TargetBytes; i++ {
		rec := records[i%len(records)]
		if rec.extract {
			w.m.ExtractHits++
		}
		if rec.exist {
			w.m.ExistExpected++
		}
		if rec.containMatch {
			w.m.ContainExpected++
		}
		if err := w.add(rec.doc); err != nil {
			return Manifest{}, err
		}
	}
	return w.close()
}

// ReadManifest loads a corpus directory's manifest.json.
func ReadManifest(dir string) (Manifest, error) {
	js, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return Manifest{}, err
	}
	var m Manifest
	if err := json.Unmarshal(js, &m); err != nil {
		return Manifest{}, fmt.Errorf("%s: %v", dir, err)
	}
	return m, nil
}
