package simdjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"unsafe"
)

var benchmarkSink any
var indexBenchmarkSink int

func TestParseAndPointer(t *testing.T) {
	src := []byte(`{
		"name": "simdjson",
		"ok": true,
		"items": [
			{"id": 1, "text": "hello\nworld"},
			{"id": 2, "text": "\uD834\uDD1E"}
		],
		"nil": null
	}`)

	v, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if v.Kind() != Object {
		t.Fatalf("kind = %v", v.Kind())
	}
	item, ok, err := v.Pointer("/items/1/text")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("pointer target missing")
	}
	text, ok := item.Text()
	if !ok || text != "𝄞" {
		t.Fatalf("text = %q, %v", text, ok)
	}

	got := v.AppendJSON(nil)
	if !json.Valid(got) {
		t.Fatalf("marshal produced invalid JSON: %s", got)
	}
}

func TestValidCases(t *testing.T) {
	valid := [][]byte{
		[]byte(`null`),
		[]byte(`true`),
		[]byte(`false`),
		[]byte(`0`),
		[]byte(`-12.34e+56`),
		[]byte(`""`),
		[]byte(`"ascii and こんにちは"`),
		[]byte(`{"a":[1,2,3],"b":{"c":"d"}}`),
	}
	for _, src := range valid {
		if err := Validate(src); err != nil {
			t.Fatalf("Validate(%s) = %v", src, err)
		}
	}
}

func TestInvalidCases(t *testing.T) {
	invalid := [][]byte{
		nil,
		[]byte(`tru`),
		[]byte(`01`),
		[]byte(`1.`),
		[]byte(`"unterminated`),
		[]byte{'"', 0x01, '"'},
		[]byte{'"', 0xff, '"'},
		[]byte(`"\z"`),
		[]byte(`"\uD800"`),
		[]byte(`"\uDC00"`),
		[]byte(`[1,]`),
		[]byte(`{"a" 1}`),
		[]byte(`{"a":1`),
	}
	for _, src := range invalid {
		if Valid(src) {
			t.Fatalf("Valid(%q) = true", src)
		}
	}
}

func TestLongUnicodeRunReportsExactInvalidByte(t *testing.T) {
	prefix := []byte(`{"s":"` + strings.Repeat("こんにちは", 64))
	src := append(append(append([]byte(nil), prefix...), 0xff), '"', '}')
	err := Validate(src)
	var syntaxErr *SyntaxError
	if !errors.As(err, &syntaxErr) {
		t.Fatalf("Validate() error = %v, want *SyntaxError", err)
	}
	if syntaxErr.Offset != len(prefix) {
		t.Fatalf("invalid UTF-8 offset = %d, want %d", syntaxErr.Offset, len(prefix))
	}
}

func TestBuildIndexAndTraverse(t *testing.T) {
	src := []byte(`{"items":[1,true,{"x":"hello"}],"escaped\u002fkey":"line\n\uD834\uDD1E","dup":1,"dup":2}`)
	count, err := RequiredIndexEntries(src)
	if err != nil {
		t.Fatal(err)
	}
	storage := make([]IndexEntry, count)
	tape, err := BuildIndex(src, storage)
	if err != nil {
		t.Fatal(err)
	}
	if tape.Len() != count {
		t.Fatalf("Index.Len() = %d, want %d", tape.Len(), count)
	}
	root := tape.Root()
	if root.Kind() != Object {
		t.Fatalf("root kind = %v, want object", root.Kind())
	}
	if members, ok := root.ObjectLen(); !ok || members != 4 {
		t.Fatalf("root ObjectLen() = %d, %v, want 4, true", members, ok)
	}

	items, ok := root.Get("items")
	if !ok {
		t.Fatal("items missing")
	}
	if n, ok := items.ArrayLen(); !ok || n != 3 {
		t.Fatalf("items ArrayLen() = %d, %v, want 3, true", n, ok)
	}
	third, ok := items.Index(2)
	if !ok {
		t.Fatal("items/2 missing")
	}
	x, ok := third.Get("x")
	if !ok {
		t.Fatal("items/2/x missing")
	}
	if text, ok := x.StringBytes(); !ok || string(text) != "hello" {
		t.Fatalf("items/2/x = %q, %v", text, ok)
	}

	escaped, ok := root.Get("escaped/key")
	if !ok {
		t.Fatal("escaped key missing")
	}
	var decoded [32]byte
	text, ok := escaped.AppendString(decoded[:0])
	if !ok || string(text) != "line\n𝄞" {
		t.Fatalf("decoded escaped string = %q, %v", text, ok)
	}

	dup, ok := root.Get("dup")
	if !ok {
		t.Fatal("duplicate key missing")
	}
	if number, ok := dup.NumberBytes(); !ok || string(number) != "2" {
		t.Fatalf("last duplicate = %q, %v, want 2", number, ok)
	}

	pointer := MustCompilePointer("/items/2/x")
	got, ok, err := tape.PointerCompiled(pointer)
	if err != nil || !ok || got.Raw().String() != `"hello"` {
		t.Fatalf("PointerCompiled() = %s, %v, %v", got.Raw().Bytes(), ok, err)
	}
	if string(root.Raw().Bytes()) != string(src) {
		t.Fatalf("root raw value changed: %s", root.Raw().Bytes())
	}
}

func TestIndexIterators(t *testing.T) {
	src := []byte(`{"a":[10,20],"b":false}`)
	storage := make([]IndexEntry, 8)
	tape, err := BuildIndex(src, storage)
	if err != nil {
		t.Fatal(err)
	}
	objects, ok := tape.Root().ObjectIter()
	if !ok {
		t.Fatal("root is not object")
	}
	key, value, ok := objects.Next()
	if !ok || stringMust(key.StringBytes()) != "a" || value.Kind() != Array {
		t.Fatalf("first member = %q, %v, %v", stringMust(key.StringBytes()), value.Kind(), ok)
	}
	array, ok := value.ArrayIter()
	if !ok {
		t.Fatal("a is not array")
	}
	first, ok := array.Next()
	if !ok {
		t.Fatal("first array value missing")
	}
	second, ok := array.Next()
	if !ok {
		t.Fatal("second array value missing")
	}
	if a, _ := first.Int64(); a != 10 {
		t.Fatalf("first array value = %d", a)
	}
	if b, _ := second.Int64(); b != 20 {
		t.Fatalf("second array value = %d", b)
	}
	if _, ok := array.Next(); ok {
		t.Fatal("array iterator produced extra value")
	}
	key, value, ok = objects.Next()
	if !ok || stringMust(key.StringBytes()) != "b" {
		t.Fatalf("second key = %q, %v", stringMust(key.StringBytes()), ok)
	}
	if b, ok := value.Bool(); !ok || b {
		t.Fatalf("second value = %v, %v, want false, true", b, ok)
	}
	if _, _, ok := objects.Next(); ok {
		t.Fatal("object iterator produced extra member")
	}

	array, _ = valueFrom(t, tape.Root(), "a").ArrayIter()
	if kind, ok := array.NextKind(); !ok || kind != Number {
		t.Fatalf("NextKind() = %v, %v, want number", kind, ok)
	}
	if raw, ok := array.NextRaw(); !ok || string(raw.Bytes()) != "20" {
		t.Fatalf("NextRaw() = %q, %v, want 20", raw.Bytes(), ok)
	}
	if _, ok := array.NextRaw(); ok {
		t.Fatal("raw array iterator produced extra value")
	}

	array, _ = valueFrom(t, tape.Root(), "a").ArrayIter()
	for index, want := range []string{"10", "20"} {
		if !array.Valid() || array.CurrentKind() != Number || array.CurrentRaw().String() != want {
			t.Fatalf("array cursor %d = %v, %q", index, array.CurrentKind(), array.CurrentRaw().String())
		}
		array = array.Advance()
	}
	if array.Valid() || array.Current().Kind() != Invalid {
		t.Fatal("exhausted array cursor is valid")
	}

	objects, _ = tape.Root().ObjectIter()
	rawKey, rawValue, ok := objects.NextRaw()
	if !ok || string(rawKey.Bytes()) != `"a"` || string(rawValue.Bytes()) != `[10,20]` {
		t.Fatalf("object NextRaw() = %q, %q, %v", rawKey.Bytes(), rawValue.Bytes(), ok)
	}

	flatArray, ok := valueFrom(t, tape.Root(), "a").FlatArrayIter()
	if !ok {
		t.Fatal("a is not a flat array")
	}
	if kind, ok := flatArray.NextKind(); !ok || kind != Number {
		t.Fatalf("flat NextKind() = %v, %v, want number", kind, ok)
	}
	if raw, ok := flatArray.NextRaw(); !ok || string(raw.Bytes()) != "20" {
		t.Fatalf("flat NextRaw() = %q, %v, want 20", raw.Bytes(), ok)
	}
	if _, ok := flatArray.Next(); ok {
		t.Fatal("flat array iterator produced extra value")
	}
	flatArray, _ = valueFrom(t, tape.Root(), "a").FlatArrayIter()
	for flatArray.Valid() {
		if flatArray.CurrentKind() != Number || flatArray.Current().Kind() != Number {
			t.Fatalf("flat cursor kind = %v", flatArray.CurrentKind())
		}
		flatArray = flatArray.Advance()
	}
	if flatArray.CurrentRaw().Bytes() != nil {
		t.Fatal("exhausted flat array cursor returned raw value")
	}
	if _, ok := tape.Root().FlatObjectIter(); ok {
		t.Fatal("object with non-empty container value reported as flat")
	}

	objects, _ = tape.Root().ObjectIter()
	for objects.Valid() {
		key, value := objects.Current()
		if key.Kind() != String || value.Kind() == Invalid {
			t.Fatalf("object cursor = %v, %v", key.Kind(), value.Kind())
		}
		rawKey, rawValue := objects.CurrentRaw()
		if len(rawKey.Bytes()) == 0 || len(rawValue.Bytes()) == 0 {
			t.Fatal("object cursor returned empty raw value")
		}
		objects = objects.Advance()
	}
	if key, value := objects.Current(); key.Kind() != Invalid || value.Kind() != Invalid {
		t.Fatal("exhausted object cursor returned values")
	}

	flatObjectTape, err := BuildIndex([]byte(`{"a":1,"b":false,"c":[]}`), storage)
	if err != nil {
		t.Fatal(err)
	}
	flatObject, ok := flatObjectTape.Root().FlatObjectIter()
	if !ok {
		t.Fatal("scalar object is not flat")
	}
	for index, want := range []struct {
		key  string
		kind Kind
	}{{"a", Number}, {"b", Bool}, {"c", Array}} {
		key, value, ok := flatObject.Next()
		if !ok || stringMust(key.StringBytes()) != want.key || value.Kind() != want.kind {
			t.Fatalf("flat object member %d = %q, %v, %v", index, stringMust(key.StringBytes()), value.Kind(), ok)
		}
	}
	if _, _, ok := flatObject.Next(); ok {
		t.Fatal("flat object iterator produced extra member")
	}
	flatObject, _ = flatObjectTape.Root().FlatObjectIter()
	for flatObject.Valid() {
		key, value := flatObject.Current()
		rawKey, rawValue := flatObject.CurrentRaw()
		if key.Kind() != String || value.Kind() == Invalid || len(rawKey.Bytes()) == 0 || len(rawValue.Bytes()) == 0 {
			t.Fatal("flat object cursor returned invalid member")
		}
		flatObject = flatObject.Advance()
	}

	nestedTape, err := BuildIndex([]byte(`[[1],2]`), storage)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := nestedTape.Root().FlatArrayIter(); ok {
		t.Fatal("array with non-empty container value reported as flat")
	}
}

