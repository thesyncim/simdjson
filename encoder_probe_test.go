package simdjson

// Adversarial differential probes for the encoder: every test compares
// simdjson.Marshal / CompileEncoder[T]().AppendJSON against encoding/json
// byte for byte, and error-vs-error, on surfaces the existing suites do not
// cover.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
)

// probeBoth encodes v with stdlib and both simdjson entry points and reports
// any acceptance or byte divergence.
func probeBoth[T any](t *testing.T, label string, v T) {
	t.Helper()
	want, wantErr := json.Marshal(&v)

	got, gotErr := Marshal(&v)
	if (gotErr == nil) != (wantErr == nil) {
		t.Errorf("%s: Marshal acceptance differs: simdjson=%v stdlib=%v", label, gotErr, wantErr)
		return
	}
	if gotErr == nil && !bytes.Equal(got, want) {
		t.Errorf("%s: Marshal bytes differ:\nsimdjson %s\nstdlib   %s", label, got, want)
	}

	encoder, compileErr := CompileEncoder[T](EncoderOptions{})
	if compileErr != nil {
		if wantErr == nil {
			t.Errorf("%s: CompileEncoder failed (%v) but stdlib accepts", label, compileErr)
		}
		return
	}
	got2, gotErr2 := encoder.AppendJSON(nil, &v)
	if (gotErr2 == nil) != (wantErr == nil) {
		t.Errorf("%s: AppendJSON acceptance differs: simdjson=%v stdlib=%v", label, gotErr2, wantErr)
		return
	}
	if gotErr2 == nil && !bytes.Equal(got2, want) {
		t.Errorf("%s: AppendJSON bytes differ:\nsimdjson %s\nstdlib   %s", label, got2, want)
	}
}

// ---------------------------------------------------------------------------
// 1. `,string` tag on types with custom marshalers. stdlib sets the quoted
// flag from the field's Kind but the marshaler encoders ignore it, so the
// custom output is emitted unquoted.

type probeQuotedJSONMarshaler int

func (q probeQuotedJSONMarshaler) MarshalJSON() ([]byte, error) {
	return fmt.Appendf(nil, "%d", int(q)), nil
}

type probeQuotedTextMarshaler int

func (q probeQuotedTextMarshaler) MarshalText() ([]byte, error) {
	return fmt.Appendf(nil, "t%d", int(q)), nil
}

func TestProbeStringOptionOnMarshalers(t *testing.T) {
	type jm struct {
		Q probeQuotedJSONMarshaler `json:"q,string"`
	}
	probeBoth(t, "json marshaler with ,string", jm{Q: 7})

	type tm struct {
		Q probeQuotedTextMarshaler `json:"q,string"`
	}
	probeBoth(t, "text marshaler with ,string", tm{Q: 7})

	type jmOmit struct {
		Q probeQuotedJSONMarshaler `json:"q,string,omitempty"`
	}
	probeBoth(t, "json marshaler ,string,omitempty zero", jmOmit{})
	probeBoth(t, "json marshaler ,string,omitempty nonzero", jmOmit{Q: 3})
}

// ---------------------------------------------------------------------------
// 2. Addressability propagation: pointer-receiver-only marshalers reachable
// one level below a non-addressable value (map value, interface contents).
// stdlib's condAddrEncoder checks CanAddr per value at encode time.

type probeStructWithPOM struct {
	M pointerOnlyMarshaler `json:"m"`
}

type probeTextPOM struct {
	Value int `json:"value"`
}

func (*probeTextPOM) MarshalText() ([]byte, error) { return []byte("ptr-text"), nil }

// ---------------------------------------------------------------------------
// 3. Map keys through TextMarshaler: nil pointer keys, string-kind keys that
// also implement TextMarshaler, ordering by marshaled form, key errors.

type probePtrTextKey struct{ N int }

func (k *probePtrTextKey) MarshalText() ([]byte, error) {
	return fmt.Appendf(nil, "p%d", k.N), nil
}

type probeErrKey struct{}

func (probeErrKey) MarshalText() ([]byte, error) { return nil, fmt.Errorf("key boom") }

func marshalBothWithRecover[T any](t *testing.T, v T) (got []byte, gotErr error, gotPanic any, want []byte, wantErr error, wantPanic any) {
	t.Helper()
	func() {
		defer func() { wantPanic = recover() }()
		want, wantErr = json.Marshal(&v)
	}()
	func() {
		defer func() { gotPanic = recover() }()
		got, gotErr = Marshal(&v)
	}()
	return
}

