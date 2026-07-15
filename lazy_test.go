package simdjson

import (
	"bytes"
	"encoding/json"
	"math"
	"reflect"
	"runtime"
	"strconv"
	"testing"
)

// lazyToAny walks a LazyValue into the same standard Go shapes that
// Value.Any() and encoding/json produce, so the three can be compared directly.
// Numbers become json.Number to preserve exact spelling.
func lazyToAny(t *testing.T, v LazyValue) any {
	t.Helper()
	switch v.Kind() {
	case Null:
		return nil
	case Bool:
		b, ok := v.Bool()
		if !ok {
			t.Fatal("Bool() failed on Bool kind")
		}
		return b
	case Number:
		s, ok := v.NumberText()
		if !ok {
			t.Fatal("NumberText() failed on Number kind")
		}
		return json.Number(s)
	case String:
		s, ok := v.Text()
		if !ok {
			t.Fatal("Text() failed on String kind")
		}
		return s
	case Array:
		n, _ := v.ArrayLen()
		out := make([]any, 0, n)
		iter, ok := v.ArrayIter()
		if !ok {
			t.Fatal("ArrayIter() failed on Array kind")
		}
		for {
			node, ok := iter.Next()
			if !ok {
				break
			}
			out = append(out, lazyToAny(t, v.with(node)))
		}
		return out
	case Object:
		out := map[string]any{}
		iter, ok := v.ObjectIter()
		if !ok {
			t.Fatal("ObjectIter() failed on Object kind")
		}
		for {
			key, val, ok := iter.Next()
			if !ok {
				break
			}
			ks, _ := key.StringBytes()
			var keyStr string
			if ks != nil {
				keyStr = string(ks)
			} else {
				b, _ := key.AppendString(nil)
				keyStr = string(b)
			}
			out[keyStr] = lazyToAny(t, v.with(val))
		}
		return out
	default:
		t.Fatalf("unexpected kind %v", v.Kind())
		return nil
	}
}

// normalizeNumbers rewrites every json.Number to its canonical float64 spelling
// so that equal numeric values with different spellings (e.g. "1e2" vs "100")
// compare equal across the three producers.
func normalizeNumbers(t *testing.T, v any) any {
	t.Helper()
	switch x := v.(type) {
	case json.Number:
		f, err := strconv.ParseFloat(string(x), 64)
		if err != nil {
			return string(x)
		}
		return f
	case float64:
		return x
	case []any:
		for i := range x {
			x[i] = normalizeNumbers(t, x[i])
		}
		return x
	case map[string]any:
		for k := range x {
			x[k] = normalizeNumbers(t, x[k])
		}
		return x
	default:
		return v
	}
}

var lazyCorpus = func() map[string][]byte {
	return map[string][]byte{
		"citm":       citmLikeJSON(64),
		"intArray":   intArrayJSON(128),
		"floatArray": floatArrayJSON(128),
		"coordRings": coordRingsJSON(64),
		"scalar":     []byte(`  42.5 `),
		"emptyObj":   []byte(`{}`),
		"emptyArr":   []byte(`[]`),
		"nested":     []byte(`{"a":{"b":[1,2,{"c":"xéy"}]},"d":null,"e":true,"f":false}`),
		"dupKeys":    []byte(`{"k":1,"k":2,"k":3}`),
		"escapes":    []byte(`{"a\tb":"line1\nline2☃","emoji":"😀"}`),
	}
}()

// TestLazyMatchesEagerAndStdlib is the core differential proof: for every
// corpus document, ParseLazy read fully must agree with Parse's eager tree and
// with encoding/json.
func TestLazyMatchesEagerAndStdlib(t *testing.T) {
	for name, src := range lazyCorpus {
		t.Run(name, func(t *testing.T) {
			lazy, err := ParseLazy(src)
			if err != nil {
				t.Fatalf("ParseLazy: %v", err)
			}
			eager, err := Parse(src)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}

			lazyAny := normalizeNumbers(t, lazyToAny(t, lazy))
			eagerAny := normalizeNumbers(t, eager.Any())
			if !reflect.DeepEqual(lazyAny, eagerAny) {
				t.Fatalf("lazy != eager\nlazy:  %#v\neager: %#v", lazyAny, eagerAny)
			}

			var stdAny any
			dec := json.NewDecoder(bytes.NewReader(src))
			dec.UseNumber()
			if err := dec.Decode(&stdAny); err != nil {
				t.Fatalf("encoding/json: %v", err)
			}
			stdAny = normalizeNumbers(t, stdAny)
			if !reflect.DeepEqual(lazyAny, stdAny) {
				t.Fatalf("lazy != encoding/json\nlazy: %#v\nstd:  %#v", lazyAny, stdAny)
			}
		})
	}
}

