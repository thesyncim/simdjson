package storeio

import (
	"fmt"
	"math/bits"
)

// ChunkTreeBounds are copied from the selected durable roots when existing
// radix pages are admitted.
type ChunkTreeBounds struct {
	FileEnd       uint64
	NextLogicalID uint64
}

// ChunkTreeMutation reports one fixed-depth radix path replacement.
type ChunkTreeMutation struct {
	Root         PageRef
	Retired      [8]PageRef
	RetiredCount uint8
	Found        bool
	Changed      bool
}

func (m *ChunkTreeMutation) retire(ref PageRef) error {
	if int(m.RetiredCount) == len(m.Retired) {
		return ErrKeyTreeDepth
	}
	m.Retired[m.RetiredCount] = ref
	m.RetiredCount++
	return nil
}

// LookupChunkTree resolves one logical chunk to its immutable document extent.
func LookupChunkTree(cache *PageCache, root PageRef, chunkID uint32, bounds ChunkTreeBounds) (PageRef, bool, error) {
	if root == (PageRef{}) {
		return PageRef{}, false, nil
	}
	if cache == nil {
		return PageRef{}, false, fmt.Errorf("%w: nil chunk-tree cache", ErrInvalidWrite)
	}
	ref := root
	for expectedShift := uint8(30); ; expectedShift -= chunkDirectoryRadixBits {
		lease, err := cache.Acquire(ref)
		if err != nil {
			return PageRef{}, false, err
		}
		view, err := OpenChunkDirectoryPage(lease.Page(), bounds.FileEnd, bounds.NextLogicalID)
		if err != nil {
			lease.Release()
			return PageRef{}, false, err
		}
		if view.Header().Shift != expectedShift || view.Header().Prefix != chunkDirectoryPrefix(chunkID, expectedShift) {
			lease.Release()
			return PageRef{}, false, ErrChunkDirectoryCorrupt
		}
		next, ok := view.Lookup(chunkID)
		lease.Release()
		if !ok {
			return PageRef{}, false, nil
		}
		if expectedShift == 0 {
			return next, true, nil
		}
		ref = next
	}
}

// UpsertChunkTree maps chunkID to one document extent.
func UpsertChunkTree(cache *PageCache, tx *WriteTransaction, root PageRef, chunkID uint32, document PageRef, bounds ChunkTreeBounds) (ChunkTreeMutation, error) {
	return mutateChunkTree(cache, tx, root, chunkID, document, false, bounds)
}

// DeleteChunkTree removes one logical chunk mapping.
func DeleteChunkTree(cache *PageCache, tx *WriteTransaction, root PageRef, chunkID uint32, bounds ChunkTreeBounds) (ChunkTreeMutation, error) {
	return mutateChunkTree(cache, tx, root, chunkID, PageRef{}, true, bounds)
}

type chunkTreeRewrite struct {
	ref     PageRef
	found   bool
	changed bool
	empty   bool
}

func mutateChunkTree(cache *PageCache, tx *WriteTransaction, root PageRef, chunkID uint32, document PageRef, deleting bool, bounds ChunkTreeBounds) (ChunkTreeMutation, error) {
	var mutation ChunkTreeMutation
	if tx == nil || !tx.active || cache == nil && root != (PageRef{}) {
		return mutation, fmt.Errorf("%w: chunk-tree mutation", ErrInvalidWrite)
	}
	if !deleting && !validChunkTreeDocumentRef(tx, document) {
		return mutation, fmt.Errorf("%w: chunk document reference", ErrInvalidWrite)
	}
	if root == (PageRef{}) {
		if deleting {
			return mutation, nil
		}
		ref, err := buildChunkTreePath(tx, chunkID, document, 30)
		if err != nil {
			return mutation, err
		}
		mutation.Root = ref
		mutation.Changed = true
		return mutation, nil
	}
	result, err := rewriteChunkTreePage(cache, tx, root, chunkID, document, deleting, bounds, 30, &mutation)
	if err != nil {
		return mutation, err
	}
	mutation.Root = result.ref
	mutation.Found = result.found
	mutation.Changed = result.changed
	return mutation, nil
}