func TestProbeNilPointerTextMarshalerMapKey(t *testing.T) {
	v := map[*probePtrTextKey]int{nil: 1, {N: 2}: 3}
	got, gotErr, gotPanic, want, wantErr, wantPanic := marshalBothWithRecover(t, v)
	if (gotPanic != nil) != (wantPanic != nil) {
		t.Errorf("nil ptr text key: panic differs: simdjson=%v stdlib=%v", gotPanic, wantPanic)
		return
	}
	if (gotErr == nil) != (wantErr == nil) {
		t.Errorf("nil ptr text key: acceptance differs: simdjson=%v stdlib=%v", gotErr, wantErr)
		return
	}
	if gotErr == nil && !bytes.Equal(got, want) {
		t.Errorf("nil ptr text key:\nsimdjson %s\nstdlib   %s", got, want)
	}
}

func TestProbeMapKeyEdges(t *testing.T) {
	// Single entry: with multiple entries both libraries emit duplicate
	// "SHOULD-NOT-BE-USED" keys in nondeterministic order under jsonv2.
	probeBoth(t, "string-kind key implementing TextMarshaler",
		map[stringTextKey]int{"raw": 5})
	probeBoth(t, "text keys sorted by marshaled form",
		map[textKey]int{{A: 10, B: 0}: 1, {A: 2, B: 0}: 2, {A: 1, B: 99}: 3})
	probeBoth(t, "int8 keys with negatives",
		map[int8]int{-128: 1, -1: 2, 0: 3, 127: 4, 10: 5, 2: 6})
	probeBoth(t, "uintptr keys",
		map[uintptr]string{0: "a", 18446744073709551615: "b", 7: "c"})
	probeBoth(t, "erroring text key", map[probeErrKey]int{{}: 1})
	probeBoth(t, "NaN value under sorted keys", map[string]float64{"a": 1, "b": math.NaN()})
	probeBoth(t, "+Inf map value", map[string]float64{"x": math.Inf(1)})
	probeBoth(t, "text key with invalid utf8", map[textKey]int{{A: -1, B: -2}: 9})
}

// ---------------------------------------------------------------------------
// 4. []T where T's underlying kind is byte but T has its own marshaler:
// stdlib only base64-encodes byte slices whose element type has no
// Marshaler/TextMarshaler methods.

type probeCustomByte uint8

func (b probeCustomByte) MarshalJSON() ([]byte, error) {
	return fmt.Appendf(nil, "%d", int(b)+1), nil
}

type probeCustomTextByte uint8

func (b probeCustomTextByte) MarshalText() ([]byte, error) {
	return fmt.Appendf(nil, "x%d", int(b)), nil
}

func TestProbeCustomByteSliceElements(t *testing.T) {
	type doc struct {
		B []probeCustomByte `json:"b"`
	}
	probeBoth(t, "custom-marshaler byte slice", doc{B: []probeCustomByte{1, 2}})

	type tdoc struct {
		B []probeCustomTextByte `json:"b"`
	}
	probeBoth(t, "custom-text byte slice", tdoc{B: []probeCustomTextByte{1, 2}})

	type adoc struct {
		B [2]byte `json:"b"`
	}
	probeBoth(t, "byte array stays numeric", adoc{B: [2]byte{1, 2}})

	type named struct {
		B namedBlob `json:"b"`
	}
	probeBoth(t, "plain named byte slice stays base64", named{B: namedBlob{1, 2}})
}

// ---------------------------------------------------------------------------
// 5. omitempty over every kind, including the zero-length array quirk.

type probeZeroArray struct {
	A [0]int         `json:"a,omitempty"`
	B [2]int         `json:"b,omitempty"`
	C struct{}       `json:"c,omitempty"`
	D any            `json:"d,omitempty"`
	E *int           `json:"e,omitempty"`
	F json.Number    `json:"f,omitempty"`
	G map[string]int `json:"g,omitempty"`
	H []int          `json:"h,omitempty"`
	I uintptr        `json:"i,omitempty"`
	J bool           `json:"j,string,omitempty"`
	K string         `json:"k,string,omitempty"`
	L float32        `json:"l,omitempty"`
}