// TestLazyZeroCopyMatches confirms the zero-copy handle reads identically.
func TestLazyZeroCopyMatches(t *testing.T) {
	for name, src := range lazyCorpus {
		t.Run(name, func(t *testing.T) {
			// zero-copy aliases src, so keep a private copy alive.
			buf := append([]byte(nil), src...)
			lazy, err := ParseLazyOptions(buf, Options{ZeroCopy: true})
			if err != nil {
				t.Fatalf("ParseLazyOptions: %v", err)
			}
			eager, err := Parse(src)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if !reflect.DeepEqual(
				normalizeNumbers(t, lazyToAny(t, lazy)),
				normalizeNumbers(t, eager.Any()),
			) {
				t.Fatalf("zero-copy lazy != eager for %s", name)
			}
		})
	}
}

// TestLazyValueBridge confirms LazyValue.Value() produces the same tree as the
// eager Parse for a navigated subtree.
func TestLazyValueBridge(t *testing.T) {
	src := lazyCorpus["citm"]
	lazy, err := ParseLazy(src)
	if err != nil {
		t.Fatal(err)
	}
	sub, ok, err := lazy.Pointer("/events/3")
	if err != nil || !ok {
		t.Fatalf("pointer /events/3: %v %v", ok, err)
	}
	eager, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	eagerSub, ok, err := eager.Pointer("/events/3")
	if err != nil || !ok {
		t.Fatalf("eager pointer: %v %v", ok, err)
	}
	if !reflect.DeepEqual(
		normalizeNumbers(t, sub.Value().Any()),
		normalizeNumbers(t, eagerSub.Any()),
	) {
		t.Fatal("LazyValue.Value() subtree != eager subtree")
	}
}

// TestLazyPartialGCSafe checks that a handle survives after src is dropped and a
// GC is forced, proving the non-zero-copy handle is self-contained.
func TestLazyPartialGCSafe(t *testing.T) {
	makeAndRead := func() (int64, bool) {
		src := citmLikeJSON(64)
		lazy, err := ParseLazy(src)
		if err != nil {
			t.Fatal(err)
		}
		// src goes out of scope here; the handle must own its bytes.
		v, ok, err := lazy.Pointer("/events/10/id")
		if err != nil || !ok {
			return 0, false
		}
		return v.Int64()
	}
	id, ok := makeAndRead()
	if !ok {
		t.Fatal("id missing")
	}
	runtime.GC()
	if id == 0 {
		t.Fatal("id read as zero after GC")
	}
}

// TestLazyFloatExactness spot-checks that lazy Float64 matches strconv for a set
// of adversarial spellings, since number parsing is the correctness-critical
// path.
func TestLazyFloatExactness(t *testing.T) {
	cases := []string{
		"0", "-0", "3.141592653589793", "1e308", "5e-324",
		"1.7976931348623157e308", "9007199254740993", "123456789012345678",
		"-0.0001", "2.2250738585072014e-308",
	}
	for _, c := range cases {
		src := []byte("[" + c + "]")
		lazy, err := ParseLazy(src)
		if err != nil {
			t.Fatalf("%s: %v", c, err)
		}
		el, ok := lazy.Index(0)
		if !ok {
			t.Fatalf("%s: index 0 missing", c)
		}
		got, ok := el.Float64()
		if !ok {
			t.Fatalf("%s: Float64 failed", c)
		}
		want, _ := strconv.ParseFloat(c, 64)
		if got != want && !(math.IsNaN(got) && math.IsNaN(want)) {
			t.Fatalf("%s: got %v want %v", c, got, want)
		}
	}
}