func rewriteChunkTreePage(cache *PageCache, tx *WriteTransaction, oldRef PageRef, chunkID uint32, document PageRef, deleting bool, bounds ChunkTreeBounds, expectedShift uint8, mutation *ChunkTreeMutation) (chunkTreeRewrite, error) {
	lease, err := cache.Acquire(oldRef)
	if err != nil {
		return chunkTreeRewrite{}, err
	}
	defer lease.Release()
	view, err := OpenChunkDirectoryPage(lease.Page(), bounds.FileEnd, bounds.NextLogicalID)
	if err != nil {
		return chunkTreeRewrite{}, err
	}
	header := view.Header()
	if header.Shift != expectedShift || header.Prefix != chunkDirectoryPrefix(chunkID, expectedShift) {
		return chunkTreeRewrite{}, ErrChunkDirectoryCorrupt
	}
	lane := uint8(chunkID >> expectedShift & 63)
	bit := uint64(1) << lane
	rank := bits.OnesCount64(header.Bitmap & (bit - 1))
	var refs [65]PageRef
	count := view.Len()
	for i := 0; i < count; i++ {
		refs[i], _ = view.RefAt(i)
	}
	found := header.Bitmap&bit != 0
	if expectedShift == 0 {
		if deleting {
			if !found {
				return chunkTreeRewrite{ref: oldRef}, nil
			}
			copy(refs[rank:], refs[rank+1:count])
			count--
			header.Bitmap &^= bit
		} else if found {
			refs[rank] = document
		} else {
			copy(refs[rank+1:], refs[rank:count])
			refs[rank] = document
			count++
			header.Bitmap |= bit
		}
	} else {
		if !found {
			if deleting {
				return chunkTreeRewrite{ref: oldRef}, nil
			}
			child, err := buildChunkTreePath(tx, chunkID, document, expectedShift-chunkDirectoryRadixBits)
			if err != nil {
				return chunkTreeRewrite{}, err
			}
			copy(refs[rank+1:], refs[rank:count])
			refs[rank] = child
			count++
			header.Bitmap |= bit
		} else {
			child, err := rewriteChunkTreePage(cache, tx, refs[rank], chunkID, document, deleting, bounds, expectedShift-chunkDirectoryRadixBits, mutation)
			if err != nil {
				return chunkTreeRewrite{}, err
			}
			if !child.changed {
				return chunkTreeRewrite{ref: oldRef, found: child.found}, nil
			}
			if child.empty {
				copy(refs[rank:], refs[rank+1:count])
				count--
				header.Bitmap &^= bit
			} else {
				refs[rank] = child.ref
			}
			found = child.found
		}
	}
	if err := mutation.retire(oldRef); err != nil {
		return chunkTreeRewrite{}, err
	}
	if count == 0 {
		return chunkTreeRewrite{found: found, changed: true, empty: true}, nil
	}
	page, err := encodeChunkTreeNode(tx, oldRef.LogicalID, header.Prefix, header.Bitmap, expectedShift, refs[:count])
	if err != nil {
		return chunkTreeRewrite{}, err
	}
	return chunkTreeRewrite{ref: page.Ref(), found: found, changed: true}, nil
}

func buildChunkTreePath(tx *WriteTransaction, chunkID uint32, document PageRef, maxShift uint8) (PageRef, error) {
	shift := uint8(0)
	lane := uint8(chunkID >> shift & 63)
	child := document
	for {
		refs := [1]PageRef{child}
		page, err := encodeChunkTreeNode(tx, 0, chunkDirectoryPrefix(chunkID, shift), uint64(1)<<lane, shift, refs[:])
		if err != nil {
			return PageRef{}, err
		}
		child = page.Ref()
		if shift == maxShift {
			return child, nil
		}
		shift += chunkDirectoryRadixBits
		lane = uint8(chunkID >> shift & 63)
	}
}

func encodeChunkTreeNode(tx *WriteTransaction, logicalID uint64, prefix uint32, bitmap uint64, shift uint8, refs []PageRef) (TransactionPage, error) {
	page, err := tx.Allocate(PageChunkDirectory, tx.options.PageSize, logicalID)
	if err != nil {
		return TransactionPage{}, err
	}
	header := ChunkDirectoryHeader{
		StoreID: tx.options.StoreID, Generation: tx.options.Generation,
		LogicalID: page.Ref().LogicalID, PageSize: page.Ref().Length,
		Prefix: prefix, Bitmap: bitmap, Shift: shift,
	}
	if _, err := EncodeChunkDirectoryPage(page.Bytes(), header, refs, tx.FileEnd(), tx.NextLogicalID()); err != nil {
		return TransactionPage{}, err
	}
	if err := page.Stage(); err != nil {
		return TransactionPage{}, err
	}
	return page, nil
}

func validChunkTreeDocumentRef(tx *WriteTransaction, ref PageRef) bool {
	quantum := uint64(tx.options.PageSize)
	length := uint64(ref.Length)
	return ref.Kind == PageDocument && ref.Flags == 0 && validPhysicalPageSize(ref.Length) &&
		ref.Length >= tx.options.PageSize && ref.Length%tx.options.PageSize == 0 &&
		ref.Generation != 0 && ref.Generation <= tx.options.Generation &&
		ref.LogicalID > StateRootLogicalID && ref.LogicalID < tx.NextLogicalID() &&
		ref.Offset >= uint64(superblockCopies)*quantum && ref.Offset%quantum == 0 &&
		length <= tx.FileEnd() && ref.Offset <= tx.FileEnd()-length
}