func TestProbeOmitemptyKinds(t *testing.T) {
	zero := 0
	probeBoth(t, "all zero omitempty", probeZeroArray{})
	probeBoth(t, "zero pointer target kept", probeZeroArray{E: &zero})
	probeBoth(t, "interface holding zero kept", probeZeroArray{D: 0})
	probeBoth(t, "empty-but-non-nil map omitted", probeZeroArray{G: map[string]int{}})
	probeBoth(t, "empty slice omitted", probeZeroArray{H: []int{}})
	probeBoth(t, "quoted string non-empty", probeZeroArray{K: "x"})
	probeBoth(t, "negative zero float32 omitted", probeZeroArray{L: float32(math.Copysign(0, -1))})

	type omitTime struct {
		T time.Time `json:"t,omitempty"`
	}
	probeBoth(t, "zero time not omitted", omitTime{})

	type omitIface struct {
		S fmt.Stringer `json:"s,omitempty"`
	}
	probeBoth(t, "nil non-empty interface omitted", omitIface{})
}

// ---------------------------------------------------------------------------
// 6. Struct shape edge cases: dominance, duplicate tags, NoNameTag,
// unexported with tags, embedded duplication annihilation.

type probeTaggedWins struct {
	A    int `json:"name"`
	B    int `json:"other"` // does not collide
	Name int
}

type probeNoName struct {
	V int `json:",omitempty"`
	W int `json:","`
}

type probeUnexportedTag struct {
	v int
	W int `json:"w"`
}

type probeDupA struct {
	V int `json:"v"`
}
type probeWrapX struct{ probeDupA }
type probeWrapY struct{ probeDupA }
type probeDupOuter struct {
	probeWrapX
	probeWrapY
	Own int `json:"own"`
}

type probeL3 struct{ probeDupA }
type probeL2a struct{ probeL3 }
type probeL2b struct{ probeL3 }
type probeDeepDup struct {
	probeL2a
	probeL2b
	Own int `json:"own"`
}

type probeShallow struct {
	probeWrapX     // provides v at depth 2
	V          int `json:"v"` // depth 1 wins
}

type probeSameDepthTagged struct {
	probeTagV   // tagged v at depth 2
	probePlainV // untagged field named V at depth 2
}
type probeTagV struct {
	T int `json:"v"`
}
type probePlainV struct {
	V int
}

type probeIfaceEmbed struct {
	fmt.Stringer
	N int `json:"n"`
}

// Embedded pointer to unexported struct type: encode reads through it.
type probeUnexpPtrEmbed struct {
	*hidden
	Top int `json:"top"`
}

func TestProbeUnexportedPointerEmbed(t *testing.T) {
	probeBoth(t, "nil unexported embedded pointer", probeUnexpPtrEmbed{Top: 1})
	probeBoth(t, "set unexported embedded pointer", probeUnexpPtrEmbed{hidden: &hidden{Inner: "i"}, Top: 2})
}

// Promoted MarshalJSON from an embedded type takes over the whole struct.
type probeEmbedsTime struct {
	time.Time
	Ignored int `json:"ignored"`
}

func TestProbePromotedMarshalerTakesOver(t *testing.T) {
	probeBoth(t, "struct embedding time.Time", probeEmbedsTime{
		Time:    time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		Ignored: 42,
	})
}

// ---------------------------------------------------------------------------
// 7. Deep non-cyclic pointer nesting: stdlib Marshal has no depth limit,
// only cycle detection over identical pointers.

type probeChain struct {
	Next *probeChain `json:"next,omitempty"`
	V    int         `json:"v,omitempty"`
}

func buildProbeChain(depth int) *probeChain {
	head := &probeChain{V: 1}
	for range depth {
		head = &probeChain{Next: head}
	}
	return head
}

func TestProbeDeepPointerNesting(t *testing.T) {
	for _, depth := range []int{500, 9000, 12000} {
		v := buildProbeChain(depth)
		want, wantErr := json.Marshal(v)
		got, gotErr := Marshal(v)
		if (gotErr == nil) != (wantErr == nil) {
			t.Errorf("depth %d: acceptance differs: simdjson=%v stdlib=%v", depth, gotErr, wantErr)
			continue
		}
		if gotErr == nil && !bytes.Equal(got, want) {
			t.Errorf("depth %d: bytes differ (len %d vs %d)", depth, len(got), len(want))
		}
	}

	// Actual cycle: both must error rather than hang.
	a := &probeChain{}
	a.Next = a
	_, gotErr := Marshal(a)
	_, wantErr := json.Marshal(a)
	if (gotErr == nil) != (wantErr == nil) {
		t.Errorf("cycle: acceptance differs: simdjson=%v stdlib=%v", gotErr, wantErr)
	}
}

