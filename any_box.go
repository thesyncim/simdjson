package simdjson

// Slab boxing for ParseAny's scalars.
//
// Converting a scalar to any normally heap-allocates a private copy of its
// value — eight bytes per float64, a two-word header per string, a
// three-word header per []any — and on dynamic decodes those conversions are
// a leading allocation class. anyBoxer batch-allocates those value copies in
// typed slabs (one []float64, one []string, one [][]any per document) and
// builds each interface value by pairing the runtime type word of the boxed
// kind with a pointer to the value's slot inside its slab. The result is
// indistinguishable from ordinary boxing: type assertions, switches,
// reflection, and interface equality all read the same two words an ordinary
// conversion would produce; equality compares pointed-to values, never slot
// addresses. All three kinds are stored indirectly in an interface (none is
// pointer-shaped), which is what makes the shared data-word construction
// valid; the boxer must not be extended to pointer-shaped types.
//
// Corruption-safety contract (see also verifyAnyBoxLayout in any_box_fast.go):
//
//   - Every slab chunk is heap memory, guaranteed by construction: the
//     newAny*Slab constructors are marked go:noinline, so their make results
//     escape their frames and can never be stack-allocated into a frame that
//     might move. An interface data word must never point into a goroutine
//     stack.
//   - The data word is an interior pointer into a live, precisely typed heap
//     object ([]float64, []string, or [][]any), so the garbage collector both
//     keeps the whole chunk reachable through any one boxed value and scans
//     the string and slice slots' own pointers correctly.
//   - A slot is written exactly once, before its address is handed out, and
//     never rewritten: the boxers only append, and a chunk that fills up is
//     abandoned in place (boxed values already pointing into it keep it
//     alive) rather than recycled. Slabs belong to one document's result and
//     are never pooled, so no boxed value can observe a later document's
//     writes.
//   - The eface construction itself runs only after verifyAnyBoxLayout has
//     proven, on this exact toolchain, that a hand-built interface matches an
//     ordinary conversion; otherwise, and always under -race or the
//     simdjson_safehooks tag, the boxers fall back to plain conversions.
//
// Chunks grow geometrically from a few values to roughly 4 KiB apiece, so a
// small document pays tens of bytes of slack while a large one amortizes
// chunk allocation to a fraction of a percent per value; the cap keeps a
// retained fragment of a document from pinning an outsized chunk.
const (
	anySlabFirstChunk  = 16
	anyFloatSlabChunk  = 512
	anyStringSlabChunk = 256
	anyValuesSlabChunk = 170
)

// nextAnySlabSize sizes the chunk that follows one of size current.
func nextAnySlabSize(current, max int) int {
	if current == 0 {
		return anySlabFirstChunk
	}
	if current >= max/2 {
		return max
	}
	return 2 * current
}

// anyBoxer carries the per-document slabs. Its zero value is ready to use;
// the first boxed value of each kind allocates that kind's first chunk.
type anyBoxer struct {
	floats  []float64
	strings []string
	values  [][]any
}

// newAnyFloatSlab, newAnyStringSlab, and newAnyValuesSlab allocate slab
// chunks. They must not be inlined: a returned make escapes the callee, so
// the chunk is guaranteed heap memory, which the boxing contract above
// requires. Inlined, the caller's escape analysis would be free to prove the
// chunk short-lived and place it on a movable goroutine stack.

//go:noinline
func newAnyFloatSlab(n int) []float64 {
	return make([]float64, 0, n)
}

//go:noinline
func newAnyStringSlab(n int) []string {
	return make([]string, 0, n)
}

//go:noinline
func newAnyValuesSlab(n int) [][]any {
	return make([][]any, 0, n)
}
