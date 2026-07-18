# Project identity and provenance

This repository is an independent Go implementation. It is not the C++
[`simdjson`](https://github.com/simdjson/simdjson) repository, and no affiliation
with that project is claimed. Where this implementation uses an upstream
algorithm, test vector, corpus, or source-derived file, the relationship should
be explicit below and in the affected file.

This document is an engineering inventory, not a substitute for a root project
license or a legal `NOTICE` file.

## Release status

The repository does not currently contain a root project license. `LICENSE-GO`
contains the Go Authors' BSD license for identified Go-derived files only. The
maintainer must select the project license before a root `LICENSE` and final
`NOTICE` can be prepared.

## Known imported or derived material

| Material | Local location | Upstream record | Current attribution | Release action |
| --- | --- | --- | --- | --- |
| Go float scaling and formatting | `number_uscale.go`, `simd/float.go`, `simd/float_pow10.go` | Go source; file headers identify the Go Authors | Copyright headers and `LICENSE-GO` | Confirm exact upstream revisions in the final notice |
| Go civil-date conversion | `simd/time_date.go` | Go `time` implementation and Neri-Schneider algorithm | Copyright header and `LICENSE-GO` | Confirm exact upstream revision in the final notice |
| Go JSON corpus and models | `tests/stdlib/testdata`, `tests/stdlib/models.go`, `benchmarks/legacy/stdlib_models_test.go` | Go commit `d468ad3648be469ffc4090e4586c29709182d6b6`; paths recorded in `tests/stdlib/testdata/UPSTREAM.md` | Go copyright headers and `tests/stdlib/testdata/LICENSE` | Preserve license and provenance when regenerating |
| JSONTestSuite parsing corpus | `testdata/corpora/JSONTestSuite` | `nst/JSONTestSuite` commit `1ef36fa01286573e846ac449e8683f8833c5b26a` | MIT license and `UPSTREAM.md` beside the corpus | Preserve both files with all corpus migrations |
| C++ simdjson UTF-8 test cases | `validation_corpus_test.go` | `tests/unicode_tests.cpp` at commit `9b33047a878264250c5361f865d0b2da86217d14` | Source and commit in the test comment | Verify the required notice text before release |
| C++ simdjson stage-1 techniques | `simd/stage1_portable.go`, `benchmarks/stage1_gap_bench_test.go` | Backslash-run algorithm and `bit_indexer::write`; the benchmark names its upstream path | Source named in comments | Record exact source revisions and verify notice requirements |
| C++ simdjson benchmark control | `benchmarks/crosslang` | simdjson 4.6.4, commit `1bcf71bd85059ab6574ea1159de9298dcc1212c5` | Version and commit are enforced by `run.sh`; source is downloaded, not vendored | Keep dependency pin and include its license in published artifacts if required |
| Eisel-Lemire number parsing | `float_eisel.go`, `float_eisel_table_gen.go`, generated table | Daniel Lemire, “Number Parsing at a Gigabyte per Second” (2021) | Paper and algorithm named in source comments | Decide whether the final notice should include a bibliographic citation |

## Dependency manifests

The root module has no third-party module requirements. Comparison and tooling
dependencies live in the nested `benchmarks`, `benchmarks/legacy`, and
`tests/stdlib` modules. Their `go.mod` and `go.sum` files are the authoritative
version inventory; those dependencies are not copied into the root package.

The cross-language benchmark downloads pinned C++ source into a user cache and
uses Cargo's locked dependency graph for its Rust diagnostic. Neither cache is
part of the repository.

## Audit procedure

Before adding or updating imported material:

1. Record repository, exact revision, source path, local changes, and license.
2. Keep the upstream license beside vendored corpora or source where practical.
3. Put copyright and derivation comments in copied or mechanically derived
   source files.
4. Update this inventory and the final `NOTICE` in the same change.
5. Run `scripts/check-stdlib-corpus.sh` for the Go corpus and the normal
   generated-file checks for generated tables.

Before release, close every “verify” or “confirm” action in the table, add the
maintainer-selected root project license, and generate a root `NOTICE` that is
consistent with that license.
