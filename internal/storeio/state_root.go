package storeio

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	// StateRootLogicalID is fixed so a recovered superblock cannot silently
	// reinterpret an arbitrary page as the root of a Store generation.
	StateRootLogicalID = uint64(1)
	// StateRootPayloadSize leaves a fixed reserved suffix for compatible fields
	// while keeping every current root reference at a stable byte offset.
	StateRootPayloadSize = 256
	// PageRefSize is the fixed encoded width of one pointer-free physical page
	// reference.
	PageRefSize = 32

	stateRootVersion        = uint32(1)
	stateRootChunkRefOffset = 64
	stateRootKeyRefOffset   = stateRootChunkRefOffset + PageRefSize
	stateRootIndexRefOffset = stateRootKeyRefOffset + PageRefSize
	stateRootTTLRefOffset   = stateRootIndexRefOffset + PageRefSize
	stateRootRefsEnd        = stateRootTTLRefOffset + PageRefSize
)

// State-root option bits are durable equivalents of Store construction
// options. Unknown bits fail closed.
const (
	StateOptionShapeTapes uint32 = 1 << iota
	StateOptionPostings
	StateOptionValueDict
	StateOptionHashKeys
)

const stateRootKnownOptions = StateOptionShapeTapes |
	StateOptionPostings |
	StateOptionValueDict |
	StateOptionHashKeys

// ErrStateRootCorrupt reports a common page that passed basic framing but does
// not encode a valid Store state root.
var ErrStateRootCorrupt = errors.New("simdjson: corrupt Store state root")

// PageRef is a durable pointer to one immutable logical-page version. Offset
// is physical and changes on replacement; LogicalID is stable. Generation may
// be older than the state root because unchanged pages are shared across
// copy-on-write generations.
type PageRef struct {
	Offset     uint64
	LogicalID  uint64
	Generation uint64
	Length     uint32
	Kind       PageKind
	Flags      uint8
}

// StateRoot is the compact, pointer-free graph root named by a Superblock.
// Its four directory references separate document placement, key lookup,
// secondary indexes, and TTL ordering so a mutation copies only affected
// paths. The persistent free-page tree remains a separate Superblock root.
type StateRoot struct {
	StoreID        [16]byte
	Generation     uint64
	PageSize       uint32
	Options        uint32
	DocumentCount  uint64
	TTLCount       uint64
	NextLogicalID  uint64
	ChunkHighWater uint32
	LiveChunks     uint32
	ChunkDocuments uint32
	IndexCount     uint32
	IndexMaxDepth  uint32
	ChunkDirectory PageRef
	KeyDirectory   PageRef
	IndexDirectory PageRef
	TTLDirectory   PageRef
}

// EncodeStateRootPage writes and seals one complete common-format page into
// caller storage. fileEnd is copied from the prospective Superblock and bounds
// every physical reference before publication. No allocation is performed.
func EncodeStateRootPage(dst []byte, root StateRoot, fileEnd uint64) ([]byte, error) {
	if err := validateStateRoot(root, fileEnd); err != nil {
		return nil, err
	}
	header := PageHeader{
		StoreID:       root.StoreID,
		Generation:    root.Generation,
		LogicalID:     StateRootLogicalID,
		PageSize:      root.PageSize,
		PayloadLength: StateRootPayloadSize,
		Kind:          PageStateRoot,
	}
	payload, err := InitPage(dst, header)
	if err != nil {
		return nil, err
	}
	binary.LittleEndian.PutUint32(payload[0:4], stateRootVersion)
	binary.LittleEndian.PutUint32(payload[4:8], root.Options)
	binary.LittleEndian.PutUint64(payload[8:16], root.DocumentCount)
	binary.LittleEndian.PutUint64(payload[16:24], root.TTLCount)
	binary.LittleEndian.PutUint64(payload[24:32], root.NextLogicalID)
	binary.LittleEndian.PutUint32(payload[32:36], root.ChunkHighWater)
	binary.LittleEndian.PutUint32(payload[36:40], root.LiveChunks)
	binary.LittleEndian.PutUint32(payload[40:44], root.ChunkDocuments)
	binary.LittleEndian.PutUint32(payload[44:48], root.IndexCount)
	binary.LittleEndian.PutUint32(payload[48:52], root.IndexMaxDepth)
	encodePageRef(payload[stateRootChunkRefOffset:stateRootKeyRefOffset], root.ChunkDirectory)
	encodePageRef(payload[stateRootKeyRefOffset:stateRootIndexRefOffset], root.KeyDirectory)
	encodePageRef(payload[stateRootIndexRefOffset:stateRootTTLRefOffset], root.IndexDirectory)
	encodePageRef(payload[stateRootTTLRefOffset:stateRootRefsEnd], root.TTLDirectory)
	page := dst[:int(root.PageSize)]
	if _, err := sealInitializedPage(page); err != nil {
		return nil, err
	}
	return page, nil
}

