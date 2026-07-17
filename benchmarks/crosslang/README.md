# Cross-language corpus benchmarks

`run.sh` measures C++ simdjson, this Go implementation, Rust serde_json, and
Rust simd-json over the exact seven-payload corpus used by the Go benchmarks:
same files, same machine, single thread, six ~300 ms samples per operation,
medians reported. It needs `clang++`, `cargo`, `zstd`, and the pinned Go tip
binary. C++ simdjson defaults to the explicit `4.6.4` release rather than a
moving `latest` download. Override it with `CPP_SIMDJSON_VERSION` only when the
reported benchmark metadata also records that change.

## Enforced equivalent contract

`parse+semantic-digest` is the only direct cross-language performance
comparison emitted by the runner. Every timed iteration does all of the
following:

1. parses the complete, already-loaded source with reusable output storage;
2. visits every array element and object member in source order;
3. decodes every object key and string value;
4. decodes every number to the same signed, unsigned, binary64, or big-integer
   category; and
5. hashes the full semantic event stream into the specified 64-bit FNV-1a
   digest.

The untimed reference digest is printed on every row. `run.sh` compares the
C++ and Go digests for all seven payloads and fails before publishing results
if any pair differs. File I/O, initial capacity discovery, allocation of the
reusable parser/index storage, and the reference digest are outside the timed
region. Parsing, grammar validation, string unescaping, number conversion,
complete traversal, and digest construction are inside it.

The C++ DOM parse/serialization, internal stage-1, Rust borrowed tree, Rust
owned tree, Go typed decode, Go dynamic decode, validation, and serialization
rows remain useful diagnostics, but they expose different representations or
perform different work. They must not be presented as head-to-head results.
The notes below describe those deliberately non-equivalent contracts.

## Historical diagnostics (Apple M4 Max, 2026-07-14)

C++ simdjson 4.6.4 (arm64 kernels), Rust serde_json 1 / simd-json 0.15.1
(NEON), Go rows from the published corpus snapshot on the same machine. This
predates the enforced semantic-digest contract and is retained only as a
per-operation baseline. Columns must be read independently, not ranked.

### Document parse, GB/s of input

"DOM parse" builds simdjson's tape plus unescaped string arena with a reused
parser — more than validation, less than materializing per-node values.
simd-json borrowed aliases strings into a scratch copy of the input, closest
to our source-backed contract; serde_json `Value` and our dynamic `any` tree
both build fully owned trees, but Rust stores scalars inline in an enum while
Go boxes each number in an interface.

| Corpus | C++ DOM parse | simd-json borrowed | serde_json `Value` | Go dynamic `any` | Go typed owned | Go strict validate |
|---|---:|---:|---:|---:|---:|---:|
| Canada geometry | 1.60 | 0.56 | 0.56 | 0.39 | 1.57 | 2.14 |
| CITM catalog | 4.94 | 1.41 | 0.86 | 1.02 | 2.77 | 2.83 |
| Go source | 1.96 | 0.75 | 0.39 | 0.59 | 1.80 | 2.07 |
| Escaped strings | 0.88 | 0.60 | 0.93 | 1.61 | 1.49 | 9.74 |
| Unicode strings | 4.84 | 2.93 | 1.33 | 2.08 | 3.22 | 5.63 |
| Synthea FHIR | 5.00 | 1.28 | 0.58 | 0.80 | 1.54 | 3.14 |
| Twitter status | 4.37 | 1.42 | 0.57 | 0.72 | 1.80 | 2.78 |

These rates expose workload sensitivity within each operation: escape-heavy
input is cheap for strict validation but expensive when strings must be
decoded, while object-dense input favors front ends with compact parser-owned
representations. The semantic-digest contract is required for implementation
comparisons.

### Serialization, GB/s of output

C++ serializes its parsed tape (strings copy straight from the arena); serde
serializes a `Value` tree; the Go row marshals native structs, formatting
times and floats from their binary form.

| Corpus | C++ `to_string` | serde_json `to_writer` | Go compiled reuse |
|---|---:|---:|---:|
| CITM catalog | 1.31 | 1.52 | 2.53 |
| Twitter status | 1.58 | 1.77 | 2.84 |
| Synthea FHIR | 1.65 | 1.42 | 1.11 |
| Go source | 0.98 | 1.23 | 2.96 |

The Synthea row makes the contract difference concrete: C++ replays pre-parsed
date strings from its tape while Go formats 2,191 `time.Time` values from
native form. Similar representation differences apply to every row in this
table, so the rates are diagnostic rather than comparative.
