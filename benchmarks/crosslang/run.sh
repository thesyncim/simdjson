#!/bin/sh
# Cross-language corpus benchmark over the exact Go encoding/json test corpus.
# The parse+semantic-digest rows have an enforced equivalent contract; the DOM,
# stage-1, serialization, and Rust rows remain explicitly labelled diagnostics.
# Requires clang++, cargo, curl, zstd, and the pinned Go tip binary.
set -eu
DIR=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$DIR/../.." && pwd)
CORPUS=${CORPUS:-"$DIR/corpus"}
TIP_GO=${TIP_GO:-"$HOME/sdk/gotip/bin/go"}
GO_EXPERIMENT=${GO_EXPERIMENT:-simd}
CPP_SIMDJSON_VERSION=${CPP_SIMDJSON_VERSION:-4.6.4}

for tool in clang++ cargo curl zstd awk grep git; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "required tool is unavailable: $tool" >&2
    exit 1
  fi
done
if [ ! -x "$TIP_GO" ]; then
  echo "pinned Go toolchain is not executable: $TIP_GO (set TIP_GO to override)" >&2
  exit 1
fi

EMBED=${EMBED:-"$("$TIP_GO" env GOROOT)/src/encoding/json/internal/jsontest/_embed"}
if [ ! -d "$EMBED" ]; then
  echo "Go corpus directory is unavailable: $EMBED" >&2
  exit 1
fi

COMMIT=$(git -C "$ROOT" rev-parse HEAD)
DIRTY=$(git -C "$ROOT" status --porcelain --untracked-files=normal)
if [ -n "$DIRTY" ] && [ "${ALLOW_DIRTY:-0}" != 1 ]; then
  echo "refusing to publish from a dirty tree; commit the release candidate or set ALLOW_DIRTY=1 for a development run" >&2
  exit 1
fi
printf 'repository commit=%s dirty=%s\n' "$COMMIT" "$([ -n "$DIRTY" ] && printf true || printf false)"
"$TIP_GO" version
clang++ --version | sed -n '1p'

trap 'rm -f "$DIR/simdjson.h.download" "$DIR/simdjson.cpp.download" "$CORPUS"/*.download' EXIT HUP INT TERM

mkdir -p "$CORPUS"
FOUND_CORPUS=0
for f in "$EMBED"/*.json.zst; do
  [ -f "$f" ] || continue
  FOUND_CORPUS=1
  out="$CORPUS/$(basename "$f" .zst)"
  zstd -d -q -c "$f" > "$out.download"
  mv "$out.download" "$out"
done
if [ "$FOUND_CORPUS" -ne 1 ]; then
  echo "no compressed JSON corpus files found under $EMBED" >&2
  exit 1
fi

file_sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    echo "sha256sum or shasum is required to verify C++ sources" >&2
    return 1
  fi
}

simdjson_sources_valid() {
  [ -f "$DIR/simdjson.h" ] && [ -f "$DIR/simdjson.cpp" ] &&
    grep -q "#define SIMDJSON_VERSION \"$CPP_SIMDJSON_VERSION\"" "$DIR/simdjson.h" || return 1
  if [ "$CPP_SIMDJSON_VERSION" = 4.6.4 ]; then
    [ "$(file_sha256 "$DIR/simdjson.h")" = fec0cb4559e25456789efb153b7e5eaa27b50ce2bc2c9d3b531ea2000928380a ] &&
      [ "$(file_sha256 "$DIR/simdjson.cpp")" = 826660ed60cf467641ab2f93a0e1a2dacf603c444f76d835358c814bc2caba3a ] || return 1
  fi
  return 0
}

if ! simdjson_sources_valid; then
  curl -fsSL -o "$DIR/simdjson.h.download" \
    "https://github.com/simdjson/simdjson/releases/download/v$CPP_SIMDJSON_VERSION/simdjson.h"
  curl -fsSL -o "$DIR/simdjson.cpp.download" \
    "https://github.com/simdjson/simdjson/releases/download/v$CPP_SIMDJSON_VERSION/simdjson.cpp"
  mv "$DIR/simdjson.h.download" "$DIR/simdjson.h"
  mv "$DIR/simdjson.cpp.download" "$DIR/simdjson.cpp"
fi
if ! simdjson_sources_valid; then
  echo "downloaded simdjson sources failed version/checksum verification" >&2
  exit 1
fi

clang++ -O3 -DNDEBUG -march=native -std=c++20 -o "$DIR/bench_simdjson" "$DIR/bench_simdjson.cpp" "$DIR/simdjson.cpp"
clang++ -O3 -DNDEBUG -march=native -std=c++20 -o "$DIR/bench_stage1" "$DIR/bench_stage1.cpp" "$DIR/simdjson.cpp"
clang++ -O3 -DNDEBUG -march=native -std=c++20 -o "$DIR/bench_contract" "$DIR/bench_contract.cpp" "$DIR/simdjson.cpp"

"$DIR/bench_simdjson" "$CORPUS"
"$DIR/bench_stage1" "$CORPUS"

CPP_CONTRACT_OUT=$("$DIR/bench_contract" "$CORPUS")
GO_CONTRACT_OUT=$(
  cd "$DIR/.."
  GOTOOLCHAIN=local GOEXPERIMENT="$GO_EXPERIMENT" "$TIP_GO" run ./crosslang/go_contract "$CORPUS"
)
printf '%s\n' "$CPP_CONTRACT_OUT"
printf '%s\n' "$GO_CONTRACT_OUT"

contract_digests() {
  printf '%s\n' "$1" | awk '/contract=parse\+semantic-digest/ {
    for (i = 1; i <= NF; i++) if ($i ~ /^digest=/) print $1, $i
  }'
}
CPP_DIGESTS=$(contract_digests "$CPP_CONTRACT_OUT")
GO_DIGESTS=$(contract_digests "$GO_CONTRACT_OUT")
if [ "$CPP_DIGESTS" != "$GO_DIGESTS" ]; then
  echo "cross-language semantic digests differ" >&2
  printf 'C++:\n%s\nGo:\n%s\n' "$CPP_DIGESTS" "$GO_DIGESTS" >&2
  exit 1
fi
echo "cross-language semantic digests match"

RUSTFLAGS="-C target-cpu=native" cargo run --release --manifest-path "$DIR/rustbench/Cargo.toml" -- "$CORPUS"
