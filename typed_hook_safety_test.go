package simdjson

import (
	"encoding/json"
	"reflect"
	"testing"
	"unsafe"
)

// TestHookLayoutSelfTest confirms the package-init interface-layout self-test
// reached a definite conclusion and matches the build: the fast build must
// prove the layout sound (so the itab path is active), and the safe build must
// force the reflect path regardless.
func TestHookLayoutSelfTest(t *testing.T) {
	if hookSafeDispatch {
		if hookLayoutOK {
			t.Fatal("safe build must keep hookLayoutOK false so every dispatch takes the reflect path")
		}
		return
	}
	if !hookLayoutOK {
		t.Fatal("fast build: interface-layout self-test failed; hooks would run entirely on the reflect fallback")
	}
}

// TestHookItabCaptured verifies that on the fast build a compiled plan for a
// hook type carries the captured itabs, and that the safe build leaves them nil
// (forcing the reflect dispatch). This is the observable that distinguishes the
// two dispatch strategies.
func TestHookItabCaptured(t *testing.T) {
	dec, err := CompileDecoder[hookPerson](DecoderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	enc, err := CompileEncoder[hookPerson](EncoderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	decNode := dec.root
	encNode := enc.root
	if hookSafeDispatch {
		if decNode.decHookTab != nil || encNode.encHookTab != nil {
			t.Fatal("safe build must not capture itabs")
		}
		return
	}
	if decNode.decHookTab == nil {
		t.Fatal("fast build: decode itab not captured")
	}
	if encNode.encHookTab == nil {
		t.Fatal("fast build: encode itab not captured")
	}
}

// TestHookItabFallbackDecodesCorrectly forces the reflect fallback by clearing
// the captured itab on a cloned plan node, then decodes and encodes and
// requires the same result as the itab path. This exercises the exact code the
// package runs when the layout self-test fails on an unexpected toolchain, so
// the fail-closed path is proven correct — not merely present.
func TestHookItabFallbackDecodesCorrectly(t *testing.T) {
	src := sampleHookPersonJSON()

	// Reference: whatever the current build's default dispatch produces.
	refDec, err := CompileDecoder[hookPerson](DecoderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var ref hookPerson
	if err := refDec.Decode(src, &ref); err != nil {
		t.Fatal(err)
	}

	// Forced fallback: a second plan whose hook itabs are cleared, so every
	// dispatch must take decodeViaSimdHookReflect / the reflect encode branch.
	fbDec, err := CompileDecoder[hookPerson](DecoderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	clearHookTabs(fbDec.root)
	var fb hookPerson
	if err := fbDec.Decode(src, &fb); err != nil {
		t.Fatalf("forced-fallback decode: %v", err)
	}
	if !hookPersonEqual(projectHook(fb), projectHook(ref)) {
		t.Fatalf("forced-fallback decode differs from itab decode:\n fb=%+v\nref=%+v", fb, ref)
	}

	fbEnc, err := CompileEncoder[hookPerson](EncoderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	clearHookTabs(fbEnc.root)
	refEnc, err := CompileEncoder[hookPerson](EncoderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	fbOut, err := fbEnc.AppendJSON(nil, &fb)
	if err != nil {
		t.Fatalf("forced-fallback encode: %v", err)
	}
	refOut, err := refEnc.AppendJSON(nil, &ref)
	if err != nil {
		t.Fatal(err)
	}
	if string(fbOut) != string(refOut) {
		t.Fatalf("forced-fallback encode differs:\n fb=%s\nref=%s", fbOut, refOut)
	}
}

// clearHookTabs walks a compiled plan and nils every captured hook itab,
// forcing the reflect dispatch throughout the subtree. Test-only.
func clearHookTabs(node *typedNode) {
	clearHookTabsSeen(node, map[*typedNode]bool{})
}

func clearHookTabsSeen(node *typedNode, seen map[*typedNode]bool) {
	if node == nil || seen[node] {
		return
	}
	seen[node] = true
	node.decHookTab = nil
	node.encHookTab = nil
	clearHookTabsSeen(node.elem, seen)
	for i := range node.fields {
		clearHookTabsSeen(node.fields[i].node, seen)
	}
	for i := range node.encFields {
		clearHookTabsSeen(node.encFields[i].node, seen)
	}
	if node.inlineMap != nil {
		clearHookTabsSeen(node.inlineMap.elem, seen)
	}
}

// TestHookLayoutRebindMechanics independently re-proves the two-word rebind the
// dispatch relies on: capture an itab, rebind the data word to a different
// receiver, and confirm the call reaches the new receiver. If this ever fails
// while hookLayoutOK is true, the self-test's guarantee is broken.
func TestHookLayoutRebindMechanics(t *testing.T) {
	if !hookLayoutOK {
		t.Skip("reflect fallback active; itab rebind not used")
	}
	a := &layoutProbe{tag: layoutProbeTag}
	var iface UnmarshalerSimd = a
	tab := (*hookIface)(unsafe.Pointer(&iface)).tab

	var rebuilt UnmarshalerSimd
	h := (*hookIface)(unsafe.Pointer(&rebuilt))
	h.tab = tab
	h.data = unsafe.Pointer(a)
	if got := rebuilt.UnmarshalSimdJSON(nil); got != nil {
		t.Fatalf("rebind to matching-tag receiver: got %v, want nil", got)
	}
	b := &layoutProbe{tag: layoutProbeTag ^ 1}
	h.data = unsafe.Pointer(b)
	if got := rebuilt.UnmarshalSimdJSON(nil); got != errLayoutProbe {
		t.Fatalf("rebind to mismatched-tag receiver: got %v, want errLayoutProbe", got)
	}
}

// TestHookLayoutFailClosed simulates the interface-layout self-test failing on
// an unexpected toolchain: with hookLayoutOK forced false, a freshly compiled
// plan must capture no itabs (captureHookTabs is a no-op) and must still decode
// and encode correctly through the reflect fallback. This proves the self-test
// fails CLOSED — a layout mismatch degrades to correct-and-slower, never to
// corruption. Restoring hookLayoutOK is deferred so the rest of the suite is
// unaffected.
func TestHookLayoutFailClosed(t *testing.T) {
	saved := hookLayoutOK
	hookLayoutOK = false
	defer func() { hookLayoutOK = saved }()

	dec, err := CompileDecoder[hookPerson](DecoderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	enc, err := CompileEncoder[hookPerson](EncoderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if dec.root.decHookTab != nil {
		t.Fatal("layout self-test failed but a decode itab was still captured (not fail-closed)")
	}
	if enc.root.encHookTab != nil {
		t.Fatal("layout self-test failed but an encode itab was still captured (not fail-closed)")
	}

	// The plan must still work, dispatching every hook through reflect.
	src := sampleHookPersonJSON()
	var got hookPerson
	if err := dec.Decode(src, &got); err != nil {
		t.Fatalf("fail-closed decode: %v", err)
	}
	out, err := enc.AppendJSON(nil, &got)
	if err != nil {
		t.Fatalf("fail-closed encode: %v", err)
	}
	var std hookPersonPlain
	if err := json.Unmarshal(src, &std); err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(&std)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(want) {
		t.Fatalf("fail-closed round trip differs:\n got=%s\nwant=%s", out, want)
	}
}

// TestHookReflectTypeGuards documents that only the pointer form of the hook is
// honoured, matching how a generator emits pointer-receiver methods.
func TestHookReflectTypeGuards(t *testing.T) {
	if !reflect.PointerTo(reflect.TypeFor[hookPerson]()).Implements(unmarshalerSimdReflectType) {
		t.Fatal("*hookPerson should implement UnmarshalerSimd")
	}
	if !reflect.PointerTo(reflect.TypeFor[hookPerson]()).Implements(marshalerSimdReflectType) {
		t.Fatal("*hookPerson should implement MarshalerSimd")
	}
}
