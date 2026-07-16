//go:build !race && !simdjson_safehooks

package simdjson

import "unsafe"

// This file holds the fast, itab-based hook dispatch used by ordinary builds.
// The corruption-catching variant lives in typed_hook_safe.go and is selected
// by -race or the simdjson_safehooks build tag; the two files present the same
// entry points (decodeViaSimdHookFast, encodeViaSimdHookFast, dispatchDecodeHook,
// dispatchEncodeHook, hookSafeDispatch) so the rest of the package is oblivious
// to which is compiled in.

// hookSafeDispatch reports whether the corruption-catching dispatch is active.
// It is false here (fast build) and true in typed_hook_safe.go. Tests consult
// it to select their expectations.
const hookSafeDispatch = false

// hookLayoutForceUnsafe permits verifyHookLayout to enable the itab fast path
// once its self-test passes. It is true here (fast build) and false under the
// safe tag, which keeps every dispatch on the reflect fallback.
const hookLayoutForceUnsafe = true

// decodeViaSimdHookFast rebuilds the *T -> UnmarshalerSimd interface from the
// captured itab and calls the method. It is the whole reason the hook tier is
// faster than the standard custom-method path: two word stores and one indirect
// call, no allocation.
//
// GC and stack safety. The data word is dst, the genuine decode destination
// inside the caller's value; it is alive for the whole decode independently of
// this interface, so scanning it here is unnecessary for its own liveness. The
// cursor pointer handed to the body is laundered through noescape so this opaque
// indirect call does not force every enclosing decoder cursor to the heap. That
// is sound only under the no-retention contract: the body must not retain the
// DecodeCursor. A build that cannot vouch for a generated body should use
// -race or the simdjson_safehooks tag, which routes through the reflect
// dispatch in typed_hook_safe.go and turns a retention into a deterministic
// panic.
func decodeViaSimdHookFast(cursor *decoderCursor, tab unsafe.Pointer, dst unsafe.Pointer) error {
	var hook UnmarshalerSimd
	h := (*hookIface)(unsafe.Pointer(&hook))
	h.tab = tab
	h.data = noescape(dst)
	c := DecodeCursor{d: (*decoderCursor)(noescape(unsafe.Pointer(cursor)))}
	// The opaque interface call would otherwise force &c to the heap. The
	// no-retention contract forbids the body from keeping the *DecodeCursor,
	// so laundering its address keeps the whole dispatch allocation-free; the
	// safe build (typed_hook_safe.go) does not launder and traps a retention.
	return hook.UnmarshalSimdJSON((*DecodeCursor)(noescape(unsafe.Pointer(&c))))
}

// encodeViaSimdHookFast rebuilds the *T -> MarshalerSimd interface from the
// captured itab and calls the method, threading the Appender by value so the
// buffer stays in registers and nothing escapes.
func encodeViaSimdHookFast(tab unsafe.Pointer, src unsafe.Pointer, w Appender) Appender {
	var hook MarshalerSimd
	h := (*hookIface)(unsafe.Pointer(&hook))
	h.tab = tab
	h.data = noescape(src)
	return hook.MarshalSimdJSON(w)
}

// dispatchDecodeHook calls a reflect-boxed decode hook (the itab-fallback path
// on this fast build). Both the cursor pointer and the DecodeCursor's address
// are laundered through noescape: on the fast build the no-retention contract
// holds package-wide, and leaving this branch un-laundered would statically
// force the enclosing decode's cursor to the heap even for non-hook types
// whose plans merely make this branch reachable. The safe build's copy
// (typed_hook_safe.go) does not launder and traps a retained DecodeCursor
// instead.
func dispatchDecodeHook(hook UnmarshalerSimd, cursor *decoderCursor) error {
	c := DecodeCursor{d: (*decoderCursor)(noescape(unsafe.Pointer(cursor)))}
	return hook.UnmarshalSimdJSON((*DecodeCursor)(noescape(unsafe.Pointer(&c))))
}

// dispatchEncodeHook calls a reflect-boxed encode hook (itab-fallback path).
func dispatchEncodeHook(hook MarshalerSimd, w Appender) Appender {
	return hook.MarshalSimdJSON(w)
}
