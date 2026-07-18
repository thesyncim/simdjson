//go:build !go1.27 || go1.28 || !goexperiment.simd || !amd64

package simdjson

const (
	indexStringPrefixProbeEnabled          = false
	indexStringPrefixProbeMaxDocumentBytes = 0
	indexStringPrefixProbeBytes            = 0
)
