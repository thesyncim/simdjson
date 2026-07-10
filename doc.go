// Package simdjson implements strict JSON validation, compiled typed decoding,
// caller-backed structural indexes, source selectors, and JSON transforms.
//
// The current module requires a Go 1.27 development toolchain for generic
// methods. It uses Go's experimental simd/archsimd package for hot string and
// number scanning when built with GOEXPERIMENT=simd on arm64 or amd64.
package simdjson
