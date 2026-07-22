package storeio

import "fmt"

// WriteTransactionOptions binds one copy-on-write publication to its current
// physical and logical allocation high-water marks.
type WriteTransactionOptions struct {
	StoreID       [16]byte
	Generation    uint64
	PageSize      uint32
	FileEnd       uint64
	NextLogicalID uint64
}

// WriteTransaction reserves worst-case committer capacity, allocates immutable
// append extents, and publishes exactly one state root. It is single-owner and
// must be aborted or published.
type WriteTransaction struct {
	committer *Committer
	cache     *PageCache
	batch     *Batch
	options   WriteTransactionOptions
	fileEnd   uint64
	nextID    uint64
	allocated int
	active    bool
}

// TransactionPage is one staging buffer and its prospective durable reference.
// The value borrows its transaction and is invalid after Publish or Abort.
type TransactionPage struct {
	tx    *WriteTransaction
	index int
	ref   PageRef
	bytes []byte
}

// Ref returns the prospective immutable page reference.
func (p TransactionPage) Ref() PageRef { return p.ref }

// Bytes returns the exact capacity-clipped encoding buffer.
func (p TransactionPage) Bytes() []byte { return p.bytes }

// Stage verifies the complete common page, admits it as dirty when applicable,
// and records its positional write.
func (p TransactionPage) Stage() error {
	if p.tx == nil || !p.tx.active || p.index < 0 || p.index >= p.tx.allocated || len(p.bytes) != int(p.ref.Length) {
		return ErrBatchState
	}
	header, _, err := OpenPage(p.bytes)
	if err != nil {
		return err
	}
	if header.StoreID != p.tx.options.StoreID || header.Generation != p.tx.options.Generation ||
		header.LogicalID != p.ref.LogicalID || header.PageSize != p.ref.Length ||
		header.Kind != p.ref.Kind || header.Flags != p.ref.Flags {
		return fmt.Errorf("%w: staged page identity", ErrInvalidWrite)
	}
	write := &p.tx.batch.pages[p.index]
	if write.Length != 0 {
		return ErrBatchState
	}
	if p.tx.cache != nil && p.ref.LogicalID != StateRootLogicalID {
		if err := p.tx.cache.AdmitDirty(p.ref, p.bytes, p.tx.options.Generation); err != nil {
			return err
		}
	}
	return p.tx.batch.SetPage(p.index, int64(p.ref.Offset), int(p.ref.Length))
}

// BeginWriteTransaction acquires bounded worst-case staging capacity.
func BeginWriteTransaction(committer *Committer, cache *PageCache, maxPages int, options WriteTransactionOptions) (*WriteTransaction, error) {
	if committer == nil || options.StoreID == ([16]byte{}) || options.Generation == 0 ||
		!validPhysicalPageSize(options.PageSize) || options.FileEnd < uint64(superblockCopies)*uint64(options.PageSize) ||
		options.FileEnd%uint64(options.PageSize) != 0 || options.FileEnd > maxSuperblockFileOffset ||
		options.NextLogicalID <= StateRootLogicalID {
		return nil, fmt.Errorf("%w: transaction identity or bounds", ErrInvalidWrite)
	}
	batch, err := committer.Begin(maxPages)
	if err != nil {
		return nil, err
	}
	return &WriteTransaction{
		committer: committer, cache: cache, batch: batch, options: options,
		fileEnd: options.FileEnd, nextID: options.NextLogicalID, active: true,
	}, nil
}

