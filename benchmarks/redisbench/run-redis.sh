#!/usr/bin/env bash
# run-redis.sh executes the RedisJSON/RediSearch half of the ADR 0003
# comparison. It drives a pinned dockerized redis-stack server (RedisJSON +
# RediSearch) over a single connection — Redis executes commands on one thread,
# so a single instance is the fair single-core comparison — and records, into
# results/redis/<corpus>.log, one self-describing fact per line:
#
#   - server version and loaded modules,
#   - the redis-cli --pipe mass JSON.SET load wall time and reply count,
#   - keyspace memory before and after load, and after FT.CREATE (used_memory),
#   - FT.CREATE ON JSON index build wall time and FT.INFO num_docs,
#   - per-scenario server-side execution time, read from the SLOWLOG so the
#     figure excludes client round-trip and process overhead: point projection
#     (JSON.GET), filtered scan (FT.SEARCH on a TAG), scalar aggregate
#     (FT.AGGREGATE REDUCE SUM), and grouped aggregate (FT.AGGREGATE GROUPBY),
#   - the value each scenario command returned, for the cross-check.
#
# Containment (@>) has no RedisJSON or RediSearch operator; it is a capability
# reported only on our side. The logs are parsed by "redisbench report"; this
# script does no ratio arithmetic.
#
# Usage:
#   ./run-redis.sh corpora/synth_s4 [corpora/... ...]
#
# Environment:
#   REDIS_IMAGE  docker image (default redis/redis-stack-server:7.4.0-v3; the
#                full server version and module versions land in every log)
#   REPS         repetitions per timed scenario (default 5; the parser discards
#                the first as warm-up and takes the minimum of the rest)
#   RESULTS      output directory (default results/redis next to this script)
#   KEEP         set to 1 to leave the container running afterwards
set -euo pipefail

# Probe docker before doing anything: if it is unavailable this is a
# protocol-only run and the report stays honest about the missing numbers.
if ! docker info >/dev/null 2>&1; then
    echo "docker is not available: this harness needs a running docker daemon" >&2
    echo "to produce real RedisJSON/RediSearch numbers. The protocol above is" >&2
    echo "runnable as-is once docker is up; until then 'redisbench report' emits" >&2
    echo "a protocol-only scoreboard with the Redis columns marked n/a." >&2
    exit 1
fi

if ! command -v python3 >/dev/null 2>&1; then
    echo "python3 is required for nanosecond wall-clock timing" >&2
    exit 1
fi

args=(); for a in "$@"; do args+=("$(cd "$a" && pwd)"); done; set -- "${args[@]}"
cd "$(dirname "$0")"

REDIS_IMAGE=${REDIS_IMAGE:-redis/redis-stack-server:7.4.0-v3}
REPS=${REPS:-5}
RESULTS=${RESULTS:-../results/redis}
CONTAINER=simdjson-redisbench

