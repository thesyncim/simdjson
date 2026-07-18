//go:build goexperiment.simd && amd64

package simd

// noescapeBytes returns the same slice while hiding the alias from escape
// analysis. The assembly body copies only the slice header; the backing store
// remains owned and kept alive by the caller. This is used at the amd64
// runtime-selected scanner boundary so an indirect call does not force a
// stack-backed parser state onto the heap.
//
//go:noescape
func noescapeBytes(src []byte) []byte
