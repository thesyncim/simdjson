#!/usr/bin/env bash
# Run the DuckDB half of the embedded JSON-store comparison.
#
# Usage:
#   ./duckdbbench/run-duckdb.sh corpora/synth_s4 [corpora/...]
#
# Environment:
#   DUCKDB_IMAGE  pinned official image (default below)
#   RESULTS       output root (default benchmarks/results/duckdb)
#   REPS          warmed read repetitions (default 7)
#
# Each corpus produces facts.log plus untouched *.profiles.jsons streams from
# DuckDB's JSON profiler. The report parser, not this shell script, performs
# timing arithmetic and correctness comparisons.
set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "$0")" && pwd)
DUCKDB_IMAGE=${DUCKDB_IMAGE:-duckdb/duckdb:1.5.4@sha256:d5e66353428256453574ddfd4ee446ef37510e61619bb5a8f63b988165bd70b8}
RESULTS=${RESULTS:-$SCRIPT_DIR/../results/duckdb}
REPS=${REPS:-7}

if (($# == 0)); then
	echo "usage: $0 corpus-dir [corpus-dir ...]" >&2
	exit 2
fi
if ! [[ $REPS =~ ^[1-9][0-9]*$ ]]; then
	echo "REPS must be a positive integer" >&2
	exit 2
fi
if ! docker info >/dev/null 2>&1; then
	echo "Docker is required to run the pinned DuckDB image" >&2
	exit 1
fi

mkdir -p "$RESULTS"
RESULTS=$(cd -- "$RESULTS" && pwd)

for corpus_arg in "$@"; do
	corpus=$(cd -- "$corpus_arg" && pwd)
	# manifest.env is generated from a deliberately narrow alphanumeric
	# alphabet. shellcheck disable=SC1090
	source "$corpus/manifest.env"
	if ! [[ $NAME =~ ^[A-Za-z0-9_]+$ && $DOCS =~ ^[1-9][0-9]*$ ]]; then
		echo "$corpus: unsafe or incomplete manifest.env" >&2
		exit 1
	fi
	for value in "$EXTRACT_FIELD" "$CONTAIN_KEY" "$CONTAIN_VALUE" "$SUM_FIELD"; do
		if ! [[ $value =~ ^[A-Za-z0-9_]*$ ]]; then
			echo "$corpus: unsafe generated SQL value $value" >&2
			exit 1
		fi
	done
	if ! [[ $SOURCE_BYTES =~ ^[1-9][0-9]*$ && $KEY_BYTES =~ ^[1-9][0-9]*$ && $NDJSON_SHA256 =~ ^[0-9a-f]{64}$ ]]; then
		echo "$corpus: invalid byte accounting or digest" >&2
		exit 1
	fi
	if command -v sha256sum >/dev/null 2>&1; then
		actual_sha256=$(sha256sum "$corpus/docs.ndjson" | awk '{print $1}')
	else
		actual_sha256=$(shasum -a 256 "$corpus/docs.ndjson" | awk '{print $1}')
	fi
	if [[ $actual_sha256 != "$NDJSON_SHA256" ]]; then
		echo "$corpus: docs.ndjson digest does not match manifest" >&2
		exit 1
	fi

	out="$RESULTS/$NAME"
	rm -rf -- "$out"
	mkdir -p "$out"
	db="$out/store.duckdb"

	duckdb() {
		docker run --rm -i --cpus=1 \
			-v "$corpus:/corpus:ro" -v "$out:/out" \
			"$DUCKDB_IMAGE" duckdb -init /dev/null -bail "$@"
	}

	filter_expr="NULL::VARCHAR"
	metric_expr="NULL::BIGINT"
	if [[ -n $CONTAIN_KEY ]]; then
		filter_expr="json_extract_string(json, '\$.${CONTAIN_KEY}')"
	fi
	if [[ -n $SUM_FIELD ]]; then
		metric_expr="try_cast(json_extract_string(json, '\$.${SUM_FIELD}') AS BIGINT)"
	fi

	cat >"$out/load.sql" <<SQL
SET threads=1;
SET preserve_insertion_order=true;
SET profiling_coverage='ALL';
SET enable_profiling='json';
CREATE TABLE docs AS
SELECT
    row_number() OVER () - 1 AS id,
    'doc:' || cast(row_number() OVER () - 1 AS VARCHAR) AS key,
    json AS doc,
    $filter_expr AS filter_value,
    $metric_expr AS metric
FROM read_ndjson_objects('/corpus/docs.ndjson');
SQL
	duckdb /out/store.duckdb <"$out/load.sql" >/dev/null 2>"$out/load.profiles.jsons"

	cat >"$out/key-index.sql" <<'SQL'
SET threads=1;
SET profiling_coverage='ALL';
SET enable_profiling='json';
CREATE UNIQUE INDEX docs_key ON docs(key);
SQL
	duckdb /out/store.duckdb <"$out/key-index.sql" >/dev/null 2>"$out/key_index.profiles.jsons"
	if [[ -n $CONTAIN_KEY ]]; then
		cat >"$out/filter-index.sql" <<'SQL'
SET threads=1;
SET profiling_coverage='ALL';
SET enable_profiling='json';
CREATE INDEX docs_filter ON docs(filter_value);
SQL
		duckdb /out/store.duckdb <"$out/filter-index.sql" >/dev/null 2>"$out/filter_index.profiles.jsons"
	fi
	duckdb /out/store.duckdb "SET threads=1; CHECKPOINT;" >/dev/null

	profile_select() {
		local label=$1 query=$2
		local sql="$out/$label.sql"
		{
			echo "SET threads=1;"
			# Warm both data and the compiled execution path before profiling.
			echo "$query"
			echo "$query"
			echo "SET enable_profiling='json';"
			for ((rep = 0; rep < REPS; rep++)); do
				echo "$query"
			done
		} >"$sql"
		duckdb /out/store.duckdb <"$sql" >/dev/null 2>"$out/$label.profiles.jsons"
	}

	point_query="SELECT json_extract_string(doc, '\$.${EXTRACT_FIELD}') FROM docs WHERE key='doc:0';"
	profile_select point "$point_query"
	if [[ -n $CONTAIN_KEY ]]; then
		profile_select filter "SELECT count(*) FROM docs WHERE filter_value='${CONTAIN_VALUE}';"
		profile_select group "SELECT count(*) FROM (SELECT filter_value FROM docs GROUP BY filter_value);"
		profile_select contain "SELECT count(*) FROM docs WHERE json_contains(doc, '{\"${CONTAIN_KEY}\":\"${CONTAIN_VALUE}\"}'::JSON);"
	fi
	if [[ -n $SUM_FIELD ]]; then
		profile_select sum "SELECT coalesce(sum(metric), 0) FROM docs;"
	fi

	verify_query="SELECT count(*), count(*) FILTER (WHERE json_exists(doc, '\$.${EXTRACT_FIELD}'))"
	if [[ -n $CONTAIN_KEY ]]; then
		verify_query+=", count(*) FILTER (WHERE filter_value='${CONTAIN_VALUE}'), count(DISTINCT filter_value) + cast(count(*) FILTER (WHERE filter_value IS NULL) > 0 AS BIGINT), count(*) FILTER (WHERE json_contains(doc, '{\"${CONTAIN_KEY}\":\"${CONTAIN_VALUE}\"}'::JSON))"
	else
		verify_query+=", 0, 0, 0"
	fi
	if [[ -n $SUM_FIELD ]]; then
		verify_query+=", coalesce(sum(metric), 0)"
	else
		verify_query+=", 0"
	fi
	verify_query+=" FROM docs;"
	verify=$(duckdb -csv -noheader /out/store.duckdb "$verify_query")
	IFS=, read -r result_docs result_extract result_filter result_group result_contain result_sum <<<"$verify"

	version=$(duckdb -csv -noheader /out/store.duckdb "SELECT library_version FROM pragma_version();")
	platform=$(docker version --format '{{.Server.Os}}/{{.Server.Arch}}')
	database_bytes=$(wc -c <"$db" | tr -d ' ')
	wal_bytes=0
	if [[ -f $db.wal ]]; then
		wal_bytes=$(wc -c <"$db.wal" | tr -d ' ')
	fi

	# Measure current engine-managed memory in one process after touching the
	# representative warm working set. This is closer to resident state than a
	# checkpointed file size, but remains a DuckDB buffer-manager counter rather
	# than process RSS.
	cat >"$out/memory.sql" <<SQL
SET threads=1;
SELECT json_extract_string(doc, '\$.${EXTRACT_FIELD}') FROM docs WHERE key='doc:0';
SQL
	if [[ -n $CONTAIN_KEY ]]; then
		cat >>"$out/memory.sql" <<SQL
SELECT count(*) FROM docs WHERE filter_value='${CONTAIN_VALUE}';
SELECT count(*) FROM (SELECT filter_value FROM docs GROUP BY filter_value);
SELECT count(*) FROM docs WHERE json_contains(doc, '{"${CONTAIN_KEY}":"${CONTAIN_VALUE}"}'::JSON);
SQL
	fi
	if [[ -n $SUM_FIELD ]]; then
		echo 'SELECT coalesce(sum(metric), 0) FROM docs;' >>"$out/memory.sql"
	fi
	echo "SELECT coalesce(sum(memory_usage_bytes), 0), coalesce(sum(memory_usage_bytes) FILTER (tag='ART_INDEX'), 0), coalesce(sum(temporary_storage_bytes), 0) FROM duckdb_memory();" >>"$out/memory.sql"
	memory_line=$(duckdb -csv -noheader /out/store.duckdb <"$out/memory.sql" | tail -n 1)
	IFS=, read -r current_buffer_bytes current_art_bytes current_temp_bytes <<<"$memory_line"
	for value in "$current_buffer_bytes" "$current_art_bytes" "$current_temp_bytes"; do
		if ! [[ $value =~ ^[0-9]+$ ]]; then
			echo "$corpus: invalid duckdb_memory() result $memory_line" >&2
			exit 1
		fi
	done
	mutation_ops=$DOCS
	if ((mutation_ops > 256)); then mutation_ops=256; fi

	cat >"$out/update.sql" <<'SQL'
SET threads=1;
SET profiling_coverage='ALL';
SET enable_profiling='json';
SQL
	for ((i = 0; i < mutation_ops; i++)); do
		printf "BEGIN; UPDATE docs SET doc='{\"bench_mutation\":true}'::JSON, filter_value=NULL, metric=NULL WHERE key='doc:%d'; COMMIT;\n" "$i" >>"$out/update.sql"
	done
	duckdb /out/store.duckdb <"$out/update.sql" >/dev/null 2>"$out/update.profiles.jsons"

	cat >"$out/delete.sql" <<'SQL'
SET threads=1;
SET profiling_coverage='ALL';
SET enable_profiling='json';
SQL
	for ((i = 0; i < mutation_ops; i++)); do
		printf "BEGIN; DELETE FROM docs WHERE key='doc:%d'; COMMIT;\n" "$i" >>"$out/delete.sql"
	done
	duckdb /out/store.duckdb <"$out/delete.sql" >/dev/null 2>"$out/delete.profiles.jsons"
	after_deletes=$(duckdb -csv -noheader /out/store.duckdb "SELECT count(*) FROM docs;")
	database_bytes_after_mutations=$(wc -c <"$db" | tr -d ' ')
	wal_after_mutations=0
	if [[ -f $db.wal ]]; then
		wal_after_mutations=$(wc -c <"$db.wal" | tr -d ' ')
	fi

	cat >"$out/facts.log" <<FACTS
IMAGE $DUCKDB_IMAGE
VERSION $version
PLATFORM $platform
CORPUS_SHA256 $actual_sha256
THREADS 1
DOCS $DOCS
SOURCE_BYTES $SOURCE_BYTES
KEY_BYTES $KEY_BYTES
DATABASE_BYTES $database_bytes
WAL_BYTES $wal_bytes
CURRENT_BUFFER_BYTES $current_buffer_bytes
CURRENT_ART_BYTES $current_art_bytes
CURRENT_TEMP_BYTES $current_temp_bytes
MUTATION_OPS $mutation_ops
DATABASE_BYTES_AFTER_MUTATIONS $database_bytes_after_mutations
WAL_BYTES_AFTER_MUTATIONS $wal_after_mutations
RESULT docs $result_docs
RESULT extract_hits $result_extract
RESULT filter $result_filter
RESULT group $result_group
RESULT contain $result_contain
RESULT sum $result_sum
RESULT after_deletes $after_deletes
FACTS

	rm -f -- "$out"/*.sql
	echo "$NAME: $DOCS rows, DuckDB $version, database $database_bytes bytes"
done
