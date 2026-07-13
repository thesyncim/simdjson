// Package simd exposes the allocation-free byte kernels used by simdjson.
//
// Builds using GOEXPERIMENT=simd select an architecture implementation once
// at package initialization. Other builds use byte-exact portable fallbacks.
package simd
