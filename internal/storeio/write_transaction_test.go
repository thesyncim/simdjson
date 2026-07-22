package storeio

import (
	"errors"
	"os"
	"testing"
)

func TestWriteTransactionPublishesRecoverableStateAndDirtyPage(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "write-transaction-*")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	committer, err := NewCommitter(file, DeviceOptions{
		Backend: BackendPortable, BufferCount: 8, BufferSize: os.Getpagesize(),
	}, CommitterOptions{QueueSlots: 4, MaxPagesPerBatch: 4, GroupLimit: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer committer.Close()
	cache, err := NewPageCache(file, PageCacheOptions{
		PageSize: int(testSuperblockPageSize), ResidentBytes: int64(4 * testSuperblockPageSize),
		StoreID: testStoreID, ReadConcurrency: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()

	tx, err := BeginWriteTransaction(committer, cache, 4, WriteTransactionOptions{
		StoreID: testStoreID, Generation: 1, PageSize: testSuperblockPageSize,
		FileEnd: 2 * uint64(testSuperblockPageSize), NextLogicalID: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	document, err := tx.Allocate(PageDocument, testSuperblockPageSize, 0)
	if err != nil {
		t.Fatal(err)
	}
	documentHeader := DocumentPageHeader{
		StoreID: testStoreID, Generation: 1, LogicalID: document.Ref().LogicalID,
		PageSize: testSuperblockPageSize, ChunkID: 0, Live: 1,
	}
	if _, err := EncodeDocumentPage(document.Bytes(), documentHeader, []DocumentRecord{{Slot: 0, Key: []byte("k"), JSON: []byte("1")}}, tx.NextLogicalID()); err != nil {
		t.Fatal(err)
	}
	if err := document.Stage(); err != nil {
		t.Fatal(err)
	}
	lease, err := cache.Acquire(document.Ref())
	if err != nil {
		t.Fatal(err)
	}
	view, err := OpenDocumentPage(lease.Page(), 1, tx.NextLogicalID())
	if err != nil {
		t.Fatal(err)
	}
	if json, ok := view.LookupString(0, "k"); !ok || string(json) != "1" {
		t.Fatalf("dirty document lookup = (%q,%v)", json, ok)
	}
	lease.Release()

	statePage, err := tx.Allocate(PageStateRoot, testSuperblockPageSize, StateRootLogicalID)
	if err != nil {
		t.Fatal(err)
	}
	wantState := StateRoot{
		StoreID: testStoreID, Generation: 1, PageSize: testSuperblockPageSize,
		NextLogicalID: tx.NextLogicalID(), ChunkDocuments: 64,
	}
	if _, err := EncodeStateRootPage(statePage.Bytes(), wantState, tx.FileEnd()); err != nil {
		t.Fatal(err)
	}
	if err := statePage.Stage(); err != nil {
		t.Fatal(err)
	}
	stateChecksum := PageChecksum(statePage.Bytes())
	if err := tx.Publish(statePage.Ref(), stateChecksum, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	if stats := cache.Stats(); stats.DirtyBytes != uint64(testSuperblockPageSize) {
		t.Fatalf("dirty cache before wait = %+v", stats)
	}
	if err := committer.Wait(1); err != nil {
		t.Fatal(err)
	}
	cache.MarkDurable(committer.DurableGeneration())
	if stats := cache.Stats(); stats.DirtyBytes != 0 {
		t.Fatalf("dirty cache after wait = %+v", stats)
	}
	scratch := make([]byte, testSuperblockPageSize)
	root, gotState, slot, err := RecoverStateRoot(file, testSuperblockPageSize, scratch)
	if err != nil || slot != 0 || gotState != wantState || root.Generation != 1 || root.FileEnd != tx.FileEnd() {
		t.Fatalf("RecoverStateRoot = (%+v,%+v,%d,%v)", root, gotState, slot, err)
	}
}

func TestWriteTransactionValidationAndAbort(t *testing.T) {
	committer, _, _ := newPortableCommitter(t, 4, 2)
	defer committer.Close()
	if _, err := BeginWriteTransaction(committer, nil, 1, WriteTransactionOptions{}); !errors.Is(err, ErrInvalidWrite) {
		t.Fatalf("invalid Begin = %v, want %v", err, ErrInvalidWrite)
	}
	tx, err := BeginWriteTransaction(committer, nil, 2, WriteTransactionOptions{
		StoreID: testStoreID, Generation: 1, PageSize: testSuperblockPageSize,
		FileEnd: 2 * uint64(testSuperblockPageSize), NextLogicalID: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Allocate(PageKeyDirectory, 2*testSuperblockPageSize, 0); !errors.Is(err, ErrInvalidWrite) {
		t.Fatalf("variable metadata = %v, want %v", err, ErrInvalidWrite)
	}
	if err := tx.Abort(); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Allocate(PageDocument, testSuperblockPageSize, 0); !errors.Is(err, ErrTooManyPages) {
		t.Fatalf("Allocate after abort = %v, want %v", err, ErrTooManyPages)
	}
}
