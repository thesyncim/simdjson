package simdjson

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"math"
	"math/bits"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/thesyncim/simdjson/document"
	"github.com/thesyncim/simdjson/internal/byteview"
	"github.com/thesyncim/simdjson/internal/storeio"
)

// WriteFileStore creates one compact, mutable FileStore generation in an
// empty file. Unlike repeated Put calls, bulk creation writes each live
// document, directory node, posting stream, and TTL record exactly once, then
// publishes one double-root durability fence. The resulting file opens with
// [OpenFileStore] and supports the ordinary update, delete, index, and TTL
// operations immediately.
//
// The method borrows the selected immutable Store state while writing. It
// does not retain source slices, and file remains owned by the caller.
// Indexes are rebuilt from options.Indexes even when the source Store has no
// corresponding heap index. A failed call may leave an unpublished partial
// file; as with [Store.WritePageFile], discard it instead of retrying in place.
func (s *Store) WriteFileStore(file *os.File, options FileStoreOptions) (int64, error) {
	if s == nil || file == nil {
		return 0, fmt.Errorf("simdjson: WriteFileStore requires non-nil Store and file")
	}
	info, err := file.Stat()
	if err != nil {
		return 0, err
	}
	if info.Size() != 0 {
		return 0, ErrFileStoreNotEmpty
	}
	normalized, err := options.normalized()
	if err != nil {
		return 0, err
	}

	s.mu.Lock()
	state := s.state.Load()
	if state == nil {
		sourceOptions, normalizeErr := s.Options.normalized()
		if normalizeErr != nil {
			s.mu.Unlock()
			return 0, normalizeErr
		}
		state = &storeState{options: sourceOptions}
	}
	rows, collectErr := collectFileStoreBulkRows(state, &s.ttl, normalized)
	s.mu.Unlock()
	if collectErr != nil {
		return 0, collectErr
	}

	var storeID [16]byte
	if _, err := rand.Read(storeID[:]); err != nil {
		return 0, fmt.Errorf("simdjson: create FileStore identity: %w", err)
	}
	build := fileStoreBulkBuild{
		source: state, rows: rows, options: normalized, storeID: storeID,
		allocator: fileStoreBulkAllocator{
			offset:      2 * uint64(normalized.PageSize),
			nextLogical: storeio.StateRootLogicalID + 1,
			generation:  1,
			pageSize:    uint32(normalized.PageSize),
		},
	}
	if err := build.plan(); err != nil {
		return 0, err
	}
	if err := build.write(file); err != nil {
		return 0, err
	}
	return int64(build.fileEnd), nil
}

type fileStoreBulkRow struct {
	sourceChunk  uint32
	sourceSlot   uint8
	deadline     int64
	overflowBase int
	overflowN    int
}

func collectFileStoreBulkRows(state *storeState, ttl *storeTTLState, options normalizedFileStoreOptions) ([]fileStoreBulkRow, error) {
	if state.count < 0 || uint64(state.count) > uint64(^uint32(0))*uint64(options.Store.ChunkDocuments) {
		return nil, ErrStoreTooLarge
	}
	rows := make([]fileStoreBulkRow, 0, state.count)
	var collectErr error
	state.chunks.each(func(chunkID uint32, chunk *storeChunk) bool {
		for live := chunk.live; live != 0; live &= live - 1 {
			slot := uint8(bits.TrailingZeros64(live))
			key := chunk.key(int(slot))
			raw := chunk.docs.rawAt(int(chunk.ord[slot]))
			if len(key) > options.MaxKeyBytes {
				collectErr = ErrFileStoreKeyTooLarge
				return false
			}
			if len(raw) > options.MaxDocumentBytes {
				collectErr = ErrFileStoreDocumentTooLarge
				return false
			}
			row := fileStoreBulkRow{sourceChunk: chunkID, sourceSlot: slot, overflowBase: -1}
			if ttl != nil {
				if position, ok := ttl.pos[storeTTLKeyOf(storeLocation{chunk: chunkID, slot: slot})]; ok {
					deadline := ttl.heap[position].deadline.time()
					nanos := deadline.UnixNano()
					if nanos == 0 || !time.Unix(0, nanos).Equal(deadline) {
						collectErr = ErrFileStoreDeadlineRange
						return false
					}
					row.deadline = nanos
				}
			}
			rows = append(rows, row)
		}
		return true
	})
	if collectErr != nil {
		return nil, collectErr
	}
	if len(rows) != state.count {
		return nil, fmt.Errorf("simdjson: FileStore bulk source count invariant")
	}
	return rows, nil
}

type fileStoreBulkAllocator struct {
	offset      uint64
	nextLogical uint64
	generation  uint64
	pageSize    uint32
}

func (a *fileStoreBulkAllocator) allocate(kind storeio.PageKind, length uint32) (storeio.PageRef, error) {
	if length < a.pageSize || length%a.pageSize != 0 || a.nextLogical == 0 ||
		a.nextLogical == math.MaxUint64 || a.offset > math.MaxInt64-uint64(length) {
		return storeio.PageRef{}, ErrStorePersistTooLarge
	}
	ref := storeio.PageRef{
		Offset: a.offset, LogicalID: a.nextLogical, Generation: a.generation,
		Length: length, Kind: kind,
	}
	a.offset += uint64(length)
	a.nextLogical++
	return ref, nil
}

func (a *fileStoreBulkAllocator) allocateStateRoot() (storeio.PageRef, error) {
	if a.offset > math.MaxInt64-uint64(a.pageSize) {
		return storeio.PageRef{}, ErrStorePersistTooLarge
	}
	ref := storeio.PageRef{
		Offset: a.offset, LogicalID: storeio.StateRootLogicalID, Generation: a.generation,
		Length: a.pageSize, Kind: storeio.PageStateRoot,
	}
	a.offset += uint64(a.pageSize)
	return ref, nil
}

