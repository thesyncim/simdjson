package simdjson

import "unsafe"

// hookLayoutOK reports whether the running toolchain lays out a non-empty
// interface value as the two words hookIface assumes (itab pointer, then data
// pointer) and whether an interface rebuilt from those two words dispatches
// correctly. It is set once at package initialization by verifyHookLayout and
// read by captureHookTabs: when it is false, no itab is ever captured, so every
// hook dispatch takes the reflect fallback. The fast itab rebind is used only
// after this self-test has proven it sound on this exact toolchain, so an
// unexpected runtime layout degrades the hooks to correct-and-slower rather
// than corrupting memory.
var hookLayoutOK = verifyHookLayout()

// layoutProbe backs the layout self-test. Its two methods return distinct
// sentinels through a data pointer, so the test can confirm both that a
// rebuilt interface reaches the right method and that it carries the right
// receiver.
type layoutProbe struct {
	tag uint64
}

// UnmarshalSimdJSON lets *layoutProbe satisfy UnmarshalerSimd for the self-test.
// It records the receiver's tag through the cursor-free error channel: the test
// passes a nil Cursor and never dereferences it, so this must not touch c.
func (p *layoutProbe) UnmarshalSimdJSON(*Cursor) error {
	if p.tag == layoutProbeTag {
		return nil
	}
	return errLayoutProbe
}

const layoutProbeTag = 0x5144_4a53_4d49_53 // "SIMDJSDQ"-ish sentinel

var errLayoutProbe = &DecodeError{Reason: "layout probe"}

// verifyHookLayout proves the two-word interface assumption on this toolchain.
// It takes an ordinary interface value, reads its itab and data words through
// the hookIface view, then rebuilds a fresh interface from those two words and
// checks that:
//
//  1. the data word equals the address of the concrete value, so the second
//     word really is the data pointer; and
//  2. a method called through the rebuilt interface reaches the concrete
//     method with the correct receiver (returns nil for the sentinel tag).
//
// Any deviation returns false and the package runs entirely on the reflect
// fallback. The build-tag files set hookLayoutForceUnsafe: the safe tag forces
// false so even a passing probe stays on reflect.
func verifyHookLayout() bool {
	if !hookLayoutForceUnsafe {
		return false
	}
	probe := &layoutProbe{tag: layoutProbeTag}
	var iface UnmarshalerSimd = probe

	view := (*hookIface)(unsafe.Pointer(&iface))
	if view.tab == nil {
		return false
	}
	// The data word must be the address of the concrete value.
	if view.data != unsafe.Pointer(probe) {
		return false
	}

	// Rebuild a fresh interface from the two words and dispatch through it.
	var rebuilt UnmarshalerSimd
	h := (*hookIface)(unsafe.Pointer(&rebuilt))
	h.tab = view.tab
	h.data = view.data
	if rebuilt.UnmarshalSimdJSON(nil) != nil {
		return false
	}

	// Rebind the data word to a second probe and confirm the call now sees the
	// new receiver, proving the data word alone selects the receiver (the itab
	// is shared) — the exact operation the per-element dispatch performs.
	other := &layoutProbe{tag: layoutProbeTag ^ 1}
	h.data = unsafe.Pointer(other)
	if rebuilt.UnmarshalSimdJSON(nil) != errLayoutProbe {
		return false
	}
	return true
}
