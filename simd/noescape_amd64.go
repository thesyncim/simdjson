//go:build goexperiment.simd && amd64

package simd

// noescapeBytes returns the same slice while hiding the alias from escape
// analysis. Its assembly body copies only the slice header; the backing store
// remains owned and kept alive by the caller. This keeps stack-backed scanner
// input from escaping through the selected function value.
//
//go:noescape
func noescapeBytes(src []byte) []byte