type fileStoreBulkOverflowPlan struct {
	row        int
	start, end int
	ref, next  storeio.PageRef
}

type fileStoreBulkDocumentPlan struct {
	first, last int
	chunk       uint32
	live        uint64
	ref         storeio.PageRef
}

type fileStoreBulkKeyPlan struct {
	level       uint8
	first, last int
	children    []fileStoreBulkKeyChild
	ref         storeio.PageRef
}

type fileStoreBulkKeyChild struct {
	lower int
	ref   storeio.PageRef
}

type fileStoreBulkPostingMask struct {
	key        storeio.IndexDirectoryKey
	bits       uint64
	certStart  uint32
	certLength uint16
	collision  bool
}

type fileStoreBulkPostingPlan struct {
	indexID     uint32
	first, last int
	ref         storeio.PageRef
}

type fileStoreBulkIndexPlan struct {
	level       uint8
	first, last int
	children    []storeio.IndexDirectoryChild
	ref         storeio.PageRef
}

type fileStoreBulkTTLPlan struct {
	level       uint8
	first, last int
	children    []storeio.TTLDirectoryChild
	ref         storeio.PageRef
}

type fileStoreBulkBuild struct {
	source  *storeState
	rows    []fileStoreBulkRow
	options normalizedFileStoreOptions
	storeID [16]byte

	allocator fileStoreBulkAllocator
	fileEnd   uint64

	overflows         []fileStoreBulkOverflowPlan
	documents         []fileStoreBulkDocumentPlan
	chunks            []storeChunkDirectoryPlan
	keys              []fileStoreBulkKeyPlan
	keyOrder          []int
	postings          []fileStoreBulkPostingPlan
	masks             []fileStoreBulkPostingMask
	indexes           []fileStoreBulkIndexPlan
	indexRows         []storeio.IndexDirectoryEntry
	indexCertificates []byte
	ttls              []fileStoreBulkTTLPlan
	ttlRows           []storeio.TTLKey

	chunkRoot storeio.PageRef
	keyRoot   storeio.PageRef
	indexRoot storeio.PageRef
	ttlRoot   storeio.PageRef
	stateRef  storeio.PageRef
	root      storeio.StateRoot
}

func (b *fileStoreBulkBuild) sourceRow(row int) (*storeChunk, string, []byte) {
	entry := b.rows[row]
	chunk := b.source.chunks.get(entry.sourceChunk)
	return chunk, chunk.key(int(entry.sourceSlot)), chunk.docs.rawAt(int(chunk.ord[entry.sourceSlot]))
}

func (b *fileStoreBulkBuild) sourceFloat64(row, column int) (float64, bool, error) {
	entry := b.rows[row]
	chunk := b.source.chunks.get(entry.sourceChunk)
	sourceRow := [1]int{int(chunk.ord[entry.sourceSlot])}
	var storage [1]RawValue
	values, err := chunk.docs.AppendPointerRows(
		storage[:0], sourceRow[:], b.options.float64Columns[column].pointer,
	)
	if err != nil || len(values) != 1 {
		return 0, false, err
	}
	value, ok := values[0].Float64()
	if !ok || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, false, nil
	}
	return value, true, nil
}

func (b *fileStoreBulkBuild) documentFloat64Bytes(first, last int) (int, error) {
	bytes := len(b.options.float64Columns) * 8
	for column := range b.options.float64Columns {
		for row := first; row < last; row++ {
			_, ok, err := b.sourceFloat64(row, column)
			if err != nil {
				return 0, err
			}
			if ok {
				bytes += 8
			}
		}
	}
	return bytes, nil
}

func (b *fileStoreBulkBuild) targetLocation(row int) storeio.KeyLocation {
	chunkDocuments := b.options.Store.ChunkDocuments
	return storeio.KeyLocation{
		Chunk: uint32(row / chunkDocuments), Slot: uint8(row % chunkDocuments),
		Deadline: b.rows[row].deadline,
	}
}

func (b *fileStoreBulkBuild) plan() error {
	if err := b.planDocuments(); err != nil {
		return err
	}
	items := make([]storeChunkDirectoryItem, len(b.documents))
	for i := range b.documents {
		items[i] = storeChunkDirectoryItem{id: b.documents[i].chunk, ref: b.documents[i].ref}
	}
	var err error
	b.chunks, b.chunkRoot, err = planFileStoreBulkChunkDirectories(items, &b.allocator)
	if err != nil {
		return err
	}
	if err := b.planKeys(); err != nil {
		return err
	}
	if err := b.planPostings(); err != nil {
		return err
	}
	if err := b.planIndexTree(); err != nil {
		return err
	}
	if err := b.planTTLTree(); err != nil {
		return err
	}
	stateRef, err := b.allocator.allocateStateRoot()
	if err != nil {
		return err
	}
	b.stateRef = stateRef
	b.fileEnd = b.allocator.offset

	chunkHighWater := uint32(len(b.documents))
	freeChunkHint := chunkHighWater
	if len(b.rows) != 0 && len(b.rows)%b.options.Store.ChunkDocuments != 0 {
		freeChunkHint--
	}
	b.root = storeio.StateRoot{
		StoreID: b.storeID, Generation: b.allocator.generation, PageSize: b.allocator.pageSize,
		DocumentCount: uint64(len(b.rows)), TTLCount: uint64(len(b.ttlRows)),
		NextLogicalID: b.allocator.nextLogical, ChunkHighWater: chunkHighWater,
		LiveChunks: chunkHighWater, ChunkDocuments: uint32(b.options.Store.ChunkDocuments),
		IndexCount: uint32(len(b.options.indexes)), IndexCatalogHash: b.options.indexCatalogHash,
		IndexMaxDepth: uint32(max(b.options.Store.IndexOptions.MaxDepth, 0)),
		FreeChunkHint: freeChunkHint, ChunkDirectory: b.chunkRoot, KeyDirectory: b.keyRoot,
		IndexDirectory: b.indexRoot, TTLDirectory: b.ttlRoot,
	}
	if len(b.options.float64Columns) != 0 {
		b.root.Options |= storeio.StateOptionFloat64Columns
	}
	return nil
}

