//go:build race || simdjson_safehooks

package simdjson

import "testing"

// retentionViolator deliberately breaks the no-retention contract: it stashes
// the *Cursor it was handed and, after UnmarshalSimdJSON returns, a later call
// dereferences it. On the safe build the dispatch nils the Cursor's inner
// pointer once the body returns, so the stashed handle traps with a nil-pointer
// panic instead of aliasing a reused or freed frame. This test proves the
// debug/-race retention trap fires; it is compiled only under the safe tag.
type retentionViolator struct {
	stashed *Cursor
}

func (r *retentionViolator) UnmarshalSimdJSON(c *Cursor) error {
	r.stashed = c // contract violation: retaining the Cursor past the call.
	return c.Skip()
}

// TestHookRetentionTrapPanics decodes with a body that retains the Cursor, then
// uses the retained handle and requires a panic. Without the trap this would be
// silent undefined behaviour; with it, the violation is immediate and
// attributable.
func TestHookRetentionTrapPanics(t *testing.T) {
	if !hookSafeDispatch {
		t.Skip("retention trap is active only on the safe/-race build")
	}
	dec, err := CompileDecoder[retentionViolator](DecoderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var v retentionViolator
	if err := dec.Decode([]byte(`{"x":1}`), &v); err != nil {
		t.Fatalf("decode itself should succeed: %v", err)
	}
	if v.stashed == nil {
		t.Fatal("expected the body to have stashed the Cursor")
	}
	defer func() {
		if recover() == nil {
			t.Fatal("using a retained Cursor after the call should panic in the safe build")
		}
	}()
	// The dispatch nil'd the inner pointer after the body returned; touching it
	// now must panic rather than read a reused frame.
	_ = v.stashed.Skip()
}
