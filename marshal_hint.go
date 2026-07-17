package simdjson

import "sync/atomic"

const (
	marshalSizeHintMin    uint64 = 64
	marshalSizeHintMax    uint64 = 256 << 10
	marshalSizeHintGrowth uint64 = 8
)

// loadMarshalSizeHint returns the bounded capacity for the next convenience
// Marshal call. Large stable users should reuse an Encoder output buffer.
func loadMarshalSizeHint(hint *atomic.Uint64) int {
	size := hint.Load()
	if size < marshalSizeHintMin {
		return int(marshalSizeHintMin)
	}
	return int(size)
}
