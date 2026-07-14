#!/bin/sh
# bench-gate.sh compares the working tree against a baseline ref with
# interleaved benchmark rounds, the zero-regression gate used before every
# performance-sensitive commit.
#
#   scripts/bench-gate.sh [-b ref] [-n rounds] [-t benchtime] [pattern]
#
# The default baseline is HEAD, rounds default to 8, benchtime to 250ms, and
# the pattern defaults to the corpus decode/validate/encode rows. Requires
# the pinned gotip and benchstat. Runs the tests/stdlib corpus benchmarks
# with GOEXPERIMENT=simd; results land under $TMPDIR/simdjson-bench-gate.
set -eu

gotip=${GOTIP:-/Users/thesyncim/go/bin/gotip}
benchstat=${BENCHSTAT:-/Users/thesyncim/go/bin/benchstat}
baseline=HEAD
rounds=8
benchtime=250ms
pattern='BenchmarkHighLevelCorpus/.*/(valid|decode-typed|decode-any|encode)/simdjson'

while getopts b:n:t: flag; do
	case $flag in
	b) baseline=$OPTARG ;;
	n) rounds=$OPTARG ;;
	t) benchtime=$OPTARG ;;
	*) exit 2 ;;
	esac
done
shift $((OPTIND - 1))
[ $# -ge 1 ] && pattern=$1

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
work=${TMPDIR:-/tmp}/simdjson-bench-gate
mkdir -p "$work"

echo "baseline: $(git -C "$root" rev-parse --short "$baseline")  rounds: $rounds  benchtime: $benchtime" >&2

git -C "$root" worktree add --force "$work/baseline" "$baseline" >/dev/null 2>&1 ||
	git -C "$work/baseline" checkout --force "$baseline" >/dev/null 2>&1

(cd "$root/tests/stdlib" && GOEXPERIMENT=simd "$gotip" test -c -o "$work/new.test" .)
(cd "$work/baseline/tests/stdlib" && GOEXPERIMENT=simd "$gotip" test -c -o "$work/old.test" .)

: >"$work/old.txt"
: >"$work/new.txt"
round=0
while [ "$round" -lt "$rounds" ]; do
	"$work/old.test" -test.run '^$' -test.bench "$pattern" -test.benchtime "$benchtime" >>"$work/old.txt" 2>&1
	"$work/new.test" -test.run '^$' -test.bench "$pattern" -test.benchtime "$benchtime" >>"$work/new.txt" 2>&1
	round=$((round + 1))
done

"$benchstat" "$work/old.txt" "$work/new.txt"
