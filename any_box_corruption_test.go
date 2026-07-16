package simdjson

// Slab-boxer corruption pass.
//
// Dynamic decoding builds interface values whose data words are interior pointers
// into shared heap slabs (any_box.go). The invariants that keep that safe —
// slabs are heap-resident, slots are written once before handout, chunks are
// never recycled — would, if violated, corrupt trees only when the collector
// or another document races the violation, so this file stresses exactly
// that: many goroutines decode concurrently, force stack growth and GC
// between iterations, retain trees across collections, and re-verify every
// retained tree at the end. A violation surfaces as a fatal "found bad
// pointer in Go heap", or as a tree that no longer DeepEquals the
// encoding/json ground truth (the safe build's boxers are ordinary
// conversions, and the differential suites prove dynamic decoding matches
// encoding/json, so ground truth and safe path agree).
//
// As with the sibling corruption files, -race MASKS this class by perturbing
// scheduling — and under -race the slab boxers are compiled out entirely —
// so the intended stress invocation is without -race:
//
//	GOGC=1 GOEXPERIMENT=simd gotip test -run TestGCCorruptionAnyBox -count=5 -cpu=1,4,8 ./
//
// The smoke form (plain, low count) is safe for CI.

import (
	"encoding/json"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// anyBoxCorpusDoc builds a document that hits every slab kind: float64s in
// and out of the plain-integer fast path, strings clean and escaped, nested
// non-empty arrays, empty arrays, and objects, salted so goroutines cannot
// share results by accident. The row count keeps the document above
// anyBoxMinSource — smaller documents box scalars with ordinary conversions
// and would not exercise the slabs at all; the test fails loudly if the two
// ever drift apart.
const anyBoxCorpusRows = 4300

func anyBoxCorpusDoc(salt int) []byte {
	var b strings.Builder
	b.Grow(640 << 10)
	fmt.Fprintf(&b, `{"salt":%d,"empty":[],"rows":[`, salt)
	for i := 0; i < anyBoxCorpusRows; i++ {
		if i != 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b,
			`{"id":%d,"f":%d.%04d,"e":1.5e%d,"s":"row-%d-%d","esc":"a\tbé%d","xs":[%d,%d.5,-3e2],"none":[],"ok":%t,"gap":null}`,
			salt+i, i, i*7%9973, i%30, salt, i, i, i, i, i%2 == 0)
	}
	b.WriteString(`]}`)
	return []byte(b.String())
}

// TestGCCorruptionAnyBoxSlabs decodes documents on goroutines that force
// stack relocation and GC between iterations, retaining trees so slab chunks
// stay live across many collections. Owned-mode sources are scribbled over
// after decoding: a slab-boxed value that still aliased the caller's buffer
// would surface as a mismatch, not just a leak. Every retained tree is
// re-verified after the churn, catching a chunk that was recycled or moved
// out from under its boxed values.
func TestGCCorruptionAnyBoxSlabs(t *testing.T) {
	if len(anyBoxCorpusDoc(0)) < anyBoxMinSource {
		t.Fatalf("corpus document is smaller than anyBoxMinSource; the slab boxers are not exercised")
	}
	ownedDecoder, err := CompileDecoder[any](DecoderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	zeroCopyDecoder, err := CompileDecoder[any](DecoderOptions{ZeroCopy: true})
	if err != nil {
		t.Fatal(err)
	}
	const goroutines = 12
	iters := 24
	if testing.Short() {
		iters = 6
	}
	var wg sync.WaitGroup
	var bad int64
	var sink int64
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			type kept struct {
				got  any
				want any
			}
			keep := make([]kept, 0, 6)
			for it := 0; it < iters; it++ {
				src := anyBoxCorpusDoc(g*1_000_000 + it)
				var want any
				if err := json.Unmarshal(src, &want); err != nil {
					atomic.AddInt64(&bad, 1)
					continue
				}
				zeroCopy := it%2 == 1
				decoder := ownedDecoder
				if zeroCopy {
					decoder = zeroCopyDecoder
				}
				var got any
				if err := decoder.Decode(src, &got); err != nil {
					atomic.AddInt64(&bad, 1)
					continue
				}
				if !zeroCopy {
					// Owned results must not alias src in any way.
					for i := range src {
						src[i] = 'X'
					}
				}
				// Relocate this goroutine's stack while the fresh tree's slab
				// chunks are only reachable through its interface values.
				atomic.AddInt64(&sink, int64(forceStackMovement(24+(it&31), it)))
				if !reflect.DeepEqual(got, want) {
					atomic.AddInt64(&bad, 1)
					continue
				}
				if zeroCopy {
					// Zero-copy trees alias src, which this loop rewrites next
					// iteration by building a fresh one; retain only owned trees.
					continue
				}
				keep = append(keep, kept{got: got, want: want})
				if len(keep) >= 6 {
					runtime.GC()
					for i := range keep {
						if !reflect.DeepEqual(keep[i].got, keep[i].want) {
							atomic.AddInt64(&bad, 1)
						}
					}
					keep = keep[:0]
				}
			}
			runtime.GC()
			for i := range keep {
				if !reflect.DeepEqual(keep[i].got, keep[i].want) {
					atomic.AddInt64(&bad, 1)
				}
			}
		}(g)
	}
	wg.Wait()
	if bad != 0 {
		t.Fatalf("bad=%d: slab-boxed trees diverged from encoding/json (sink=%d)", bad, atomic.LoadInt64(&sink))
	}
}

// TestAnyBoxLayoutVerified pins the expectation that the slab boxers are
// active on ordinary builds and disabled on safe builds, so a silently
// failing layout probe cannot masquerade as a pass: if the probe ever fails
// where it should succeed, this test fails rather than the boxers quietly
// running in fallback mode forever.
func TestAnyBoxLayoutVerified(t *testing.T) {
	if hookSafeDispatch {
		// Safe builds (-race or simdjson_safehooks) compile the boxers as
		// ordinary conversions.
		if anyBoxLayoutOK {
			t.Fatal("slab boxers must be disabled in the safe build")
		}
		return
	}
	if !anyBoxLayoutOK {
		t.Fatal("slab boxer layout probe failed on this toolchain")
	}
}
