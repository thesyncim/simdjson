# Toolchain policy

The root module requires Go 1.27 because its SIMD implementation uses the
experimental `simd/archsimd` API. Go 1.27 is not a stable release yet.

## Supported compiler

The authoritative compiler revision is `go_tip_commit` in
[`scripts/bootstrap-gotip.sh`](../scripts/bootstrap-gotip.sh). CI and published
benchmarks build that exact revision. A newer Go tip revision may work, but is
best effort until it passes the same test and benchmark gates and the pin is
advanced.

The default build omits `GOEXPERIMENT=simd` and uses portable Go kernels. The
SIMD build sets `GOEXPERIMENT=simd`; accelerated kernels are maintained for
amd64 and arm64. CI also cross-compiles portable 386 and s390x builds to cover
32-bit and big-endian assumptions.

| Configuration | Support |
| --- | --- |
| Pinned Go tip, portable build | Required |
| Pinned Go tip, `GOEXPERIMENT=simd`, amd64 or arm64 | Required |
| Newer Go tip | Best effort until pinned |
| Stable Go release before 1.27 | Unsupported |

## Advancing the pin

An update to the compiler revision is a compatibility change. The same commit
must:

1. update `go_tip_commit` and any required bootstrap version;
2. regenerate checked-in output and tidy module files;
3. pass generic and SIMD tests, race detection, checkptr, vet, static analysis,
   fuzz smoke, corpus parity, and cross-compilation;
4. compare the maintained high-level benchmarks against the previous compiler
   revision on the dedicated runner; and
5. record any compiler or `archsimd` behavior change that required source
   changes.

A release is cut only from a revision green under the pinned compiler. Stable
Go support will be declared explicitly when the required compiler and SIMD APIs
are available in a stable release; it is not inferred from a moving tip build
happening to compile.
