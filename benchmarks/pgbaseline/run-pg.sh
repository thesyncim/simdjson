#!/usr/bin/env bash
# run-pg.sh executes the PostgreSQL half of the ADR 0002 phase-0 baseline.
#
# For each corpus directory (produced by "pgbaseline gen") it loads the
# documents into a single-jsonb-column table over a single connection and
# records, into results/pg/<corpus>.log:
#
#   - server version and every non-default setting (SHOW output),
#   - COPY wall time (server-side file read, psql \timing),
#   - table sizes after VACUUM ANALYZE (pg_table_size, pg_total_relation_size),
#   - CREATE INDEX wall time and pg_relation_size for gin jsonb_ops with
#     fastupdate=on and fastupdate=off, and for gin jsonb_path_ops,
#   - point extraction (doc->>'field'): full-scan aggregate and single-row
#     by ctid, unindexed,
#   - existence (doc ? 'key') and containment (doc @> '{"k":"v"}'), each
#     without any index and with the applicable GIN variants,
#   - EXPLAIN (ANALYZE, BUFFERS) for one repetition of every query form.
#
# The logs are parsed by "pgbaseline report". Raw logs are the artifact;
# this script deliberately does no arithmetic.
#
# Usage:
#   ./run-pg.sh corpora/synth_s4 [corpora/... ...]
#
# Environment:
#   PG_IMAGE   docker image (default postgres:18.4-alpine; the major version
#              is the pin, the full tag is recorded in every log)
#   REPS       repetitions per timed query (default 3; first is discarded as
#              warm-up by the parser, which takes the minimum of the rest)
#   RESULTS    output directory (default results/pg next to this script)
#   KEEP       set to 1 to leave the container running afterwards
set -euo pipefail

cd "$(dirname "$0")"

PG_IMAGE=${PG_IMAGE:-postgres:18.4-alpine}
REPS=${REPS:-3}
RESULTS=${RESULTS:-results/pg}
CONTAINER=simdjson-pgbaseline