// ---------------------------------------------------------------------------
// 8. MarshalJSON output shapes: nil, empty, whitespace-padded, null, invalid
// JSON, invalid UTF-8 (documented strictness carve-out), U+2028 raw bytes,
// HTML specials; TextMarshaler with invalid UTF-8; panicking marshalers.

type probeRawOut struct{ Out string }

func (r probeRawOut) MarshalJSON() ([]byte, error) {
	if r.Out == "<nil>" {
		return nil, nil
	}
	return []byte(r.Out), nil
}

type probeTextOut struct{ Out string }

func (r probeTextOut) MarshalText() ([]byte, error) { return []byte(r.Out), nil }

type probePanicMarshaler struct{}

func (probePanicMarshaler) MarshalJSON() ([]byte, error) { panic("marshaler exploded") }

func TestProbeMarshalerOutputShapes(t *testing.T) {
	outputs := []string{
		"<nil>",                          // nil slice
		"",                               // empty
		"null",                           // bare null passthrough
		" null ",                         // padded null
		` "x" `,                          // padded string
		"{\"a\" : 1,\n\"b\":[ 1 , 2 ] }", // needs compaction
		`{"a":1}`,                        // already compact
		`"<&>"`,                          // HTML re-escape
		"\"\u2028\u2029\"",               // raw line separators re-escape
		"\"\\u2028\"",                    // pre-escaped passthrough
		`{`,                              // invalid
		`123 456`,                        // trailing data
		"[1,2,]",                         // trailing comma
		"\t[1]\n",                        // whitespace around array
		`"tab	char"`,                     // raw tab inside string: invalid JSON both ways
	}
	for _, out := range outputs {
		type doc struct {
			V probeRawOut `json:"v"`
		}
		probeBoth(t, fmt.Sprintf("marshaler output %q", out), doc{V: probeRawOut{Out: out}})
	}
}

func TestProbeMarshalerInvalidUTF8CarveOut(t *testing.T) {
	// Documented strictness divergence: simdjson validates MarshalJSON output
	// as strict JSON including UTF-8; stdlib's compact() does not examine
	// string contents. Both must at least be deterministic; record whichever
	// way each library goes so the divergence stays exactly the carve-out.
	type doc struct {
		V probeRawOut `json:"v"`
	}
	v := doc{V: probeRawOut{Out: "\"\xff\""}}
	want, wantErr := json.Marshal(&v)
	got, gotErr := Marshal(&v)
	if wantErr != nil {
		t.Fatalf("stdlib unexpectedly rejects invalid UTF-8 from MarshalJSON: %v", wantErr)
	}
	if gotErr == nil {
		if !bytes.Equal(got, want) {
			t.Errorf("invalid UTF-8 passthrough differs:\nsimdjson %s\nstdlib   %s", got, want)
		}
		t.Logf("note: simdjson accepted invalid UTF-8 marshaler output (carve-out says reject)")
	}
}

func TestProbeTextMarshalerInvalidUTF8(t *testing.T) {
	type doc struct {
		V probeTextOut `json:"v"`
	}
	for _, out := range []string{"\xff", "a\xffb", "\xed\xa0\x80", "ok\xc3"} {
		probeBoth(t, fmt.Sprintf("text marshaler output %q", out), doc{V: probeTextOut{Out: out}})
	}
}

func TestProbePanickingMarshalerPropagates(t *testing.T) {
	type doc struct {
		V probePanicMarshaler `json:"v"`
	}
	v := doc{}
	_, _, gotPanic, _, _, wantPanic := marshalBothWithRecover(t, v)
	if (gotPanic != nil) != (wantPanic != nil) {
		t.Errorf("panic propagation differs: simdjson=%v stdlib=%v", gotPanic, wantPanic)
	}
}

// ---------------------------------------------------------------------------
// 9. json.Number literal acceptance parity.

