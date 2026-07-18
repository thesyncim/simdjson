package simdjson

import (
	"reflect"
	"sync/atomic"
)

const (
	decoderPlanStateCacheSlots      = 4
	decoderMapScratchRetentionBytes = 64 << 10
)

// decoderMapScratch owns the addressable reflection boxes for one compiled
// map node. SetMapIndex copies both boxes into the destination map. The boxes
// are cleared before reuse so cached scratch retains no decoded object graph.
type decoderMapScratch struct {
	key     reflect.Value
	element reflect.Value
	inUse   bool
}

// decoderPlanState is shared by copies of one immutable Decoder. Its bounded
// cache holds only operation metadata and cleared reflection boxes; detached
// standard-method receiver arrays are always operation-local and never enter
// the cache.
type decoderPlanState struct {
	mapSlots int
	cache    [decoderPlanStateCacheSlots]atomic.Pointer[decoderState]
}

func newDecoderPlanState(mapSlots int, receivers bool) *decoderPlanState {
	if mapSlots == 0 && !receivers {
		return nil
	}
	return &decoderPlanState{mapSlots: mapSlots}
}

func (p *decoderPlanState) take() *decoderState {
	for i := range p.cache {
		if state := p.cache[i].Swap(nil); state != nil {
			return state
		}
	}
	operation := &decoderOperationState{}
	if p.mapSlots != 0 {
		operation.maps = make([]decoderMapScratch, p.mapSlots)
	}
	return &decoderState{operation: operation}
}

func (p *decoderPlanState) release(state *decoderState) {
	state.strings = nil
	state.resetOperationState()
	state.structural = decoderStructuralTape{}
	state.structuralActive = false
	for i := range p.cache {
		if p.cache[i].CompareAndSwap(nil, state) {
			return
		}
	}
}

func (s *decoderState) resetOperationState() {
	operation := s.operation
	if operation == nil {
		return
	}
	operation.resetReceivers()
	for i := range operation.maps {
		scratch := &operation.maps[i]
		if scratch.key.IsValid() {
			scratch.key.SetZero()
		}
		if scratch.element.IsValid() {
			scratch.element.SetZero()
		}
		scratch.inUse = false
	}
}

func (c *decoderCursor) takeMapScratch(node *typedNode) *decoderMapScratch {
	if node.decMapScratch == 0 || c.state == nil || c.state.operation == nil {
		return nil
	}
	index := int(node.decMapScratch - 1)
	if index >= len(c.state.operation.maps) {
		return nil
	}
	scratch := &c.state.operation.maps[index]
	if scratch.inUse {
		return nil
	}
	if !scratch.element.IsValid() {
		element := reflect.New(node.elem.typ)
		scratch.element = element.Elem()
		scratch.key = reflect.New(node.typ.Key()).Elem()
	}
	scratch.inUse = true
	return scratch
}

func releaseMapScratch(scratch *decoderMapScratch) {
	if scratch == nil {
		return
	}
	scratch.key.SetZero()
	scratch.element.SetZero()
	scratch.inUse = false
}

// prepareDecoderMapScratch assigns fixed operation-state slots only to maps
// whose key and value boxes cannot be observed by user code. Text key methods,
// custom value methods, and dynamic interfaces retain the ordinary one-call
// allocation path. The cumulative shallow box size bounds every cached state.
func prepareDecoderMapScratch(root *typedNode) int {
	seen := make(map[*typedNode]bool)
	usedBytes := uintptr(0)
	slots := 0
	var visit func(*typedNode)
	visit = func(node *typedNode) {
		if node == nil || seen[node] {
			return
		}
		seen[node] = true
		if node.kind == typedMap && !node.mapKeyTextDecode && decoderMapScratchSafe(node.elem, make(map[*typedNode]bool)) {
			boxBytes := node.typ.Key().Size() + node.elem.typ.Size()
			if boxBytes == 0 {
				boxBytes = 1
			}
			if usedBytes <= decoderMapScratchRetentionBytes && boxBytes <= decoderMapScratchRetentionBytes-usedBytes {
				slots++
				node.decMapScratch = uint32(slots)
				usedBytes += boxBytes
			}
		}
		switch node.kind {
		case typedStruct:
			for i := range node.fields {
				visit(node.fields[i].node)
			}
			if node.inlineMap != nil {
				visit(node.inlineMap.elem)
			}
		case typedSlice, typedArray, typedMap, typedPointer:
			visit(node.elem)
		}
	}
	visit(root)
	return slots
}

func decoderMapScratchSafe(node *typedNode, visiting map[*typedNode]bool) bool {
	if node == nil {
		return true
	}
	if visiting[node] {
		return true
	}
	visiting[node] = true
	defer delete(visiting, node)
	switch node.kind {
	case typedBool, typedString, typedNumber, typedInt, typedUint, typedFloat, typedBytes:
		return true
	case typedStruct:
		for i := range node.fields {
			if !decoderMapScratchSafe(node.fields[i].node, visiting) {
				return false
			}
		}
		return node.inlineMap == nil || decoderMapScratchSafe(node.inlineMap.elem, visiting)
	case typedSlice, typedArray, typedPointer:
		return decoderMapScratchSafe(node.elem, visiting)
	case typedMap:
		return !node.mapKeyTextDecode && decoderMapScratchSafe(node.elem, visiting)
	default:
		return false
	}
}