func valueFrom(t *testing.T, value Node, key string) Node {
	t.Helper()
	got, ok := value.Get(key)
	if !ok {
		t.Fatalf("key %q missing", key)
	}
	return got
}

func TestIndexCapacityAndDepthErrors(t *testing.T) {
	src := []byte(`{"a":[1,2,3]}`)
	count, err := RequiredIndexEntries(src)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BuildIndex(src, make([]IndexEntry, count-1)); !errors.Is(err, ErrIndexFull) {
		t.Fatalf("short tape error = %v, want ErrIndexFull", err)
	}

	deep := []byte(`[[0]]`)
	if _, err := BuildIndexOptions(deep, make([]IndexEntry, 3), IndexOptions{MaxDepth: 1}); err == nil {
		t.Fatal("MaxDepth=1 accepted depth 2")
	}
}

func TestBuildIndexDeepWithoutStackStorage(t *testing.T) {
	const depth = 96
	src := []byte(strings.Repeat("[", depth) + "0" + strings.Repeat("]", depth))
	storage := make([]IndexEntry, depth+1)
	if allocs := testing.AllocsPerRun(1000, func() {
		tape, err := BuildIndexOptions(src, storage, IndexOptions{MaxDepth: depth})
		if err != nil {
			t.Fatal(err)
		}
		if tape.Len() != depth+1 {
			t.Fatalf("Index.Len = %d, want %d", tape.Len(), depth+1)
		}
	}); allocs != 0 {
		t.Fatalf("deep BuildIndex allocs = %v, want 0", allocs)
	}
}

func TestBuildIndexAllocs(t *testing.T) {
	src := benchmarkJSON()
	count, err := RequiredIndexEntries(src)
	if err != nil {
		t.Fatal(err)
	}
	storage := make([]IndexEntry, count)
	pointer := MustCompilePointer("/items/2/id")
	allocs := testing.AllocsPerRun(1000, func() {
		tape, err := BuildIndex(src, storage)
		if err != nil {
			t.Fatal(err)
		}
		value, ok, err := tape.PointerCompiled(pointer)
		if err != nil || !ok {
			t.Fatalf("PointerCompiled() = %v, %v", ok, err)
		}
		n, ok := value.NumberBytes()
		if !ok {
			t.Fatal("pointer value is not number")
		}
		indexBenchmarkSink += int(n[0])
	})
	if allocs != 0 {
		t.Fatalf("BuildIndex + PointerCompiled allocs = %v, want 0", allocs)
	}

	allocs = testing.AllocsPerRun(1000, func() {
		count, err := buildIndexWithInlineEntries(src)
		if err != nil {
			t.Fatal(err)
		}
		indexBenchmarkSink += count
	})
	if allocs != 0 {
		t.Fatalf("stack-backed BuildIndex allocs = %v, want 0", allocs)
	}

	allocs = testing.AllocsPerRun(1000, func() {
		count, err := RequiredIndexEntries(src)
		if err != nil {
			t.Fatal(err)
		}
		indexBenchmarkSink += count
	})
	if allocs != 0 {
		t.Fatalf("RequiredIndexEntries allocs = %v, want 0", allocs)
	}
}

func TestIndexStorageIsCompact(t *testing.T) {
	if size := unsafe.Sizeof(IndexEntry{}); size != 20 {
		t.Fatalf("IndexEntry size = %d, want 20", size)
	}
	pointerSize := unsafe.Sizeof(uintptr(0))
	if size, want := unsafe.Sizeof(Node{}), 2*pointerSize; size != want {
		t.Fatalf("Node size = %d, want %d", size, want)
	}
	wantIter := (2*pointerSize + 4 + pointerSize - 1) &^ (pointerSize - 1)
	if size := unsafe.Sizeof(ArrayIter{}); size != wantIter {
		t.Fatalf("ArrayIter size = %d, want %d", size, wantIter)
	}
	if size := unsafe.Sizeof(ObjectIter{}); size != wantIter {
		t.Fatalf("ObjectIter size = %d, want %d", size, wantIter)
	}
	if size := unsafe.Sizeof(FlatArrayIter{}); size != wantIter {
		t.Fatalf("FlatArrayIter size = %d, want %d", size, wantIter)
	}
	if size := unsafe.Sizeof(FlatObjectIter{}); size != wantIter {
		t.Fatalf("FlatObjectIter size = %d, want %d", size, wantIter)
	}
}

func TestParseAnyArenaKeepsValuesAlive(t *testing.T) {
	value, err := detachedAnyValue()
	if err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	runtime.GC()

	root := value.(map[string]any)
	items := root["items"].([]any)
	first := items[0].(map[string]any)
	if first["name"] != "still alive" || first["score"] != 1234567890123456.0 {
		t.Fatalf("detached any value = %#v", first)
	}
	first["mutated"] = true
	if first["mutated"] != true {
		t.Fatal("map mutation did not persist")
	}
	wide := root["wide"].(map[string]any)
	if len(wide) != 8 {
		t.Fatalf("wide map length = %d, want 8", len(wide))
	}
	wide["i"] = 9.0
	delete(wide, "a")
	items[0] = "replaced"
	items = append(items, 3.0)
	runtime.GC()
	if len(wide) != 8 || wide["i"] != 9.0 {
		t.Fatalf("grown map = %#v", wide)
	}
	if len(items) != 3 || items[0] != "replaced" || items[2] != 3.0 {
		t.Fatalf("mutated array = %#v", items)
	}
}

func TestParseAnyLargeDocumentKeepsValuesAlive(t *testing.T) {
	var src strings.Builder
	src.WriteString(`{"items":[`)
	for i := range 192 {
		if i != 0 {
			src.WriteByte(',')
		}
		src.WriteString(`{"id":`)
		src.WriteString(strconv.Itoa(i))
		src.WriteString(`,"payload":"`)
		src.WriteString(strings.Repeat("x", 110))
		src.WriteString(`"}`)
	}
	src.WriteString(`]}`)

	value, err := parseAnyZeroCopyForTest([]byte(src.String()))
	if err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	runtime.GC()

	items := value.(map[string]any)["items"].([]any)
	first := items[0].(map[string]any)
	last := items[len(items)-1].(map[string]any)
	if first["id"] != 0.0 || last["id"] != 191.0 || len(last["payload"].(string)) != 110 {
		t.Fatalf("large document values = first %#v, last %#v", first, last)
	}
	first["mutated"] = true
	delete(last, "payload")
	runtime.GC()
	if first["mutated"] != true {
		t.Fatal("large document mutation did not persist")
	}
	if _, ok := last["payload"]; ok {
		t.Fatal("large document deletion did not persist")
	}
}

func detachedAnyValue() (any, error) {
	src := []byte(`{"items":[{"name":"still alive","score":1234567890123456},{"name":"second","score":2.5}],"wide":{"a":1,"b":2,"c":3,"d":4,"e":5,"f":6,"g":7,"h":8},"padding":"plain ascii payload keeps this input on the arena path"}`)
	return parseAnyZeroCopyForTest(src)
}

func TestIndexValueKeepsSourceAndEntriesAlive(t *testing.T) {
	value, err := detachedIndexValue()
	if err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	runtime.GC()
	if text, ok := value.StringBytes(); !ok || string(text) != "still alive" {
		t.Fatalf("detached tape value = %q, %v", text, ok)
	}
}

func detachedIndexValue() (Node, error) {
	src := append([]byte(nil), `{"value":"still alive"}`...)
	storage := make([]IndexEntry, 3)
	tape, err := BuildIndex(src, storage)
	if err != nil {
		return Node{}, err
	}
	value, _ := tape.Root().Get("value")
	return value, nil
}

func buildIndexWithInlineEntries(src []byte) (int, error) {
	var storage [64]IndexEntry
	tape, err := BuildIndex(src, storage[:])
	if err != nil {
		return 0, err
	}
	return tape.Len(), nil
}

func stringMust(value []byte, ok bool) string {
	if !ok {
		return ""
	}
	return string(value)
}