func TestProbeNumberLiterals(t *testing.T) {
	literals := []string{
		"", "0", "-0", "1", "-1", "0.5", "1.", ".5", "-", "+1", "01", "0123",
		"1e", "1e+", "1E5", "1e-0", "1e309", "1e999", "-1.5e-300",
		"9007199254740993", "18446744073709551616", "1e+21", "  1", "1 ",
		"NaN", "Infinity", "0x10", "1_000",
	}
	type doc struct {
		N json.Number `json:"n"`
	}
	for _, lit := range literals {
		probeBoth(t, fmt.Sprintf("number literal %q", lit), doc{N: json.Number(lit)})
	}
	// json.Number inside any and as map value.
	for _, lit := range []string{"5.5", "1e", ""} {
		probeBoth(t, fmt.Sprintf("any number %q", lit), any(json.Number(lit)))
		probeBoth(t, fmt.Sprintf("map number %q", lit), map[string]json.Number{"k": json.Number(lit)})
	}
	type qdoc struct {
		N json.Number `json:"n,string"`
	}
	probeBoth(t, "quoted empty number", qdoc{})
	probeBoth(t, "quoted number", qdoc{N: "5.5"})
}

// ---------------------------------------------------------------------------
// 10. Long-string escape parity: specials at every offset around SIMD chunk
// boundaries, in both HTML modes, against the stdlib Encoder for the
// no-escape mode.