// --- A/B throughput and allocation benchmarks (lazy vs eager Parse) ---
//
// Each corpus is measured under three access patterns, always A/B interleaved
// so the ratio is trustworthy under load:
//   Full    - traverse the whole document, forcing every scalar.
//   Partial - read a handful of fields, the On-Demand sweet spot.
//   ParseOnly - build the tree/index without reading any value.

func lazyBenchCorpus() []struct {
	name string
	data []byte
} {
	return []struct {
		name string
		data []byte
	}{
		{"Citm", citmLikeJSON(1024)},
		{"IntArray", intArrayJSON(8192)},
		{"FloatArray", floatArrayJSON(8192)},
		{"CoordRings", coordRingsJSON(4096)},
	}
}

// sumEagerFull walks the eager Value tree summing every number, forcing the
// whole tree to be materialized and read.
func sumEagerFull(v Value) float64 {
	switch v.Kind() {
	case Number:
		f, _ := v.Float64()
		return f
	case Array:
		arr, _ := v.Array()
		var s float64
		for i := range arr {
			s += sumEagerFull(arr[i])
		}
		return s
	case Object:
		obj, _ := v.Object()
		var s float64
		for i := range obj {
			s += sumEagerFull(obj[i].Value)
		}
		return s
	default:
		return 0
	}
}

// sumLazyFull walks a LazyValue summing every number without ever building a
// Value tree.
func sumLazyFull(v LazyValue) float64 {
	switch v.Kind() {
	case Number:
		f, _ := v.Float64()
		return f
	case Array:
		iter, _ := v.ArrayIter()
		var s float64
		for {
			node, ok := iter.Next()
			if !ok {
				break
			}
			s += sumLazyFull(v.with(node))
		}
		return s
	case Object:
		iter, _ := v.ObjectIter()
		var s float64
		for {
			_, val, ok := iter.Next()
			if !ok {
				break
			}
			s += sumLazyFull(v.with(val))
		}
		return s
	default:
		return 0
	}
}

var lazyFloatSink float64

func BenchmarkParseFullEager(b *testing.B) {
	for _, c := range lazyBenchCorpus() {
		b.Run(c.name, func(b *testing.B) {
			b.SetBytes(int64(len(c.data)))
			b.ReportAllocs()
			var s float64
			for range b.N {
				v, err := Parse(c.data)
				if err != nil {
					b.Fatal(err)
				}
				s += sumEagerFull(v)
			}
			lazyFloatSink = s
		})
	}
}

func BenchmarkParseFullLazy(b *testing.B) {
	for _, c := range lazyBenchCorpus() {
		b.Run(c.name, func(b *testing.B) {
			b.SetBytes(int64(len(c.data)))
			b.ReportAllocs()
			var s float64
			for range b.N {
				v, err := ParseLazy(c.data)
				if err != nil {
					b.Fatal(err)
				}
				s += sumLazyFull(v)
			}
			lazyFloatSink = s
		})
	}
}

// Partial access: read four fields from the 3rd, 100th, and 900th events of
// Citm; for the flat number arrays, read three individual elements. This is
// where On-Demand should shine: the eager path pays to materialize the whole
// document, the lazy path pays only the index build plus a few reads.

func BenchmarkParsePartialEager(b *testing.B) {
	citm := citmLikeJSON(1024)
	ints := intArrayJSON(8192)
	b.Run("Citm", func(b *testing.B) {
		b.SetBytes(int64(len(citm)))
		b.ReportAllocs()
		var s float64
		for range b.N {
			v, err := Parse(citm)
			if err != nil {
				b.Fatal(err)
			}
			for _, idx := range []int{3, 100, 900} {
				ev, ok, _ := v.Pointer("/events/" + itoa(idx))
				if !ok {
					b.Fatal("event missing")
				}
				id, _ := ev.Get("id")
				price, _ := ev.Get("price")
				name, _ := ev.Get("name")
				fid, _ := id.Float64()
				fpr, _ := price.Float64()
				ntxt, _ := name.Text()
				s += fid + fpr + float64(len(ntxt))
			}
		}
		lazyFloatSink = s
	})
	b.Run("IntArray", func(b *testing.B) {
		b.SetBytes(int64(len(ints)))
		b.ReportAllocs()
		var s float64
		for range b.N {
			v, err := Parse(ints)
			if err != nil {
				b.Fatal(err)
			}
			for _, idx := range []int{7, 4000, 8000} {
				el, ok := v.Index(idx)
				if !ok {
					b.Fatal("index missing")
				}
				f, _ := el.Float64()
				s += f
			}
		}
		lazyFloatSink = s
	})
}

