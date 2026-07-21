#!/usr/bin/env bash
# run-pg-contains.sh verifies the curated containment oracle against a real
# PostgreSQL server: the ADR 0002 phase-4 containment evaluator
# (RawContains) claims PostgreSQL's documented @> semantics, and this
# script is the claim's teeth.
#
# For every row of testdata/contains_oracle.tsv it evaluates
#
#   SELECT haystack::jsonb @> needle::jsonb
#
# over one psql round trip per row (psql variable interpolation, so
# arbitrary document bytes need no shell quoting discipline) and records
# the server's verbatim answer into results/pg/contains-oracle.log. Rows
# marked pg=y must match the table's want column and any disagreement
# fails the run; rows marked pg=s document the exact-decimal extension —
# the server is expected to error on the cast, and the error text is
# recorded as the row's result.
#
# The raw log is the artifact; this script does no arithmetic.
#
# Usage:
#   ./run-pg-contains.sh
#
# Environment:
#   PG_IMAGE   docker image (default postgres:18.4-alpine, the phase-0 pin;
#              the full server version is recorded in the log)
#   RESULTS    output directory (default results/pg next to this script)
#   KEEP       set to 1 to leave the container running afterwards
set -euo pipefail
cd "$(dirname "$0")"

PG_IMAGE=${PG_IMAGE:-postgres:18.4-alpine}
RESULTS=${RESULTS:-../results/pg}
CONTAINER=simdjson-pgcontains
TABLE=../../testdata/contains_oracle.tsv
LOG="$RESULTS/contains-oracle.log"

[ -f "$TABLE" ] || { echo "oracle table not found: $TABLE" >&2; exit 2; }
mkdir -p "$RESULTS"

if docker inspect "$CONTAINER" >/dev/null 2>&1; then
    docker rm -f "$CONTAINER" >/dev/null
fi
docker run -d --rm --name "$CONTAINER" \
    -e POSTGRES_HOST_AUTH_METHOD=trust \
    -e POSTGRES_INITDB_ARGS="--encoding=UTF8" \
    "$PG_IMAGE" >/dev/null
trap '[ "${KEEP:-0}" = 1 ] || docker rm -f "$CONTAINER" >/dev/null 2>&1 || true' EXIT
for _ in $(seq 60); do
    docker exec "$CONTAINER" pg_isready -U postgres -q && break
    sleep 1
done
docker exec "$CONTAINER" pg_isready -U postgres -q || {
    echo "postgres did not become ready" >&2
    exit 1
}

# ask <haystack> <needle>: the server's verbatim verdict (t/f) or its
# first error line. The query arrives on stdin because psql interpolates
# variables there but not in -c commands, and :'h'/:'n' interpolation is
# what makes arbitrary document bytes safe without quoting discipline.
# psql exits nonzero on the expected extension-row errors, so the
# pipeline's status is deliberately discarded.
ask() {
    echo "SELECT :'h'::jsonb @> :'n'::jsonb;" |
        docker exec -i "$CONTAINER" psql -U postgres -X -q -t -A \
            -v h="$1" -v n="$2" 2>&1 | head -1 || true
}

: >"$LOG"
{
    echo "IMAGE $PG_IMAGE"
    echo "VERSION $(docker exec "$CONTAINER" psql -U postgres -X -q -t -A -c 'SELECT version();')"
    echo "TABLE testdata/contains_oracle.tsv"
} >>"$LOG"

rows=0 pass=0 fail=0 documented=0
while IFS=$'\t' read -r name hay nee want pg; do
    case "$name" in ''|'#'*) continue ;; esac
    rows=$((rows + 1))
    result=$(ask "$hay" "$nee")
    verdict=FAIL
    if [ "$pg" = s ]; then
        # The extension rows: PostgreSQL is expected to reject the cast.
        case "$result" in
            ERROR*) verdict=DOCUMENTED; documented=$((documented + 1)) ;;
            "$want") verdict=PASS; pass=$((pass + 1)) ;;
        esac
    elif [ "$result" = "$want" ]; then
        verdict=PASS
        pass=$((pass + 1))
    fi
    if [ "$verdict" = FAIL ]; then
        fail=$((fail + 1))
    fi
    {
        echo "ROW $name want=$want pg=$pg"
        echo "HAYSTACK $hay"
        echo "NEEDLE $nee"
        echo "RESULT $result"
        echo "VERDICT $verdict"
    } >>"$LOG"
done <"$TABLE"

echo "SUMMARY rows=$rows pass=$pass documented=$documented fail=$fail" >>"$LOG"
echo "rows=$rows pass=$pass documented=$documented fail=$fail (log: $LOG)"
[ "$fail" -eq 0 ]
