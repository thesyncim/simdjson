# Safety-first roadmap

The default build is the only supported safety mode. Safety is not controlled
by build tags or runtime options. Unsafe remains acceptable only for bounded,
tested internals whose ownership and lifetime stay visible to the Go runtime.

## Practical priority order

1. Freeze new performance work for several days around the release candidate
   and audit reflection, noescape/pointer visibility, custom-method, and scratch
   lifetimes.
2. Eliminate heterogeneous scratch-slot retyping and allocation. **Done in the
   current candidate.**
3. Keep forced stack-growth, aggressive-GC, retained-receiver, and pool-poison
   tests in the release gate. **Done in the current candidate.**
4. Keep operation-sequence fuzzing across success, error, invalid custom
   output, GC, and recovery paths. **Done in the current candidate.**
5. Require genuinely equivalent C++ benchmark contracts, including complete
   traversal and matching semantic digests. **Done in the current candidate.**
6. Profile only object-dense stage 2 after the safety freeze; do not broaden
   optimization work from synthetic kernels alone.
7. Improve key dispatch and packed known-struct decoding only behind
   differential, whitespace, duplicate-key, and allocation tests.
8. Reduce structural-index memory traffic only when validation and navigation
   contracts remain identical.
9. Do not explore parallel top-level arrays: subsequent scope explicitly
   cancelled parallel encode/decode work, so it remains out of scope.
10. Republish every result from the exact, clean release-candidate commit with
    the repository and toolchain revisions recorded.

Measure the allocation tax of safe ownership, but consider reductions only
when they use ordinary typed/runtime-managed storage and pass the full lifetime
gate. Addressable encode hooks have no per-hook allocation; an otherwise local
source may escape once as a whole because receivers are legally retainable.
The common path must not be made less robust to recover benchmark losses, and a
specialized path must not change the default safety contract.
