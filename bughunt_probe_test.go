package simdjson

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Probe A: duplicate-key semantics must agree across every pointer lookup path.
// Node.Get and Value.Get document last-wins; GetRaw is tested for last-wins in
// TestGetRawPointer. ScanRaw stops early, so its duplicate behavior is the open
// question.
// ---------------------------------------------------------------------------

func probeIndex(t *testing.T, src []byte) Index {
	t.Helper()
	count, err := RequiredIndexEntries(src)
	if err != nil {
		t.Fatal(err)
	}
	index, err := BuildIndex(src, make([]IndexEntry, count))
	if err != nil {
		t.Fatal(err)
	}
	return index
}

// Duplicate object keys resolve differently by contract: the Get family
// matches encoding/json's last occurrence, while the early-exit Scan family
// resolves each token to the first occurrence it meets and never sees later
// duplicates. This pins both contracts on the divergent example.
func TestProbeDuplicateKeyContracts(t *testing.T) {
	src := []byte(`{"dup":{"x":1},"dup":{}}`)

	for _, tc := range []struct {
		pointer string
		getOK   bool
		getRaw  string
		scanOK  bool
		scanRaw string
	}{
		{"/dup", true, `{}`, true, `{"x":1}`},
		{"/dup/x", false, ``, true, `1`},
		{"/dup/y", false, ``, false, ``},
		{"", true, string(src), true, string(src)},
	} {
		getRaw, getOK, getErr := GetRaw(src, tc.pointer)
		if getErr != nil || getOK != tc.getOK || string(getRaw.Bytes()) != tc.getRaw {
			t.Errorf("GetRaw(%q) = %q, %v, %v; want %q, %v", tc.pointer, getRaw.Bytes(), getOK, getErr, tc.getRaw, tc.getOK)
		}
		node, nodeOK, nodeErr := probeIndex(t, src).Pointer(tc.pointer)
		if nodeErr != nil || nodeOK != tc.getOK || string(node.Raw().Bytes()) != tc.getRaw {
			t.Errorf("Index.Pointer(%q) = %q, %v, %v; want %q, %v", tc.pointer, node.Raw().Bytes(), nodeOK, nodeErr, tc.getRaw, tc.getOK)
		}
		scanRaw, scanOK, scanErr := ScanRaw(src, tc.pointer)
		if scanErr != nil || scanOK != tc.scanOK || string(scanRaw.Bytes()) != tc.scanRaw {
			t.Errorf("ScanRaw(%q) = %q, %v, %v; want %q, %v", tc.pointer, scanRaw.Bytes(), scanOK, scanErr, tc.scanRaw, tc.scanOK)
		}
		compiled, err := CompilePointer(tc.pointer)
		if err != nil {
			t.Fatal(err)
		}
		compiledRaw, compiledOK, compiledErr := compiled.ScanRaw(src)
		if compiledErr != nil || compiledOK != tc.scanOK || string(compiledRaw.Bytes()) != tc.scanRaw {
			t.Errorf("CompiledPointer.ScanRaw(%q) = %q, %v, %v; want %q, %v", tc.pointer, compiledRaw.Bytes(), compiledOK, compiledErr, tc.scanRaw, tc.scanOK)
		}
	}
}

// ---------------------------------------------------------------------------
// Probe B: RFC 6901 conformance across all five lookup paths, with values
// compared semantically. Uses the RFC's own example document plus adversarial
// keys: escaped-in-document unicode keys, digit keys on objects, "~01" order.
// ---------------------------------------------------------------------------

type pointerOutcome struct {
	ok    bool
	isErr bool
	raw   string // canonical JSON of target when ok
}

// resolveAll runs one pointer through GetRaw, ScanRaw, compiled GetRaw,
// Index.Pointer and Value.Pointer and asserts they agree; returns the outcome.
func resolveAll(t *testing.T, src []byte, pointer string) pointerOutcome {
	t.Helper()

	canonical := func(raw []byte) string {
		c, err := Canonicalize(raw)
		if err != nil {
			t.Fatalf("pointer %q target %q is not valid JSON: %v", pointer, raw, err)
		}
		return string(c)
	}

	getRaw, getOK, getErr := GetRaw(src, pointer)
	out := pointerOutcome{ok: getOK, isErr: getErr != nil}
	if getOK {
		out.raw = canonical(getRaw.Bytes())
	}

	scanRaw, scanOK, scanErr := ScanRaw(src, pointer)
	if scanOK != getOK || (scanErr != nil) != (getErr != nil) || (scanOK && canonical(scanRaw.Bytes()) != out.raw) {
		t.Errorf("pointer %q: ScanRaw = (%q, %v, %v), GetRaw = (%q, %v, %v)",
			pointer, scanRaw.Bytes(), scanOK, scanErr, getRaw.Bytes(), getOK, getErr)
	}

	compiled, compileErr := CompilePointer(pointer)
	if compileErr != nil {
		if getErr == nil {
			t.Errorf("pointer %q: CompilePointer rejected but GetRaw accepted", pointer)
		}
		return out
	}
	cRaw, cOK, cErr := compiled.GetRaw(src)
	if cOK != getOK || (cErr != nil) != (getErr != nil) || (cOK && canonical(cRaw.Bytes()) != out.raw) {
		t.Errorf("pointer %q: compiled GetRaw = (%q, %v, %v), dynamic = (%q, %v, %v)",
			pointer, cRaw.Bytes(), cOK, cErr, getRaw.Bytes(), getOK, getErr)
	}

	node, nodeOK, nodeErr := probeIndex(t, src).Pointer(pointer)
	if nodeOK != getOK || (nodeErr != nil) != (getErr != nil) || (nodeOK && canonical(node.Raw().Bytes()) != out.raw) {
		t.Errorf("pointer %q: Index.Pointer = (%q, %v, %v), GetRaw = (%q, %v, %v)",
			pointer, node.Raw().Bytes(), nodeOK, nodeErr, getRaw.Bytes(), getOK, getErr)
	}

	value, valueOK, valueErr := Get(src, pointer)
	if valueOK != getOK || (valueErr != nil) != (getErr != nil) {
		t.Errorf("pointer %q: Value.Pointer = (%v, %v), GetRaw = (%v, %v)", pointer, valueOK, valueErr, getOK, getErr)
	} else if valueOK {
		if got := canonical(value.AppendJSON(nil)); got != out.raw {
			t.Errorf("pointer %q: Value target = %s, raw target = %s", pointer, got, out.raw)
		}
	}
	return out
}

