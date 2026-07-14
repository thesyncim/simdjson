#!/bin/sh
# Cross-language corpus benchmark: C++ simdjson and Rust serde_json/simd-json
# over the exact Go encoding/json test corpus. Requires clang++, cargo, and a
# Go tip checkout for the corpus files.
set -e
DIR=$(cd "$(dirname "$0")" && pwd)
CORPUS=${CORPUS:-"$DIR/corpus"}
EMBED=${EMBED:-"$(go env GOROOT)/src/encoding/json/internal/jsontest/_embed"}

mkdir -p "$CORPUS"
for f in "$EMBED"/*.json.zst; do
  out="$CORPUS/$(basename "$f" .zst)"
  [ -f "$out" ] || zstd -d -c "$f" > "$out"
done

if [ ! -f "$DIR/simdjson.h" ]; then
  curl -sL -o "$DIR/simdjson.h" https://github.com/simdjson/simdjson/releases/latest/download/simdjson.h
  curl -sL -o "$DIR/simdjson.cpp" https://github.com/simdjson/simdjson/releases/latest/download/simdjson.cpp
fi
clang++ -O3 -DNDEBUG -march=native -std=c++20 -o "$DIR/bench_simdjson" "$DIR/bench_simdjson.cpp" "$DIR/simdjson.cpp"
"$DIR/bench_simdjson" "$CORPUS"

RUSTFLAGS="-C target-cpu=native" cargo run --release --manifest-path "$DIR/rustbench/Cargo.toml" -- "$CORPUS"
