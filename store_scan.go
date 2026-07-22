package simdjson

// Snapshot column gathers used by the query engine. They preserve Store scan
// order while delegating JSON path resolution to each immutable DocSet chunk,
// so shape tapes, compiled keys, value dictionaries, and duplicate-key
// semantics stay centralized in the existing column primitives.

// AppendPointer appends one value per live Snapshot row in chunk/slot order.
// An absent path appends a zero RawValue. Values borrow s. With sufficient dst
// capacity the operation allocates nothing.
func (s Snapshot) AppendPointer(dst []RawValue, pointer CompiledPointer) ([]RawValue, error) {
	mark := len(dst)
	if s.state == nil {
		return dst, nil
	}
	var scanErr error
	s.state.chunks.each(func(_ uint32, chunk *storeChunk) bool {
		var err error
		dst, err = chunk.docs.AppendPointer(dst, pointer)
		if err != nil {
			scanErr = err
			return false
		}
		return true
	})
	if scanErr != nil {
		return dst[:mark], scanErr
	}
	return dst, nil
}

// AppendPointerRows is the sparse-gather form of [Snapshot.AppendPointer].
// Rows may be in any order and may repeat. Invalid or stale addresses panic;
// callers must use rows produced by this Snapshot.
func (s Snapshot) AppendPointerRows(dst []RawValue, rows []StoreRow, pointer CompiledPointer) ([]RawValue, error) {
	mark := len(dst)
	for first := 0; first < len(rows); {
		chunkID := rows[first].Chunk
		chunk := (*storeChunk)(nil)
		if s.state != nil {
			chunk = s.state.chunks.get(chunkID)
		}
		if chunk == nil {
			panic("simdjson: StoreRow chunk is not live in Snapshot")
		}
		var ords [storeMaxChunkDocuments]int
		last := first
		for last < len(rows) && rows[last].Chunk == chunkID && last-first < len(ords) {
			slot := int(rows[last].Slot)
			if slot >= len(chunk.ord) || chunk.live&(uint64(1)<<uint(slot)) == 0 {
				panic("simdjson: StoreRow slot is not live in Snapshot")
			}
			ords[last-first] = int(chunk.ord[slot])
			last++
		}
		var err error
		dst, err = chunk.docs.AppendPointerRows(dst, ords[:last-first], pointer)
		if err != nil {
			return dst[:mark], err
		}
		first = last
	}
	return dst, nil
}

// AppendRowKeys appends the keys addressed by rows. With sufficient capacity
// it allocates nothing.
func (s Snapshot) AppendRowKeys(dst []string, rows []StoreRow) []string {
	for _, row := range rows {
		chunk := (*storeChunk)(nil)
		if s.state != nil {
			chunk = s.state.chunks.get(row.Chunk)
		}
		if chunk == nil || int(row.Slot) >= len(chunk.ord) || chunk.live&(uint64(1)<<row.Slot) == 0 {
			panic("simdjson: StoreRow is not live in Snapshot")
		}
		dst = append(dst, chunk.key(int(row.Slot)))
	}
	return dst
}