// FileStore chunk lookup has a fixed six-node radix path (shifts
// 30,24,18,12,6,0). The read-only page checkpoint may stop at a shallower
// root, so its otherwise shared planner cannot be used here.
func planFileStoreBulkChunkDirectories(items []storeChunkDirectoryItem, allocator *fileStoreBulkAllocator) ([]storeChunkDirectoryPlan, storeio.PageRef, error) {
	if len(items) == 0 {
		return nil, storeio.PageRef{}, nil
	}
	all := make([]storeChunkDirectoryPlan, 0, (len(items)+62)/63+5)
	for shift := uint8(0); ; shift += 6 {
		next := make([]storeChunkDirectoryItem, 0, (len(items)+63)/64)
		for start := 0; start < len(items); {
			covered := uint(shift) + 6
			group := items[start].id
			if covered < 32 {
				group >>= covered
			} else {
				group = 0
			}
			end := start + 1
			for end < len(items) {
				other := items[end].id
				if covered < 32 {
					other >>= covered
				} else {
					other = 0
				}
				if other != group {
					break
				}
				end++
			}
			children := make([]storeio.PageRef, end-start)
			var bitmap uint64
			for i := start; i < end; i++ {
				bitmap |= uint64(1) << uint(items[i].id>>shift&63)
				children[i-start] = items[i].ref
			}
			prefix := items[start].id
			if covered < 32 {
				prefix &^= uint32(1)<<covered - 1
			} else {
				prefix = 0
			}
			ref, err := allocator.allocate(storeio.PageChunkDirectory, allocator.pageSize)
			if err != nil {
				return nil, storeio.PageRef{}, err
			}
			all = append(all, storeChunkDirectoryPlan{
				prefix: prefix, shift: shift, bitmap: bitmap, children: children, ref: ref,
			})
			next = append(next, storeChunkDirectoryItem{id: prefix, ref: ref})
			start = end
		}
		if shift == 30 {
			if len(next) != 1 {
				return nil, storeio.PageRef{}, fmt.Errorf(
					"%w: FileStore chunk radix root", storeio.ErrInvalidWrite,
				)
			}
			return all, next[0].ref, nil
		}
		items = next
	}
}

func (b *fileStoreBulkBuild) planDocuments() error {
	if len(b.rows) == 0 {
		return nil
	}
	chunkDocuments := b.options.Store.ChunkDocuments
	chunkCount := (len(b.rows) + chunkDocuments - 1) / chunkDocuments
	if uint64(chunkCount) > uint64(^uint32(0)) {
		return ErrStoreTooLarge
	}
	overflowPayload := b.options.MaxPageSize - storeio.PageHeaderSize -
		storeio.PageTrailerSize - storeio.OverflowPagePayloadHeaderSize
	b.documents = make([]fileStoreBulkDocumentPlan, 0, chunkCount)
	for first := 0; first < len(b.rows); first += chunkDocuments {
		last := min(first+chunkDocuments, len(b.rows))
		chunkID := uint32(first / chunkDocuments)
		required := storeio.PageHeaderSize + storeio.PageTrailerSize +
			storeio.DocumentPagePayloadHeaderSize + (last-first)*storeio.DocumentPageRecordSize
		for row := first; row < last; row++ {
			_, key, raw := b.sourceRow(row)
			required += len(key) + len(raw)
		}
		float64Bytes, err := b.documentFloat64Bytes(first, last)
		if err != nil {
			return err
		}
		required += float64Bytes
		// InlineValueBytes is the ordinary online-write threshold, not a
		// format limit. A compact generation instead keeps complete values in
		// the document extent while the chunk fits, avoiding a 64 KiB overflow
		// extent for a value that only pushed one 4 KiB chunk to 8 KiB. If the
		// chunk is genuinely too large, spill the largest remaining value
		// first; this reaches the bound with the fewest overflow chains.
		var overflowMask uint64
		for required > b.options.MaxPageSize {
			largestRow, largestBytes := -1, -1
			for row := first; row < last; row++ {
				bit := uint64(1) << uint(row-first)
				if overflowMask&bit != 0 {
					continue
				}
				_, _, raw := b.sourceRow(row)
				if len(raw) > largestBytes {
					largestRow, largestBytes = row, len(raw)
				}
			}
			if largestRow < 0 {
				return ErrFileStoreDocumentTooLarge
			}
			overflowMask |= uint64(1) << uint(largestRow-first)
			required -= largestBytes
			required += storeio.DocumentOverflowDescriptorSize
		}
		for row := first; row < last; row++ {
			if overflowMask&(uint64(1)<<uint(row-first)) == 0 {
				continue
			}
			_, _, raw := b.sourceRow(row)
			base := len(b.overflows)
			for start := 0; start < len(raw); start += overflowPayload {
				end := min(start+overflowPayload, len(raw))
				ref, err := b.allocator.allocate(storeio.PageOverflow, uint32(b.options.MaxPageSize))
				if err != nil {
					return err
				}
				b.overflows = append(b.overflows, fileStoreBulkOverflowPlan{
					row: row, start: start, end: end, ref: ref,
				})
			}
			b.rows[row].overflowBase = base
			b.rows[row].overflowN = len(b.overflows) - base
			for i := base; i+1 < len(b.overflows); i++ {
				b.overflows[i].next = b.overflows[i+1].ref
			}
		}
		pageSize, ok := fileStoreBulkExtent(required, b.options.PageSize, b.options.MaxPageSize)
		if !ok {
			return ErrFileStoreDocumentTooLarge
		}
		ref, err := b.allocator.allocate(storeio.PageDocument, pageSize)
		if err != nil {
			return err
		}
		count := last - first
		live := ^uint64(0)
		if count < 64 {
			live = uint64(1)<<uint(count) - 1
		}
		b.documents = append(b.documents, fileStoreBulkDocumentPlan{
			first: first, last: last, chunk: chunkID, live: live, ref: ref,
		})
	}
	return nil
}

