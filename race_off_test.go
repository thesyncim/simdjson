//go:build !race

package simdjson

// raceEnabled reports whether the test binary was built with -race. See
// race_on_test.go for why allocation assertions consult it.
const raceEnabled = false
