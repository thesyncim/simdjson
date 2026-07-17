#!/bin/sh
# Fail on statistically significant benchmark regressions. benchstat emits a
# numeric "vs base" only after its significance test passes; "~" is noise.
set -eu

if [ "$#" -lt 2 ] || [ "$#" -gt 3 ]; then
	echo "usage: check-benchstat.sh old.txt new.txt [max-sec-regression-percent]" >&2
	exit 2
fi

old=$1
new=$2
limit=${3:-${BENCH_REGRESSION_LIMIT:-2}}
benchstat=${BENCHSTAT:-benchstat}

case $limit in
'' | *[!0-9.]* | *.*.*)
	echo "invalid regression limit: $limit" >&2
	exit 2
	;;
esac
if [ ! -x "$benchstat" ] && ! command -v "$benchstat" >/dev/null 2>&1; then
	echo "benchstat is not executable: $benchstat" >&2
	exit 1
fi

csv=$(mktemp "${TMPDIR:-/tmp}/simdjson-benchstat.XXXXXX")
trap 'rm -f "$csv"' EXIT HUP INT TERM
"$benchstat" -format csv "$old" "$new" >"$csv"

awk -F, -v sec_limit="$limit" '
$2 == "sec/op" || $2 == "B/op" || $2 == "allocs/op" {
	metric = $2
	next
}
$1 == "" { next }
$1 == "geomean" { next }
metric != "" && $6 ~ /^\+/ {
	change = $6
	sub(/^\+/, "", change)
	sub(/%$/, "", change)
	threshold = metric == "sec/op" ? sec_limit : 0
	if (change == "Inf" || change + 0 > threshold) {
		printf "regression: %s %s increased %s (limit %.2f%%; %s)\n", $1, metric, $6, threshold, $7 > "/dev/stderr"
		failed = 1
	}
}
END { exit failed }
' "$csv"