func fileStoreBulkExtent(required, minimum, maximum int) (uint32, bool) {
	if required < 0 || required > maximum {
		return 0, false
	}
	size := minimum
	for size < required {
		if size > maximum/2 {
			return 0, false
		}
		size <<= 1
	}
	return uint32(size), true
}

func (b *fileStoreBulkBuild) planKeys() error {
	if len(b.rows) == 0 {
		return nil
	}
	b.keyOrder = make([]int, len(b.rows))
	for i := range b.keyOrder {
		b.keyOrder[i] = i
	}
	slices.SortFunc(b.keyOrder, func(a, c int) int {
		_, ak, _ := b.sourceRow(a)
		_, ck, _ := b.sourceRow(c)
		return strings.Compare(ak, ck)
	})
	for i := 1; i < len(b.keyOrder); i++ {
		_, previous, _ := b.sourceRow(b.keyOrder[i-1])
		_, current, _ := b.sourceRow(b.keyOrder[i])
		if previous == current {
			return fmt.Errorf("%w %q", ErrStoreDuplicateKey, current)
		}
	}

	levelStart := 0
	for first := 0; first < len(b.keyOrder); {
		used := storeio.PageHeaderSize + storeio.PageTrailerSize + storeio.KeyDirectoryPayloadHeaderSize
		last := first
		for last < len(b.keyOrder) {
			_, key, _ := b.sourceRow(b.keyOrder[last])
			next := used + storeio.KeyDirectoryLeafRecordSize + len(key)
			if next > b.options.PageSize {
				break
			}
			used = next
			last++
		}
		if last == first {
			return ErrFileStoreKeyTooLarge
		}
		ref, err := b.allocator.allocate(storeio.PageKeyDirectory, b.allocator.pageSize)
		if err != nil {
			return err
		}
		b.keys = append(b.keys, fileStoreBulkKeyPlan{first: first, last: last, ref: ref})
		first = last
	}
	levelEnd := len(b.keys)
	for level := uint8(1); levelEnd-levelStart > 1; level++ {
		if level > 10 {
			return storeio.ErrKeyTreeDepth
		}
		nextStart := len(b.keys)
		for first := levelStart; first < levelEnd; {
			used := storeio.PageHeaderSize + storeio.PageTrailerSize + storeio.KeyDirectoryPayloadHeaderSize
			children := make([]fileStoreBulkKeyChild, 0, min(64, levelEnd-first))
			for last := first; last < levelEnd && len(children) < 64; last++ {
				lower := b.keyPlanLower(b.keys[last])
				_, key, _ := b.sourceRow(lower)
				next := used + storeio.KeyDirectoryBranchRecordSize + len(key)
				if next > b.options.PageSize {
					break
				}
				used = next
				children = append(children, fileStoreBulkKeyChild{lower: lower, ref: b.keys[last].ref})
			}
			if len(children) == 0 {
				return ErrFileStoreKeyTooLarge
			}
			ref, err := b.allocator.allocate(storeio.PageKeyDirectory, b.allocator.pageSize)
			if err != nil {
				return err
			}
			b.keys = append(b.keys, fileStoreBulkKeyPlan{level: level, children: children, ref: ref})
			first += len(children)
		}
		levelStart, levelEnd = nextStart, len(b.keys)
	}
	b.keyRoot = b.keys[levelStart].ref
	return nil
}

func (b *fileStoreBulkBuild) keyPlanLower(plan fileStoreBulkKeyPlan) int {
	if plan.level == 0 {
		return b.keyOrder[plan.first]
	}
	return plan.children[0].lower
}

