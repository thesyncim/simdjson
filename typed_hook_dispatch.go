package simdjson

// Hook dispatch has one implementation in every build. Decode receiver/cursor
// state is owned and the cursor handle is invalidated on return. Encode
// receivers follow ordinary GC-visible Go ownership. Interface values are
// constructed by the Go runtime; there is no safety build tag or unsafe fast
// alternative.

// dispatchDecodeHook runs the decode hook against a heap-resident handle that
// owns its cursor copy, then copies the advanced state back. This gives every
// build three
// properties at once:
//
//   - Corruption safety. The heap DecodeCursor owns the cursor copy it points
//     at, so there is never a heap object holding a pointer into a stack frame.
//     The copy's src slice header aliases the caller-owned backing array;
//     ordinary escape analysis keeps that backing visible for the call.
//   - Zero cost to non-hook types. The original cursor is only value-read here
//     (never stored), so it does not escape; a decode whose plan merely makes
//     this branch statically reachable pays nothing. The heap copy is allocated
//     only when a hook actually runs.
//   - Retention trap. The DecodeCursor box is heap-resident and its inner
//     pointer is nil'd after the body returns, so a retained *DecodeCursor
//     traps on a nil deref rather than aliasing a reused frame.
func dispatchDecodeHook(hook UnmarshalerSimd, cursor *decoderCursor) error {
	c := new(DecodeCursor)
	c.state = *cursor
	c.d = &c.state
	defer func() {
		// Invalidate on success, error, or panic so a retained handle cannot
		// pin or later inspect this operation's parser and source.
		c.d = nil
	}()
	err := hook.UnmarshalSimdJSON(c)
	// Copy the advanced parse state back into the caller's cursor. All mutated
	// fields travel back, including any string arena the body allocated.
	*cursor = c.state
	return err
}

// dispatchEncodeHook calls the encode hook with an ordinary value. Retaining
// that value cannot create a dangling stack pointer, but it still violates the
// buffer-ownership contract if the caller later reuses the output storage.
func dispatchEncodeHook(hook MarshalerSimd, w TrustedAppender) TrustedAppender {
	return hook.MarshalSimdJSON(w)
}