func stdlibNoHTML(t *testing.T, v any) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		t.Fatal(err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

func TestProbeLongStringSpecialOffsets(t *testing.T) {
	specials := []string{"\"", "\\", "\x00", "\x1f", "\n", "\x7f", "<", ">", "&",
		" ", " ", "\xff", "\xed\xa0\x80", "é", "日"}
	lengths := []int{17, 31, 32, 33, 47, 63, 64, 65, 100, 127, 128, 129}
	encPlain, err := CompileEncoder[string](EncoderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	encNoHTML, err := CompileEncoder[string](EncoderOptions{DisableHTMLEscaping: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range lengths {
		base := strings.Repeat("a", n)
		for pos := 0; pos <= n; pos += 1 {
			for _, sp := range specials {
				if pos > n {
					continue
				}
				s := base[:pos] + sp + base[pos:]
				want, wantErr := json.Marshal(s)
				got, gotErr := encPlain.AppendJSON(nil, &s)
				if (gotErr == nil) != (wantErr == nil) || !bytes.Equal(got, want) {
					t.Fatalf("html mode len=%d pos=%d special=%q:\nsimdjson %q err=%v\nstdlib   %q err=%v",
						n, pos, sp, got, gotErr, want, wantErr)
				}
				wantRaw := stdlibNoHTML(t, s)
				gotRaw, gotErrRaw := encNoHTML.AppendJSON(nil, &s)
				if gotErrRaw != nil || !bytes.Equal(gotRaw, wantRaw) {
					t.Fatalf("raw mode len=%d pos=%d special=%q:\nsimdjson %q err=%v\nstdlib   %q",
						n, pos, sp, gotRaw, gotErrRaw, wantRaw)
				}
			}
		}
	}
}

func TestProbeControlBytesAllValues(t *testing.T) {
	enc, err := CompileEncoder[string](EncoderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for c := 0; c < 0x20; c++ {
		s := "pad-pad-pad-pad-pad" + string(rune(c)) + "tail-tail-tail-tail"
		want, _ := json.Marshal(s)
		got, gotErr := enc.AppendJSON(nil, &s)
		if gotErr != nil || !bytes.Equal(got, want) {
			t.Fatalf("control byte %#x: simdjson %q err=%v, stdlib %q", c, got, gotErr, want)
		}
	}
}

// ---------------------------------------------------------------------------
// 11. any/interface contents: RawMessage compaction, typed nils, exotic
// nesting.

type probeValueMarshalerStruct struct{}

func (probeValueMarshalerStruct) MarshalJSON() ([]byte, error) { return []byte(`"vm"`), nil }

func TestProbeAnyContents(t *testing.T) {
	probeBoth(t, "raw message inside any compacts", any(json.RawMessage("{\"x\" : 1 }")))
	probeBoth(t, "nil raw message inside any", any(json.RawMessage(nil)))
	probeBoth(t, "empty raw message inside any", any(json.RawMessage{}))
	probeBoth(t, "typed nil plain pointer", any((*int)(nil)))
	probeBoth(t, "typed nil pointer with value-receiver marshaler", any((*probeValueMarshalerStruct)(nil)))
	probeBoth(t, "typed nil pointer with pointer-receiver marshaler", any((*retainingCustomReceiver)(nil)))
	probeBoth(t, "typed nil map", any(map[string]int(nil)))
	probeBoth(t, "typed nil slice", any([]int(nil)))
	probeBoth(t, "nil interface in slice", []any{nil, 1, "x"})
	probeBoth(t, "float32 inside any", any(float32(1.5)))
	probeBoth(t, "uint64 max inside any", any(uint64(math.MaxUint64)))
	probeBoth(t, "map with exotic keys inside any", any(map[string]any{
		" ": 1, "<&>": 2, "": 3, "\x01": 4, "é": 5,
	}))
	probeBoth(t, "text-marshaler map inside any", any(map[textKey]bool{{A: 1, B: 2}: true}))
	probeBoth(t, "raw message nested in map in any", any(map[string]any{
		"r": json.RawMessage(" [ 1 ,2] "),
	}))
	probeBoth(t, "chan inside nested any errors", any(map[string]any{"c": make(chan int)}))
}

// ---------------------------------------------------------------------------
// 12. time.Time parity across zones, precision, and error acceptance.

func TestProbeTimeParity(t *testing.T) {
	zones := []*time.Location{
		time.UTC,
		time.FixedZone("plus", 5*3600+1800),
		time.FixedZone("minus", -(9*3600 + 45*60)),
	}
	base := []time.Time{
		{},
		time.Date(1, 1, 1, 0, 0, 0, 1, time.UTC),
		time.Date(2026, 7, 14, 1, 2, 3, 999999999, time.UTC),
		time.Date(2026, 7, 14, 1, 2, 3, 4000, time.UTC),
		time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC),
		time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(-1, 6, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 7, 14, 1, 2, 3, 0, time.FixedZone("secoff", 3661)),
	}
	type doc struct {
		T time.Time `json:"t"`
	}
	for _, loc := range zones {
		for _, ts := range base {
			v := doc{T: ts.In(loc)}
			probeBoth(t, fmt.Sprintf("time %v in %v", ts, loc), v)
		}
	}
	// Monotonic-clock-carrying time.
	probeBoth(t, "time.Now monotonic", struct {
		T time.Time `json:"t"`
	}{T: time.Now()})
}

// ---------------------------------------------------------------------------
// 13. Float spellings at documented thresholds (exact spot checks on top of
// the random differential suites).

func TestProbeFloatThresholds(t *testing.T) {
	values := []float64{
		1e20, 1e21, 9.999999999999997e20, 1.0000000000000001e21,
		1e-6, 1e-7, 9.999999999999999e-7,
		5e-324, math.SmallestNonzeroFloat64, -5e-324,
		math.MaxFloat64, -math.MaxFloat64,
		float64(math.MaxFloat32), float64(math.SmallestNonzeroFloat32),
		0.1, 2.2250738585072014e-308, // smallest normal
	}
	type doc struct {
		F float64 `json:"f"`
		G float32 `json:"g"`
	}
	for _, f := range values {
		g := float32(f)
		if math.IsInf(float64(g), 0) {
			g = 0
		}
		probeBoth(t, fmt.Sprintf("float %g", f), doc{F: f, G: g})
	}
	probeBoth(t, "NaN top-level field errors", struct {
		F float64 `json:"f"`
	}{F: math.NaN()})
	probeBoth(t, "float32 quoted threshold", struct {
		G float32 `json:"g,string"`
	}{G: 1e21})
}

// ---------------------------------------------------------------------------
// 14. AppendJSON error-path contract: length-unchanged result, prefix intact.

func TestProbeAppendJSONErrorPathPreservesPrefix(t *testing.T) {
	type doc struct {
		A string  `json:"a"`
		F float64 `json:"f"`
	}
	enc, err := CompileEncoder[doc](EncoderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	prefix := []byte(`{"keep":true}`)
	storage := make([]byte, 0, 256)
	storage = append(storage, prefix...)
	v := doc{A: "text", F: math.NaN()}
	out, encErr := enc.AppendJSON(storage, &v)
	if encErr == nil {
		t.Fatal("expected NaN error")
	}
	if len(out) != len(prefix) {
		t.Fatalf("error path changed length: %d, want %d", len(out), len(prefix))
	}
	if !bytes.Equal(out, prefix) {
		t.Fatalf("error path corrupted prefix: %q", out)
	}
}

// ---------------------------------------------------------------------------
// 14b. Pinpoint the encoder depth threshold for pointer chains and show the
// decode->encode asymmetry: a document simdjson decodes cannot be re-encoded.

func TestProbeDepthThresholdAndRoundTrip(t *testing.T) {
	// Each list node costs two depth units in the encoder (pointer + struct).
	for _, tc := range []struct{ nodes int }{{4999}, {5000}, {5001}, {6000}} {
		v := buildProbeChain(tc.nodes - 1) // total nodes = tc.nodes
		_, wantErr := json.Marshal(v)
		_, gotErr := Marshal(v)
		t.Logf("nodes=%d simdjson err=%v stdlib err=%v", tc.nodes, gotErr != nil, wantErr != nil)
	}

	// Build JSON nested 6000 objects deep. simdjson decodes it (6000 < 10000
	// containers) — can it re-encode its own decode?
	depth := 6000
	var sb strings.Builder
	for range depth {
		sb.WriteString(`{"next":`)
	}
	sb.WriteString(`{"v":1}`)
	for range depth {
		sb.WriteString(`}`)
	}
	src := []byte(sb.String())
	var head probeChain
	if err := Unmarshal(src, &head); err != nil {
		t.Fatalf("simdjson failed to decode depth-%d doc: %v", depth, err)
	}
	_, gotErr := Marshal(&head)
	var stdHead probeChain
	if err := json.Unmarshal(src, &stdHead); err != nil {
		t.Fatalf("stdlib failed to decode: %v", err)
	}
	_, wantErr := json.Marshal(&stdHead)
	if (gotErr == nil) != (wantErr == nil) {
		t.Errorf("re-encode of own decode at depth %d: simdjson err=%v, stdlib err=%v", depth, gotErr != nil, wantErr)
	}
}

// ---------------------------------------------------------------------------
// 14c. More map key classifications.

type probeIntTextKey int

func (k probeIntTextKey) MarshalText() ([]byte, error) { return fmt.Appendf(nil, "i%d", int(k)), nil }

type probePtrOnlyTextStringKey string

func (k *probePtrOnlyTextStringKey) MarshalText() ([]byte, error) { return []byte("PTRONLY"), nil }

// ---------------------------------------------------------------------------
// 14d. DisableHTMLEscaping parity for field names and NaN inside any.

func TestProbeDisableHTMLEscapingFieldNames(t *testing.T) {
	type doc struct {
		A string `json:"a<b"`
		B string `json:"c&d"`
	}
	v := doc{A: "<x>", B: "&y"}
	want := stdlibNoHTML(t, &v)
	enc, err := CompileEncoder[doc](EncoderOptions{DisableHTMLEscaping: true})
	if err != nil {
		t.Fatal(err)
	}
	got, err := enc.AppendJSON(nil, &v)
	if err != nil || !bytes.Equal(got, want) {
		t.Errorf("no-escape field names: simdjson %s err=%v, stdlib %s", got, err, want)
	}
}

func TestProbeNaNInsideAny(t *testing.T) {
	probeBoth(t, "NaN inside any", any(math.NaN()))
	probeBoth(t, "Inf inside []any", []any{1.0, math.Inf(-1)})
	probeBoth(t, "NaN float32 field", struct {
		F float32 `json:"f"`
	}{F: float32(math.NaN())})
}

// ---------------------------------------------------------------------------
// 15. Top-level scalars and containers via the generic entry point.

func TestProbeTopLevelValues(t *testing.T) {
	probeBoth(t, "top-level string with specials", "a\"b\\c\ncontrol\x01<&> end")
	probeBoth(t, "top-level negative zero", math.Copysign(0, -1))
	probeBoth(t, "top-level nil byte slice", []byte(nil))
	probeBoth(t, "top-level empty byte slice", []byte{})
	probeBoth(t, "top-level byte slice", []byte{0, 1, 254, 255})
	probeBoth(t, "top-level nil map", map[string]int(nil))
	probeBoth(t, "top-level bool", true)
	probeBoth(t, "top-level json.RawMessage", json.RawMessage(` {"a": 1} `))
	probeBoth(t, "top-level pointer to pointer", func() **int { x := 5; p := &x; return &p }())
}