func (b *fileStoreBulkBuild) planPostings() error {
	if len(b.options.indexes) == 0 || len(b.rows) == 0 {
		return nil
	}
	if len(b.rows) > int(^uint(0)>>1)/len(b.options.indexes) {
		return ErrStoreTooLarge
	}
	b.masks = make([]fileStoreBulkPostingMask, 0, len(b.rows)*len(b.options.indexes))
	var textScratch []byte
	for row := range b.rows {
		chunk, _, _ := b.sourceRow(row)
		location := b.targetLocation(row)
		for indexID, exact := range b.options.indexes {
			hash, ok, values, scratch, err := fileStoreBulkTupleHash(
				exact, chunk, int(b.rows[row].sourceSlot), textScratch[:0],
			)
			textScratch = scratch
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			mask := fileStoreBulkPostingMask{
				key: storeio.IndexDirectoryKey{
					IndexID: uint32(indexID), TupleHash: hash, Chunk: location.Chunk,
				},
				bits: uint64(1) << location.Slot,
			}
			maxCertificate := b.options.PageSize - storeio.PageHeaderSize -
				storeio.PageTrailerSize - storeio.PostingPagePayloadHeaderSize -
				storeio.PostingSegmentHeaderSize - 16
			certificateStart := len(b.indexCertificates)
			certificates, certified := appendFileIndexCertificate(
				b.indexCertificates, values[:exact.n], maxCertificate,
			)
			certificateLength := len(certificates) - certificateStart
			if certified && certificateStart <= int(^uint32(0)) &&
				certificateLength <= int(^uint16(0)) {
				b.indexCertificates = certificates
				mask.certStart = uint32(certificateStart)
				mask.certLength = uint16(certificateLength)
			}
			b.masks = append(b.masks, mask)
		}
	}
	slices.SortFunc(b.masks, func(a, c fileStoreBulkPostingMask) int {
		return compareFileStoreBulkIndexKey(a.key, c.key)
	})
	if len(b.masks) != 0 {
		out := b.masks[:1]
		for _, entry := range b.masks[1:] {
			last := &out[len(out)-1]
			if compareFileStoreBulkIndexKey(last.key, entry.key) == 0 {
				last.bits |= entry.bits
				if last.certLength != 0 && entry.certLength != 0 {
					left := b.indexCertificates[last.certStart : last.certStart+uint32(last.certLength) : last.certStart+uint32(last.certLength)]
					right := b.indexCertificates[entry.certStart : entry.certStart+uint32(entry.certLength) : entry.certStart+uint32(entry.certLength)]
					columns := int(b.options.indexes[last.key.IndexID].n)
					if !fileIndexCertificatesEqual(left, right, columns) {
						last.collision = true
					}
				} else {
					last.certStart, last.certLength, last.collision = 0, 0, false
				}
				continue
			}
			out = append(out, entry)
		}
		clear(b.masks[len(out):])
		b.masks = out
	}

	payloadLimit := b.options.PageSize - storeio.PageHeaderSize - storeio.PageTrailerSize
	// The compact generation densely packs streams from one declared index.
	// Directory refs mark these pages as an immutable base: an online mutation
	// redirects only its changed stream to an isolated delta page and never
	// retires shared base storage. Repeated churn therefore plateaus without a
	// durable page-level reference-count tree.
	for first := 0; first < len(b.masks); {
		indexID := b.masks[first].key.IndexID
		used := storeio.PostingPagePayloadHeaderSize
		last := first
		for last < len(b.masks) && b.masks[last].key.IndexID == indexID {
			entry := storeio.PostingEntry{Chunk: b.masks[last].key.Chunk, Bits: b.masks[last].bits}
			encoded, err := storeio.PostingEntryEncodedSize(entry.Chunk, entry, true)
			if err != nil {
				return err
			}
			next := used + storeio.PostingSegmentHeaderSize + encoded +
				int(b.masks[last].certLength)
			if next > payloadLimit {
				break
			}
			used = next
			last++
		}
		if last == first {
			return storeio.ErrInvalidWrite
		}
		ref, err := b.allocator.allocate(storeio.PageIndexPosting, b.allocator.pageSize)
		if err != nil {
			return err
		}
		for position := first; position < last; position++ {
			b.indexRows = append(b.indexRows, storeio.IndexDirectoryEntry{
				Key: b.masks[position].key,
				Posting: storeio.IndexPostingRef{
					Page: ref, Segment: uint16(position - first),
					Flags: storeio.IndexPostingImmutableBase,
				},
			})
		}
		b.postings = append(b.postings, fileStoreBulkPostingPlan{
			indexID: indexID, first: first, last: last, ref: ref,
		})
		first = last
	}
	return nil
}

// fileStoreBulkTupleHash extracts directly from compact Store chunks. It
// avoids widening shape tapes into one cached classic Index per row while
// producing the same process-independent hash used by FileStore probes.
func fileStoreBulkTupleHash(exact *storeExactIndex, chunk *storeChunk, slot int, textScratch []byte) (uint64, bool, [StoreIndexMaxColumns]RawValue, []byte, error) {
	var values [StoreIndexMaxColumns]RawValue
	if !storeIndexExtractValues(chunk, slot, exact, &values) {
		return 0, false, values, textScratch, nil
	}
	hash := uint64(14695981039346656037)
	for _, raw := range values[:exact.n] {
		hash = fileIndexHashBytes(hash, []byte{byte(raw.Kind()), 0xff})
		switch raw.Kind() {
		case document.Null:
		case document.Bool:
			value, _ := raw.Bool()
			if value {
				hash = fileIndexHashBytes(hash, []byte{1})
			} else {
				hash = fileIndexHashBytes(hash, []byte{0})
			}
		case document.Number:
			if value, ok := raw.Float64(); ok {
				if value == 0 {
					value = 0
				}
				var encoded [8]byte
				binary.LittleEndian.PutUint64(encoded[:], math.Float64bits(value))
				hash = fileIndexHashBytes(hash, encoded[:])
			} else {
				hash = fileIndexHashBytes(hash, []byte{0x7f})
			}
		case document.String:
			if text, clean := raw.StringBytes(); clean {
				hash = fileIndexHashBytes(hash, text)
			} else {
				text, ok, err := raw.AppendText(textScratch[:0])
				if err != nil || !ok {
					return 0, false, values, textScratch, err
				}
				textScratch = text
				hash = fileIndexHashBytes(hash, text)
			}
		default:
			return 0, false, values, textScratch, nil
		}
		hash = fileIndexHashBytes(hash, []byte{0xfe})
	}
	return hash, true, values, textScratch, nil
}