// Allocate reserves one append-only extent. logicalID zero allocates a new
// logical identity; non-zero rewrites that logical page at the new generation.
func (t *WriteTransaction) Allocate(kind PageKind, length uint32, logicalID uint64) (TransactionPage, error) {
	if t == nil || !t.active || t.allocated >= len(t.batch.pages) || !validPageKind(kind) ||
		!validPhysicalPageSize(length) || length < t.options.PageSize || length%t.options.PageSize != 0 ||
		uint64(length) > uint64(t.committer.bufferSize) {
		return TransactionPage{}, ErrTooManyPages
	}
	if kind != PageDocument && kind != PageOverflow && length != t.options.PageSize {
		return TransactionPage{}, fmt.Errorf("%w: variable metadata extent", ErrInvalidWrite)
	}
	if logicalID == 0 {
		logicalID = t.nextID
		if logicalID <= StateRootLogicalID || logicalID == ^uint64(0) {
			return TransactionPage{}, fmt.Errorf("%w: logical id exhausted", ErrInvalidWrite)
		}
		t.nextID++
	} else if logicalID != StateRootLogicalID && (logicalID <= StateRootLogicalID || logicalID >= t.nextID) {
		return TransactionPage{}, fmt.Errorf("%w: replacement logical id", ErrInvalidWrite)
	}
	if kind == PageStateRoot && logicalID != StateRootLogicalID || kind != PageStateRoot && logicalID == StateRootLogicalID {
		return TransactionPage{}, fmt.Errorf("%w: state-root logical id", ErrInvalidWrite)
	}
	if uint64(length) > maxSuperblockFileOffset-t.fileEnd {
		return TransactionPage{}, fmt.Errorf("%w: physical file exhausted", ErrInvalidWrite)
	}
	index := t.allocated
	buffer, err := t.batch.PageBuffer(index)
	if err != nil {
		return TransactionPage{}, err
	}
	ref := PageRef{
		Offset: t.fileEnd, LogicalID: logicalID, Generation: t.options.Generation,
		Length: length, Kind: kind,
	}
	t.fileEnd += uint64(length)
	t.allocated++
	return TransactionPage{tx: t, index: index, ref: ref, bytes: buffer[:int(length):int(length)]}, nil
}

// FileEnd returns the prospective exclusive allocation high-water mark.
func (t *WriteTransaction) FileEnd() uint64 {
	if t == nil {
		return 0
	}
	return t.fileEnd
}

// NextLogicalID returns the prospective logical high-water mark.
func (t *WriteTransaction) NextLogicalID() uint64 {
	if t == nil {
		return 0
	}
	return t.nextID
}

// Generation returns the transaction publication generation.
func (t *WriteTransaction) Generation() uint64 {
	if t == nil {
		return 0
	}
	return t.options.Generation
}

// Publish selects stateRef through the alternate superblock. stateRef must be
// a staged state-root page from this transaction. free root fields may retain
// an older immutable tree or be zero when no free extents exist.
func (t *WriteTransaction) Publish(stateRef PageRef, stateChecksum uint32, freeOffset uint64, freeLength, freeChecksum uint32) error {
	if t == nil || !t.active || stateRef.Kind != PageStateRoot || stateRef.LogicalID != StateRootLogicalID ||
		stateRef.Generation != t.options.Generation || stateRef.Length != t.options.PageSize {
		return ErrBatchState
	}
	stagedState := false
	for i := 0; i < t.allocated; i++ {
		write := t.batch.pages[i]
		if write.Length == 0 {
			return fmt.Errorf("%w: unstaged transaction page", ErrInvalidWrite)
		}
		if uint64(write.Offset) == stateRef.Offset && write.Length == stateRef.Length {
			page := t.committer.buffers[write.Buffer][:write.Length]
			if PageChecksum(page) != stateChecksum {
				return fmt.Errorf("%w: state-root checksum", ErrInvalidWrite)
			}
			stagedState = true
		}
	}
	if !stagedState {
		return fmt.Errorf("%w: state root was not staged", ErrInvalidWrite)
	}
	if err := t.batch.ResizePages(t.allocated); err != nil {
		return err
	}
	root := Superblock{
		StoreID: t.options.StoreID, Generation: t.options.Generation,
		StateOffset: stateRef.Offset, StateLength: stateRef.Length, StateChecksum: stateChecksum,
		FileEnd: t.fileEnd, FreeOffset: freeOffset, FreeLength: freeLength,
		FreeChecksum: freeChecksum, PageSize: t.options.PageSize,
	}
	if err := t.batch.SetSuperblock(root); err != nil {
		return err
	}
	if err := t.batch.Publish(t.options.Generation); err != nil {
		return err
	}
	t.active = false
	t.batch = nil
	return nil
}

// Abort releases every reserved buffer. It is idempotent after a successful
// Publish only in the sense that it returns ErrBatchState and changes nothing.
func (t *WriteTransaction) Abort() error {
	if t == nil || !t.active || t.batch == nil {
		return ErrBatchState
	}
	err := t.batch.Abort()
	if err == nil {
		t.active = false
		t.batch = nil
		if t.cache != nil {
			err = t.cache.DiscardDirty(t.options.Generation)
		}
	}
	return err
}