func TestValidateMatchesStdlibGrammarCorpus(t *testing.T) {
	cases := [][]byte{
		nil,
		[]byte(``),
		[]byte(` `),
		[]byte("\n\t\r "),
		[]byte(`null`),
		[]byte(` true `),
		[]byte(`false`),
		[]byte(`0`),
		[]byte(`-0`),
		[]byte(`0.0`),
		[]byte(`1.25`),
		[]byte(`-12.34e+56`),
		[]byte(`1e-999999`),
		[]byte(`01`),
		[]byte(`00`),
		[]byte(`-`),
		[]byte(`-.1`),
		[]byte(`.1`),
		[]byte(`1.`),
		[]byte(`1e`),
		[]byte(`1e+`),
		[]byte(`+1`),
		[]byte(`NaN`),
		[]byte(`Infinity`),
		[]byte(`""`),
		[]byte(`"ascii"`),
		[]byte(`"こんにちは"`),
		[]byte(`"escaped\n\t\"\\\/"`),
		[]byte(`"\u0000\u001f"`),
		[]byte(`"\uD834\uDD1E"`),
		[]byte(`"unterminated`),
		[]byte{'"', 0x1f, '"'},
		[]byte(`"\z"`),
		[]byte(`"\u12G4"`),
		[]byte(`[]`),
		[]byte(`[1,true,false,null,"x",{"a":[2]}]`),
		[]byte(`[1,]`),
		[]byte(`[1,,2]`),
		[]byte(`[1 2]`),
		[]byte(`[`),
		[]byte(`]`),
		[]byte(`{}`),
		[]byte(`{"a":1,"a":2}`),
		[]byte(`{"a":1,"b":[true,{}]}`),
		[]byte(`{"a":}`),
		[]byte(`{"a":1,}`),
		[]byte(`{"a" 1}`),
		[]byte(`{[]:1}`),
		[]byte(`{"a":1 "b":2}`),
		[]byte(`{} false`),
	}
	for _, src := range cases {
		got := Valid(src)
		want := json.Valid(src)
		if got != want {
			t.Fatalf("Valid(%q) = %v, want stdlib %v", src, got, want)
		}
		if want {
			if _, err := Parse(src); err != nil {
				t.Fatalf("Parse(%q) = %v", src, err)
			}
			if _, err := parseOptionsZeroCopyForTest(src); err != nil {
				t.Fatalf("ParseOptions(ZeroCopy, %q) = %v", src, err)
			}
			if out, err := AppendCompact(nil, src); err != nil || !json.Valid(out) {
				t.Fatalf("AppendCompact(%q) = %q, %v", src, out, err)
			}
			continue
		}
		if err := Validate(src); err == nil {
			t.Fatalf("Validate(%q) succeeded, want error", src)
		}
		if _, err := Parse(src); err == nil {
			t.Fatalf("Parse(%q) succeeded, want error", src)
		}
		if _, err := AppendCompact(nil, src); err == nil {
			t.Fatalf("AppendCompact(%q) succeeded, want error", src)
		}
	}
}

func TestStrictStringValidation(t *testing.T) {
	valid := [][]byte{
		[]byte(`"\uD834\uDD1E"`),
		[]byte(`"\u0000"`),
		[]byte("\"\xef\xbf\xbd\""),
	}
	for _, src := range valid {
		if err := Validate(src); err != nil {
			t.Fatalf("Validate(%q) = %v", src, err)
		}
		raw, ok, err := GetRaw(src, "")
		if err != nil || !ok {
			t.Fatalf("GetRaw(%q) = %v, %v", src, ok, err)
		}
		if _, ok, err := raw.Text(); err != nil || !ok {
			t.Fatalf("RawValue.Text(%q) = %v, %v", src, ok, err)
		}
	}

	invalid := [][]byte{
		[]byte{'"', 0xff, '"'},
		[]byte("\"\xc0\x80\""),
		[]byte(`"\uD800"`),
		[]byte(`"\uD800x"`),
		[]byte(`"\uD800\u0041"`),
		[]byte(`"\uDC00"`),
	}
	for _, src := range invalid {
		if err := Validate(src); err == nil {
			t.Fatalf("Validate(%q) succeeded, want strict string error", src)
		}
		if _, _, err := GetRaw(src, ""); err == nil {
			t.Fatalf("GetRaw(%q) succeeded, want strict string error", src)
		}
		if _, err := AppendCompact(nil, src); err == nil {
			t.Fatalf("AppendCompact(%q) succeeded, want strict string error", src)
		}
	}
}

func TestScalarValidators(t *testing.T) {
	validNumbers := [][]byte{
		[]byte(`0`),
		[]byte(`-0`),
		[]byte(`123`),
		[]byte(`-12.34e+56`),
		[]byte(`1e-999999`),
	}
	for _, src := range validNumbers {
		if err := ValidateNumber(src); err != nil {
			t.Fatalf("ValidateNumber(%q) = %v", src, err)
		}
		if !ValidNumber(src) {
			t.Fatalf("ValidNumber(%q) = false", src)
		}
	}

	invalidNumbers := [][]byte{
		nil,
		[]byte(``),
		[]byte(`01`),
		[]byte(`1.`),
		[]byte(`1e`),
		[]byte(`+1`),
		[]byte(`1 true`),
		[]byte(`NaN`),
	}
	for _, src := range invalidNumbers {
		if err := ValidateNumber(src); err == nil {
			t.Fatalf("ValidateNumber(%q) succeeded, want error", src)
		}
		if ValidNumber(src) {
			t.Fatalf("ValidNumber(%q) = true", src)
		}
	}

	validStrings := [][]byte{
		[]byte(`""`),
		[]byte(`"hello\nworld"`),
		[]byte(`"\uD834\uDD1E"`),
		[]byte("\"\xef\xbf\xbd\""),
	}
	for _, src := range validStrings {
		if err := ValidateString(src); err != nil {
			t.Fatalf("ValidateString(%q) = %v", src, err)
		}
		if !ValidString(src) {
			t.Fatalf("ValidString(%q) = false", src)
		}
	}

	invalidStrings := [][]byte{
		nil,
		[]byte(``),
		[]byte(`"x" true`),
		[]byte(`"\uD800"`),
		[]byte{'"', 0xff, '"'},
	}
	for _, src := range invalidStrings {
		if err := ValidateString(src); err == nil {
			t.Fatalf("ValidateString(%q) succeeded, want error", src)
		}
		if ValidString(src) {
			t.Fatalf("ValidString(%q) = true", src)
		}
	}
}

func TestRawValueNumberAccessorsRequireExactJSON(t *testing.T) {
	valid := RawValue{src: []byte(`-12.34e+5`)}
	if b, ok := valid.NumberBytes(); !ok || string(b) != `-12.34e+5` {
		t.Fatalf("NumberBytes(valid) = %q, %v", b, ok)
	}
	if s, ok := valid.NumberText(); !ok || s != `-12.34e+5` {
		t.Fatalf("NumberText(valid) = %q, %v", s, ok)
	}
	if f, ok := valid.Float64(); !ok || f != -12.34e+5 {
		t.Fatalf("Float64(valid) = %v, %v", f, ok)
	}

	invalid := []RawValue{
		{src: []byte(`1x`)},
		{src: []byte(`01`)},
		{src: []byte(`-`)},
		{src: []byte(`NaN`)},
		{src: nil},
	}
	for _, raw := range invalid {
		if b, ok := raw.NumberBytes(); ok {
			t.Fatalf("NumberBytes(%q) = %q, true", raw.Bytes(), b)
		}
		if s, ok := raw.NumberText(); ok {
			t.Fatalf("NumberText(%q) = %q, true", raw.Bytes(), s)
		}
		if n, ok := raw.Int64(); ok {
			t.Fatalf("Int64(%q) = %d, true", raw.Bytes(), n)
		}
		if f, ok := raw.Float64(); ok {
			t.Fatalf("Float64(%q) = %f, true", raw.Bytes(), f)
		}
	}
}

func TestMaxDepthRejectsEmptyNestedContainers(t *testing.T) {
	opts := Options{MaxDepth: 1}
	valid := [][]byte{
		[]byte(`[]`),
		[]byte(`{}`),
		[]byte(`[1]`),
		[]byte(`{"a":1}`),
	}
	for _, src := range valid {
		assertDepthAccepted(t, src, opts)
	}

	invalid := [][]byte{
		[]byte(`[[]]`),
		[]byte(`[{}]`),
		[]byte(`{"a":[]}`),
		[]byte(`{"a":{}}`),
		[]byte(`[[1]]`),
		[]byte(`{"a":{"b":1}}`),
	}
	for _, src := range invalid {
		assertDepthRejected(t, src, opts)
	}

	if err := ValidateOptions([]byte(`[[]]`), Options{MaxDepth: 2}); err != nil {
		t.Fatalf("ValidateOptions MaxDepth=2 rejected nested array: %v", err)
	}
}

func assertDepthAccepted(t *testing.T, src []byte, opts Options) {
	t.Helper()
	if err := ValidateOptions(src, opts); err != nil {
		t.Fatalf("ValidateOptions(%q) = %v", src, err)
	}
	if _, err := ParseOptions(src, opts); err != nil {
		t.Fatalf("ParseOptions(%q) = %v", src, err)
	}
	if _, err := ParseOptions(src, Options{MaxDepth: opts.MaxDepth, ZeroCopy: true}); err != nil {
		t.Fatalf("ParseOptions prealloc(%q) = %v", src, err)
	}
	if _, _, err := GetRawOptions(src, "", opts); err != nil {
		t.Fatalf("GetRawOptions(%q) = %v", src, err)
	}
	if _, _, err := ScanRawOptions(src, "", opts); err != nil {
		t.Fatalf("ScanRawOptions(%q) = %v", src, err)
	}
	if _, err := appendCompact(nil, src, opts.MaxDepth); err != nil {
		t.Fatalf("appendCompact(%q) = %v", src, err)
	}
}

func assertDepthRejected(t *testing.T, src []byte, opts Options) {
	t.Helper()
	if err := ValidateOptions(src, opts); err == nil {
		t.Fatalf("ValidateOptions(%q) succeeded, want depth error", src)
	}
	if _, err := ParseOptions(src, opts); err == nil {
		t.Fatalf("ParseOptions(%q) succeeded, want depth error", src)
	}
	if _, err := ParseOptions(src, Options{MaxDepth: opts.MaxDepth, ZeroCopy: true}); err == nil {
		t.Fatalf("ParseOptions prealloc(%q) succeeded, want depth error", src)
	}
	if _, _, err := GetRawOptions(src, "", opts); err == nil {
		t.Fatalf("GetRawOptions(%q) succeeded, want depth error", src)
	}
	if _, _, err := ScanRawOptions(src, "", opts); err == nil {
		t.Fatalf("ScanRawOptions(%q) succeeded, want depth error", src)
	}
	if _, err := appendCompact(nil, src, opts.MaxDepth); err == nil {
		t.Fatalf("appendCompact(%q) succeeded, want depth error", src)
	}
}