// DecodeStateRootPage verifies a complete common page and its state-root
// schema. fileEnd is the high-water mark from the selecting Superblock. The
// result contains values only and retains no reference to src.
func DecodeStateRootPage(src []byte, fileEnd uint64) (StateRoot, error) {
	header, payload, err := OpenPage(src)
	if err != nil {
		return StateRoot{}, fmt.Errorf("%w: %w", ErrStateRootCorrupt, err)
	}
	if header.Kind != PageStateRoot || header.LogicalID != StateRootLogicalID ||
		len(payload) != StateRootPayloadSize ||
		binary.LittleEndian.Uint32(payload[0:4]) != stateRootVersion ||
		!allZero(payload[52:stateRootChunkRefOffset]) ||
		!pageRefReservedZero(payload[stateRootChunkRefOffset:stateRootKeyRefOffset]) ||
		!pageRefReservedZero(payload[stateRootKeyRefOffset:stateRootIndexRefOffset]) ||
		!pageRefReservedZero(payload[stateRootIndexRefOffset:stateRootTTLRefOffset]) ||
		!pageRefReservedZero(payload[stateRootTTLRefOffset:stateRootRefsEnd]) ||
		!allZero(payload[stateRootRefsEnd:]) {
		return StateRoot{}, fmt.Errorf("%w: header, version, or reserved bytes", ErrStateRootCorrupt)
	}
	root := StateRoot{
		StoreID:        header.StoreID,
		Generation:     header.Generation,
		PageSize:       header.PageSize,
		Options:        binary.LittleEndian.Uint32(payload[4:8]),
		DocumentCount:  binary.LittleEndian.Uint64(payload[8:16]),
		TTLCount:       binary.LittleEndian.Uint64(payload[16:24]),
		NextLogicalID:  binary.LittleEndian.Uint64(payload[24:32]),
		ChunkHighWater: binary.LittleEndian.Uint32(payload[32:36]),
		LiveChunks:     binary.LittleEndian.Uint32(payload[36:40]),
		ChunkDocuments: binary.LittleEndian.Uint32(payload[40:44]),
		IndexCount:     binary.LittleEndian.Uint32(payload[44:48]),
		IndexMaxDepth:  binary.LittleEndian.Uint32(payload[48:52]),
		ChunkDirectory: decodePageRef(payload[stateRootChunkRefOffset:stateRootKeyRefOffset]),
		KeyDirectory:   decodePageRef(payload[stateRootKeyRefOffset:stateRootIndexRefOffset]),
		IndexDirectory: decodePageRef(payload[stateRootIndexRefOffset:stateRootTTLRefOffset]),
		TTLDirectory:   decodePageRef(payload[stateRootTTLRefOffset:stateRootRefsEnd]),
	}
	if err := validateStateRoot(root, fileEnd); err != nil {
		return StateRoot{}, fmt.Errorf("%w: %v", ErrStateRootCorrupt, err)
	}
	return root, nil
}

func encodePageRef(dst []byte, ref PageRef) {
	binary.LittleEndian.PutUint64(dst[0:8], ref.Offset)
	binary.LittleEndian.PutUint64(dst[8:16], ref.LogicalID)
	binary.LittleEndian.PutUint64(dst[16:24], ref.Generation)
	binary.LittleEndian.PutUint32(dst[24:28], ref.Length)
	dst[28] = byte(ref.Kind)
	dst[29] = ref.Flags
}

