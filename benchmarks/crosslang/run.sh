#!/bin/sh
# Cross-language corpus benchmark: C++ simdjson and Rust serde_json/simd-json
# over the repository's exact Go encoding/json test corpus.
set -eu

dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
root=$(CDPATH= cd -- "$dir/../.." && pwd)
corpus=${CORPUS:-"$dir/corpus"}
compressed=${COMPRESSED_CORPUS:-"$root/tests/stdlib/testdata"}
build=${BUILD_DIR:-"$dir/.build"}
cache=${CACHE_DIR:-"${XDG_CACHE_HOME:-$HOME/.cache}/simdjson-crosslang"}
cpp_tag=v4.6.4
cpp_commit=1bcf71bd85059ab6574ea1159de9298dcc1212c5
cpp_src="$cache/simdjson-$cpp_tag"

mkdir -p "$corpus" "$build" "$cache"
for f in "$compressed"/*.json.zst; do
	out="$corpus/$(basename "$f" .zst)"
	zstd -d -q -f "$f" -o "$out"
done

if [ ! -d "$cpp_src/.git" ]; then
	git clone --depth 1 --branch "$cpp_tag" https://github.com/simdjson/simdjson.git "$cpp_src"
fi
actual_commit=$(git -C "$cpp_src" rev-parse HEAD)
if [ "$actual_commit" != "$cpp_commit" ]; then
	echo "C++ simdjson checkout is $actual_commit, want $cpp_commit" >&2
	exit 1
fi

cppflags="-O3 -DNDEBUG -march=native -std=c++20"
clang++ $cppflags -I"$cpp_src/singleheader" -o "$build/bench_simdjson" \
	"$dir/bench_simdjson.cpp" "$cpp_src/singleheader/simdjson.cpp"
clang++ $cppflags -I"$cpp_src/singleheader" -o "$build/bench_stage1" \
	"$dir/bench_stage1.cpp" "$cpp_src/singleheader/simdjson.cpp"

"$build/bench_simdjson" "$corpus"
"$build/bench_stage1" "$corpus"

RUSTFLAGS="-C target-cpu=native" cargo run --release --locked \
	--manifest-path "$dir/rustbench/Cargo.toml" -- "$corpus"
