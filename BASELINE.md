# Maintenance baseline

This record fixes the starting point for the pre-v1 simplification work. Its
counts describe commit `d779a8165638da22d7c10b149e04ac637b9603cf`; later
changes should report deltas against that commit instead of silently moving the
baseline.

## Revisions and environment

| Component | Baseline |
| --- | --- |
| Repository | `d779a8165638da22d7c10b149e04ac637b9603cf` |
| Module declaration | `go 1.27` |
| Pinned Go source | `03845e30f7b73d1703bd8c21017297f6eecb76d6` |
| Pinned Go version | `go1.27-devel_03845e30 Fri Jul 10 12:31:49 2026 -0700 darwin/arm64` |
| Local baseline machine | Apple M4 Max, `darwin/arm64` |
| Published benchmark machine | Apple M4 Max, `darwin/arm64`, one CPU |

The compiler revision comes from `go_tip_commit` in
`scripts/bootstrap-gotip.sh`. Reproduce local commands with:

```sh
export GOTIP="$HOME/sdk/simdjson-gotip/bin/go"
GOTOOLCHAIN=local "$GOTIP" version
```

## Release blockers

- There is no root project `LICENSE`, `LICENSE.md`, or `COPYING` file.
  `LICENSE-GO` applies to the identified Go-derived files; it is not a license
  grant for the repository's original code.
- There is no root `NOTICE`. Add one only after the maintainer selects the
  project license and confirms the attribution format.
- The upstream relationship and known provenance are recorded in
  `docs/provenance.md`, but the attribution audit remains open until its
  unresolved rows are closed.
- `CODEOWNERS` names one account for all files, unsafe inventory changes, and
  SIMD. A second independent unsafe reviewer is not yet identified.
- Stable Go cannot parse the root module's `go 1.27` declaration. Portable
  stable-Go support therefore remains unproven, even though the default build
  selects portable kernels.
- The latest committed performance publication is ARM64-only. There is no
  dedicated AMD64 performance record.

Do not tag a release while any of these blockers remains open.

## Source and test size

Counts use tracked `*.go` files. Generated production files are included;
vendored corpus data is not.

| Area | Production files | Production lines | Test files | Test lines |
| --- | ---: | ---: | ---: | ---: |
| Root package | 68 | 21,853 | 92 | 27,740 |
| Public `simd` package | 38 | 7,602 | 10 | 2,356 |
| Internal tools | 7 | 1,479 | 1 | 146 |
| Internal support | 1 | 45 | 1 | 258 |
| Benchmark module | 5 | 903 | 14 | 4,903 |
| Standard-library corpus module | 3 | 587 | 2 | 417 |
| **Total** | **122** | **32,469** | **120** | **35,820** |

The root package therefore has more test lines than production lines before
test consolidation begins.

## Exported API inventory

`go doc -all` exposes 286 declaration heads in the root package and 81 in the
public `simd` package. Grouped constant blocks count as one declaration head in
this measurement.

| Package | Variables/constants | Functions and constructors | Types | Exported methods |
| --- | ---: | ---: | ---: | ---: |
| `simdjson` | 4 | 43 | 37 | 202 |
| `simdjson/simd` | 6 | 51 | 14 | 10 |

Root functions and constructors:

```text
AppendCanonicalize, AppendCompact, AppendIndent, Canonicalize, Compact,
DecodeFrom, DecodeNext, EachArray, EachArrayOptions, EachObject,
EachObjectOptions, EncodeTo, Indent, Marshal, RequiredIndexEntries, Unmarshal,
Valid, ValidNumber, ValidString, Validate, ValidateNumber, ValidateOptions,
ValidateString, CompileCodec, CompilePointer, MustCompilePointer,
CompileDecoder, CompileEncoder, MakeField, MakeFieldSet, BuildIndex,
BuildIndexOptions, GetRaw, GetRawOptions, ScanFirstRaw, ScanFirstRawOptions,
NewReader, NewReaderSize, NewReaderWithOptions, Parse, ParseOptions, NewWriter,
NewWriterSize
```

Root types:

```text
ArrayIter, Codec, CodecOptions, CompiledPointer, DecodeCursor, DecodeError,
Decoder, DecoderOptions, EncodeError, Encoder, EncoderOptions, Field,
FieldCursor, FieldSet, FlatArrayIter, FlatObjectIter, Index, IndexEntry,
IndexOptions, Kind, MarshalerSimd, Member, Node, ObjectIter, Options,
PointerError, RawValue, Reader, ReaderOptions, SyntaxError, TrustedAppender,
UnmarshalerSimd, UnsupportedTypeError, Value, ValueCursor, ValueFieldCursor,
Writer
```

Method counts by root receiver:

| Receiver | Methods | Receiver | Methods |
| --- | ---: | --- | ---: |
| `ArrayIter` | 8 | `Codec` | 8 |
| `CompiledPointer` | 5 | `DecodeCursor` | 26 |
| `DecodeError` | 1 | `Decoder` | 3 |
| `EncodeError` | 1 | `Encoder` | 1 |
| `Field` | 1 | `FieldCursor` | 1 |
| `FieldSet` | 3 | `FlatArrayIter` | 8 |
| `FlatObjectIter` | 6 | `Index` | 4 |
| `IndexEntry` | 2 | `Kind` | 1 |
| `Node` | 23 | `ObjectIter` | 6 |
| `PointerError` | 1 | `RawValue` | 19 |
| `Reader` | 7 | `SyntaxError` | 1 |
| `TrustedAppender` | 11 | `UnsupportedTypeError` | 1 |
| `Value` | 21 | `ValueCursor` | 14 |
| `ValueFieldCursor` | 1 | `Writer` | 18 |

The `simd` package has 51 exported functions, 14 exported types, two methods on
`CPUFeatures`, eight methods on `UncheckedScans`, five exported constant blocks,
and the exported `Unchecked` variable. Regenerate the declaration inventory
with:

```sh
GOTOOLCHAIN=local "$GOTIP" doc -all . |
  rg '^(const|var|type|func) [A-Z]|^func \([^)]*\) [A-Z]'
GOTOOLCHAIN=local "$GOTIP" doc -all ./simd |
  rg '^(const|var|type|func) [A-Z]|^func \([^)]*\) [A-Z]'
```

## Unsafe perimeter

The generated `UNSAFE.md` block contains **240 scopes** across **51 production
files that import `unsafe`**. The inventory tool itself is excluded from the
file count. The first-pass reduction target is at most 156 scopes, a 35%
decrease, while preserving typed Go pointers and all current safety gates.

Reproduce the scope count with:

```sh
awk '
  /BEGIN GENERATED UNSAFE SCOPES/ { inside=1; next }
  /END GENERATED UNSAFE SCOPES/ { inside=0 }
  inside && /^- `/ { count++ }
  END { print count }