func TestProbePointerRFC6901(t *testing.T) {
	rfcDoc := []byte(`{
		"foo": ["bar", "baz"],
		"": 0,
		"a/b": 1,
		"c%d": 2,
		"e^f": 3,
		"g|h": 4,
		"i\\j": 5,
		"k\"l": 6,
		" ": 7,
		"m~n": 8
	}`)
	for _, tc := range []struct {
		pointer string
		ok      bool
		isErr   bool
		raw     string
	}{
		{"", true, false, `{"":0," ":7,"a/b":1,"c%d":2,"e^f":3,"foo":["bar","baz"],"g|h":4,"i\\j":5,"k\"l":6,"m~n":8}`},
		{"/foo", true, false, `["bar","baz"]`},
		{"/foo/0", true, false, `"bar"`},
		{"/", true, false, `0`},
		{"/a~1b", true, false, `1`},
		{"/c%d", true, false, `2`},
		{"/e^f", true, false, `3`},
		{"/g|h", true, false, `4`},
		{"/i\\j", true, false, `5`},
		{"/k\"l", true, false, `6`},
		{"/ ", true, false, `7`},
		{"/m~0n", true, false, `8`},
		{"/foo/-", false, false, ``},                   // past-the-end: not found, no error
		{"/foo/2", false, false, ``},                   // out of range
		{"/foo/01", false, true, ``},                   // leading zero index must error
		{"/foo/00", false, true, ``},                   // ditto
		{"/foo/1e0", false, true, ``},                  // non-numeric index must error
		{"/foo/ 1", false, true, ``},                   // space is not numeric
		{"/foo/-1", false, true, ``},                   // negative index token is not numeric
		{"/foo/0/x", false, false, ``},                 // pointer into scalar: not found
		{"/nope", false, false, ``},                    // absent key
		{"/foo/99999999999999999999", false, true, ``}, // index overflows int
	} {
		got := resolveAll(t, rfcDoc, tc.pointer)
		if got.ok != tc.ok || got.isErr != tc.isErr || got.raw != tc.raw {
			t.Errorf("pointer %q = %+v, want ok=%v err=%v raw=%s", tc.pointer, got, tc.ok, tc.isErr, tc.raw)
		}
	}

	// "~01" decodes to the literal "~1", never to "/".
	tildeDoc := []byte(`{"~1":5,"~01":6,"/":7}`)
	if got := resolveAll(t, tildeDoc, "/~01"); !got.ok || got.raw != "5" {
		t.Errorf("/~01 = %+v, want the literal key %q", got, "~1")
	}
	if got := resolveAll(t, tildeDoc, "/~1"); !got.ok || got.raw != "7" {
		t.Errorf("/~1 = %+v, want the key %q", got, "/")
	}

	// Pure-digit and leading-zero keys are plain keys on objects.
	digitDoc := []byte(`{"0":10,"01":11,"-":12,"1e0":13}`)
	for pointer, want := range map[string]string{"/0": "10", "/01": "11", "/-": "12", "/1e0": "13"} {
		if got := resolveAll(t, digitDoc, pointer); !got.ok || got.raw != want {
			t.Errorf("object pointer %q = %+v, want %s", pointer, got, want)
		}
	}

	// Keys escaped in the DOCUMENT must match unescaped pointer tokens.
	escapedKeyDoc := []byte(`{"caf\u00e9":1,"tab\tkey":2,"g\uD834\uDD1Eclef":3,"a\/b":4,"nul\u0000key":5}`)
	for pointer, want := range map[string]string{
		"/caf\u00e9":       "1",
		"/tab\tkey":        "2",
		"/g\U0001D11Eclef": "3",
		"/a~1b":            "4",
		"/nul\x00key":      "5",
	} {
		if got := resolveAll(t, escapedKeyDoc, pointer); !got.ok || got.raw != want {
			t.Errorf("escaped-doc-key pointer %q = %+v, want %s", pointer, got, want)
		}
	}
	// Raw unicode key in the document, matched by the identical token.
	rawKeyDoc := []byte(`{"café":1,"𝄞":2}`)
	for pointer, want := range map[string]string{"/café": "1", "/𝄞": "2"} {
		if got := resolveAll(t, rawKeyDoc, pointer); !got.ok || got.raw != want {
			t.Errorf("raw-key pointer %q = %+v, want %s", pointer, got, want)
		}
	}

	// Empty keys, nested.
	emptyKeyDoc := []byte(`{"":{"":[5,6]}}`)
	if got := resolveAll(t, emptyKeyDoc, "//"); !got.ok || got.raw != "[5,6]" {
		t.Errorf("// = %+v, want [5,6]", got)
	}
	if got := resolveAll(t, emptyKeyDoc, "///1"); !got.ok || got.raw != "6" {
		t.Errorf("///1 = %+v, want 6", got)
	}

	// Very long token.
	longKey := strings.Repeat("k", 8192)
	longDoc := []byte(`{"` + longKey + `":42}`)
	if got := resolveAll(t, longDoc, "/"+longKey); !got.ok || got.raw != "42" {
		t.Errorf("long token = %+v, want 42", got)
	}

	// Pointer into scalar root.
	if got := resolveAll(t, []byte(`5`), "/a"); got.ok || got.isErr {
		t.Errorf("scalar root /a = %+v, want not-found without error", got)
	}
	if got := resolveAll(t, []byte(`"text"`), "/0"); got.ok || got.isErr {
		t.Errorf("string root /0 = %+v, want not-found without error", got)
	}
}

