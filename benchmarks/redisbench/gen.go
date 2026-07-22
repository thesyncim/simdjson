// Package redisbench is the ADR 0003 comparison harness: it generates the
// shared corpus set, measures this library's side of the query surface
// (a keyed Store with a matching declared exact index, plus DocSet
// representation diagnostics), parses the
// RedisJSON/RediSearch protocol logs produced by run-redis.sh, and emits the
// scoreboard the query subpackage must keep green as it lands.
//
// Everything here is methodology: the corpus definitions, the byte
// accounting rules, and the scenario matrix are code so that later work
// cannot move the goalposts. See redis-methodology.md in this directory.
package redisbench

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

// keyPrefix is the Redis key namespace every document is loaded under; it is
// also the FT.CREATE ON JSON prefix, so the RediSearch index sees exactly the
// corpus and nothing else.
const keyPrefix = "doc:"

// Manifest describes one generated corpus. It is written as manifest.json
// (read by the Go tools) and manifest.env (read by run-redis.sh), and carries
// the query parameters plus the expected result counts, so both sides of the
// comparison run the same scenarios and can be cross-checked against the same
// expectations.
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
	// underscore) by construction so they can be spliced into RediSearch
	// schema paths and TAG queries verbatim; Generate fails otherwise.
	//
	// ExtractField is the point-projection path. ContainKey/ContainValue is
	// the equality predicate: the filtered-scan and group-by scenarios read
	// ContainKey (a low-cardinality categorical), and containment matches the
	// object {ContainKey: ContainValue}. ExistKey is a presence anchor kept
	// for generator cross-checks. SumField, when non-empty, is the numeric
	// path the scalar and grouped SUM aggregates reduce over.
	ExtractField string `json:"extract_field"`
	ExistKey     string `json:"exist_key"`
	ContainKey   string `json:"contain_key,omitempty"`
	ContainValue string `json:"contain_value,omitempty"`
	SumField     string `json:"sum_field,omitempty"`

	// Expected results, computed during generation. ExtractHits counts
	// documents where ExtractField is present; ExistExpected counts documents
	// containing ExistKey; ContainExpected counts documents whose ContainKey
	// value equals the JSON string ContainValue. SumExpected is the int64 sum
	// of SumField over the documents that carry it, and GroupExpected is the
	// number of distinct ContainKey values across the documents that carry
	// it — the cardinality a GROUP BY ContainKey returns.
	ExtractHits     int   `json:"extract_hits"`
	ExistExpected   int   `json:"exist_expected"`
	ContainExpected int   `json:"contain_expected"`
	SumExpected     int64 `json:"sum_expected,omitempty"`
	GroupExpected   int   `json:"group_expected,omitempty"`
}

// spliceSafe is the alphabet permitted in query keys and values; it is what
// makes the RediSearch schema-path and TAG splicing in run-redis.sh safe.
var spliceSafe = regexp.MustCompile(`^[A-Za-z0-9_]*$`)

// groupCardinality is the GROUP BY result size under SQL semantics: the number
// of distinct present group values, plus one NULL group when any of the total
// documents lacks the group field — matching RediSearch, which collects every
// document missing the TAG into a single empty-tag group.
func groupCardinality(distinct, present, total int) int {
	if present < total {
		return distinct + 1
	}
	return distinct
}

// corpusWriter streams a corpus to docs.ndjson (one minified document per
// line, our side's input) and docs.resp (the same documents as JSON.SET
// commands in the Redis RESP protocol, mass-loadable with redis-cli --pipe),
// accumulating the manifest as it goes.
type corpusWriter struct {
	dir    string
	ndjson *bufio.Writer
	resp   *bufio.Writer
	nf, rf *os.File
	m      Manifest
}

func newCorpusWriter(dir string, m Manifest) (*corpusWriter, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	nf, err := os.Create(filepath.Join(dir, "docs.ndjson"))
	if err != nil {
		return nil, err
	}
	rf, err := os.Create(filepath.Join(dir, "docs.resp"))
	if err != nil {
		nf.Close()
		return nil, err
	}
	return &corpusWriter{
		dir:    dir,
		ndjson: bufio.NewWriterSize(nf, 1<<20),
		resp:   bufio.NewWriterSize(rf, 1<<20),
		nf:     nf,
		rf:     rf,
		m:      m,
	}, nil
}