[ $# -ge 1 ] || { echo "usage: $0 corpus-dir..." >&2; exit 2; }
mkdir -p "$RESULTS"

now_ns() { python3 -c 'import time;print(time.time_ns())'; }
rcli() { docker exec -i "$CONTAINER" redis-cli "$@"; }

start_container() {
    if docker inspect "$CONTAINER" >/dev/null 2>&1; then
        docker rm -f "$CONTAINER" >/dev/null
    fi
    docker run -d --rm --name "$CONTAINER" "$REDIS_IMAGE" >/dev/null
    for _ in $(seq 60); do
        rcli PING 2>/dev/null | grep -q PONG && break
        sleep 0.5
    done
    rcli PING 2>/dev/null | grep -q PONG || { echo "redis did not become ready" >&2; exit 1; }
    # Log every command's server-side time, and lift the aggregate result cap
    # so a full GROUP BY cardinality is countable.
    rcli CONFIG SET slowlog-log-slower-than 0 >/dev/null
    rcli CONFIG SET slowlog-max-len 128 >/dev/null
    rcli FT.CONFIG SET MAXAGGREGATERESULTS 100000000 >/dev/null 2>&1 || true
}

used_memory() { rcli INFO memory | awk -F: '/^used_memory:/{gsub(/\r/,"",$2);print $2;exit}'; }
ft_field()   { rcli FT.INFO idx | awk -v k="$1" '$0==k{getline;gsub(/\r/,"");print;exit}'; }

# slow_ns runs the given command once and returns its server-side execution
# time in nanoseconds, read from the SLOWLOG within the same connection so the
# handshake cannot pollute the reading and the client round trip is excluded.
# The trailing EVAL returns only the newest entry's microsecond duration, so
# tail -1 is the number regardless of the command's own reply length.
slow_ns() {
    local usec
    usec=$(rcli <<EOF | tail -1
SLOWLOG RESET
$*
EVAL "return redis.call('SLOWLOG','GET',1)[1][3]" 0
EOF
)
    echo $(( ${usec:-0} * 1000 ))
}

run_corpus() {
    local dir=$1
    # manifest.env is written by "redisbench gen": NAME, DOCS, KEY_PREFIX,
    # EXTRACT_FIELD, CONTAIN_KEY, CONTAIN_VALUE, SUM_FIELD (all splice-safe by
    # generator contract, so the schema paths and TAG values below are safe).
    # shellcheck disable=SC1091
    source "$dir/manifest.env"
    local log="$RESULTS/$NAME.log"
    : >"$log"

    echo "== $NAME: loading $(du -h "$dir/docs.resp" | cut -f1) =="
    rcli FLUSHALL >/dev/null

    {
        echo "IMAGE $REDIS_IMAGE"
        echo "VERSION $(rcli INFO server | awk -F: '/^redis_version:/{gsub(/\r/,"",$2);print $2;exit}')"
        rcli MODULE LIST | awk '$0=="name"{getline n;next} $0=="ver"{getline v;print "MODULE",n,v}'
        echo "DOCS $DOCS"
    } >>"$log"

    local mem_base t0 t1 replies
    mem_base=$(used_memory)
    echo "USED_MEMORY_BASE $mem_base" >>"$log"

    # Mass load: redis-cli --pipe streams the RESP JSON.SET commands and
    # acknowledges every reply. The wall time is the ingest cost.
    t0=$(now_ns)
    replies=$(docker exec -i "$CONTAINER" redis-cli --pipe <"$dir/docs.resp" 2>&1 | awk '/replies:/{for(i=1;i<=NF;i++)if($i=="replies:")print $(i+1)+0}')
    t1=$(now_ns)
    {
        echo "LOAD_NS $((t1 - t0))"
        echo "LOAD_REPLIES ${replies:-0}"
        echo "USED_MEMORY $(used_memory)"
    } >>"$log"

    # Build the RediSearch index. Only pre-declared fields are queryable — the
    # schema is mandatory, and every scenario field must be named here.
    local schema=("\$.$EXTRACT_FIELD" AS proj TEXT)
    [ -n "${CONTAIN_KEY:-}" ] && schema+=("\$.$CONTAIN_KEY" AS filt TAG)
    [ -n "${SUM_FIELD:-}" ] && schema+=("\$.$SUM_FIELD" AS agg NUMERIC)

    t0=$(now_ns)
    rcli FT.CREATE idx ON JSON PREFIX 1 "$KEY_PREFIX" SCHEMA "${schema[@]}" >/dev/null
    local pct
    for _ in $(seq 600); do
        pct=$(ft_field percent_indexed)
        [ "$pct" = "1" ] && break
        sleep 0.1
    done
    t1=$(now_ns)
    {
        echo "USED_MEMORY_INDEXED $(used_memory)"
        echo "INDEX_NS $((t1 - t0))"
        echo "INDEX_NUM_DOCS $(ft_field num_docs)"
    } >>"$log"

    # Point projection: JSON.GET on keys spread across the corpus, resolving the
    # projection path — the same mostly-miss point read our single-doc probe
    # times. One SLOWLOG-timed sample per repetition.
    local stride=$(( DOCS / REPS )); [ "$stride" -lt 1 ] && stride=1
    local j
    for ((j = 0; j < REPS; j++)); do
        local key="${KEY_PREFIX}$(( (j * stride) % DOCS ))"
        echo "SAMPLE projection $(slow_ns JSON.GET "$key" "\$.$EXTRACT_FIELD")" >>"$log"
    done

    if [ -n "${CONTAIN_KEY:-}" ]; then
        # Filtered scan: FT.SEARCH on the TAG, count only.
        local filter=(FT.SEARCH idx "@filt:{$CONTAIN_VALUE}" LIMIT 0 0)
        for ((j = 0; j < REPS; j++)); do
            echo "SAMPLE filter $(slow_ns "${filter[@]}")" >>"$log"
        done
        echo "RESULT filter $(rcli "${filter[@]}" | head -1 | tr -d '\r')" >>"$log"

        # Grouped aggregate: FT.AGGREGATE GROUPBY the TAG, count per group; the
        # first reply element is the group cardinality (NULL group included).
        local group=(FT.AGGREGATE idx '*' GROUPBY 1 '@filt' REDUCE COUNT 0 AS c LIMIT 0 100000000)
        for ((j = 0; j < REPS; j++)); do
            echo "SAMPLE groupby $(slow_ns "${group[@]}")" >>"$log"
        done
        echo "RESULT groupby $(rcli "${group[@]}" | head -1 | tr -d '\r')" >>"$log"
    fi

    if [ -n "${SUM_FIELD:-}" ]; then
        # Scalar aggregate: FT.AGGREGATE REDUCE SUM over the numeric field.
        local sum=(FT.AGGREGATE idx '*' GROUPBY 0 REDUCE SUM 1 '@agg' AS total)
        for ((j = 0; j < REPS; j++)); do
            echo "SAMPLE sum $(slow_ns "${sum[@]}")" >>"$log"
        done
        # The reply is [1, ["total", "<sum>"]]; the sum is the last value.
        # Normalize any scientific-notation double to an integer for the check.
        local raw
        raw=$(rcli "${sum[@]}" | tail -1 | tr -d '\r')
        echo "RESULT sum $(printf '%.0f' "${raw:-0}")" >>"$log"
    fi

    rcli FT.DROPINDEX idx >/dev/null 2>&1 || true
    echo "== $NAME: done -> $log =="
}

start_container
trap '[ "${KEEP:-0}" = 1 ] || docker rm -f "$CONTAINER" >/dev/null 2>&1 || true' EXIT
rcli INFO server | awk -F: '/^redis_version:/{gsub(/\r/,"",$2);print "redis " $2}'
for dir in "$@"; do
    run_corpus "$dir"
done