// ---------------------------------------------------------------------------
// Probe C: accessor kind discipline. Wrong-kind accessors must report !ok with
// zero values on every API (Node, RawValue, Value).
// ---------------------------------------------------------------------------

func TestProbeAccessorWrongKinds(t *testing.T) {
	src := []byte(`{"str":"12","num":42,"bool":true,"null":null,"arr":[1],"obj":{"a":1}}`)
	index := probeIndex(t, src)
	value, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}

	check := func(name string, gotOK bool, zero bool) {
		t.Helper()
		if gotOK {
			t.Errorf("%s: ok = true on wrong kind", name)
		}
		if !zero {
			t.Errorf("%s: non-zero result on wrong kind", name)
		}
	}

	for _, field := range []string{"str", "num", "bool", "null", "arr", "obj"} {
		node, ok, err := index.Pointer("/" + field)
		if err != nil || !ok {
			t.Fatal(field, ok, err)
		}
		raw, ok, err := GetRaw(src, "/"+field)
		if err != nil || !ok {
			t.Fatal(field, ok, err)
		}
		val, ok := value.Get(field)
		if !ok {
			t.Fatal(field)
		}
		kind := node.Kind()
		if raw.Kind() != kind || val.Kind() != kind {
			t.Fatalf("%s: kinds disagree: node %v raw %v value %v", field, kind, raw.Kind(), val.Kind())
		}

		if kind != Number {
			n, ok := node.Int64()
			check(field+" Node.Int64", ok, n == 0)
			f, ok := node.Float64()
			check(field+" Node.Float64", ok, f == 0)
			nb, ok := node.NumberBytes()
			check(field+" Node.NumberBytes", ok, nb == nil)
			rn, ok := raw.Int64()
			check(field+" RawValue.Int64", ok, rn == 0)
			rf, ok := raw.Float64()
			check(field+" RawValue.Float64", ok, rf == 0)
			vn, ok := val.Int64()
			check(field+" Value.Int64", ok, vn == 0)
			vf, ok := val.Float64()
			check(field+" Value.Float64", ok, vf == 0)
		}
		if kind != Bool {
			b, ok := node.Bool()
			check(field+" Node.Bool", ok, !b)
			rb, ok := raw.Bool()
			check(field+" RawValue.Bool", ok, !rb)
			vb, ok := val.Bool()
			check(field+" Value.Bool", ok, !vb)
		}
		if kind != String {
			sb, ok := node.StringBytes()
			check(field+" Node.StringBytes", ok, sb == nil)
			dst, ok := node.AppendString([]byte("pre"))
			check(field+" Node.AppendString", ok, string(dst) == "pre")
			text, ok, err := raw.Text()
			if err != nil {
				t.Fatal(err)
			}
			check(field+" RawValue.Text", ok, text == "")
			vt, ok := val.Text()
			check(field+" Value.Text", ok, vt == "")
		}
		if kind != Array {
			n, ok := node.ArrayLen()
			check(field+" Node.ArrayLen", ok, n == 0)
			_, ok = node.Index(0)
			check(field+" Node.Index", ok, true)
			_, ok = val.Index(0)
			check(field+" Value.Index", ok, true)
			a, ok := val.Array()
			check(field+" Value.Array", ok, a == nil)
		}
		if kind != Object {
			n, ok := node.ObjectLen()
			check(field+" Node.ObjectLen", ok, n == 0)
			_, ok = node.Get("a")
			check(field+" Node.Get", ok, true)
			_, ok = val.Get("a")
			check(field+" Value.Get", ok, true)
			o, ok := val.Object()
			check(field+" Value.Object", ok, o == nil)
		}
		if kind != Null && node.IsNull() {
			t.Errorf("%s: Node.IsNull true", field)
		}
		if kind != Null && raw.IsNull() {
			t.Errorf("%s: RawValue.IsNull true", field)
		}
	}

	// Get on an empty object must be a clean miss (also exercises the
	// end-of-tape entry arithmetic under -race/checkptr).
	emptyIndex := probeIndex(t, []byte(`{}`))
	if _, ok := emptyIndex.Root().Get("missing"); ok {
		t.Error("Get on empty object returned ok")
	}
	if _, ok := probeIndex(t, []byte(`[]`)).Root().Index(0); ok {
		t.Error("Index on empty array returned ok")
	}
}

// ---------------------------------------------------------------------------
// Probe D: number parsing must agree with strconv across all three accessor
// families, including overflow and exotic spellings.
// ---------------------------------------------------------------------------

