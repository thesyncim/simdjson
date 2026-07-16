package benchmarks

import (
	"reflect"
	"testing"

	"github.com/thesyncim/simdjson"
	simdkernels "github.com/thesyncim/simdjson/simd"
)

// TestStage2IndexCorpora holds the mask-driven index engine to
// byte-identical tapes on every corpus payload. The reference is the
// portable builder, reached through the public API by capping MaxDepth
// below the machine's gate (the corpora nest far shallower than either
// limit, so the cap changes routing and nothing else). Node-level
// navigation over both indexes cross-checks the comparison itself.
func TestStage2IndexCorpora(t *testing.T) {
	if !simdkernels.Stage2Enabled() {
		t.Skip("stage-2 machine not available on this build")
	}
	for _, c := range loadGapCorpora(t) {
		need, err := simdjson.RequiredIndexEntries(c.src)
		if err != nil {
			t.Fatalf("%s: %v", c.label, err)
		}
		machine, err := simdjson.BuildIndex(c.src, make([]simdjson.IndexEntry, need))
		if err != nil {
			t.Fatalf("%s: machine-path build: %v", c.label, err)
		}
		reference, err := simdjson.BuildIndexOptions(c.src, make([]simdjson.IndexEntry, need),
			simdjson.IndexOptions{MaxDepth: 63})
		if err != nil {
			t.Fatalf("%s: reference build: %v", c.label, err)
		}
		if machine.Len() != reference.Len() {
			t.Fatalf("%s: machine tape %d entries, reference %d", c.label, machine.Len(), reference.Len())
		}
		if !reflect.DeepEqual(machine, reference) {
			t.Fatalf("%s: tapes differ", c.label)
		}
		mr, rr := machine.Root(), reference.Root()
		if mr.Kind() != rr.Kind() {
			t.Fatalf("%s: root kind %v vs %v", c.label, mr.Kind(), rr.Kind())
		}
	}
}
