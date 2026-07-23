//go:build linux

package simdjson

import (
	"errors"
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
	if _, err := store.Put("linux:direct", []byte(`{"v":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenFileStore(file, options)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got, ok, err := reopened.AppendRaw(nil, "linux:direct"); err != nil || !ok || string(got) != `{"v":1}` {
		t.Fatalf("required direct read = (%q,%v,%v)", got, ok, err)
	}
	if stats := reopened.Stats(); !stats.DirectReads || stats.PageReads == 0 {
		t.Fatalf("required direct stats = %+v", stats)
	}
}
