#!/bin/sh
# Prove that pinned-SIMD amd64 bitmap builds runtime-gate AVX2 for GOAMD64
# v1/v2 and compile a direct AVX2 path for v3.
set -eu

go_bin=${1:-go}
work=$(mktemp -d "${TMPDIR:-/tmp}/simdjson-bitset-isa.XXXXXX")
trap 'rm -rf "$work"' EXIT HUP INT TERM
package_path=$(GOTOOLCHAIN=local "$go_bin" list -f '{{.ImportPath}}' ./internal/bitset)
package_pattern=$(printf '%s\n' "$package_path" | sed 's/\./\\./g')

for level in v1 v2 v3; do
	files=$(
		GOOS=linux GOARCH=amd64 GOAMD64=$level GOEXPERIMENT=simd GOTOOLCHAIN=local \
			"$go_bin" list -f '{{range .GoFiles}}{{println .}}{{end}}' ./internal/bitset
	)
	if ! printf '%s\n' "$files" | grep -qx 'ops_simd.go'; then
		echo "GOAMD64=$level did not select the AVX2 bitmap bodies" >&2
		exit 1
	fi
	case $level in
	v1 | v2)
		if ! printf '%s\n' "$files" | grep -qx 'ops_dispatch_amd64.go'; then
			echo "GOAMD64=$level did not select runtime bitmap dispatch" >&2
			exit 1
		fi
		;;
	v3)
		if ! printf '%s\n' "$files" | grep -qx 'ops_dispatch_v3_amd64.go'; then
			echo 'GOAMD64=v3 did not select direct bitmap dispatch' >&2
			exit 1
		fi
		;;
	esac

	binary="$work/bitset-$level.test"
	GOOS=linux GOARCH=amd64 GOAMD64=$level GOEXPERIMENT=simd GOTOOLCHAIN=local \
		"$go_bin" test -c ./internal/bitset -o "$binary"
	vector_assembly="$work/bitset-$level-vector.asm"
	"$go_bin" tool objdump -s "^${package_pattern}\\.(andWordsAVX2|and3WordsAVX2|orWordsAVX2|andNotWordsAVX2)$" \
		"$binary" >"$vector_assembly"
	for instruction in VPAND VPOR VPANDN VZEROUPPER; do
		if ! grep -Eq "[[:space:]]${instruction}[[:space:]]" "$vector_assembly"; then
			echo "GOAMD64=$level bitmap bodies did not retain $instruction" >&2
			exit 1
		fi
	done

	if test "$level" != v3; then
		dispatch_assembly="$work/bitset-$level-dispatch.asm"
		"$go_bin" tool objdump -s "^${package_pattern}\\.andWords$" "$binary" >"$dispatch_assembly"
		if grep -Eq '[[:space:]]V[A-Z0-9]+[[:space:]]' "$dispatch_assembly"; then
			echo "GOAMD64=$level emitted AVX before bitmap capability dispatch" >&2
			exit 1
		fi
		for target in bitsetAVX2Available andWordsAVX2 andWordsScalar; do
			if ! grep -q "$target" "$dispatch_assembly"; then
				echo "GOAMD64=$level bitmap dispatch omitted $target" >&2
				exit 1
			fi
		done
	fi
done