func decodePageRef(src []byte) PageRef {
	return PageRef{
		Offset:     binary.LittleEndian.Uint64(src[0:8]),
		LogicalID:  binary.LittleEndian.Uint64(src[8:16]),
		Generation: binary.LittleEndian.Uint64(src[16:24]),
		Length:     binary.LittleEndian.Uint32(src[24:28]),
		Kind:       PageKind(src[28]),
		Flags:      src[29],
	}
}

func pageRefReservedZero(src []byte) bool {
	return allZero(src[30:PageRefSize])
}

func validateStateRoot(root StateRoot, fileEnd uint64) error {
	if root.StoreID == ([16]byte{}) || root.Generation == 0 ||
		!validPhysicalPageSize(root.PageSize) || root.Options&^stateRootKnownOptions != 0 {
		return fmt.Errorf("%w: state identity, page size, or options", ErrInvalidWrite)
	}
	pageSize := uint64(root.PageSize)
	if fileEnd < uint64(superblockCopies)*pageSize || fileEnd > maxSuperblockFileOffset || fileEnd%pageSize != 0 {
		return fmt.Errorf("%w: state file high-water mark", ErrInvalidWrite)
	}
	if root.ChunkDocuments == 0 || root.ChunkDocuments > 64 ||
		root.LiveChunks > root.ChunkHighWater || root.TTLCount > root.DocumentCount ||
		root.NextLogicalID <= StateRootLogicalID {
		return fmt.Errorf("%w: state counts", ErrInvalidWrite)
	}
	if root.LiveChunks == 0 {
		if root.DocumentCount != 0 {
			return fmt.Errorf("%w: documents without chunks", ErrInvalidWrite)
		}
	} else if root.DocumentCount < uint64(root.LiveChunks) ||
		root.DocumentCount > uint64(root.LiveChunks)*uint64(root.ChunkDocuments) {
		return fmt.Errorf("%w: document/chunk counts", ErrInvalidWrite)
	}

	refs := [4]struct {
		ref      PageRef
		kind     PageKind
		required bool
	}{
		{root.ChunkDirectory, PageChunkDirectory, root.LiveChunks != 0},
		{root.KeyDirectory, PageKeyDirectory, root.DocumentCount != 0},
		{root.IndexDirectory, PageIndexDirectory, root.IndexCount != 0},
		{root.TTLDirectory, PageTTLDirectory, root.TTLCount != 0},
	}
	for i := range refs {
		if err := validateStatePageRef(refs[i].ref, refs[i].kind, refs[i].required, root, fileEnd); err != nil {
			return err
		}
		if !refs[i].required && refs[i].ref != (PageRef{}) {
			return fmt.Errorf("%w: unneeded %d root", ErrInvalidWrite, refs[i].kind)
		}
		if refs[i].ref == (PageRef{}) {
			continue
		}
		for j := 0; j < i; j++ {
			if refs[j].ref != (PageRef{}) &&
				(refs[j].ref.LogicalID == refs[i].ref.LogicalID || refs[j].ref.Offset == refs[i].ref.Offset) {
				return fmt.Errorf("%w: duplicate root reference", ErrInvalidWrite)
			}
		}
	}
	return nil
}

func validateStatePageRef(ref PageRef, kind PageKind, required bool, root StateRoot, fileEnd uint64) error {
	if ref == (PageRef{}) {
		if required {
			return fmt.Errorf("%w: missing %d root", ErrInvalidWrite, kind)
		}
		return nil
	}
	pageSize := uint64(root.PageSize)
	if ref.Kind != kind || ref.Flags != 0 || ref.Length != root.PageSize ||
		ref.Generation == 0 || ref.Generation > root.Generation ||
		ref.LogicalID <= StateRootLogicalID || ref.LogicalID >= root.NextLogicalID ||
		ref.Offset < uint64(superblockCopies)*pageSize || ref.Offset%pageSize != 0 ||
		ref.Offset > maxSuperblockFileOffset || ref.Offset > fileEnd-pageSize {
		return fmt.Errorf("%w: invalid %d root reference", ErrInvalidWrite, kind)
	}
	return nil
}
