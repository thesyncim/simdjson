package simdjson

import "testing"

var retainedPanicCursor *DecodeCursor

// retentionViolator deliberately breaks the no-retention contract: it stashes
// the *DecodeCursor it was handed and, after UnmarshalSimdJSON returns, a
// later call dereferences it. The default dispatch nils the
// DecodeCursor's inner pointer once the body returns, so the stashed handle
// traps with a nil-pointer panic instead of aliasing a reused or freed frame.
// This test proves the always-on retention trap fires.
type retentionViolator struct {
	stashed *DecodeCursor
}

func (r *retentionViolator) UnmarshalSimdJSON(c *DecodeCursor) error {
	r.stashed = c // contract violation: retaining the DecodeCursor past the call.
	return c.Skip()
}

// TestHookRetentionTrapPanics decodes with a body that retains the
// DecodeCursor, then uses the retained handle and requires a panic. Without
// the trap this would be silent undefined behaviour; with it, the violation
// is immediate and attributable.
func TestHookRetentionTrapPanics(t *testing.T) {
	dec, err := CompileDecoder[retentionViolator](DecoderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var v retentionViolator
	if err := dec.Decode([]byte(`{"x":1}`), &v); err != nil {
		t.Fatalf("decode itself should succeed: %v", err)
	}
	if v.stashed == nil {
		t.Fatal("expected the body to have stashed the DecodeCursor")
	}
	defer func() {
		if recover() == nil {
			t.Fatal("using a retained DecodeCursor after the call should panic")
		}
	}()
	// The dispatch nil'd the inner pointer after the body returned; touching it
	// now must panic rather than read a reused frame.
	_ = v.stashed.Skip()
}

type panicRetentionViolator struct{}

func (*panicRetentionViolator) UnmarshalSimdJSON(c *DecodeCursor) error {
	retainedPanicCursor = c
	panic("hook panic")
}

// TestHookRetentionTrapAfterPanic proves invalidation is unconditional: a
// panicking hook cannot leave a retained cursor connected to parser state or
// the caller's input after recovery.
func TestHookRetentionTrapAfterPanic(t *testing.T) {
	retainedPanicCursor = nil
	t.Cleanup(func() { retainedPanicCursor = nil })
	dec, err := CompileDecoder[panicRetentionViolator](DecoderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("panicking hook did not propagate its panic")
			}
		}()
		var value panicRetentionViolator
		_ = dec.Decode([]byte(`null`), &value)
	}()
	if retainedPanicCursor == nil {
		t.Fatal("panicking hook did not retain the cursor")
	}
	defer func() {
		if recover() == nil {
			t.Fatal("cursor retained by a panicking hook remained usable")
		}
	}()
	_ = retainedPanicCursor.Skip()
}
