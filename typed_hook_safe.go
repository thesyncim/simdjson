//go:build race || simdjson_safehooks

package simdjson

import "unsafe"

// This file holds the corruption-catching hook dispatch, selected by building
// with -race or with the simdjson_safehooks build tag. It replaces the
// itab-based fast dispatch in typed_hook_fast.go with a purely reflect-boxed
// dispatch that is memory-safe even when a hook body violates the no-retention
// contract, and it actively catches such a violation.
//
// Under this tag captureHookTabs never records an itab (hookLayoutOK is forced
// false below), so every node keeps decHookTab and encHookTab nil and
// decodeViaSimdHook / encodeViaSimdHook always take their reflect branch. The
// fast entry points therefore cannot run; they remain only to satisfy the
// shared signatures and panic if the invariant is ever broken.
//
// The retention trap. dispatchDecodeHook clears the DecodeCursor's inner
// pointer once the body returns. A body that stashed the *DecodeCursor and
// dereferences it later hits a nil pointer and panics deterministically,
// instead of silently reading a reused or freed frame. That converts a
// no-retention-contract violation from silent corruption (which -race alone
// does not reliably surface here) into an immediate, attributable crash.

// hookSafeDispatch reports whether the corruption-catching dispatch is active.
// Tests consult it to select their expectations. See typed_hook_fast.go.
const hookSafeDispatch = true

// hookLayoutForceUnsafe is false here so verifyHookLayout leaves hookLayoutOK
// false under the safe tag, keeping every dispatch on the reflect path.
const hookLayoutForceUnsafe = false

// decodeViaSimdHookFast is unreachable in the safe build (no itab is ever
// captured). It panics rather than pretend to dispatch.
func decodeViaSimdHookFast(cursor *decoderCursor, tab unsafe.Pointer, dst unsafe.Pointer) error {
	_ = cursor
	_ = tab
	_ = dst
	panic("simdjson: fast hook dispatch reached in safe build")
}

// encodeViaSimdHookFast is unreachable in the safe build.
func encodeViaSimdHookFast(tab unsafe.Pointer, src unsafe.Pointer, w Appender) Appender {
	_ = tab
	_ = src
	_ = w
	panic("simdjson: fast hook dispatch reached in safe build")
}

// dispatchDecodeHook runs the decode hook against a heap-resident copy of the
// cursor, then copies the advanced state back. This gives the safe build three
// properties at once:
//
//   - Corruption safety. The heap DecodeCursor box points at the heap cursor
//     copy, so there is never a heap object holding a pointer into a stack
//     frame — the "bad pointer in Go heap" hazard the itab path must avoid.
//     (The copy's src slice header aliases the same caller-owned backing
//     array, which is not on any goroutine stack.)
//   - Zero cost to non-hook types. The original cursor is only value-read here
//     (never stored), so it does not escape; a decode whose plan merely makes
//     this branch statically reachable pays nothing. The heap copy is allocated
//     only when a hook actually runs.
//   - Retention trap. The DecodeCursor box is heap-resident and its inner
//     pointer is nil'd after the body returns, so a retained *DecodeCursor
//     traps on a nil deref rather than aliasing a reused frame.
func dispatchDecodeHook(hook UnmarshalerSimd, cursor *decoderCursor) error {
	heapCursor := new(decoderCursor)
	*heapCursor = *(*decoderCursor)(noescape(unsafe.Pointer(cursor)))
	c := &DecodeCursor{d: heapCursor}
	err := hook.UnmarshalSimdJSON(c)
	c.d = nil // trap a retained *DecodeCursor: later use panics on nil deref.
	// Copy the advanced parse state back into the caller's cursor. All mutated
	// fields travel back, including any string arena the body allocated.
	*(*decoderCursor)(noescape(unsafe.Pointer(cursor))) = *heapCursor
	return err
}

// dispatchEncodeHook calls the encode hook. A retained Appender is already
// harmless: it is a value copy, so a body cannot mutate the encoder's buffer
// through a stale copy, and its poison bool defaults to false. Nothing to
// invalidate.
func dispatchEncodeHook(hook MarshalerSimd, w Appender) Appender {
	return hook.MarshalSimdJSON(w)
}