[ $# -ge 1 ] || { echo "usage: $0 corpus-dir..." >&2; exit 2; }
mkdir -p "$RESULTS"

# Single backend, no parallelism: the comparison rule is one core on each
# side. Everything passed with -c here is a deliberate non-default and is
# re-recorded from the server with SHOW below.
SETTINGS=(
    shared_buffers=1GB
    maintenance_work_mem=1GB
    max_parallel_workers_per_gather=0
    max_parallel_maintenance_workers=0
    max_wal_size=4GB
    autovacuum=off
)

start_container() {
    if docker inspect "$CONTAINER" >/dev/null 2>&1; then
        docker rm -f "$CONTAINER" >/dev/null
    fi
    local args=()
    for s in "${SETTINGS[@]}"; do args+=(-c "$s"); done
    docker run -d --rm --name "$CONTAINER" \
        -e POSTGRES_HOST_AUTH_METHOD=trust \
        "$PG_IMAGE" "${args[@]}" >/dev/null
    for _ in $(seq 60); do
        docker exec "$CONTAINER" pg_isready -U postgres -q && return 0
        sleep 1
    done
    echo "postgres did not become ready" >&2
    exit 1
}

psql_file() { # psql_file <sql-file> <log-file>
    docker exec -i "$CONTAINER" psql -U postgres -X -q \
        -v ON_ERROR_STOP=1 <"$1" >>"$2" 2>&1
}

# emit_reps <file> <label> <sql>: one warm-up-inclusive timed block. The
# first repetition doubles as the EXPLAIN capture.
emit_reps() {
    local f=$1 label=$2 sql=$3
    printf '\\echo STEP explain_%s\n' "$label" >>"$f"
    printf 'EXPLAIN (ANALYZE, BUFFERS) %s;\n' "$sql" >>"$f"
    for _ in $(seq "$REPS"); do
        printf '\\echo STEP %s\n' "$label" >>"$f"
        printf '%s;\n' "$sql" >>"$f"
    done
}

run_corpus() {
    local dir=$1
    # manifest.env is written by "pgbaseline gen": NAME, DOCS, EXTRACT_FIELD,
    # EXIST_KEY, CONTAIN_KEY, CONTAIN_VALUE (values are alphanumeric by
    # generator contract, so literal splicing below is safe).
    # shellcheck disable=SC1091
    source "$dir/manifest.env"
    local log="$RESULTS/$NAME.log" sql
    sql=$(mktemp)
    : >"$log"

    echo "== $NAME: loading $(du -h "$dir/docs.pgcopy" | cut -f1) =="
    docker cp "$dir/docs.pgcopy" "$CONTAINER:/tmp/docs.pgcopy"

    {
        printf '\\timing on\n'
        printf '\\echo STEP version\n'
        printf 'SELECT version();\n'
        for s in "${SETTINGS[@]}"; do
            printf '\\echo STEP setting_%s\n' "${s%%=*}"
            printf 'SHOW %s;\n' "${s%%=*}"
        done
        printf 'DROP TABLE IF EXISTS t;\n'
        # Single jsonb column, default fillfactor (100, recorded in the
        # methodology doc rather than varied).
        printf 'CREATE TABLE t (doc jsonb);\n'
        printf '\\echo STEP copy\n'
        printf "COPY t(doc) FROM '/tmp/docs.pgcopy';\n"
        printf '\\echo STEP vacuum\n'
        printf 'VACUUM ANALYZE t;\n'
        printf '\\echo STEP rowcount\n'
        printf 'SELECT count(*) FROM t;\n'
        printf '\\echo STEP size_table\n'
        printf "SELECT pg_table_size('t');\n"
        printf '\\echo STEP size_total_unindexed\n'
        printf "SELECT pg_total_relation_size('t');\n"
    } >"$sql"

    # Unindexed query costs. count() keeps the client transfer out of the
    # measurement; the per-row cost is Time/DOCS.
    emit_reps "$sql" q_extract_seq \
        "SELECT count(doc->>'$EXTRACT_FIELD') FROM t"
    emit_reps "$sql" q_extract_ctid \
        "SELECT doc->>'$EXTRACT_FIELD' FROM t WHERE ctid = '(0,1)'"
    emit_reps "$sql" q_exist_seq \
        "SELECT count(*) FROM t WHERE doc ? '$EXIST_KEY'"
    emit_reps "$sql" q_contain_seq \
        "SELECT count(*) FROM t WHERE doc @> '{\"$CONTAIN_KEY\": \"$CONTAIN_VALUE\"}'"

    {
        # gin jsonb_ops, fastupdate on and off as separate build/size rows.
        printf '\\echo STEP create_gin_ops_fastupdate_on\n'
        printf 'CREATE INDEX idx_ops ON t USING gin (doc) WITH (fastupdate = on);\n'
        printf '\\echo STEP size_gin_ops\n'
        printf "SELECT pg_relation_size('idx_ops');\n"
    } >>"$sql"

    # Indexed existence and containment; jsonb_ops supports both ? and @>.
    # The planner is left free: if it prefers a seq scan that is the honest
    # PostgreSQL number, and the EXPLAIN capture shows which plan ran.
    emit_reps "$sql" q_exist_gin_ops \
        "SELECT count(*) FROM t WHERE doc ? '$EXIST_KEY'"
    emit_reps "$sql" q_contain_gin_ops \
        "SELECT count(*) FROM t WHERE doc @> '{\"$CONTAIN_KEY\": \"$CONTAIN_VALUE\"}'"

    {
        printf 'DROP INDEX idx_ops;\n'
        printf '\\echo STEP create_gin_ops_fastupdate_off\n'
        printf 'CREATE INDEX idx_ops_nofu ON t USING gin (doc) WITH (fastupdate = off);\n'
        printf '\\echo STEP size_gin_ops_fastupdate_off\n'
        printf "SELECT pg_relation_size('idx_ops_nofu');\n"
        printf 'DROP INDEX idx_ops_nofu;\n'
        # gin jsonb_path_ops: @> only (no ? support), smaller by design.
        printf '\\echo STEP create_gin_path_ops\n'
        printf 'CREATE INDEX idx_path ON t USING gin (doc jsonb_path_ops);\n'
        printf '\\echo STEP size_gin_path_ops\n'
        printf "SELECT pg_relation_size('idx_path');\n"
    } >>"$sql"

    emit_reps "$sql" q_contain_gin_path_ops \
        "SELECT count(*) FROM t WHERE doc @> '{\"$CONTAIN_KEY\": \"$CONTAIN_VALUE\"}'"

    {
        printf '\\echo STEP size_total_with_path_ops\n'
        printf "SELECT pg_total_relation_size('t');\n"
        printf 'DROP TABLE t;\n'
    } >>"$sql"

    psql_file "$sql" "$log"
    rm -f "$sql"
    docker exec "$CONTAINER" rm -f /tmp/docs.pgcopy
    echo "== $NAME: done -> $log =="
}

start_container
docker exec "$CONTAINER" psql -U postgres -X -q -c 'SELECT version();'
for dir in "$@"; do
    run_corpus "$dir"
done
if [ "${KEEP:-0}" != 1 ]; then
    docker rm -f "$CONTAINER" >/dev/null
fi