func FuzzValidateConsistency(f *testing.F) {
	seeds := [][]byte{
		nil,
		[]byte(`null`),
		[]byte(`true`),
		[]byte(`0`),
		[]byte(`-12.34e+56`),
		[]byte(`""`),
		[]byte(`"hello\nworld"`),
		[]byte(`"\uD834\uDD1E"`),
		[]byte(`{"a":[1,true,null],"b":{"c":"d"}}`),
		[]byte(`[[]]`),
		[]byte(`{"a":[]}`),
		[]byte(`01`),
		[]byte(`[1,]`),
		[]byte(`{"a":}`),
		[]byte(`"\uD800"`),
		[]byte{'"', 0xff, '"'},
	}
	for _, seed := range seeds {
		f.Add(seed)
	}
	addJSONTestSuiteSeeds(f)
	f.Fuzz(func(t *testing.T, src []byte) {
		if len(src) > 1<<16 {
			t.Skip("input too large for consistency fuzz")
		}
		checkValidationConsistency(t, src, strictJSONValid(src))
	})
}

func FuzzParseAny(f *testing.F) {
	for _, seed := range [][]byte{
		[]byte(`null`),
		[]byte(`-0`),
		[]byte(`1234567890123456.25`),
		[]byte(`[1234567890123456,1000000000000001,-1000000000000002]`),
		[]byte(`1.00000000000000011102230246251565404236316680908203125`),
		[]byte(`{"a":[1,true,null,"text"],"b":2.5}`),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, src []byte) {
		if len(src) > 1<<16 || !strictJSONValid(src) {
			t.Skip()
		}
		var want any
		wantErr := json.Unmarshal(src, &want)
		for _, parse := range []func([]byte) (any, error){ParseAny, parseAnyZeroCopyForTest} {
			got, gotErr := parse(src)
			if (gotErr == nil) != (wantErr == nil) {
				t.Fatalf("parse error = %v, encoding/json error = %v", gotErr, wantErr)
			}
			if gotErr == nil && !reflect.DeepEqual(got, want) {
				t.Fatalf("parse = %#v, encoding/json = %#v", got, want)
			}
		}
	})
}

func verifyIndexStructure(t *testing.T, value Node) {
	t.Helper()
	if value.Kind() != value.Raw().Kind() {
		t.Fatalf("tape kind = %v, raw kind = %v for %q", value.Kind(), value.Raw().Kind(), value.Raw().Bytes())
	}
	switch value.Kind() {
	case Array:
		want, _ := value.ArrayLen()
		iter, _ := value.ArrayIter()
		got := 0
		for {
			child, ok := iter.Next()
			if !ok {
				break
			}
			verifyIndexStructure(t, child)
			got++
		}
		if got != want {
			t.Fatalf("array iteration count = %d, want %d", got, want)
		}
	case Object:
		want, _ := value.ObjectLen()
		iter, _ := value.ObjectIter()
		got := 0
		for {
			key, child, ok := iter.Next()
			if !ok {
				break
			}
			if key.Kind() != String {
				t.Fatalf("object key kind = %v, want string", key.Kind())
			}
			verifyIndexStructure(t, child)
			got++
		}
		if got != want {
			t.Fatalf("object iteration count = %d, want %d", got, want)
		}
	}
}

func TestCompactIndentCanonicalize(t *testing.T) {
	src := []byte(`{ "b" : [2, 1], "a" : {"d":4,"c":3} }`)

	compact, err := Compact(src)
	if err != nil {
		t.Fatal(err)
	}
	if string(compact) != `{"b":[2,1],"a":{"d":4,"c":3}}` {
		t.Fatalf("compact = %s", compact)
	}

	pretty, err := Indent(src, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(pretty) {
		t.Fatalf("indent produced invalid JSON: %s", pretty)
	}

	canon, err := Canonicalize(src)
	if err != nil {
		t.Fatal(err)
	}
	if string(canon) != `{"a":{"c":3,"d":4},"b":[2,1]}` {
		t.Fatalf("canonical = %s", canon)
	}
}

func TestAppendJSONUsesJSONEscapes(t *testing.T) {
	for _, text := range []string{
		"plain",
		"quote\"slash\\",
		"\x00\x1b\x1f",
		"\x7f\u0080\u2028\u2029",
		"\U000b948e",
	} {
		src, err := json.Marshal(text)
		if err != nil {
			t.Fatal(err)
		}
		value, err := Parse(src)
		if err != nil {
			t.Fatal(err)
		}
		encoded := value.AppendJSON(nil)
		if !json.Valid(encoded) {
			t.Fatalf("AppendJSON(%q) produced invalid JSON: %q", text, encoded)
		}
		var decoded string
		if err := json.Unmarshal(encoded, &decoded); err != nil || decoded != text {
			t.Fatalf("AppendJSON(%q) round trip = %q, %v", text, decoded, err)
		}

		object, err := Parse(append(append([]byte(`{"`), src[1:len(src)-1]...), []byte(`":0}`)...))
		if err != nil {
			t.Fatal(err)
		}
		if encoded := object.AppendJSON(nil); !json.Valid(encoded) {
			t.Fatalf("object key %q produced invalid JSON: %q", text, encoded)
		}
	}
}

func TestAppendCompactAllocs(t *testing.T) {
	src := benchmarkJSON()
	dst := make([]byte, 0, len(src))
	allocs := testing.AllocsPerRun(1000, func() {
		out, err := AppendCompact(dst[:0], src)
		if err != nil || !json.Valid(out) {
			t.Fatalf("compact = %q, %v", out, err)
		}
	})
	if allocs != 0 {
		t.Fatalf("AppendCompact allocs = %v, want 0", allocs)
	}
}

func TestGetPointer(t *testing.T) {
	src := []byte(`{"a/b":{"~key":[10,20,30]},"dup":1,"dup":2}`)
	v, ok, err := Get(src, "/a~1b/~0key/2")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("missing pointer")
	}
	n, ok := v.Int64()
	if !ok || n != 30 {
		t.Fatalf("number = %d, %v", n, ok)
	}
	dup, ok, err := Get(src, "/dup")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("missing dup")
	}
	n, ok = dup.Int64()
	if !ok || n != 2 {
		t.Fatalf("dup = %d, %v", n, ok)
	}
}

func TestGetRawPointer(t *testing.T) {
	src := []byte(`{
		"a/b": {"~key": [10, 20, {"message": "raw"}]},
		"dup": {"x": 1},
		"dup": {},
		"items": [
			{"message": "first"},
			{"message": "second"}
		]
	}`)

	raw, ok, err := GetRaw(src, "/a~1b/~0key/2/message")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || string(raw.Bytes()) != `"raw"` || raw.Kind() != String {
		t.Fatalf("raw = %q, %v, %v", raw.Bytes(), ok, raw.Kind())
	}

	raw, ok, err = GetRaw(src, "/items/1/message")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || string(raw.Bytes()) != `"second"` {
		t.Fatalf("raw item = %q, %v", raw.Bytes(), ok)
	}

	raw, ok, err = GetRaw(src, "/dup/x")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatalf("duplicate last-key semantics returned %q", raw.Bytes())
	}

	if _, _, err = GetRaw([]byte(`1`), "/~2"); err == nil {
		t.Fatal("invalid pointer escape did not error")
	}
}

func TestScanRawPointer(t *testing.T) {
	src := []byte(`{"items":[{"message":"first"},{"message":"second"}],"tail":`)
	raw, ok, err := ScanRaw(src, "/items/1/message")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || string(raw.Bytes()) != `"second"` {
		t.Fatalf("raw = %q, %v", raw.Bytes(), ok)
	}
	if _, _, err = ScanRaw(src, "/tail"); err == nil {
		t.Fatal("ScanRaw did not validate target value")
	}
}

func TestCompiledPointer(t *testing.T) {
	src := []byte(`{
		"a/b": {"~key": [10, 20, {"message": "raw"}]},
		"items": [{"id": 1}, {"id": 2, "message": "second"}]
	}`)

	ptr, err := CompilePointer("/a~1b/~0key/2/message")
	if err != nil {
		t.Fatal(err)
	}

	v, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := ptr.Get(v)
	if err != nil {
		t.Fatal(err)
	}
	text, textOK := got.Text()
	if !ok || !textOK || text != "raw" {
		t.Fatalf("compiled AST pointer = %q, %v, %v", text, ok, textOK)
	}

	raw, ok, err := ptr.GetRaw(src)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || string(raw.Bytes()) != `"raw"` {
		t.Fatalf("compiled raw pointer = %q, %v", raw.Bytes(), ok)
	}

	early := MustCompilePointer("/items/1/message")
	raw, ok, err = early.ScanRaw(src)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || string(raw.Bytes()) != `"second"` {
		t.Fatalf("compiled find raw = %q, %v", raw.Bytes(), ok)
	}

	if _, err = CompilePointer("/~2"); err == nil {
		t.Fatal("invalid compiled pointer escape did not error")
	}

	badArrayToken := MustCompilePointer("/foo")
	if _, _, err = badArrayToken.GetRaw([]byte(`[1]`)); err == nil {
		t.Fatal("compiled pointer did not reject non-numeric array token")
	}
}