func TestProbeNumberAccessorConsistency(t *testing.T) {
	spellings := []string{
		"0", "-0", "9223372036854775807", "9223372036854775808",
		"-9223372036854775808", "-9223372036854775809",
		"18446744073709551615", "123456789012345678901234567890",
		"1e2", "1E+2", "1.0", "0.5", "-0.0", "1e999", "-1e999", "1e-999",
		"5e-324", "2.2250738585072011e-308", "9007199254740993",
		"1" + strings.Repeat("0", 400),
		"0." + strings.Repeat("0", 400) + "1",
	}
	for _, spelling := range spellings {
		src := []byte("[" + spelling + "]")

		wantInt, intErr := strconv.ParseInt(spelling, 10, 64)
		wantIntOK := intErr == nil
		wantFloat, floatErr := strconv.ParseFloat(spelling, 64)
		wantFloatOK := floatErr == nil

		node, ok, err := probeIndex(t, src).Pointer("/0")
		if err != nil || !ok {
			t.Fatal(spelling, err)
		}
		raw, ok, err := GetRaw(src, "/0")
		if err != nil || !ok {
			t.Fatal(spelling, err)
		}
		val, ok, err := Get(src, "/0")
		if err != nil || !ok {
			t.Fatal(spelling, err)
		}

		if text, ok := node.NumberText(); !ok || text != spelling {
			t.Errorf("%s: Node.NumberText = %q, %v", spelling, text, ok)
		}
		if text, ok := raw.NumberText(); !ok || text != spelling {
			t.Errorf("%s: RawValue.NumberText = %q, %v", spelling, text, ok)
		}
		if text, ok := val.NumberText(); !ok || text != spelling {
			t.Errorf("%s: Value.NumberText = %q, %v", spelling, text, ok)
		}

		for name, got := range map[string]func() (int64, bool){
			"Node.Int64":     node.Int64,
			"RawValue.Int64": raw.Int64,
			"Value.Int64":    val.Int64,
		} {
			n, ok := got()
			if ok != wantIntOK || (ok && n != wantInt) {
				t.Errorf("%s: %s = %d, %v; strconv = %d, %v", spelling, name, n, ok, wantInt, wantIntOK)
			}
		}
		for name, got := range map[string]func() (float64, bool){
			"Node.Float64":     node.Float64,
			"RawValue.Float64": raw.Float64,
			"Value.Float64":    val.Float64,
		} {
			f, ok := got()
			if ok != wantFloatOK || (ok && f != wantFloat) {
				t.Errorf("%s: %s = %g, %v; strconv = %g, %v", spelling, name, f, ok, wantFloat, wantFloatOK)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Probe E: string decoding must be byte-exact with encoding/json for every
// escape form, on Node.AppendString, RawValue.Text, Value.Text, EachObject
// keys, and Node.Get key matching.
// ---------------------------------------------------------------------------

func TestProbeStringDecodingVsStdlib(t *testing.T) {
	quoted := []string{
		`"plain"`,
		`""`,
		`"\u0041"`,
		`"\u0041B\u0043"`,
		`"\uD834\uDD1E"`,
		`"g\uD834\uDD1Eclef"`,
		`"\n\t\b\f\r\"\\\/"`,
		`"\u0000"`,
		`"a\u007Fb"`,
		`"\u00e9 raw and \u00E9 escaped"`,
		"\"\\u00e9 mixed raw \u00e9\"",
		`"\u2028\u2029"`,
		`"` + strings.Repeat(`\u00e9`, 300) + `"`,
		`"` + strings.Repeat(`ab\n`, 300) + `"`,
		`"ends with escape\\"`,
		"\"\uFFFD raw pass-through\"",
		`"\uFFFF"`,
	}
	for _, q := range quoted {
		var want string
		if err := json.Unmarshal([]byte(q), &want); err != nil {
			t.Fatalf("stdlib rejected %s: %v", q, err)
		}

		src := []byte("[" + q + "]")
		node, ok, err := probeIndex(t, src).Pointer("/0")
		if err != nil || !ok {
			t.Fatal(q, err)
		}
		if got, ok := node.AppendString(nil); !ok || string(got) != want {
			t.Errorf("Node.AppendString(%s) = %q, %v; want %q", q, got, ok, want)
		}
		if got, ok := node.AppendString([]byte("prefix-")); !ok || string(got) != "prefix-"+want {
			t.Errorf("Node.AppendString(%s) with prefix = %q, %v", q, got, ok)
		}
		if sb, ok := node.StringBytes(); ok && string(sb) != want {
			t.Errorf("Node.StringBytes(%s) = %q, want %q", q, sb, want)
		}

		raw, ok, err := GetRaw(src, "/0")
		if err != nil || !ok {
			t.Fatal(q, err)
		}
		if got, ok, err := raw.Text(); err != nil || !ok || got != want {
			t.Errorf("RawValue.Text(%s) = %q, %v, %v; want %q", q, got, ok, err, want)
		}

		val, ok, err := Get(src, "/0")
		if err != nil || !ok {
			t.Fatal(q, err)
		}
		if got, ok := val.Text(); !ok || got != want {
			t.Errorf("Value.Text(%s) = %q, %v; want %q", q, got, ok, want)
		}

		// The same content as an object key: EachObject key decoding, Node.Get,
		// and Value.Get key matching (tapeKeyEqual path).
		keyDoc := []byte("{" + q + ":1}")
		var gotKeys []string
		if err := EachObject(keyDoc, func(key string, _ RawValue) error {
			gotKeys = append(gotKeys, key)
			return nil
		}); err != nil {
			t.Fatalf("EachObject(%s): %v", keyDoc, err)
		}
		if len(gotKeys) != 1 || gotKeys[0] != want {
			t.Errorf("EachObject key for %s = %q, want %q", q, gotKeys, want)
		}
		if _, ok := probeIndex(t, keyDoc).Root().Get(want); !ok {
			t.Errorf("Node.Get(%q) missed key spelled %s", want, q)
		}
		keyValue, err := Parse(keyDoc)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := keyValue.Get(want); !ok {
			t.Errorf("Value.Get(%q) missed key spelled %s", want, q)
		}
		// Near-miss keys must not match.
		if want != "" {
			miss := want[:len(want)-1]
			if _, ok := probeIndex(t, keyDoc).Root().Get(miss); ok {
				t.Errorf("Node.Get(%q) matched key spelled %s", miss, q)
			}
			if _, ok := probeIndex(t, keyDoc).Root().Get(want + "x"); ok {
				t.Errorf("Node.Get(%q) matched key spelled %s", want+"x", q)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Probe F: iterators. Order, duplicates, early stop, empty containers,
// whitespace-heavy documents, and Index iterator agreement.
// ---------------------------------------------------------------------------

func TestProbeIterationSemantics(t *testing.T) {
	src := []byte(" { \"b\" : 1 ,\n\t\"a\" : [ true , null ] ,\r\"b\" : \"x\" } ")

	// EachObject must deliver every member, duplicates included, in order.
	var keys []string
	var vals []string
	if err := EachObject(src, func(key string, value RawValue) error {
		keys = append(keys, key)
		vals = append(vals, string(value.Bytes()))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(keys, []string{"b", "a", "b"}) {
		t.Errorf("EachObject keys = %q", keys)
	}
	if !reflect.DeepEqual(vals, []string{"1", "[ true , null ]", `"x"`}) {
		t.Errorf("EachObject values = %q", vals)
	}

	// Value tree retains both duplicates in order.
	value, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	members, ok := value.Object()
	if !ok || len(members) != 3 {
		t.Fatalf("Object() = %v, %v", members, ok)
	}
	for i, wantKey := range []string{"b", "a", "b"} {
		if members[i].Key != wantKey {
			t.Errorf("member %d key = %q, want %q", i, members[i].Key, wantKey)
		}
	}
	if got, _ := value.Get("b"); got.Kind() != String {
		t.Errorf("Value.Get(b) kind = %v, want last-wins String", got.Kind())
	}

	// Index ObjectIter agrees, both pull styles.
	root := probeIndex(t, src).Root()
	iter, ok := root.ObjectIter()
	if !ok {
		t.Fatal("ObjectIter")
	}
	var iterKeys []string
	for {
		key, val, ok := iter.Next()
		if !ok {
			break
		}
		kb, _ := key.AppendString(nil)
		iterKeys = append(iterKeys, string(kb))
		if val.Kind() == Invalid {
			t.Error("iterator value invalid")
		}
	}
	if !reflect.DeepEqual(iterKeys, keys) {
		t.Errorf("ObjectIter keys = %q, EachObject keys = %q", iterKeys, keys)
	}
	cursor, _ := root.ObjectIter()
	var cursorKeys []string
	for ; cursor.Valid(); cursor = cursor.Advance() {
		key, _ := cursor.Current()
		kb, _ := key.AppendString(nil)
		cursorKeys = append(cursorKeys, string(kb))
	}
	if !reflect.DeepEqual(cursorKeys, keys) {
		t.Errorf("ObjectIter cursor keys = %q, want %q", cursorKeys, keys)
	}

	// Early stop: sentinel error must be returned as-is and stop iteration.
	stopErr := errProbeStop
	calls := 0
	err = EachObject(src, func(string, RawValue) error {
		calls++
		if calls == 2 {
			return stopErr
		}
		return nil
	})
	if err != stopErr || calls != 2 {
		t.Errorf("early stop: err = %v, calls = %d", err, calls)
	}

	// Empty containers with whitespace.
	if err := EachArray([]byte(" [ \n ] "), func(int, RawValue) error {
		t.Error("callback on empty array")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := EachObject([]byte(" { \t } "), func(string, RawValue) error {
		t.Error("callback on empty object")
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Wrong container kinds.
	if err := EachArray([]byte(`{"a":1}`), nil); err == nil {
		t.Error("EachArray accepted an object")
	}
	if err := EachObject([]byte(`[1]`), nil); err == nil {
		t.Error("EachObject accepted an array")
	}

	// EachArray element raw spans are exact even with whitespace padding.
	var elems []string
	if err := EachArray([]byte("[ 1 , [ 2 ] , \"s\" ]"), func(_ int, v RawValue) error {
		elems = append(elems, string(v.Bytes()))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(elems, []string{"1", "[ 2 ]", `"s"`}) {
		t.Errorf("EachArray elements = %q", elems)
	}

	// ArrayIter and FlatArrayIter agree on a flat array.
	flatSrc := []byte(`[1,"two",null,true,[],{}]`)
	flatRoot := probeIndex(t, flatSrc).Root()
	gen, _ := flatRoot.ArrayIter()
	flat, flatOK := flatRoot.FlatArrayIter()
	if !flatOK {
		t.Fatal("FlatArrayIter rejected flat array")
	}
	for {
		a, aOK := gen.Next()
		b, bOK := flat.Next()
		if aOK != bOK {
			t.Fatal("iterator lengths differ")
		}
		if !aOK {
			break
		}
		if !bytes.Equal(a.Raw().Bytes(), b.Raw().Bytes()) {
			t.Errorf("flat/general element mismatch: %q vs %q", a.Raw().Bytes(), b.Raw().Bytes())
		}
	}
	if _, ok := probeIndex(t, []byte(`[[1]]`)).Root().FlatArrayIter(); ok {
		t.Error("FlatArrayIter accepted a nested array")
	}
}

var errProbeStop = &PointerError{Pointer: "stop", Message: "sentinel"}

// Minimal repro for the empty-object Get checkptr fault: when the empty object
// is the last tape entry and storage is exactly sized, Node.Get computed a
// one-past-the-end *IndexEntry before checking the member count. Under
// -race/checkptr instrumentation this is a fatal "converted pointer straddles
// multiple allocations" crash; without instrumentation it silently violates
// the unsafe.Pointer contract.
func TestProbeEmptyObjectGetAtTapeEnd(t *testing.T) {
	index, err := BuildIndex([]byte(`{}`), make([]IndexEntry, 1))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := index.Root().Get("k"); ok {
		t.Fatal("Get on empty object returned ok")
	}

	// Same shape one level down, reached through a pointer.
	index, err = BuildIndex([]byte(`{"a":{}}`), make([]IndexEntry, 3))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := index.Pointer("/a/x"); ok || err != nil {
		t.Fatalf("Pointer(/a/x) = %v, %v", ok, err)
	}
}

// ---------------------------------------------------------------------------
// Probe G: depth-limit agreement between Valid and the seeker-based APIs.
// ---------------------------------------------------------------------------

func TestProbeDepthLimitAgreement(t *testing.T) {
	deepArray := func(depth int) []byte {
		return []byte(strings.Repeat("[", depth) + "0" + strings.Repeat("]", depth))
	}
	for _, depth := range []int{defaultMaxDepth, defaultMaxDepth + 1} {
		src := deepArray(depth)
		want := Valid(src)

		if err := EachArray(src, nil); (err == nil) != want {
			t.Errorf("depth %d: EachArray err = %v, Valid = %v", depth, err, want)
		}
		if _, _, err := GetRaw(src, "/0"); (err == nil) != want {
			t.Errorf("depth %d: GetRaw err = %v, Valid = %v", depth, err, want)
		}
		if _, _, err := ScanRaw(src, "/0"); (err == nil) != want {
			t.Errorf("depth %d: ScanRaw err = %v, Valid = %v", depth, err, want)
		}
		if _, err := AppendCompact(nil, src); (err == nil) != want {
			t.Errorf("depth %d: AppendCompact err = %v, Valid = %v", depth, err, want)
		}
		if _, err := unmarshalAnyForTest(src); (err == nil) != want {
			t.Errorf("depth %d: Unmarshal any err = %v, Valid = %v", depth, err, want)
		}
		if _, err := Indent(src, "", " "); (err == nil) != want {
			t.Errorf("depth %d: Indent err = %v, Valid = %v", depth, err, want)
		}
		if _, err := Canonicalize(src); (err == nil) != want {
			t.Errorf("depth %d: Canonicalize err = %v, Valid = %v", depth, err, want)
		}
	}

	// Nested objects too.
	deepObject := strings.Repeat(`{"k":`, defaultMaxDepth-1) + "0" + strings.Repeat("}", defaultMaxDepth-1)
	if !Valid([]byte(deepObject)) {
		t.Fatal("deep object should be valid")
	}
	if err := EachObject([]byte(deepObject), nil); err != nil {
		t.Errorf("EachObject rejected a Valid deep object: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Probe H: Canonicalize contract. Doc: "sorts object members recursively and
// emits compact JSON." Verify the observable contract precisely.
// ---------------------------------------------------------------------------

func TestProbeCanonicalizeContract(t *testing.T) {
	for _, tc := range []struct {
		name, src, want string
	}{
		// Number spellings are preserved verbatim (no RFC 8785 normalization is
		// promised or performed).
		{"numbers", `{"a":1e2,"b":-0,"c":1.50,"d":100}`, `{"a":1e2,"b":-0,"c":1.50,"d":100}`},
		// Duplicates: both retained, stable among equals, sorted by key.
		{"duplicates", `{"b":1,"a":9,"a":8}`, `{"a":9,"a":8,"b":1}`},
		// Keys compare after unescaping; output re-escapes minimally.
		{"escaped keys", `{"\u0062":1,"a":2}`, `{"a":2,"b":1}`},
		// Nested objects sorted recursively; arrays keep order.
		{"nested", `{"z":{"b":1,"a":2},"y":[{"d":1,"c":2}]}`, `{"y":[{"c":2,"d":1}],"z":{"a":2,"b":1}}`},
		// Byte-order (Go string <) key sorting.
		{"ascii order", `{"Z":1,"a":2,"0":3,"~":4}`, `{"0":3,"Z":1,"a":2,"~":4}`},
	} {
		got, err := Canonicalize([]byte(tc.src))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if string(got) != tc.want {
			t.Errorf("%s: Canonicalize(%s) = %s, want %s", tc.name, tc.src, got, tc.want)
		}
	}

	// Record the non-ASCII ordering rule: byte order, not UTF-16 code-unit
	// order. U+FB00 (EF AC 80) sorts before U+1F600 (F0 9F 98 80) by bytes;
	// RFC 8785 would sort U+1F600 (D83D DE00) before U+FB00.
	got, err := Canonicalize([]byte(`{"ﬀ":1,"😀":2}`))
	if err != nil {
		t.Fatal(err)
	}
	byteOrder := `{"ﬀ":1,"😀":2}`
	utf16Order := `{"😀":2,"ﬀ":1}`
	switch string(got) {
	case byteOrder:
		t.Logf("Canonicalize sorts keys in byte order (not RFC 8785 UTF-16 order): %s", got)
	case utf16Order:
		t.Logf("Canonicalize sorts keys in UTF-16 code-unit order: %s", got)
	default:
		t.Errorf("Canonicalize non-ASCII order = %s", got)
	}
}

// ---------------------------------------------------------------------------
// Probe I: Indent with adversarial indents and content. Whitespace indents
// must produce valid JSON that compacts back to the same document; the U+2028
// case must stay valid.
// ---------------------------------------------------------------------------

func TestProbeIndentContract(t *testing.T) {
	src := []byte(`{"a":[1,{"b":"x"},[]],"c":{},"d":"e f","g":"h` + " " + `i"}`)
	wantCompactCanonical, err := Canonicalize(src)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ prefix, indent string }{
		{"", "  "},
		{"", "\t"},
		{"", ""},
		{"\t\t", " "},
		{"", " \t "},
	} {
		pretty, err := Indent(src, tc.prefix, tc.indent)
		if err != nil {
			t.Fatalf("prefix=%q indent=%q: %v", tc.prefix, tc.indent, err)
		}
		if !Valid(pretty) {
			t.Errorf("prefix=%q indent=%q: output is not valid JSON: %s", tc.prefix, tc.indent, pretty)
		}
		roundTrip, err := Canonicalize(pretty)
		if err != nil {
			t.Fatalf("prefix=%q indent=%q: %v", tc.prefix, tc.indent, err)
		}
		if !bytes.Equal(roundTrip, wantCompactCanonical) {
			t.Errorf("prefix=%q indent=%q: semantic change:\n%s\n%s", tc.prefix, tc.indent, roundTrip, wantCompactCanonical)
		}
	}

	// U+2028/U+2029 inside strings, both raw and escaped in the source: the
	// indented output must stay valid JSON and preserve the string value.
	for _, doc := range []string{
		"[\"a b c\"]",
		`["a\u2028b\u2029c"]`,
	} {
		pretty, err := Indent([]byte(doc), "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if !Valid(pretty) {
			t.Errorf("Indent(%q) output invalid: %q", doc, pretty)
		}
		var got []string
		if err := json.Unmarshal(pretty, &got); err != nil || len(got) != 1 || got[0] != "a b c" {
			t.Errorf("Indent(%q) round-trip = %q, %v", doc, got, err)
		}
	}

	// Non-whitespace indent: match stdlib's behavior class (stdlib also emits
	// invalid JSON in that case, so just record parity).
	ours, err := Indent([]byte(`[1,2]`), "", "→")
	if err != nil {
		t.Fatal(err)
	}
	var stdBuf bytes.Buffer
	if err := json.Indent(&stdBuf, []byte(`[1,2]`), "", "→"); err != nil {
		t.Fatal(err)
	}
	if Valid(ours) != json.Valid(stdBuf.Bytes()) {
		t.Errorf("unicode indent validity: ours %v (%s), stdlib %v (%s)", Valid(ours), ours, json.Valid(stdBuf.Bytes()), stdBuf.Bytes())
	}
}

// TestProbeIndentPreservesEscapeSpelling pins Indent to json.Indent byte for
// byte: like stdlib, it copies string and number tokens from the source
// verbatim rather than re-encoding a decoded value, so \uXXXX, \/, and
// surrogate-pair spellings survive. Inputs are pre-compacted so no surrounding
// whitespace is involved.
func TestProbeIndentPreservesEscapeSpelling(t *testing.T) {
	for _, src := range []string{
		`{"k":"A\/>"}`,
		`["😀","plain"]`,
		`{"num":1e10,"big":123456789012345678,"neg":-0.5}`,
		`{"nested":{"a":"\t\n","b":[true,null,false]}}`,
		`"  "`,
	} {
		got, err := Indent([]byte(src), "", "  ")
		if err != nil {
			t.Fatalf("Indent(%s): %v", src, err)
		}
		var want bytes.Buffer
		if err := json.Indent(&want, []byte(src), "", "  "); err != nil {
			t.Fatalf("json.Indent(%s): %v", src, err)
		}
		if string(got) != want.String() {
			t.Errorf("Indent(%s) not byte-identical to json.Indent:\n simd=%q\n json=%q", src, got, want.String())
		}
	}
}

// ---------------------------------------------------------------------------
// Probe J: raw spans. Targets never include surrounding whitespace; root
// scalars work; ScanRaw's documented stop-early behavior on trailing garbage.
// ---------------------------------------------------------------------------

func TestProbeRawSpans(t *testing.T) {
	src := []byte("  { \"a\" : [ 1 , 2 ] , \"s\" : \"x\" }  ")
	raw, ok, err := GetRaw(src, "/a")
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if string(raw.Bytes()) != "[ 1 , 2 ]" {
		t.Errorf("target span = %q", raw.Bytes())
	}
	node, ok, err := probeIndex(t, src).Pointer("/a")
	if err != nil || !ok || !bytes.Equal(node.Raw().Bytes(), raw.Bytes()) {
		t.Errorf("index span = %q, raw span = %q", node.Raw().Bytes(), raw.Bytes())
	}

	// Root scalar with whitespace padding.
	rootRaw, ok, err := GetRaw([]byte("  42\n"), "")
	if err != nil || !ok || string(rootRaw.Bytes()) != "42" {
		t.Errorf("root scalar = %q, %v, %v", rootRaw.Bytes(), ok, err)
	}
	if rootRaw.Kind() != Number {
		t.Errorf("root scalar kind = %v", rootRaw.Kind())
	}

	// GetRaw validates the tail; ScanRaw documents that it does not.
	garbage := []byte(`{"a":1} trailing`)
	if _, _, err := GetRaw(garbage, "/a"); err == nil {
		t.Error("GetRaw did not validate trailing garbage")
	}
	if raw, ok, err := ScanRaw(garbage, "/a"); err != nil || !ok || string(raw.Bytes()) != "1" {
		t.Errorf("ScanRaw stop-early = %q, %v, %v", raw.Bytes(), ok, err)
	}

	// Nested Get on a RawValue re-anchors pointers relative to the target.
	inner, ok, err := raw.Pointer("/1")
	if err != nil || !ok || string(inner.Bytes()) != "2" {
		t.Errorf("RawValue.Get(/1) = %q, %v, %v", inner.Bytes(), ok, err)
	}
}

// ---------------------------------------------------------------------------
// Probe K: cross-API semantic agreement on a set of adversarial documents:
// every scalar reachable by pointer must decode identically through raw,
// index, AST, and dynamic Unmarshal+manual-walk paths.
// ---------------------------------------------------------------------------

func TestProbeCrossAPIScalarAgreement(t *testing.T) {
	docs := [][]byte{
		[]byte(`{"a":{"b":[1,"xAy",null,true,1.5e3]},"":[-0.0],"k/s":"v"}`),
		[]byte(` [ {"n":9007199254740993} , {"n":-0} , {"s":"𝄞"} ] `),
		[]byte(`{"dup":1,"dup":{"inner":"last"},"other":[[],{}]}`),
	}
	for _, src := range docs {
		var std any
		if err := json.Unmarshal(src, &std); err != nil {
			t.Fatal(err)
		}
		parsed, err := Parse(src)
		if err != nil {
			t.Fatal(err)
		}
		dynamic, err := parseAnyUseNumberForTest(src)
		if err != nil {
			t.Fatal(err)
		}
		index := probeIndex(t, src)

		var walk func(pointer string, std any)
		walk = func(pointer string, std any) {
			raw, ok, err := GetRaw(src, pointer)
			if err != nil || !ok {
				t.Fatalf("GetRaw(%q) = %v, %v", pointer, ok, err)
			}
			node, ok, err := index.Pointer(pointer)
			if err != nil || !ok {
				t.Fatalf("Index.Pointer(%q) = %v, %v", pointer, ok, err)
			}
			value, ok, err := parsed.Pointer(pointer)
			if err != nil || !ok {
				t.Fatalf("Value.Pointer(%q) = %v, %v", pointer, ok, err)
			}

			// Semantic agreement with the stdlib value at this pointer.
			var fromRaw any
			if err := json.Unmarshal(raw.Bytes(), &fromRaw); err != nil {
				t.Fatalf("raw target %q at %q: %v", raw.Bytes(), pointer, err)
			}
			if !reflect.DeepEqual(fromRaw, std) {
				t.Errorf("pointer %q: raw %v != stdlib %v", pointer, fromRaw, std)
			}
			if !bytes.Equal(node.Raw().Bytes(), raw.Bytes()) {
				t.Errorf("pointer %q: node raw %q != seeker raw %q", pointer, node.Raw().Bytes(), raw.Bytes())
			}

			switch typed := std.(type) {
			case map[string]any:
				n, ok := node.ObjectLen()
				members, mok := value.Object()
				if !ok || !mok {
					t.Fatalf("pointer %q: object accessors failed", pointer)
				}
				// stdlib map drops duplicates; our count includes them.
				if n < len(typed) || len(members) < len(typed) {
					t.Errorf("pointer %q: member counts %d/%d < stdlib %d", pointer, n, len(members), len(typed))
				}
				for key, sub := range typed {
					walk(pointer+"/"+strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1"), sub)
				}
			case []any:
				n, ok := node.ArrayLen()
				arr, aok := value.Array()
				if !ok || !aok || n != len(typed) || len(arr) != len(typed) {
					t.Fatalf("pointer %q: array lengths %d/%d, stdlib %d", pointer, n, len(arr), len(typed))
				}
				for i, sub := range typed {
					walk(pointer+"/"+strconv.Itoa(i), sub)
				}
			case string:
				got, ok := node.AppendString(nil)
				if !ok || string(got) != typed {
					t.Errorf("pointer %q: node string %q, stdlib %q", pointer, got, typed)
				}
				vt, ok := value.Text()
				if !ok || vt != typed {
					t.Errorf("pointer %q: value string %q, stdlib %q", pointer, vt, typed)
				}
			case float64:
				got, ok := node.Float64()
				if !ok || got != typed {
					t.Errorf("pointer %q: node float %g, stdlib %g", pointer, got, typed)
				}
			case bool:
				got, ok := node.Bool()
				if !ok || got != typed {
					t.Errorf("pointer %q: node bool %v, stdlib %v", pointer, got, typed)
				}
			case nil:
				if !node.IsNull() || !raw.IsNull() {
					t.Errorf("pointer %q: null not reported", pointer)
				}
			}
		}
		walk("", std)

		// The UseNumber dynamic tree must round-trip to the stdlib value semantically.
		encoded, err := json.Marshal(dynamic)
		if err != nil {
			t.Fatal(err)
		}
		var back any
		if err := json.Unmarshal(encoded, &back); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(back, std) {
			t.Errorf("dynamic tree %v != stdlib tree %v", back, std)
		}
	}
}

// ---------------------------------------------------------------------------
// Probe L: owned Parse results and RawValue.Text allocations must not alias a
// mutable src; zero-copy documents its aliasing.
// ---------------------------------------------------------------------------

func TestProbeOwnedValueSurvivesMutation(t *testing.T) {
	src := []byte(`{"clean":"alpha","escaped":"beéta","num":123.5,"arr":["x","y\ny"]}`)
	owned, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	zero, err := ParseOptions(bytes.Clone(src), Options{ZeroCopy: true})
	if err != nil {
		t.Fatal(err)
	}
	_ = zero
	for i := range src {
		src[i] = '#'
	}
	for key, want := range map[string]string{"clean": "alpha", "escaped": "beéta"} {
		v, ok := owned.Get(key)
		if !ok {
			t.Fatal(key)
		}
		if got, _ := v.Text(); got != want {
			t.Errorf("owned %s = %q after mutation, want %q", key, got, want)
		}
	}
	num, _ := owned.Get("num")
	if text, ok := num.NumberText(); !ok || text != "123.5" {
		t.Errorf("owned number = %q after mutation", text)
	}
	arr, _ := owned.Get("arr")
	if e, ok := arr.Index(1); !ok {
		t.Fatal("arr index")
	} else if got, _ := e.Text(); got != "y\ny" {
		t.Errorf("owned array elem = %q after mutation", got)
	}
}