func compareFileStoreBulkIndexKey(a, b storeio.IndexDirectoryKey) int {
	if a.IndexID < b.IndexID {
		return -1
	}
	if a.IndexID > b.IndexID {
		return 1
	}
	if a.TupleHash < b.TupleHash {
		return -1
	}
	if a.TupleHash > b.TupleHash {
		return 1
	}
	if a.Chunk < b.Chunk {
		return -1
	}
	if a.Chunk > b.Chunk {
		return 1
	}
	return 0
}

func (b *fileStoreBulkBuild) planIndexTree() error {
	if len(b.indexRows) == 0 {
		return nil
	}
	leafCapacity := (b.options.PageSize - storeio.PageHeaderSize - storeio.PageTrailerSize -
		storeio.IndexDirectoryPayloadHeaderSize) / storeio.IndexDirectoryLeafRecordSize
	branchCapacity := min(64, (b.options.PageSize-storeio.PageHeaderSize-storeio.PageTrailerSize-
		storeio.IndexDirectoryPayloadHeaderSize)/storeio.IndexDirectoryBranchRecordSize)
	if leafCapacity < 1 || branchCapacity < 2 {
		return storeio.ErrInvalidWrite
	}
	levelStart := 0
	for first := 0; first < len(b.indexRows); first += leafCapacity {
		last := min(first+leafCapacity, len(b.indexRows))
		ref, err := b.allocator.allocate(storeio.PageIndexDirectory, b.allocator.pageSize)
		if err != nil {
			return err
		}
		b.indexes = append(b.indexes, fileStoreBulkIndexPlan{first: first, last: last, ref: ref})
	}
	levelEnd := len(b.indexes)
	for level := uint8(1); levelEnd-levelStart > 1; level++ {
		if level > 10 {
			return storeio.ErrIndexTreeDepth
		}
		nextStart := len(b.indexes)
		for first := levelStart; first < levelEnd; first += branchCapacity {
			last := min(first+branchCapacity, levelEnd)
			children := make([]storeio.IndexDirectoryChild, last-first)
			for i := first; i < last; i++ {
				children[i-first] = storeio.IndexDirectoryChild{
					Lower: b.indexPlanLower(b.indexes[i]), Ref: b.indexes[i].ref,
				}
			}
			ref, err := b.allocator.allocate(storeio.PageIndexDirectory, b.allocator.pageSize)
			if err != nil {
				return err
			}
			b.indexes = append(b.indexes, fileStoreBulkIndexPlan{
				level: level, children: children, ref: ref,
			})
		}
		levelStart, levelEnd = nextStart, len(b.indexes)
	}
	b.indexRoot = b.indexes[levelStart].ref
	return nil
}

func (b *fileStoreBulkBuild) indexPlanLower(plan fileStoreBulkIndexPlan) storeio.IndexDirectoryKey {
	if plan.level == 0 {
		return b.indexRows[plan.first].Key
	}
	return plan.children[0].Lower
}

func (b *fileStoreBulkBuild) planTTLTree() error {
	for row := range b.rows {
		location := b.targetLocation(row)
		if location.Deadline == 0 {
			continue
		}
		b.ttlRows = append(b.ttlRows, storeio.TTLKey{
			Deadline: location.Deadline, Chunk: location.Chunk, Slot: location.Slot,
		})
	}
	if len(b.ttlRows) == 0 {
		return nil
	}
	slices.SortFunc(b.ttlRows, func(a, c storeio.TTLKey) int {
		if a.Deadline < c.Deadline {
			return -1
		}
		if a.Deadline > c.Deadline {
			return 1
		}
		if a.Chunk < c.Chunk {
			return -1
		}
		if a.Chunk > c.Chunk {
			return 1
		}
		return int(a.Slot) - int(c.Slot)
	})
	leafCapacity := (b.options.PageSize - storeio.PageHeaderSize - storeio.PageTrailerSize -
		storeio.TTLDirectoryPayloadHeaderSize) / storeio.TTLDirectoryLeafRecordSize
	branchCapacity := min(64, (b.options.PageSize-storeio.PageHeaderSize-storeio.PageTrailerSize-
		storeio.TTLDirectoryPayloadHeaderSize)/storeio.TTLDirectoryBranchRecordSize)
	if leafCapacity < 1 || branchCapacity < 2 {
		return storeio.ErrInvalidWrite
	}
	levelStart := 0
	for first := 0; first < len(b.ttlRows); first += leafCapacity {
		last := min(first+leafCapacity, len(b.ttlRows))
		ref, err := b.allocator.allocate(storeio.PageTTLDirectory, b.allocator.pageSize)
		if err != nil {
			return err
		}
		b.ttls = append(b.ttls, fileStoreBulkTTLPlan{first: first, last: last, ref: ref})
	}
	levelEnd := len(b.ttls)
	for level := uint8(1); levelEnd-levelStart > 1; level++ {
		if level > 10 {
			return storeio.ErrTTLTreeDepth
		}
		nextStart := len(b.ttls)
		for first := levelStart; first < levelEnd; first += branchCapacity {
			last := min(first+branchCapacity, levelEnd)
			children := make([]storeio.TTLDirectoryChild, last-first)
			for i := first; i < last; i++ {
				children[i-first] = storeio.TTLDirectoryChild{
					Lower: b.ttlPlanLower(b.ttls[i]), Ref: b.ttls[i].ref,
				}
			}
			ref, err := b.allocator.allocate(storeio.PageTTLDirectory, b.allocator.pageSize)
			if err != nil {
				return err
			}
			b.ttls = append(b.ttls, fileStoreBulkTTLPlan{
				level: level, children: children, ref: ref,
			})
		}
		levelStart, levelEnd = nextStart, len(b.ttls)
	}
	b.ttlRoot = b.ttls[levelStart].ref
	return nil
}