func TestRawValueAccessors(t *testing.T) {
	src := []byte(`{"s":"plain","esc":"hello\nworld","n":-42,"f":1.25,"b":true,"z":null,"nested":{"x":7}}`)
	root, ok, err := GetRaw(src, "")
	if err != nil || !ok {
		t.Fatalf("root = %v, %v", ok, err)
	}

	raw, ok, err := root.Get("/s")
	if err != nil || !ok {
		t.Fatalf("string = %v, %v", ok, err)
	}
	text, ok, err := raw.Text()
	if err != nil || !ok || text != "plain" {
		t.Fatalf("text = %q, %v, %v", text, ok, err)
	}

	raw, ok, err = root.Get("/esc")
	if err != nil || !ok {
		t.Fatalf("escaped string = %v, %v", ok, err)
	}
	text, ok, err = raw.Text()
	if err != nil || !ok || text != "hello\nworld" {
		t.Fatalf("escaped text = %q, %v, %v", text, ok, err)
	}

	raw, ok, err = root.Get("/n")
	if err != nil || !ok {
		t.Fatalf("number = %v, %v", ok, err)
	}
	if n, ok := raw.Int64(); !ok || n != -42 {
		t.Fatalf("int = %d, %v", n, ok)
	}
	if s, ok := raw.NumberText(); !ok || s != "-42" {
		t.Fatalf("number text = %q, %v", s, ok)
	}

	raw, ok, err = root.Get("/f")
	if err != nil || !ok {
		t.Fatalf("float = %v, %v", ok, err)
	}
	if f, ok := raw.Float64(); !ok || f != 1.25 {
		t.Fatalf("float = %f, %v", f, ok)
	}

	raw, ok, err = root.Get("/b")
	if err != nil || !ok {
		t.Fatalf("bool = %v, %v", ok, err)
	}
	if b, ok := raw.Bool(); !ok || !b {
		t.Fatalf("bool = %v, %v", b, ok)
	}

	raw, ok, err = root.Get("/z")
	if err != nil || !ok || !raw.IsNull() {
		t.Fatalf("null = %v, %v, %v", raw.IsNull(), ok, err)
	}

	ptr := MustCompilePointer("/nested/x")
	raw, ok, err = root.ScanCompiled(ptr)
	if err != nil || !ok {
		t.Fatalf("compiled nested = %v, %v", ok, err)
	}
	if n, ok := raw.Int64(); !ok || n != 7 {
		t.Fatalf("compiled nested number = %d, %v", n, ok)
	}
}

