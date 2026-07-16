//go:build race || simdjson_safehooks

package simdjson

// This file holds the memory-safe boxing fallback, selected by building with
// -race or with the simdjson_safehooks tag. Every boxer is an ordinary
// conversion: the compiler and runtime own the interface construction, so
// this build cannot produce a malformed interface value no matter what the
// slab machinery in any_box_fast.go assumes. The corruption stress tests
// compare the fast build's trees against trees built by these conversions.

// anyBoxLayoutOK is false here so tests can see the slab boxers are disabled.
const anyBoxLayoutOK = false

func (b *anyBoxer) float(f float64) any {
	return f
}

func (b *anyBoxer) str(v string) any {
	return v
}

func (b *anyBoxer) slice(v []any) any {
	return v
}