func (b *fileStoreBulkBuild) ttlPlanLower(plan fileStoreBulkTTLPlan) storeio.TTLKey {
	if plan.level == 0 {
		return b.ttlRows[plan.first]
	}
	return plan.children[0].Lower
}

func (b *fileStoreBulkBuild) write(file *os.File) error {
	if err := file.Truncate(int64(b.fileEnd)); err != nil {
		return err
	}
	scratch := make([]byte, b.options.MaxPageSize)
	if err := b.writeOverflowPages(file, scratch); err != nil {
		return err
	}
	if err := b.writeDocumentPages(file, scratch); err != nil {
		return err
	}
	for _, plan := range b.chunks {
		page, err := storeio.EncodeChunkDirectoryPage(scratch[:b.options.PageSize], storeio.ChunkDirectoryHeader{
			StoreID: b.storeID, Generation: b.allocator.generation, LogicalID: plan.ref.LogicalID,
			PageSize: b.allocator.pageSize, Prefix: plan.prefix, Bitmap: plan.bitmap, Shift: plan.shift,
		}, plan.children, b.fileEnd, b.allocator.nextLogical)
		if err != nil {
			return err
		}
		if err := writeStorePageAt(file, page, plan.ref.Offset); err != nil {
			return err
		}
	}
	if err := b.writeKeyPages(file, scratch); err != nil {
		return err
	}
	if err := b.writePostingPages(file, scratch); err != nil {
		return err
	}
	if err := b.writeIndexPages(file, scratch); err != nil {
		return err
	}
	if err := b.writeTTLPages(file, scratch); err != nil {
		return err
	}
	statePage, err := storeio.EncodeStateRootPage(scratch[:b.options.PageSize], b.root, b.fileEnd)
	if err != nil {
		return err
	}
	if err := writeStorePageAt(file, statePage, b.stateRef.Offset); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	super := storeio.Superblock{
		StoreID: b.storeID, Generation: b.allocator.generation,
		StateOffset: b.stateRef.Offset, StateLength: b.stateRef.Length,
		StateChecksum: storeio.PageChecksum(statePage), FileEnd: b.fileEnd,
		PageSize: b.allocator.pageSize,
	}
	rootPage := scratch[:b.options.PageSize]
	clear(rootPage)
	if _, err := storeio.EncodeSuperblock(rootPage[:storeio.SuperblockSize], super); err != nil {
		return err
	}
	rootOffset, err := storeio.SuperblockOffset(b.allocator.generation, b.allocator.pageSize)
	if err != nil {
		return err
	}
	if err := writeStorePageAt(file, rootPage, uint64(rootOffset)); err != nil {
		return err
	}
	return file.Sync()
}

func (b *fileStoreBulkBuild) writeOverflowPages(file *os.File, scratch []byte) error {
	for _, plan := range b.overflows {
		_, _, raw := b.sourceRow(plan.row)
		location := b.targetLocation(plan.row)
		page, err := storeio.EncodeOverflowPage(scratch[:b.options.MaxPageSize], storeio.OverflowPageHeader{
			StoreID: b.storeID, Generation: b.allocator.generation, LogicalID: plan.ref.LogicalID,
			PageSize: plan.ref.Length, Chunk: location.Chunk, Slot: location.Slot,
			Total: uint64(len(raw)), Offset: uint64(plan.start), Next: plan.next,
		}, raw[plan.start:plan.end], b.fileEnd, b.allocator.nextLogical,
			b.allocator.pageSize, uint32(len(b.documents)), uint8(b.options.Store.ChunkDocuments))
		if err != nil {
			return err
		}
		if err := writeStorePageAt(file, page, plan.ref.Offset); err != nil {
			return err
		}
	}
	return nil
}

func (b *fileStoreBulkBuild) writeDocumentPages(file *os.File, scratch []byte) error {
	var storage [storeMaxChunkDocuments]storeio.DocumentRecord
	masks := make([]uint64, len(b.options.float64Columns))
	values := make([]float64, len(b.options.float64Columns)*64)
	for _, plan := range b.documents {
		rows := storage[:plan.last-plan.first]
		for i := range rows {
			rowIndex := plan.first + i
			_, key, raw := b.sourceRow(rowIndex)
			record := storeio.DocumentRecord{Key: byteview.Bytes(key), Slot: uint8(i)}
			if b.rows[rowIndex].overflowN == 0 {
				record.JSON = raw
			} else {
				record.Overflow = b.overflows[b.rows[rowIndex].overflowBase].ref
				record.JSONLength = uint64(len(raw))
			}
			rows[i] = record
		}
		clear(masks)
		for column := range b.options.float64Columns {
			for i := range rows {
				value, ok, err := b.sourceFloat64(plan.first+i, column)
				if err != nil {
					return err
				}
				if !ok {
					continue
				}
				masks[column] |= uint64(1) << uint(i)
				values[column*64+i] = value
			}
		}
		page, err := storeio.EncodeDocumentPageWithColumns(scratch[:plan.ref.Length], storeio.DocumentPageHeader{
			StoreID: b.storeID, Generation: b.allocator.generation, LogicalID: plan.ref.LogicalID,
			PageSize: plan.ref.Length, ChunkID: plan.chunk, Live: plan.live,
		}, rows, storeio.DocumentFloat64Columns{Masks: masks, Values: values},
			b.allocator.nextLogical, b.fileEnd, b.allocator.pageSize)
		if err != nil {
			return err
		}
		if err := writeStorePageAt(file, page, plan.ref.Offset); err != nil {
			return err
		}
		clear(rows)
	}
	return nil
}