func TestEachRaw(t *testing.T) {
	array := []byte(`[1,true,{"x":"y"},[2,3]]`)
	var (
		count int
		kinds []Kind
	)
	if err := EachArray(array, func(index int, value RawValue) error {
		if index != count {
			t.Fatalf("index = %d, want %d", index, count)
		}
		count++
		kinds = append(kinds, value.Kind())
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	wantKinds := []Kind{Number, Bool, Object, Array}
	if count != len(wantKinds) {
		t.Fatalf("count = %d", count)
	}
	for i := range wantKinds {
		if kinds[i] != wantKinds[i] {
			t.Fatalf("kind[%d] = %v, want %v", i, kinds[i], wantKinds[i])
		}
	}

	object := []byte(`{"a":1,"b":true,"c":{"nested":"ok"}}`)
	keys := ""
	sum := int64(0)
	if err := EachObject(object, func(key string, value RawValue) error {
		keys += key
		if value.Kind() == Number {
			n, ok := value.Int64()
			if !ok {
				t.Fatalf("number value did not parse")
			}
			sum += n
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if keys != "abc" || sum != 1 {
		t.Fatalf("object iteration keys=%q sum=%d", keys, sum)
	}

	root, ok, err := GetRaw([]byte(`{"items":`+string(array)+`}`), "/items")
	if err != nil || !ok {
		t.Fatalf("items raw = %v, %v", ok, err)
	}
	count = 0
	if err = root.EachArray(func(_ int, value RawValue) error {
		count += len(value.Bytes())
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if count == 0 {
		t.Fatal("raw array iteration did not visit values")
	}
}

func TestGetRawAllocs(t *testing.T) {
	src := benchmarkJSON()
	allocs := testing.AllocsPerRun(1000, func() {
		raw, ok, err := GetRaw(src, "/items/2/message")
		if err != nil || !ok || raw.Kind() != String {
			t.Fatalf("raw = %q, %v, %v", raw.Bytes(), ok, err)
		}
	})
	if allocs != 0 {
		t.Fatalf("GetRaw allocs = %v, want 0", allocs)
	}
}

func TestScanRawAllocs(t *testing.T) {
	src := benchmarkJSON()
	allocs := testing.AllocsPerRun(1000, func() {
		raw, ok, err := ScanRaw(src, "/items/2/message")
		if err != nil || !ok || raw.Kind() != String {
			t.Fatalf("raw = %q, %v, %v", raw.Bytes(), ok, err)
		}
	})
	if allocs != 0 {
		t.Fatalf("ScanRaw allocs = %v, want 0", allocs)
	}
}

func TestCompiledScanRawAllocs(t *testing.T) {
	src := benchmarkJSON()
	ptr := MustCompilePointer("/items/2/message")
	allocs := testing.AllocsPerRun(1000, func() {
		raw, ok, err := ptr.ScanRaw(src)
		if err != nil || !ok || raw.Kind() != String {
			t.Fatalf("raw = %q, %v, %v", raw.Bytes(), ok, err)
		}
	})
	if allocs != 0 {
		t.Fatalf("compiled ScanRaw allocs = %v, want 0", allocs)
	}
}

func TestEachRawAllocs(t *testing.T) {
	array := rawArrayJSON()
	allocs := testing.AllocsPerRun(1000, func() {
		total := 0
		err := EachArray(array, func(_ int, value RawValue) error {
			total += len(value.Bytes())
			return nil
		})
		if err != nil || total == 0 {
			t.Fatalf("array iteration total=%d err=%v", total, err)
		}
	})
	if allocs != 0 {
		t.Fatalf("EachArray allocs = %v, want 0", allocs)
	}

	object := rawObjectJSON()
	allocs = testing.AllocsPerRun(1000, func() {
		total := 0
		err := EachObject(object, func(key string, value RawValue) error {
			total += len(key) + len(value.Bytes())
			return nil
		})
		if err != nil || total == 0 {
			t.Fatalf("object iteration total=%d err=%v", total, err)
		}
	})
	if allocs != 0 {
		t.Fatalf("EachObject allocs = %v, want 0", allocs)
	}
}

func TestRawValueAccessorAllocs(t *testing.T) {
	src := []byte(`{"s":"plain","n":12345,"f":12.5,"b":false}`)
	raw, ok, err := GetRaw(src, "/s")
	if err != nil || !ok {
		t.Fatalf("string raw = %v, %v", ok, err)
	}
	allocs := testing.AllocsPerRun(1000, func() {
		text, ok, err := raw.Text()
		if err != nil || !ok || text != "plain" {
			t.Fatalf("text = %q, %v, %v", text, ok, err)
		}
	})
	if allocs != 0 {
		t.Fatalf("RawValue.Text allocs = %v, want 0", allocs)
	}

	raw, ok, err = GetRaw(src, "/n")
	if err != nil || !ok {
		t.Fatalf("number raw = %v, %v", ok, err)
	}
	allocs = testing.AllocsPerRun(1000, func() {
		n, ok := raw.Int64()
		if !ok || n != 12345 {
			t.Fatalf("int = %d, %v", n, ok)
		}
	})
	if allocs != 0 {
		t.Fatalf("RawValue.Int64 allocs = %v, want 0", allocs)
	}

	raw, ok, err = GetRaw(src, "/f")
	if err != nil || !ok {
		t.Fatalf("float raw = %v, %v", ok, err)
	}
	allocs = testing.AllocsPerRun(1000, func() {
		f, ok := raw.Float64()
		if !ok || f != 12.5 {
			t.Fatalf("float = %f, %v", f, ok)
		}
	})
	if allocs != 0 {
		t.Fatalf("RawValue.Float64 allocs = %v, want 0", allocs)
	}

	raw, ok, err = GetRaw(src, "/b")
	if err != nil || !ok {
		t.Fatalf("bool raw = %v, %v", ok, err)
	}
	allocs = testing.AllocsPerRun(1000, func() {
		v, ok := raw.Bool()
		if !ok || v {
			t.Fatalf("bool = %v, %v", v, ok)
		}
	})
	if allocs != 0 {
		t.Fatalf("RawValue.Bool allocs = %v, want 0", allocs)
	}
}

func TestAnyRoundTrip(t *testing.T) {
	src := []byte(`{"z": [true, false, null, 12.5], "s": "x"}`)
	v, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(v.Any())
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(got) {
		t.Fatalf("Any marshal invalid: %s", got)
	}
}

func TestParseAnyDirect(t *testing.T) {
	for _, src := range [][]byte{
		[]byte(`null`),
		[]byte(`true`),
		[]byte(`-12.5e2`),
		[]byte(`"plain"`),
		[]byte(`"escaped\n\uD834\uDD1E"`),
		[]byte(`[1,true,null,"x",{"a":2}]`),
		[]byte(`{"a":1,"b":[false,2.5],"a":3}`),
		[]byte(`[0.1,1.2,2.3,3.4,4.5,5.6,6.7,7.8,8.9,9.0,-0.1,-9.9,3e4,-3e4,9e15]`),
	} {
		var want any
		if err := json.Unmarshal(src, &want); err != nil {
			t.Fatal(err)
		}
		got, err := ParseAny(src)
		if err != nil {
			t.Fatalf("ParseAny(%s): %v", src, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("ParseAny(%s) = %#v, want %#v", src, got, want)
		}
		got, err = parseAnyZeroCopyForTest(src)
		if err != nil {
			t.Fatalf("parseAnyZeroCopyForTest(%s): %v", src, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("parseAnyZeroCopyForTest(%s) = %#v, want %#v", src, got, want)
		}
	}
}

func TestParseAnyLongNumberArrayFastPath(t *testing.T) {
	for _, src := range [][]byte{
		[]byte(`[1234567890123456,1000000000000001,-1000000000000002]`),
		[]byte(`[ 1234567890123456 , 9007199254740993, 2.5, true, {"x":1} ]`),
		[]byte(`[1234567890123456,"switch",[1,2],null]`),
	} {
		var want any
		if err := json.Unmarshal(src, &want); err != nil {
			t.Fatal(err)
		}
		for _, parse := range []func([]byte) (any, error){ParseAny, parseAnyZeroCopyForTest} {
			got, err := parse(src)
			if err != nil {
				t.Fatalf("parse(%s): %v", src, err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("parse(%s) = %#v, want %#v", src, got, want)
			}
		}
	}
	for _, src := range [][]byte{
		[]byte(`[1234567890123456,]`),
		[]byte(`[1234567890123456 true]`),
		[]byte(`[12345678901234567]`),
	} {
		_, err := ParseAny(src)
		if (err == nil) != json.Valid(src) {
			t.Fatalf("ParseAny(%s) error = %v, json.Valid = %v", src, err, json.Valid(src))
		}
	}
}

func TestParseAnyRepeatedObjectSchemas(t *testing.T) {
	src := []byte(`[
		{"id":1,"active":true,"name":"first","message":"m","scores":[1,2,3]},
		{"id":2,"active":false,"name":"second","message":"n","scores":[4,5,6]},
		{"x":1,"y":2,"x":3},
		{"a":1,"b":2,"c":3,"d":4,"e":5,"f":6,"g":7,"h":8,"i":9}
	]`)
	var want any
	if err := json.Unmarshal(src, &want); err != nil {
		t.Fatal(err)
	}
	for _, parse := range []func([]byte) (any, error){ParseAny, parseAnyZeroCopyForTest} {
		got, err := parse(src)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("parse = %#v, want %#v", got, want)
		}
		wide := got.([]any)[3].(map[string]any)
		wide["j"] = 10.0
		delete(wide, "a")
		if len(wide) != 9 || wide["j"] != 10.0 {
			t.Fatalf("mutated wide map = %#v", wide)
		}
	}
}

func TestParseAnyOptions(t *testing.T) {
	v, err := ParseAnyOptions([]byte(`1e400`), AnyOptions{UseNumber: true})
	if err != nil {
		t.Fatal(err)
	}
	if number, ok := v.(json.Number); !ok || number != "1e400" {
		t.Fatalf("UseNumber result = %#v", v)
	}
	if _, err := ParseAny([]byte(`1e400`)); err == nil {
		t.Fatal("ParseAny accepted a float64 overflow")
	}
	if _, err := ParseAnyOptions([]byte(`[[0]]`), AnyOptions{MaxDepth: 1}); err == nil {
		t.Fatal("ParseAnyOptions accepted depth 2 with MaxDepth 1")
	}
}

func TestExactJSONFloat64(t *testing.T) {
	for _, text := range []string{
		"0", "-0", "1", "-42", "9007199254740992", "9007199254740993",
		"2.5", "1.25", "0.1", "-3e4", "1e-2", "5e-1", "1.2345678901234567",
	} {
		want, err := strconv.ParseFloat(text, 64)
		if err != nil {
			t.Fatal(err)
		}
		src := []byte(text)
		got, ok := exactJSONFloat64(unsafe.Pointer(unsafe.SliceData(src)), 0, len(src))
		if ok && math.Float64bits(got) != math.Float64bits(want) {
			t.Fatalf("exactJSONFloat64(%q) = %v, want %v", text, got, want)
		}
		decoded, err := ParseAny(src)
		if err != nil {
			t.Fatalf("ParseAny(%q): %v", text, err)
		}
		if math.Float64bits(decoded.(float64)) != math.Float64bits(want) {
			t.Fatalf("ParseAny(%q) = %v, want %v", text, decoded, want)
		}
	}
}

func TestParseAnyFloat64MatchesStrconv(t *testing.T) {
	for _, text := range []string{
		"0.000000000000000000000000000000001",
		"1234567890123456",
		"1234567890123456.25",
		"1234567890123456789",
		"12345678901234567890",
		"1.00000000000000011102230246251565404236316680908203125",
		"2.2250738585072014e-308",
		"4.9406564584124654e-324",
		"1.7976931348623157e308",
	} {
		assertParseAnyFloatBits(t, text)
	}

	state := uint64(0x243f6a8885a308d3)
	for range 50000 {
		state ^= state << 13
		state ^= state >> 7
		state ^= state << 17
		value := math.Float64frombits(state)
		if math.IsNaN(value) || math.IsInf(value, 0) {
			continue
		}
		assertParseAnyFloatBits(t, strconv.FormatFloat(value, 'g', -1, 64))
	}
}

func TestScaledJSONFloat64MatchesStrconv(t *testing.T) {
	state := uint64(0x6a09e667f3bcc909)
	for range 100000 {
		state ^= state << 13
		state ^= state >> 7
		state ^= state << 17
		mantissa := state%9999999999999999999 + 1
		exponent := -1 - int(state>>59)%22
		text := strconv.FormatUint(mantissa, 10) + "e" + strconv.Itoa(exponent)
		want, err := strconv.ParseFloat(text, 64)
		if err != nil {
			t.Fatal(err)
		}
		got, err := ParseFloat64([]byte(text))
		if err != nil {
			t.Fatalf("ParseFloat64(%q): %v", text, err)
		}
		if math.Float64bits(got) != math.Float64bits(want) {
			t.Fatalf("ParseFloat64(%q) = %.17g (%#x), want %.17g (%#x)",
				text, got, math.Float64bits(got), want, math.Float64bits(want))
		}
	}
}

func TestParseFloat64(t *testing.T) {
	for _, text := range []string{
		"0",
		"-0",
		"  2.5\n",
		"1234567890123456",
		"1234567890123456.25",
		"1.00000000000000011102230246251565404236316680908203125",
		"4.9406564584124654e-324",
	} {
		trimmed := strings.TrimSpace(text)
		want, err := strconv.ParseFloat(trimmed, 64)
		if err != nil {
			t.Fatal(err)
		}
		got, err := ParseFloat64([]byte(text))
		if err != nil {
			t.Fatalf("ParseFloat64(%q): %v", text, err)
		}
		if math.Float64bits(got) != math.Float64bits(want) {
			t.Fatalf("ParseFloat64(%q) = %.17g, want %.17g", text, got, want)
		}
	}
	for _, text := range []string{"", " ", "+1", "01", "1.", "1e", "NaN", "1 2", "1e400"} {
		if _, err := ParseFloat64([]byte(text)); err == nil {
			t.Fatalf("ParseFloat64(%q) succeeded", text)
		}
	}
	allocs := testing.AllocsPerRun(1000, func() {
		value, err := ParseFloat64([]byte("1234567890123456"))
		if err != nil {
			t.Fatal(err)
		}
		benchmarkFloatSink = value
	})
	if allocs != 0 {
		t.Fatalf("ParseFloat64 allocs = %v, want 0", allocs)
	}
}

func assertParseAnyFloatBits(t testing.TB, text string) {
	t.Helper()
	want, err := strconv.ParseFloat(text, 64)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseAny([]byte(text))
	if err != nil {
		t.Fatalf("ParseAny(%q): %v", text, err)
	}
	if math.Float64bits(got.(float64)) != math.Float64bits(want) {
		t.Fatalf("ParseAny(%q) = %.17g (%#x), want %.17g (%#x)",
			text, got, math.Float64bits(got.(float64)), want, math.Float64bits(want))
	}
	direct, err := ParseFloat64([]byte(text))
	if err != nil {
		t.Fatalf("ParseFloat64(%q): %v", text, err)
	}
	if math.Float64bits(direct) != math.Float64bits(want) {
		t.Fatalf("ParseFloat64(%q) = %.17g (%#x), want %.17g (%#x)",
			text, direct, math.Float64bits(direct), want, math.Float64bits(want))
	}
}

func TestParseAnyOptionsZeroCopyAliasesStrings(t *testing.T) {
	src := []byte(`["abc"]`)
	zeroCopy, err := parseAnyZeroCopyForTest(src)
	if err != nil {
		t.Fatal(err)
	}
	owned, err := ParseAny(src)
	if err != nil {
		t.Fatal(err)
	}
	src[2] = 'z'
	if got := zeroCopy.([]any)[0]; got != "zbc" {
		t.Fatalf("zero-copy string = %q, want zbc", got)
	}
	if got := owned.([]any)[0]; got != "abc" {
		t.Fatalf("owned string = %q, want abc", got)
	}
}

func TestValidateAllocs(t *testing.T) {
	src := benchmarkJSON()
	allocs := testing.AllocsPerRun(1000, func() {
		if !Valid(src) {
			t.Fatal("invalid")
		}
	})
	if allocs != 0 {
		t.Fatalf("Valid allocs = %v, want 0", allocs)
	}
}

func TestScalarValidatorAllocs(t *testing.T) {
	num := []byte(`-12.34e+56`)
	allocs := testing.AllocsPerRun(1000, func() {
		if !ValidNumber(num) {
			t.Fatal("invalid number")
		}
	})
	if allocs != 0 {
		t.Fatalf("ValidNumber allocs = %v, want 0", allocs)
	}

	allocs = testing.AllocsPerRun(1000, func() {
		if err := ValidateNumber(num); err != nil {
			t.Fatal(err)
		}
	})
	if allocs != 0 {
		t.Fatalf("ValidateNumber allocs = %v, want 0", allocs)
	}

	str := []byte(`"plain ascii string"`)
	allocs = testing.AllocsPerRun(1000, func() {
		if !ValidString(str) {
			t.Fatal("invalid string")
		}
	})
	if allocs != 0 {
		t.Fatalf("ValidString allocs = %v, want 0", allocs)
	}

	allocs = testing.AllocsPerRun(1000, func() {
		if err := ValidateString(str); err != nil {
			t.Fatal(err)
		}
	})
	if allocs != 0 {
		t.Fatalf("ValidateString allocs = %v, want 0", allocs)
	}
}

func BenchmarkValid(b *testing.B) {
	src := benchmarkJSON()
	b.SetBytes(int64(len(src)))
	for i := 0; i < b.N; i++ {
		if !Valid(src) {
			b.Fatal("invalid")
		}
	}
}

func BenchmarkStdlibValid(b *testing.B) {
	src := benchmarkJSON()
	b.SetBytes(int64(len(src)))
	for i := 0; i < b.N; i++ {
		if !json.Valid(src) {
			b.Fatal("invalid")
		}
	}
}

func BenchmarkValidNumber(b *testing.B) {
	src := []byte(`-12.34e+56`)
	b.SetBytes(int64(len(src)))
	for i := 0; i < b.N; i++ {
		if !ValidNumber(src) {
			b.Fatal("invalid")
		}
	}
}

func BenchmarkStdlibValidNumber(b *testing.B) {
	src := []byte(`-12.34e+56`)
	b.SetBytes(int64(len(src)))
	for i := 0; i < b.N; i++ {
		if !json.Valid(src) {
			b.Fatal("invalid")
		}
	}
}

func BenchmarkValidString(b *testing.B) {
	src := []byte(`"plain ascii string"`)
	b.SetBytes(int64(len(src)))
	for i := 0; i < b.N; i++ {
		if !ValidString(src) {
			b.Fatal("invalid")
		}
	}
}

func BenchmarkStdlibValidString(b *testing.B) {
	src := []byte(`"plain ascii string"`)
	b.SetBytes(int64(len(src)))
	for i := 0; i < b.N; i++ {
		if !json.Valid(src) {
			b.Fatal("invalid")
		}
	}
}

func BenchmarkValidSmall(b *testing.B) {
	src := smallJSON()
	b.SetBytes(int64(len(src)))
	for i := 0; i < b.N; i++ {
		if !Valid(src) {
			b.Fatal("invalid")
		}
	}
}

func BenchmarkStdlibValidSmall(b *testing.B) {
	src := smallJSON()
	b.SetBytes(int64(len(src)))
	for i := 0; i < b.N; i++ {
		if !json.Valid(src) {
			b.Fatal("invalid")
		}
	}
}

func BenchmarkValidLongString(b *testing.B) {
	src := longStringJSON()
	b.SetBytes(int64(len(src)))
	for i := 0; i < b.N; i++ {
		if !Valid(src) {
			b.Fatal("invalid")
		}
	}
}

func BenchmarkStdlibValidLongString(b *testing.B) {
	src := longStringJSON()
	b.SetBytes(int64(len(src)))
	for i := 0; i < b.N; i++ {
		if !json.Valid(src) {
			b.Fatal("invalid")
		}
	}
}

func BenchmarkValidLongUnicodeString(b *testing.B) {
	src := longUnicodeStringJSON()
	b.SetBytes(int64(len(src)))
	for i := 0; i < b.N; i++ {
		if !Valid(src) {
			b.Fatal("invalid")
		}
	}
}

func BenchmarkStdlibValidLongUnicodeString(b *testing.B) {
	src := longUnicodeStringJSON()
	b.SetBytes(int64(len(src)))
	for i := 0; i < b.N; i++ {
		if !json.Valid(src) {
			b.Fatal("invalid")
		}
	}
}

func BenchmarkParse(b *testing.B) {
	src := benchmarkJSON()
	b.SetBytes(int64(len(src)))
	for i := 0; i < b.N; i++ {
		if _, err := Parse(src); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStdlibUnmarshal(b *testing.B) {
	src := benchmarkJSON()
	b.SetBytes(int64(len(src)))
	for i := 0; i < b.N; i++ {
		var v any
		if err := json.Unmarshal(src, &v); err != nil {
			b.Fatal(err)
		}
		benchmarkSink = v
	}
}

func BenchmarkParseOptionsZeroCopy(b *testing.B) {
	src := benchmarkJSON()
	b.SetBytes(int64(len(src)))
	for i := 0; i < b.N; i++ {
		if _, err := parseOptionsZeroCopyForTest(src); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkBuildIndex(b *testing.B) {
	src := benchmarkJSON()
	count, err := RequiredIndexEntries(src)
	if err != nil {
		b.Fatal(err)
	}
	storage := make([]IndexEntry, count)
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		tape, err := BuildIndex(src, storage)
		if err != nil {
			b.Fatal(err)
		}
		indexBenchmarkSink = tape.Len()
	}
}

func BenchmarkBuildIndexPointerCompiled(b *testing.B) {
	src := benchmarkJSON()
	count, err := RequiredIndexEntries(src)
	if err != nil {
		b.Fatal(err)
	}
	storage := make([]IndexEntry, count)
	pointer := MustCompilePointer("/items/2/message")
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		tape, err := BuildIndex(src, storage)
		if err != nil {
			b.Fatal(err)
		}
		value, ok, err := tape.PointerCompiled(pointer)
		if err != nil || !ok {
			b.Fatal("pointer missing")
		}
		indexBenchmarkSink = len(value.Raw().Bytes())
	}
}

func BenchmarkIndexArrayIter4(b *testing.B) {
	src := rawArrayJSON()
	storage := make([]IndexEntry, len(src))
	tape, err := BuildIndex(src, storage)
	if err != nil {
		b.Fatal(err)
	}
	root := tape.Root()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		iter, ok := root.ArrayIter()
		if !ok {
			b.Fatal("root is not array")
		}
		total := 0
		for {
			value, ok := iter.Next()
			if !ok {
				break
			}
			total += int(value.Kind())
		}
		indexBenchmarkSink = total
	}
}

func BenchmarkIndexArrayIter1024(b *testing.B) {
	src := []byte("[" + strings.Repeat("0,", 1023) + "0]")
	storage := make([]IndexEntry, 1025)
	tape, err := BuildIndex(src, storage)
	if err != nil {
		b.Fatal(err)
	}
	root := tape.Root()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		iter, ok := root.ArrayIter()
		if !ok {
			b.Fatal("root is not array")
		}
		total := 0
		for {
			value, ok := iter.Next()
			if !ok {
				break
			}
			total += int(value.Kind())
		}
		indexBenchmarkSink = total
	}
}

func BenchmarkIndexArrayIterKind1024(b *testing.B) {
	src := []byte("[" + strings.Repeat("0,", 1023) + "0]")
	storage := make([]IndexEntry, 1025)
	tape, err := BuildIndex(src, storage)
	if err != nil {
		b.Fatal(err)
	}
	root := tape.Root()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		iter, ok := root.ArrayIter()
		if !ok {
			b.Fatal("root is not array")
		}
		total := 0
		for {
			kind, ok := iter.NextKind()
			if !ok {
				break
			}
			total += int(kind)
		}
		indexBenchmarkSink = total
	}
}

func BenchmarkIndexArrayCursorKind1024(b *testing.B) {
	src := []byte("[" + strings.Repeat("0,", 1023) + "0]")
	storage := make([]IndexEntry, 1025)
	tape, err := BuildIndex(src, storage)
	if err != nil {
		b.Fatal(err)
	}
	root := tape.Root()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		iter, ok := root.ArrayIter()
		if !ok {
			b.Fatal("root is not an array")
		}
		total := 0
		for iter.Valid() {
			total += int(iter.CurrentKind())
			iter = iter.Advance()
		}
		indexBenchmarkSink = total
	}
}

func BenchmarkIndexFlatArrayIterKind1024(b *testing.B) {
	src := []byte("[" + strings.Repeat("0,", 1023) + "0]")
	storage := make([]IndexEntry, 1025)
	tape, err := BuildIndex(src, storage)
	if err != nil {
		b.Fatal(err)
	}
	root := tape.Root()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		iter, ok := root.FlatArrayIter()
		if !ok {
			b.Fatal("root is not a flat array")
		}
		total := 0
		for {
			kind, ok := iter.NextKind()
			if !ok {
				break
			}
			total += int(kind)
		}
		indexBenchmarkSink = total
	}
}

func BenchmarkIndexFlatArrayDirectKind1024(b *testing.B) {
	src := []byte("[" + strings.Repeat("0,", 1023) + "0]")
	storage := make([]IndexEntry, 1025)
	tape, err := BuildIndex(src, storage)
	if err != nil {
		b.Fatal(err)
	}
	root := tape.Root()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		entry := tapeEntryOffset(root.entry, 1)
		remaining := root.entry.count
		total := 0
		for remaining != 0 {
			total += int(entry.kind)
			remaining--
			if remaining != 0 {
				entry = tapeEntryOffset(entry, 1)
			}
		}
		indexBenchmarkSink = total
	}
}

func BenchmarkIndexFlatArrayCursorKind1024(b *testing.B) {
	src := []byte("[" + strings.Repeat("0,", 1023) + "0]")
	storage := make([]IndexEntry, 1025)
	tape, err := BuildIndex(src, storage)
	if err != nil {
		b.Fatal(err)
	}
	root := tape.Root()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		iter, ok := root.FlatArrayIter()
		if !ok {
			b.Fatal("root is not a flat array")
		}
		total := 0
		for iter.Valid() {
			total += int(iter.CurrentKind())
			iter = iter.Advance()
		}
		indexBenchmarkSink = total
	}
}

func BenchmarkIndexArrayIterRaw1024(b *testing.B) {
	src := []byte("[" + strings.Repeat("0,", 1023) + "0]")
	storage := make([]IndexEntry, 1025)
	tape, err := BuildIndex(src, storage)
	if err != nil {
		b.Fatal(err)
	}
	root := tape.Root()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		iter, ok := root.ArrayIter()
		if !ok {
			b.Fatal("root is not array")
		}
		total := 0
		for {
			raw, ok := iter.NextRaw()
			if !ok {
				break
			}
			total += len(raw.Bytes())
		}
		indexBenchmarkSink = total
	}
}

func BenchmarkIndexObjectIter(b *testing.B) {
	src := rawObjectJSON()
	storage := make([]IndexEntry, len(src))
	tape, err := BuildIndex(src, storage)
	if err != nil {
		b.Fatal(err)
	}
	root := tape.Root()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		iter, ok := root.ObjectIter()
		if !ok {
			b.Fatal("root is not object")
		}
		total := 0
		for {
			key, value, ok := iter.Next()
			if !ok {
				break
			}
			total += int(key.Kind()) + int(value.Kind())
		}
		indexBenchmarkSink = total
	}
}

func BenchmarkIndexObjectCursor1024(b *testing.B) {
	src := []byte("{" + strings.Repeat(`"a":0,`, 1023) + `"a":0}`)
	storage := make([]IndexEntry, 2049)
	tape, err := BuildIndex(src, storage)
	if err != nil {
		b.Fatal(err)
	}
	root := tape.Root()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		iter, ok := root.ObjectIter()
		if !ok {
			b.Fatal("root is not an object")
		}
		total := 0
		for iter.Valid() {
			key, value := iter.Current()
			total += int(key.Kind()) + int(value.Kind())
			iter = iter.Advance()
		}
		indexBenchmarkSink = total
	}
}

func BenchmarkIndexFlatObjectCursor1024(b *testing.B) {
	src := []byte("{" + strings.Repeat(`"a":0,`, 1023) + `"a":0}`)
	storage := make([]IndexEntry, 2049)
	tape, err := BuildIndex(src, storage)
	if err != nil {
		b.Fatal(err)
	}
	root := tape.Root()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		iter, ok := root.FlatObjectIter()
		if !ok {
			b.Fatal("root is not a flat object")
		}
		total := 0
		for iter.Valid() {
			key, value := iter.Current()
			total += int(key.Kind()) + int(value.Kind())
			iter = iter.Advance()
		}
		indexBenchmarkSink = total
	}
}