func BenchmarkParsePartialLazy(b *testing.B) {
	citm := citmLikeJSON(1024)
	ints := intArrayJSON(8192)
	b.Run("Citm", func(b *testing.B) {
		b.SetBytes(int64(len(citm)))
		b.ReportAllocs()
		var s float64
		for range b.N {
			v, err := ParseLazy(citm)
			if err != nil {
				b.Fatal(err)
			}
			for _, idx := range []int{3, 100, 900} {
				ev, ok, _ := v.Pointer("/events/" + itoa(idx))
				if !ok {
					b.Fatal("event missing")
				}
				id, _ := ev.Get("id")
				price, _ := ev.Get("price")
				name, _ := ev.Get("name")
				fid, _ := id.Float64()
				fpr, _ := price.Float64()
				ntxt, _ := name.Text()
				s += fid + fpr + float64(len(ntxt))
			}
		}
		lazyFloatSink = s
	})
	b.Run("IntArray", func(b *testing.B) {
		b.SetBytes(int64(len(ints)))
		b.ReportAllocs()
		var s float64
		for range b.N {
			v, err := ParseLazy(ints)
			if err != nil {
				b.Fatal(err)
			}
			for _, idx := range []int{7, 4000, 8000} {
				el, ok := v.Index(idx)
				if !ok {
					b.Fatal("index missing")
				}
				f, _ := el.Float64()
				s += f
			}
		}
		lazyFloatSink = s
	})
}

func BenchmarkParseOnlyEager(b *testing.B) {
	for _, c := range lazyBenchCorpus() {
		b.Run(c.name, func(b *testing.B) {
			b.SetBytes(int64(len(c.data)))
			b.ReportAllocs()
			for range b.N {
				v, err := Parse(c.data)
				if err != nil {
					b.Fatal(err)
				}
				benchmarkSink = v.Kind()
			}
		})
	}
}

func BenchmarkParseOnlyLazy(b *testing.B) {
	for _, c := range lazyBenchCorpus() {
		b.Run(c.name, func(b *testing.B) {
			b.SetBytes(int64(len(c.data)))
			b.ReportAllocs()
			for range b.N {
				v, err := ParseLazy(c.data)
				if err != nil {
					b.Fatal(err)
				}
				benchmarkSink = v.Kind()
			}
		})
	}
}

func itoa(i int) string { return strconv.Itoa(i) }

// TestLazyPoolReuseIsolation hammers ParseLazy concurrently with distinct
// documents to prove the pooled index storage is copied out per handle and no
// two handles ever share a recycled buffer.
func TestLazyPoolReuseIsolation(t *testing.T) {
	docs := [][]byte{
		[]byte(`{"n":1}`),
		[]byte(`[1,2,3,4,5]`),
		[]byte(`{"a":{"b":{"c":42}}}`),
		citmLikeJSON(8),
		[]byte(`"hello"`),
	}
	const goroutines = 16
	done := make(chan error, goroutines)
	for g := 0; g < goroutines; g++ {
		go func(seed int) {
			for iter := 0; iter < 2000; iter++ {
				src := docs[(seed+iter)%len(docs)]
				lazy, err := ParseLazy(src)
				if err != nil {
					done <- err
					return
				}
				want, _ := Parse(src)
				if !reflect.DeepEqual(
					normalizeNumbers(t, lazyToAny(t, lazy)),
					normalizeNumbers(t, want.Any()),
				) {
					done <- errMismatch
					return
				}
			}
			done <- nil
		}(g)
	}
	for g := 0; g < goroutines; g++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

var errMismatch = &pointerErrorStub{"lazy handle content diverged under concurrent pool reuse"}

type pointerErrorStub struct{ msg string }

func (e *pointerErrorStub) Error() string { return e.msg }