// writeRESP appends one `JSON.SET doc:<n> $ <doc>` command in the Redis RESP
// protocol. Bulk strings are length-prefixed and binary-safe, so the minified
// document bytes need no escaping — this is the redis-cli --pipe mass-load
// format, the RedisJSON analogue of a bulk COPY.
func (w *corpusWriter) writeRESP(n int, doc []byte) error {
	key := keyPrefix + strconv.Itoa(n)
	// *4 command: "JSON.SET", key, "$", doc.
	if _, err := fmt.Fprintf(w.resp, "*4\r\n$8\r\nJSON.SET\r\n$%d\r\n%s\r\n$1\r\n$\r\n$%d\r\n", len(key), key, len(doc)); err != nil {
		return err
	}
	if _, err := w.resp.Write(doc); err != nil {
		return err
	}
	_, err := w.resp.WriteString("\r\n")
	return err
}

// add writes one minified document to both outputs and counts its bytes.
func (w *corpusWriter) add(doc []byte) error {
	if _, err := w.ndjson.Write(doc); err != nil {
		return err
	}
	if err := w.ndjson.WriteByte('\n'); err != nil {
		return err
	}
	if err := w.writeRESP(w.m.Docs, doc); err != nil {
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
	if err := w.resp.Flush(); err != nil {
		return Manifest{}, err
	}
	if err := w.nf.Close(); err != nil {
		return Manifest{}, err
	}
	if err := w.rf.Close(); err != nil {
		return Manifest{}, err
	}
	for _, s := range []string{w.m.ExtractField, w.m.ExistKey, w.m.ContainKey, w.m.ContainValue, w.m.SumField} {
		if !spliceSafe.MatchString(s) {
			return Manifest{}, fmt.Errorf("corpus %s: %q is not splice safe for a RediSearch schema", w.m.Name, s)
		}
	}
	js, err := json.MarshalIndent(w.m, "", "\t")
	if err != nil {
		return Manifest{}, err
	}
	if err := os.WriteFile(filepath.Join(w.dir, "manifest.json"), append(js, '\n'), 0o644); err != nil {
		return Manifest{}, err
	}
	env := fmt.Sprintf("NAME=%s\nDOCS=%d\nKEY_PREFIX=%s\nEXTRACT_FIELD=%s\nCONTAIN_KEY=%s\nCONTAIN_VALUE=%s\nSUM_FIELD=%s\n",
		w.m.Name, w.m.Docs, keyPrefix, w.m.ExtractField, w.m.ContainKey, w.m.ContainValue, w.m.SumField)
	if err := os.WriteFile(filepath.Join(w.dir, "manifest.env"), []byte(env), 0o644); err != nil {
		return Manifest{}, err
	}
	return w.m, nil
}

// SynthSpec configures one synthetic corpus. Documents are flat objects with
// sixteen fields. In clustered mode there are Shapes distinct key sets:
// document n has shape n%Shapes and its keys are named sSS_fFF, so distinct
// shapes share no keys. In heterogeneous mode every document is its own shape:
// keys are named dNNNNNNN_fFF with the document ordinal baked in.
type SynthSpec struct {
	Name     string
	Docs     int
	Shapes   int  // ignored when Hetero
	Hetero   bool // every document a distinct shape
	DocBytes int  // target document size; actual sizes get ±10% jitter
	Seed     uint64
}

// The synthetic value alphabets. Everything is alphanumeric so containment
// values are splice-safe and string comparison equals structural equality.
const synthToken = "abcdefghijklmnopqrstuvwxyz0123456789"

func appendToken(dst []byte, r *rand.Rand, n int) []byte {
	for range n {
		dst = append(dst, synthToken[r.IntN(len(synthToken))])
	}
	return dst
}

// synthDoc appends document n of the corpus. prefix is the shape's key prefix
// ("s03" or "d0000042"). The categorical field f02 draws from 32 values (the
// group/filter anchor), f01 is the numeric aggregate anchor, the existence
// anchor f11 draws from 4, and f15 is filler padding the document to the
// target size.
func synthDoc(dst []byte, prefix string, n int, r *rand.Rand, target int) (doc []byte, f02 string, f01 int64) {
	f02 = fmt.Sprintf("cat%02d", r.IntN(32))
	f01 = int64(r.IntN(1000))
	dst = fmt.Appendf(dst, `{"%s_f00":%d`, prefix, n)
	dst = fmt.Appendf(dst, `,"%s_f01":%d`, prefix, f01)
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
	return dst, f02, f01
}

// GenerateSynthetic writes a synthetic corpus into dir. The generator is
// deterministic: math/rand/v2's PCG is seeded from spec.Seed only, so a given
// spec reproduces byte-identical corpora on any platform.
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

	// Query anchors. The extraction field lives in shape 0, the existence key
	// in a later shape (selectivity 1/Shapes; every document when Shapes==1),
	// and the categorical filter/group field plus the numeric aggregate field
	// share the containment shape so a single shape carries the whole WHERE +
	// GROUP BY + SUM story. Heterogeneous corpora anchor on document 0's
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
	m.SumField = prefixFor(containShape) + "_f01"

	w, err := newCorpusWriter(dir, m)
	if err != nil {
		return Manifest{}, err
	}
	groups := map[string]bool{}
	containDocs := 0
	r := rand.New(rand.NewPCG(spec.Seed, 0x726564697362656e)) // "redisben"
	var doc []byte
	for n := range spec.Docs {
		var f02 string
		var f01 int64
		doc, f02, f01 = synthDoc(doc[:0], prefixFor(n), n, r, spec.DocBytes)
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
		if shape == containShape {
			containDocs++
			w.m.SumExpected += f01
			groups[f02] = true
			if f02 == w.m.ContainValue {
				w.m.ContainExpected++
			}
		}
		if err := w.add(doc); err != nil {
			return Manifest{}, err
		}
	}
	// Grouping is SQL GROUP BY semantics: documents lacking the group field
	// form one NULL group, exactly as RediSearch collects them into a single
	// empty-tag group.
	w.m.GroupExpected = groupCardinality(len(groups), containDocs, spec.Docs)
	return w.close()
}

