#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
: "${TIP_GO:?set TIP_GO to the pinned Go binary; see benchmarks/README.md}"
tip_go=$TIP_GO
legacy_go=${LEGACY_GO:-go}
legacy_toolchain=${LEGACY_GOTOOLCHAIN:-go1.26.4}
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
	GOTOOLCHAIN="$legacy_toolchain" "$legacy_go" test -run='^$' -bench="$bench" -benchmem -benchtime="$benchtime" -count="$count"
)
