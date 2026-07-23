//go:build linux

package simdjson

import (
	"errors"
	"fmt"
	"os"
	"testing"
)

func TestFileStoreRequiredDirectReads(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "file-store-required-direct-*")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	options := testFileStoreOptions()
	options.ReadMode = FileStoreReadDirectRequire
	store, err := CreateFileStore(file, options)
	if errors.Is(err, ErrStoreDirectIOUnsupported) {
		t.Skipf("test filesystem has no O_DIRECT support: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	if !store.Stats().DirectReads {
		t.Fatal("required direct reads were not reported active")
	}
	for row := range 64 {
		key := fmt.Sprintf("linux:direct:%02d", row)
		value := fmt.Appendf(nil, `{"v":%d}`, row)
		if _, err := store.Put(key, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenFileStore(file, options)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got, ok, err := reopened.AppendRaw(nil, "linux:direct:01"); err != nil || !ok || string(got) != `{"v":1}` {
		t.Fatalf("required direct read = (%q,%v,%v)", got, ok, err)
	}
	snapshot, err := reopened.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	rows := 0
	if _, err := snapshot.RangeRawReadAheadBuffer(nil, func(_, _ []byte) error {
		rows++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
	if rows != 64 {
		t.Fatalf("required direct read-ahead rows = %d, want 64", rows)
	}
	if stats := reopened.Stats(); !stats.DirectReads || stats.PageReads == 0 ||
		stats.PrefetchQueued == 0 || stats.PrefetchHits+stats.CoalescedReads == 0 {
		t.Fatalf("required direct stats = %+v", stats)
	}
}