' UNSAFE.md
```

## Fuzz targets and corpus

There are **34 fuzz targets**:

```text
FuzzAPIConsistency
FuzzCompiledNumericAcceptance
FuzzDecodeTrust
FuzzEncoderMatchesStdlib
FuzzEncoderScratchOperationSequence
FuzzEncoderScratchRetentionSequence
FuzzFieldSetLookupParity
FuzzFloatDecodeAllPaths
FuzzFloatDecodeFreeform
FuzzFloatDecodeMatchesStrconv
FuzzFloatExactness
FuzzFloatRoundTripMarshalDecode
FuzzHookIntegritySpan
FuzzHookMatchesReflection
FuzzHookPlanRecoverySequence
FuzzIndexStorageBoundaries
FuzzInlineRoundTrip
FuzzMergeSemanticsMatchStdlib
FuzzParse16Digits
FuzzPointerConsistency
FuzzReaderLifecycleOperations
FuzzSIMDScannersMatchScalar
FuzzScalarSliceDecodeMatchesStdlib
FuzzScalarValidators
FuzzStreamFramerAdversarial
FuzzStreamReaderChunkEquivalence
FuzzTransforms
FuzzTypedDecoderMatchesStdlib
FuzzTypedStructuralRouteParity
FuzzUnmarshalAny
FuzzValidBitmap
FuzzValidateConsistency
FuzzValueCursorDifferential
FuzzValueFrameSIMDMatchesScalar
```

The checked-in disk corpus has six files totaling 740 bytes:

| Target | Files | Bytes |
| --- | ---: | ---: |
| `FuzzAPIConsistency` | 1 | 45 |
| `FuzzDecodeTrust` | 3 | 565 |
| `FuzzStreamFramerAdversarial` | 1 | 89 |
| `FuzzTransforms` | 1 | 41 |
| All other targets | 0 | 0 |

Source-level `f.Add` seeds are not included in those disk-corpus counts. The
vendored JSONTestSuite corpus contains 318 parsing cases plus its license and
provenance files. Fuzzer consolidation must preserve both disk and source-level
seeds before removing a target.

## Correctness and safety status

Local uncached tests on the baseline passed:

| Check | Result |
| --- | --- |
| Pinned-tip portable `go test -count=1 ./...` | Pass; root package 19.906 s |
| Pinned-tip SIMD `go test -count=1 ./...` | Pass; root package 23.030 s |
| Generated unsafe inventory check | Pass |

The GitHub Actions run for the baseline commit recorded the architecture
matrix more completely:

- Native Linux ARM64 passed portable and SIMD tests, stdlib corpus, vet, race,
  checkptr, lifetime stress, cross-compilation, and its fuzz shard.
- Linux AMD64 passed portable, SIMD, shuffled, corpus, vet, staticcheck,
  vulnerability, generated-file, unsafe-inventory, module-tidy, race, checkptr,
  lifetime, and AMD64/ARM64/386/s390x build checks before its fuzz shard.
- The AMD64 job then failed `FuzzEncoderScratchRetentionSequence/seed#1` because
  a cold encoder pool retained 2,621,480 bytes of map-entry scratch against a
  524,288-byte budget. Follow-up commit `4374911` fixes that existing baseline
  failure and adds a deterministic cold-pool regression test.

The baseline CI evidence is run
[`29627030996`](https://github.com/thesyncim/simdjson/actions/runs/29627030996).

## Build size

A cold temporary `GOCACHE` on the local Apple M4 Max produced:

| Root test binary | Compile wall time | Bytes |
| --- | ---: | ---: |
| Portable | 3.76 s | 12,263,378 |
| SIMD | 3.67 s | 12,669,922 |

These numbers are local trend indicators, not cross-machine release gates.
Future comparisons must use the same compiler, machine, command, and cold-cache
setup.

## Performance baseline

The latest committed publication record is `benchmarks/results/latest.json`.
It measures repository commit `b05b7ce145bb9a3c53301beb2619241180c786ce`, not
the later documentation/chart commit used for this maintenance baseline. It is
the starting performance record until a clean candidate is republished.

| Operation | Versus `encoding/json` | Versus fastest compatible rival | SIMD / portable |
| --- | ---: | ---: | ---: |
| Strict validation | 3.094x | 2.785x | 1.804x |
| Typed owned decode | 4.169x | 1.808x | 1.124x |
| Dynamic owned decode | 3.653x | 1.878x | 1.068x |
| Owned encode | 2.553x | 1.458x | 1.318x |
| Compiled encode reuse | 4.634x | 2.647x | 1.497x |
| Parse and complete walk | 6.293x | not applicable | 1.230x |

The publication uses seven corpus payloads, six approximately 300 ms samples,
the median, an Apple M4 Max, and one ARM64 CPU. No AMD64 performance result is
published at the baseline.

## Required comparison rules

- Behavioral work compares against the pinned commit with differential tests.
- Hot-path work uses `scripts/bench-gate.sh` with interleaved rounds on the same
  compiler and machine.
- A maintained benchmark may not regress by more than 3%; `B/op` and
  `allocs/op` may not regress.
- Public behavior changes require an ADR and external-package tests.
- Deleted fuzz targets must have every useful disk and source seed migrated.
- Counts in this file remain fixed; progress is reported as a delta from this
  baseline.
