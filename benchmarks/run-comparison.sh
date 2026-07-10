#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
tip_go=${TIP_GO:-/Users/thesyncim/sdk/gotip/bin/go}
legacy_go=${LEGACY_GO:-/Users/thesyncim/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.4.darwin-arm64/bin/go}
bench=${BENCH:-.}
benchtime=${BENCHTIME:-1s}
count=${COUNT:-5}
jsonv2_bench=${JSONV2_BENCH:-^BenchmarkParseTypedJSONV2}

printf '\nGo tip pure Go benchmarks\n'
(
	cd "$root"
	"$tip_go" test -run='^$' -bench="$bench" -benchmem -benchtime="$benchtime" -count="$count"
)

printf '\nGo tip SIMD benchmarks\n'
(
	cd "$root"
	GOEXPERIMENT=simd "$tip_go" test -run='^$' -bench="$bench" -benchmem -benchtime="$benchtime" -count="$count"
)

printf '\nGo tip encoding/json/v2 benchmarks\n'
(
	cd "$root"
	GOEXPERIMENT=simd,jsonv2 "$tip_go" test -run='^$' -bench="$jsonv2_bench" -benchmem -benchtime="$benchtime" -count="$count"
)

printf '\nGo 1.26 native compatibility benchmarks\n'
(
	cd "$root/legacy"
	GOTOOLCHAIN=local "$legacy_go" test -run='^$' -bench="$bench" -benchmem -benchtime="$benchtime" -count="$count"
)