// RealSpec configures a corpus derived from one of the repository's real
// corpora (tests/stdlib). When RecordsField is set, the top-level array under
// that key is split into per-record documents — the natural document unit —
// and the records are cycled in order until TargetBytes of minified source
// have been written. When RecordsField is empty the whole corpus is one
// document, replicated to TargetBytes; this exercises large-document handling.
type RealSpec struct {
	Name         string
	Corpus       string // stdlibcorpus name, e.g. "twitter_status.json.zst"
	RecordsField string
	TargetBytes  int64
	ExtractField string
	ExistKey     string
	ContainKey   string // empty skips the filter/group/containment scenarios
	ContainValue string
	SumField     string // empty skips the SUM scenarios
}

// realRecord is one distinct source record with its precomputed query facts,
// so expectations scale exactly with replication.
type realRecord struct {
	doc          []byte
	extract      bool
	exist        bool
	containMatch bool
	groupValue   string
	sum          int64
	sumValid     bool
}

// GenerateReal writes a real-derived corpus into dir. Records are minified
// with simdjson.Compact, so the byte accounting matches the "corpora are
// measured minified" rule; the pretty-printed original's size is recorded in
// the manifest for provenance.
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
			// Only a non-empty string is a real group; anything else (absent,
			// null, empty, or non-string) lands in RediSearch's empty-tag group.
			var s string
			if json.Unmarshal(v, &s) == nil {
				rec.groupValue = s
			}
		}
		if v, ok := fields[spec.SumField]; ok && spec.SumField != "" {
			var n int64
			if json.Unmarshal(v, &n) == nil {
				rec.sum, rec.sumValid = n, true
			}
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
		SumField:     spec.SumField,
	}
	w, err := newCorpusWriter(dir, m)
	if err != nil {
		return Manifest{}, err
	}
	groups := map[string]bool{}
	total, groupPresent := 0, 0
	for i := 0; w.m.SourceBytes < spec.TargetBytes; i++ {
		rec := records[i%len(records)]
		total++
		if rec.extract {
			w.m.ExtractHits++
		}
		if rec.exist {
			w.m.ExistExpected++
		}
		if rec.containMatch {
			w.m.ContainExpected++
		}
		if spec.ContainKey != "" && rec.groupValue != "" {
			groups[rec.groupValue] = true
			groupPresent++
		}
		if rec.sumValid {
			w.m.SumExpected += rec.sum
		}
		if err := w.add(rec.doc); err != nil {
			return Manifest{}, err
		}
	}
	if spec.ContainKey != "" {
		w.m.GroupExpected = groupCardinality(len(groups), groupPresent, total)
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
