//go:build linux && (amd64 || arm64 || riscv64 || loong64)

package storeio

import (
	"errors"
	"os"
	"runtime"
	"syscall"
	"testing"
	"unsafe"
)

func TestUAPILayout(t *testing.T) {
	if got := unsafe.Sizeof(ioUringParams{}); got != 120 {
		t.Fatalf("io_uring_params size = %d, want 120", got)
	}
	if got := unsafe.Offsetof(ioUringParams{}.SQOffsets); got != 40 {
		t.Fatalf("io_uring_params.sq_off = %d, want 40", got)
	}
	if got := unsafe.Offsetof(ioUringParams{}.CQOffsets); got != 80 {
		t.Fatalf("io_uring_params.cq_off = %d, want 80", got)
	}
	if got := unsafe.Sizeof(ioUringSQE{}); got != 64 {
		t.Fatalf("io_uring_sqe size = %d, want 64", got)
	}
	if got := unsafe.Offsetof(ioUringSQE{}.UserData); got != 32 {
		t.Fatalf("io_uring_sqe.user_data = %d, want 32", got)
	}
	if got := unsafe.Sizeof(ioUringCQE{}); got != 16 {
		t.Fatalf("io_uring_cqe size = %d, want 16", got)
	}
}

func TestFixedWriteAndDataSync(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	ring, file := newFixedTestRing(t)
	defer func() {
		if err := ring.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()
	page, err := ring.Buffer(0)
	if err != nil {
		t.Fatal(err)
	}
	root, err := ring.Buffer(1)
	if err != nil {
		t.Fatal(err)
	}
	wantPage := []byte("immutable-data-page")
	wantRoot := []byte("alternate-root")
	copy(page, wantPage)
	copy(root, wantRoot)
	pageOffset := int64(syscall.Getpagesize())
	if err := ring.PrepareWriteFixed(0, 0, len(wantPage), pageOffset, 41, true); err != nil {
		t.Fatal(err)
	}
	if _, err := ring.Buffer(0); !errors.Is(err, ErrBufferBusy) {
		t.Fatalf("in-flight Buffer error = %v, want %v", err, ErrBufferBusy)
	}
	if err := ring.PrepareDataSync(0, 42, true); err != nil {
		t.Fatal(err)
	}
	if err := ring.PrepareWriteFixed(0, 1, len(wantRoot), 0, 43, true); err != nil {
		t.Fatal(err)
	}
	if err := ring.PrepareDataSync(0, 44, false); err != nil {
		t.Fatal(err)
	}
	if err := ring.SubmitAndWait(4); err != nil {
		t.Fatal(err)
	}
	for i, userData := range []uint64{41, 42, 43, 44} {
		completion, ok, err := ring.Pop()
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Fatalf("completion %d missing", i)
		}
		if completion.UserData != userData {
			t.Fatalf("completion %d user data = %d, want %d", i, completion.UserData, userData)
		}
		if err := completion.Err(); err != nil {
			t.Fatalf("completion %d: %v", i, err)
		}
	}
	if ring.Available() != 0 {
		t.Fatalf("available = %d, want 0", ring.Available())
	}
	gotPage := make([]byte, len(wantPage))
	if _, err := file.ReadAt(gotPage, pageOffset); err != nil {
		t.Fatal(err)
	}
	if string(gotPage) != string(wantPage) {
		t.Fatalf("page = %q, want %q", gotPage, wantPage)
	}
	gotRoot := make([]byte, len(wantRoot))
	if _, err := file.ReadAt(gotRoot, 0); err != nil {
		t.Fatal(err)
	}
	if string(gotRoot) != string(wantRoot) {
		t.Fatalf("root = %q, want %q", gotRoot, wantRoot)
	}
}

func newFixedTestRing(t *testing.T) (*Ring, *os.File) {
	t.Helper()
	ring, err := Open(Config{Entries: 8, SingleIssuer: true})
	if errors.Is(err, ErrUnavailable) || errors.Is(err, ErrUnsupported) {
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.CreateTemp(t.TempDir(), "ring")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	if err := ring.RegisterFiles([]int{int(file.Fd())}); err != nil {
		if errors.Is(err, syscall.ENOMEM) || errors.Is(err, syscall.EPERM) {
			t.Skip(err)
		}
		t.Fatal(err)
	}
	if err := ring.RegisterBuffers(2, syscall.Getpagesize()); err != nil {
		if errors.Is(err, syscall.ENOMEM) || errors.Is(err, syscall.EPERM) {
			t.Skip(err)
		}
		t.Fatal(err)
	}
	return ring, file
}
