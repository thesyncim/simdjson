package simdjson

// Persistent 32-way hash trie for the mutable store's key directory.
//
// Each mutation path-copies at most thirteen nodes (five hash bits per level
// over a 64-bit keyed hash); every untouched subtree is shared by old and new
// snapshots. Full-hash collisions are held in a tiny immutable leaf chain and
// still compare the complete key, so a collision cannot produce a false hit.
// The store hashes with a per-instance maphash seed, making attacker-chosen
// collision chains infeasible without weakening deterministic JSON indexes.

const storeTrieBits = 5

type storeLocation struct {
	chunk uint32
	slot  uint8
}

type storeKeyLeaf struct {
	hash uint64
	key  string
	loc  storeLocation
	next *storeKeyLeaf
}

type storeKeySlot struct {
	child *storeKeyNode
	leaf  *storeKeyLeaf
}

type storeKeyNode struct {
	slots [1 << storeTrieBits]storeKeySlot
}

func storeKeyLookup(root *storeKeyNode, hash uint64, key string) (storeLocation, bool) {
	for shift := uint(0); root != nil; shift += storeTrieBits {
		slot := root.slots[(hash>>shift)&31]
		for leaf := slot.leaf; leaf != nil; leaf = leaf.next {
			if leaf.hash == hash && leaf.key == key {
				return leaf.loc, true
			}
		}
		root = slot.child
	}
	return storeLocation{}, false
}

// storeKeyInsert returns a new root containing key. The caller has already
// established whether this is an insert or a location replacement.
func storeKeyInsert(root *storeKeyNode, hash uint64, key string, loc storeLocation) *storeKeyNode {
	return storeKeyInsertAt(root, 0, &storeKeyLeaf{hash: hash, key: key, loc: loc})
}

func storeKeyInsertAt(root *storeKeyNode, shift uint, add *storeKeyLeaf) *storeKeyNode {
	var out storeKeyNode
	if root != nil {
		out = *root
	}
	i := (add.hash >> shift) & 31
	slot := out.slots[i]
	if slot.child != nil {
		slot.child = storeKeyInsertAt(slot.child, shift+storeTrieBits, add)
		out.slots[i] = slot
		return &out
	}
	if slot.leaf == nil {
		slot.leaf = add
		out.slots[i] = slot
		return &out
	}

	// A complete hash collision remains a leaf chain. Copy the chain so an
	// existing key's location can change without mutating an older snapshot.
	if slot.leaf.hash == add.hash {
		slot.leaf = storeLeafInsert(slot.leaf, add)
		out.slots[i] = slot
		return &out
	}

	// Two distinct hashes share this prefix. Push the existing chain and the
	// new leaf into a child; recursion stops at their first differing group.
	child := (*storeKeyNode)(nil)
	for leaf := slot.leaf; leaf != nil; leaf = leaf.next {
		child = storeKeyInsertAt(child, shift+storeTrieBits, &storeKeyLeaf{
			hash: leaf.hash,
			key:  leaf.key,
			loc:  leaf.loc,
		})
	}
	child = storeKeyInsertAt(child, shift+storeTrieBits, add)
	out.slots[i] = storeKeySlot{child: child}
	return &out
}

func storeLeafInsert(head, add *storeKeyLeaf) *storeKeyLeaf {
	if head == nil {
		return add
	}
	if head.key == add.key {
		return &storeKeyLeaf{hash: add.hash, key: add.key, loc: add.loc, next: head.next}
	}
	return &storeKeyLeaf{
		hash: head.hash,
		key:  head.key,
		loc:  head.loc,
		next: storeLeafInsert(head.next, add),
	}
}

func storeKeyDelete(root *storeKeyNode, hash uint64, key string) *storeKeyNode {
	out, _ := storeKeyDeleteAt(root, 0, hash, key)
	return out
}

func storeKeyDeleteAt(root *storeKeyNode, shift uint, hash uint64, key string) (*storeKeyNode, bool) {
	if root == nil {
		return nil, false
	}
	i := (hash >> shift) & 31
	slot := root.slots[i]
	var changed bool
	if slot.child != nil {
		slot.child, changed = storeKeyDeleteAt(slot.child, shift+storeTrieBits, hash, key)
		if !changed {
			return root, false
		}
		if leaf, ok := storeKeySingleton(slot.child); ok {
			slot = storeKeySlot{leaf: leaf}
		}
	} else {
		slot.leaf, changed = storeLeafDelete(slot.leaf, hash, key)
		if !changed {
			return root, false
		}
	}
	out := *root
	out.slots[i] = slot
	if storeKeyNodeEmpty(&out) {
		return nil, true
	}
	return &out, true
}

func storeLeafDelete(head *storeKeyLeaf, hash uint64, key string) (*storeKeyLeaf, bool) {
	if head == nil {
		return nil, false
	}
	if head.hash == hash && head.key == key {
		return head.next, true
	}
	next, changed := storeLeafDelete(head.next, hash, key)
	if !changed {
		return head, false
	}
	return &storeKeyLeaf{hash: head.hash, key: head.key, loc: head.loc, next: next}, true
}

func storeKeyNodeEmpty(node *storeKeyNode) bool {
	for i := range node.slots {
		if node.slots[i].child != nil || node.slots[i].leaf != nil {
			return false
		}
	}
	return true
}

// storeKeySingleton permits deletion to collapse a child with one occupied
// leaf slot back into its parent. A full-hash collision chain is one leaf slot
// and can be shared there unchanged.
func storeKeySingleton(node *storeKeyNode) (*storeKeyLeaf, bool) {
	if node == nil {
		return nil, false
	}
	var one *storeKeyLeaf
	for i := range node.slots {
		slot := node.slots[i]
		if slot.child != nil {
			return nil, false
		}
		if slot.leaf != nil {
			if one != nil {
				return nil, false
			}
			one = slot.leaf
		}
	}
	return one, one != nil
}
