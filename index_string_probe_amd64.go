//go:build go1.27 && !go1.28 && goexperiment.simd && amd64

package simdjson

// Small index builds contain enough short strings that one direct scalar
// prefix avoids repeated SIMD setup. Long strings resume the selected scanner
// from the end of the prefix, so already-scanned bytes are never revisited.
const (
	indexStringPrefixProbeEnabled          = true
	indexStringPrefixProbeMaxDocumentBytes = 1024
	indexStringPrefixProbeBytes            = 16
)
