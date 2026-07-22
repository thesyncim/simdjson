#!/bin/sh
# Prove that pinned-SIMD checksum builds retain carry-less multiplication,
# introduce no heap calls, and keep the capability wrapper free of SIMD work.
set -eu

go_bin=${1:-go}
work=$(mktemp -d "${TMPDIR:-/tmp}/simdjson-storeio-checksum-isa.XXXXXX")
trap 'rm -rf "$work"' EXIT HUP INT TERM
package_path=$(GOTOOLCHAIN=local "$go_bin" list -f '{{.ImportPath}}' ./internal/storeio)
package_pattern=$(printf '%s\n' "$package_path" | sed 's/\./\\./g')

check_target() {
	arch=$1
	body=$2
	instruction=$3
	binary="$work/storeio-$arch.test"
	body_assembly="$work/storeio-$arch-body.asm"
	dispatch_assembly="$work/storeio-$arch-dispatch.asm"

	GOOS=linux GOARCH=$arch GOEXPERIMENT=simd GOTOOLCHAIN=local \
		"$go_bin" test -c ./internal/storeio -o "$binary"
	"$go_bin" tool objdump -s "^${package_pattern}\.${body}$" \
		"$binary" >"$body_assembly"
	if ! grep -Eq "[[:space:]]${instruction}[[:space:]]" "$body_assembly"; then
		echo "$arch checksum body did not retain $instruction" >&2
		exit 1
	fi
	if grep -Eq 'runtime\.(newobject|mallocgc)' "$body_assembly"; then
		echo "$arch checksum body contains a heap-allocation call" >&2
		exit 1
	fi

	"$go_bin" tool objdump -s "^${package_pattern}\.pageChecksum$" \
		"$binary" >"$dispatch_assembly"
	if grep -Eq '[[:space:]](VPCLMULQDQ|VPMULL2?)[[:space:]]' "$dispatch_assembly"; then
		echo "$arch checksum capability wrapper contains SIMD work" >&2
		exit 1
	fi
}

check_target amd64 pageChecksumAVX512 VPCLMULQDQ
check_target arm64 pageChecksumPMULL9 VPMULL
