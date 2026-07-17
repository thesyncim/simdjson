#!/bin/sh
# bench-gate.sh compares the working tree against a baseline ref with
# interleaved benchmark rounds, the zero-regression gate used before every
# performance-sensitive commit.
#
#   scripts/bench-gate.sh [-b ref] [-d directory] [-n rounds]
#                         [-t benchtime] [-r max-regression-percent] [pattern]
#
# The default baseline is HEAD, rounds default to 8, benchtime to 250ms, and
# the pattern defaults to the corpus decode/validate/encode rows. Requires
# the pinned gotip and benchstat. Runs the tests/stdlib corpus benchmarks
# with GOEXPERIMENT=simd; results land under $TMPDIR/simdjson-bench-gate.
set -eu

gotip=${GOTIP:-"$HOME/sdk/simdjson-gotip/bin/go"}
benchstat=${BENCHSTAT:-"$HOME/go/bin/benchstat"}
baseline=HEAD
benchdir=tests/stdlib
rounds=8
benchtime=250ms
regression_limit=${BENCH_REGRESSION_LIMIT:-2}
pattern='BenchmarkHighLevelCorpus/.*/(valid|index|decode-typed|decode-any|encode-typed)/simdjson'

while getopts b:d:n:t:r: flag; do
	case $flag in
	b) baseline=$OPTARG ;;
	d) benchdir=$OPTARG ;;
	n) rounds=$OPTARG ;;
	t) benchtime=$OPTARG ;;
	r) regression_limit=$OPTARG ;;
	*) exit 2 ;;
	esac
done
shift $((OPTIND - 1))
[ $# -ge 1 ] && pattern=$1

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
repo_id=$(printf '%s' "$root" | cksum | cut -d ' ' -f 1)
work=${TMPDIR:-/tmp}/simdjson-bench-gate-$repo_id
mkdir -p "$work"
baseline_commit=$(git -C "$root" rev-parse "$baseline^{commit}")

if [ ! -x "$gotip" ]; then
	echo "pinned Go toolchain is not executable: $gotip (set GOTIP to override)" >&2
	exit 1
fi
if [ ! -x "$benchstat" ]; then
	echo "benchstat is not executable: $benchstat (set BENCHSTAT to override)" >&2
	exit 1
fi

echo "baseline: $(git -C "$root" rev-parse --short "$baseline_commit")  rounds: $rounds  benchtime: $benchtime" >&2
echo "benchdir: $benchdir  max significant sec/op regression: $regression_limit%" >&2

git -C "$root" worktree add --force --detach "$work/baseline" "$baseline_commit" >/dev/null 2>&1 ||
	git -C "$work/baseline" checkout --force "$baseline_commit" >/dev/null 2>&1

(cd "$root/$benchdir" && GOEXPERIMENT=simd "$gotip" test -c -o "$work/new.test" .)
(cd "$work/baseline/$benchdir" && GOEXPERIMENT=simd "$gotip" test -c -o "$work/old.test" .)

: >"$work/old.txt"
: >"$work/new.txt"
round=0
while [ "$round" -lt "$rounds" ]; do
	"$work/old.test" -test.run '^$' -test.bench "$pattern" -test.benchtime "$benchtime" >>"$work/old.txt" 2>&1
	"$work/new.test" -test.run '^$' -test.bench "$pattern" -test.benchtime "$benchtime" >>"$work/new.txt" 2>&1
	round=$((round + 1))
done

"$benchstat" "$work/old.txt" "$work/new.txt"
BENCHSTAT="$benchstat" "$root/scripts/check-benchstat.sh" \
	"$work/old.txt" "$work/new.txt" "$regression_limit"
