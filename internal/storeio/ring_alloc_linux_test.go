//go:build linux && (amd64 || arm64 || riscv64 || loong64)

package storeio

import (
	"runtime"
	"testing"
)

func TestFixedWriteSteadyAllocation(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	ring, _ := newFixedTestRing(t)
	defer func() {
		if err := ring.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()
	const value = "failure-atomic-page"
	if allocs := testing.AllocsPerRun(100, func() {
		buffer, err := ring.Buffer(0)
		if err != nil {
			panic(err)
		}
		copy(buffer, value)
		if err := ring.PrepareWriteFixed(0, 0, len(value), 0, 43, false); err != nil {
			panic(err)
		}
		if err := ring.SubmitAndWait(1); err != nil {
			panic(err)
		}
		completion, ok, err := ring.Pop()
		if err != nil {
			panic(err)
		}
		if !ok || completion.UserData != 43 || completion.Result != int32(len(value)) {
			panic("unexpected completion")
		}
	}); allocs != 0 {
		t.Fatalf("fixed write allocations = %g, want 0", allocs)
	}
}