func BenchmarkParseOptionsZeroCopySmall(b *testing.B) {
	src := smallJSON()
	b.SetBytes(int64(len(src)))
	for i := 0; i < b.N; i++ {
		if _, err := parseOptionsZeroCopyForTest(src); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStdlibUnmarshalSmall(b *testing.B) {
	src := smallJSON()
	b.SetBytes(int64(len(src)))
	for i := 0; i < b.N; i++ {
		var v any
		if err := json.Unmarshal(src, &v); err != nil {
			b.Fatal(err)
		}
		benchmarkSink = v
	}
}

func BenchmarkPointer(b *testing.B) {
	src := benchmarkJSON()
	b.SetBytes(int64(len(src)))
	for i := 0; i < b.N; i++ {
		v, ok, err := Get(src, "/items/2/message")
		if err != nil || !ok || v.kind != String {
			b.Fatal(v, ok, err)
		}
	}
}

func BenchmarkPointerZeroCopy(b *testing.B) {
	src := benchmarkJSON()
	b.SetBytes(int64(len(src)))
	for i := 0; i < b.N; i++ {
		v, ok, err := GetZeroCopy(src, "/items/2/message")
		if err != nil || !ok || v.kind != String {
			b.Fatal(v, ok, err)
		}
	}
}

func BenchmarkGetRaw(b *testing.B) {
	src := benchmarkJSON()
	b.SetBytes(int64(len(src)))
	for i := 0; i < b.N; i++ {
		raw, ok, err := GetRaw(src, "/items/2/message")
		if err != nil || !ok || raw.Kind() != String {
			b.Fatal(raw, ok, err)
		}
	}
}

func BenchmarkScanRaw(b *testing.B) {
	src := benchmarkJSON()
	b.SetBytes(int64(len(src)))
	for i := 0; i < b.N; i++ {
		raw, ok, err := ScanRaw(src, "/items/2/message")
		if err != nil || !ok || raw.Kind() != String {
			b.Fatal(raw, ok, err)
		}
	}
}

func BenchmarkGetRawCompiled(b *testing.B) {
	src := benchmarkJSON()
	ptr := MustCompilePointer("/items/2/message")
	b.SetBytes(int64(len(src)))
	for i := 0; i < b.N; i++ {
		raw, ok, err := ptr.GetRaw(src)
		if err != nil || !ok || raw.Kind() != String {
			b.Fatal(raw, ok, err)
		}
	}
}

func BenchmarkScanRawCompiled(b *testing.B) {
	src := benchmarkJSON()
	ptr := MustCompilePointer("/items/2/message")
	b.SetBytes(int64(len(src)))
	for i := 0; i < b.N; i++ {
		raw, ok, err := ptr.ScanRaw(src)
		if err != nil || !ok || raw.Kind() != String {
			b.Fatal(raw, ok, err)
		}
	}
}

func BenchmarkGetRawEarly(b *testing.B) {
	src := benchmarkJSON()
	b.SetBytes(int64(len(src)))
	for i := 0; i < b.N; i++ {
		raw, ok, err := GetRaw(src, "/items/0/id")
		if err != nil || !ok || raw.Kind() != Number {
			b.Fatal(raw, ok, err)
		}
	}
}

func BenchmarkScanRawEarly(b *testing.B) {
	src := benchmarkJSON()
	b.SetBytes(int64(len(src)))
	for i := 0; i < b.N; i++ {
		raw, ok, err := ScanRaw(src, "/items/0/id")
		if err != nil || !ok || raw.Kind() != Number {
			b.Fatal(raw, ok, err)
		}
	}
}

func BenchmarkScanRawEarlyCompiled(b *testing.B) {
	src := benchmarkJSON()
	ptr := MustCompilePointer("/items/0/id")
	b.SetBytes(int64(len(src)))
	for i := 0; i < b.N; i++ {
		raw, ok, err := ptr.ScanRaw(src)
		if err != nil || !ok || raw.Kind() != Number {
			b.Fatal(raw, ok, err)
		}
	}
}

func BenchmarkGetRawLongString(b *testing.B) {
	src := longStringJSON()
	b.SetBytes(int64(len(src)))
	for i := 0; i < b.N; i++ {
		raw, ok, err := GetRaw(src, "/s")
		if err != nil || !ok || raw.Kind() != String {
			b.Fatal(raw, ok, err)
		}
	}
}

func BenchmarkScanRawLongString(b *testing.B) {
	src := longStringJSON()
	b.SetBytes(int64(len(src)))
	for i := 0; i < b.N; i++ {
		raw, ok, err := ScanRaw(src, "/s")
		if err != nil || !ok || raw.Kind() != String {
			b.Fatal(raw, ok, err)
		}
	}
}

func BenchmarkScanRawLongStringCompiled(b *testing.B) {
	src := longStringJSON()
	ptr := MustCompilePointer("/s")
	b.SetBytes(int64(len(src)))
	for i := 0; i < b.N; i++ {
		raw, ok, err := ptr.ScanRaw(src)
		if err != nil || !ok || raw.Kind() != String {
			b.Fatal(raw, ok, err)
		}
	}
}

func BenchmarkAppendCompact(b *testing.B) {
	src := benchmarkJSON()
	dst := make([]byte, 0, len(src))
	b.SetBytes(int64(len(src)))
	for i := 0; i < b.N; i++ {
		out, err := AppendCompact(dst[:0], src)
		if err != nil {
			b.Fatal(err)
		}
		dst = out[:0]
	}
}

func BenchmarkStdlibCompact(b *testing.B) {
	src := benchmarkJSON()
	var dst bytes.Buffer
	dst.Grow(len(src))
	b.SetBytes(int64(len(src)))
	for i := 0; i < b.N; i++ {
		dst.Reset()
		if err := json.Compact(&dst, src); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEachArrayRaw(b *testing.B) {
	src := rawArrayJSON()
	b.SetBytes(int64(len(src)))
	total := 0
	for i := 0; i < b.N; i++ {
		if err := EachArray(src, func(_ int, value RawValue) error {
			total += len(value.Bytes())
			return nil
		}); err != nil {
			b.Fatal(err)
		}
	}
	benchmarkSink = total
}

func BenchmarkStdlibArrayRawMessages(b *testing.B) {
	src := rawArrayJSON()
	b.SetBytes(int64(len(src)))
	var total int
	for i := 0; i < b.N; i++ {
		var values []json.RawMessage
		if err := json.Unmarshal(src, &values); err != nil {
			b.Fatal(err)
		}
		for _, value := range values {
			total += len(value)
		}
	}
	benchmarkSink = total
}

func BenchmarkEachObjectRaw(b *testing.B) {
	src := rawObjectJSON()
	b.SetBytes(int64(len(src)))
	total := 0
	for i := 0; i < b.N; i++ {
		if err := EachObject(src, func(key string, value RawValue) error {
			total += len(key) + len(value.Bytes())
			return nil
		}); err != nil {
			b.Fatal(err)
		}
	}
	benchmarkSink = total
}

func BenchmarkStdlibObjectRawMessages(b *testing.B) {
	src := rawObjectJSON()
	b.SetBytes(int64(len(src)))
	var total int
	for i := 0; i < b.N; i++ {
		var values map[string]json.RawMessage
		if err := json.Unmarshal(src, &values); err != nil {
			b.Fatal(err)
		}
		for key, value := range values {
			total += len(key) + len(value)
		}
	}
	benchmarkSink = total
}

func benchmarkJSON() []byte {
	return []byte(`{
		"items": [
			{"id": 1, "active": true, "message": "this is a long ASCII string that should make the SIMD scanner skip most bytes quickly"},
			{"id": 2, "active": false, "message": "another long string with escaped data\nand normal text around it"},
			{"id": 3, "active": true, "message": "the parser keeps object order and number spelling intact"}
		],
		"meta": {"source": "benchmark", "version": 1}
	}`)
}

func smallJSON() []byte {
	return []byte(`{"id":7,"name":"small ascii payload","ok":true,"n":123}`)
}

func longStringJSON() []byte {
	return []byte(`{"s":"` + strings.Repeat("a", 4096) + `"}`)
}

func longUnicodeStringJSON() []byte {
	return []byte(`{"s":"` + strings.Repeat("こんにちは世界", 256) + `"}`)
}

func rawArrayJSON() []byte {
	return []byte(`[
		{"id":1,"name":"alpha","active":true},
		{"id":2,"name":"beta","active":false},
		{"id":3,"name":"gamma","active":true},
		{"id":4,"name":"delta","active":false}
	]`)
}

func rawObjectJSON() []byte {
	return []byte(`{
		"id": 42,
		"name": "simdjson",
		"active": true,
		"tags": ["simd", "json", "go"],
		"meta": {"version": 1, "status": "fast"}
	}`)
}

func parseOptionsZeroCopyForTest(src []byte) (Value, error) {
	return ParseOptions(src, Options{ZeroCopy: true})
}

func parseAnyZeroCopyForTest(src []byte) (any, error) {
	return ParseAnyOptions(src, AnyOptions{ZeroCopy: true})
}

func TestOwnedResultsSurviveSourceMutation(t *testing.T) {
	src := []byte(`{"name":"keepsake","n":12345,"nested":["alpha","beta"]}`)

	value, err := Parse(bytes.Clone(src))
	if err != nil {
		t.Fatal(err)
	}
	parsed := bytes.Clone(src)
	dynamic, err := ParseAny(parsed)
	if err != nil {
		t.Fatal(err)
	}
	for i := range parsed {
		parsed[i] = 'x'
	}

	name, _ := value.Get("name")
	if got, _ := name.Text(); got != "keepsake" {
		t.Fatalf("Parse string = %q, want keepsake", got)
	}
	object := dynamic.(map[string]any)
	if got := object["name"]; got != "keepsake" {
		t.Fatalf("ParseAny string = %v, want keepsake", got)
	}
	if got := object["nested"].([]any)[1]; got != "beta" {
		t.Fatalf("ParseAny nested string = %v, want beta", got)
	}
}