func (b *fileStoreBulkBuild) writeKeyPages(file *os.File, scratch []byte) error {
	entries := make([]storeio.KeyDirectoryEntry, 0, 128)
	children := make([]storeio.KeyDirectoryChild, 0, 64)
	for _, plan := range b.keys {
		header := storeio.KeyDirectoryHeader{
			StoreID: b.storeID, Generation: b.allocator.generation,
			LogicalID: plan.ref.LogicalID, PageSize: b.allocator.pageSize, Level: plan.level,
		}
		var page []byte
		var err error
		if plan.level == 0 {
			entries = entries[:0]
			for _, row := range b.keyOrder[plan.first:plan.last] {
				_, key, _ := b.sourceRow(row)
				entries = append(entries, storeio.KeyDirectoryEntry{
					Key: byteview.Bytes(key), Location: b.targetLocation(row),
				})
			}
			page, err = storeio.EncodeKeyDirectoryLeaf(
				scratch[:b.options.PageSize], header, entries, b.allocator.nextLogical,
				uint32(len(b.documents)), uint8(b.options.Store.ChunkDocuments),
			)
		} else {
			children = children[:0]
			for _, child := range plan.children {
				_, key, _ := b.sourceRow(child.lower)
				children = append(children, storeio.KeyDirectoryChild{
					Lower: byteview.Bytes(key), Ref: child.ref,
				})
			}
			page, err = storeio.EncodeKeyDirectoryBranch(
				scratch[:b.options.PageSize], header, children, b.fileEnd, b.allocator.nextLogical,
			)
		}
		if err != nil {
			return err
		}
		if err := writeStorePageAt(file, page, plan.ref.Offset); err != nil {
			return err
		}
	}
	return nil
}

func (b *fileStoreBulkBuild) writePostingPages(file *os.File, scratch []byte) error {
	entries := make([]storeio.PostingEntry, 0, 80)
	segments := make([]storeio.PostingSegment, 0, 80)
	for _, plan := range b.postings {
		count := plan.last - plan.first
		if cap(entries) < count {
			entries = make([]storeio.PostingEntry, count)
		} else {
			entries = entries[:count]
		}
		if cap(segments) < count {
			segments = make([]storeio.PostingSegment, count)
		} else {
			segments = segments[:count]
		}
		for i := 0; i < count; i++ {
			mask := b.masks[plan.first+i]
			entries[i] = storeio.PostingEntry{Chunk: mask.key.Chunk, Bits: mask.bits}
			certificate := b.indexCertificates[mask.certStart : mask.certStart+uint32(mask.certLength) : mask.certStart+uint32(mask.certLength)]
			flags := uint16(0)
			if mask.collision {
				flags |= storeio.PostingSegmentCollision
			}
			segments[i] = storeio.PostingSegment{
				StreamID: uint32(i + 1), TupleHash: mask.key.TupleHash,
				Flags: flags, Certificate: certificate, Entries: entries[i : i+1],
			}
		}
		page, err := storeio.EncodePostingPage(scratch[:b.options.PageSize], storeio.PostingPageHeader{
			StoreID: b.storeID, Generation: b.allocator.generation, LogicalID: plan.ref.LogicalID,
			PageSize: b.allocator.pageSize, IndexID: plan.indexID,
		}, segments, b.allocator.nextLogical, uint32(len(b.options.indexes)))
		if err != nil {
			return err
		}
		if err := writeStorePageAt(file, page, plan.ref.Offset); err != nil {
			return err
		}
		clear(entries)
		clear(segments)
	}
	return nil
}

func (b *fileStoreBulkBuild) writeIndexPages(file *os.File, scratch []byte) error {
	for _, plan := range b.indexes {
		header := storeio.IndexDirectoryHeader{
			StoreID: b.storeID, Generation: b.allocator.generation,
			LogicalID: plan.ref.LogicalID, PageSize: b.allocator.pageSize, Level: plan.level,
		}
		var page []byte
		var err error
		if plan.level == 0 {
			page, err = storeio.EncodeIndexDirectoryLeaf(
				scratch[:b.options.PageSize], header, b.indexRows[plan.first:plan.last],
				b.fileEnd, b.allocator.nextLogical, uint32(len(b.options.indexes)),
			)
		} else {
			page, err = storeio.EncodeIndexDirectoryBranch(
				scratch[:b.options.PageSize], header, plan.children, b.fileEnd, b.allocator.nextLogical,
			)
		}
		if err != nil {
			return err
		}
		if err := writeStorePageAt(file, page, plan.ref.Offset); err != nil {
			return err
		}
	}
	return nil
}

func (b *fileStoreBulkBuild) writeTTLPages(file *os.File, scratch []byte) error {
	for _, plan := range b.ttls {
		header := storeio.TTLDirectoryHeader{
			StoreID: b.storeID, Generation: b.allocator.generation,
			LogicalID: plan.ref.LogicalID, PageSize: b.allocator.pageSize, Level: plan.level,
		}
		var page []byte
		var err error
		if plan.level == 0 {
			page, err = storeio.EncodeTTLDirectoryLeaf(
				scratch[:b.options.PageSize], header, b.ttlRows[plan.first:plan.last],
				b.allocator.nextLogical, uint32(len(b.documents)), uint8(b.options.Store.ChunkDocuments),
			)
		} else {
			page, err = storeio.EncodeTTLDirectoryBranch(
				scratch[:b.options.PageSize], header, plan.children, b.fileEnd, b.allocator.nextLogical,
			)
		}
		if err != nil {
			return err
		}
		if err := writeStorePageAt(file, page, plan.ref.Offset); err != nil {
			return err
		}
	}
	return nil
}
