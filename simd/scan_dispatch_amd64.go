//go:build goexperiment.simd && amd64

package simd

// selectAVX2Scanner installs AVX2 targets into the assembly tail
// trampolines. Package initialization calls it at most once.
func selectAVX2Scanner()

// The runtime scanner entry points preserve the slice's noescape
// contract while tail-jumping to the implementation selected at init.
//
//go:noescape
func scanStringSpecialRuntime(src []byte, i int) int

//go:noescape
func scanStringSyntaxRuntime(src []byte, i int) int

//go:noescape
func scanEncodedHTMLSpecialRuntime(src []byte, i int) int

//go:noescape
func scanEncodedHTMLSyntaxRuntime(src []byte, i int) int
