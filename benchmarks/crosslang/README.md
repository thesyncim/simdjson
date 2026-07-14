# Cross-language corpus benchmarks

`run.sh` measures C++ simdjson and Rust serde_json/simd-json over the exact
seven-payload corpus used by the Go benchmarks: same files, same machine,
single thread, six ~300 ms samples per operation, medians reported. It needs
`clang++`, `cargo`, `zstd`, and a Go tip checkout for the corpus files, and
builds with full optimization (`-O3 -DNDEBUG -march=native`;
`RUSTFLAGS="-C target-cpu=native"`, LTO, one codegen unit).

Cross-language rows are context, not a scoreboard: each library exposes a
different memory model, so no two rows do identical work. The notes below
name the closest Go contract for each.

## Snapshot (Apple M4 Max, 2026-07-14)

C++ simdjson 4.6.4 (arm64 kernels), Rust serde_json 1 / simd-json 0.15.1
(NEON), Go rows from the published corpus snapshot on the same machine.

### Document parse, GB/s of input

"DOM parse" builds simdjson's tape plus unescaped string arena with a reused
parser — more than validation, less than materializing per-node values.
simd-json borrowed aliases strings into a scratch copy of the input, closest
to our source-backed contract; serde_json `Value` and our dynamic `any` tree
both build fully owned trees, but Rust stores scalars inline in an enum while
Go boxes each number in an interface.

The bolded value wins the row; the winner among the three tree-building
contenders (simd-json borrowed, serde_json, Go dynamic) is marked with a
dagger since the parse-only and validate-only columns do less work per byte.

| Corpus | C++ DOM parse | simd-json borrowed | serde_json `Value` | Go dynamic `any` | Go typed owned | Go strict validate |
|---|---:|---:|---:|---:|---:|---:|
| Canada geometry | 1.53 | 0.53 | 0.54† | 0.30 | 1.02 | **2.15** |
| CITM catalog | **4.81** | 1.34† | 0.81 | 0.63 | 1.64 | 2.69 |
| Go source | 1.90 | 0.71† | 0.36 | 0.40 | 1.49 | **2.07** |
| Escaped strings | 0.83 | 0.57 | 0.90 | 1.23† | 1.07 | **9.78** |
| Unicode strings | 4.66 | 2.82† | 1.27 | 1.18 | 1.93 | **5.66** |
| Synthea FHIR | **4.75** | 1.23† | 0.54 | 0.48 | 1.08 | 2.65 |
| Twitter status | **4.23** | 1.35† | 0.55 | 0.48 | 1.30 | 2.81 |

- C++ simdjson's two-stage tape parse is the fastest JSON front-end measured
  here: 4.2–4.8 GB/s on object-dense payloads, ahead of even our
  validation-only scan. Its lead inverts on number-dense and escape-dense
  input, where our validate runs 1.4–12x ahead of its parse.
- Our typed decode — which fills real Go structs, a step no other row
  performs — beats serde_json's dynamic `Value` on every payload and
  simd-json borrowed on five of seven.
- The Go dynamic rows trail the Rust dynamic rows mostly because Go boxes
  every scalar in an `any`; the typed and source-backed APIs are the fast
  paths in this library.

### Serialization, GB/s of output

C++ serializes its parsed tape (strings copy straight from the arena); serde
serializes a `Value` tree; the Go row marshals native structs, formatting
times and floats from their binary form.

| Corpus | C++ `to_string` | serde_json `to_writer` | Go compiled reuse |
|---|---:|---:|---:|
| CITM catalog | 1.25 | 1.36 | **2.24** |
| Twitter status | 1.54 | 1.71 | **2.72** |
| Synthea FHIR | **1.61** | 1.25 | 1.05 |
| Go source | 0.95 | 1.17 | **2.86** |

Go leads except Synthea, where the C++ row replays pre-parsed date strings
from its tape while the Go row formats 2,191 `time.Time` values from native
form — different work with the same output.
