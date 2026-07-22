package simdjson

import (
	"errors"
	"fmt"
	"testing"

	"github.com/thesyncim/simdjson/document"
)

func TestStoreBuilderEquivalentAndMutable(t *testing.T) {
	for _, options := range []StoreOptions{
		{ChunkDocuments: 1},
		{ChunkDocuments: 7, ShapeTapes: true},
		{ChunkDocuments: 64, ShapeTapes: true, Postings: true, ValueDict: true,
			IndexOptions: document.IndexOptions{HashKeys: true}},
	} {
		t.Run(fmt.Sprintf("chunk=%d/shape=%v/postings=%v", options.ChunkDocuments, options.ShapeTapes, options.Postings), func(t *testing.T) {
			builder, err := NewStoreBuilder(options)
			if err != nil {
				t.Fatal(err)
			}
			if err := builder.CreateIndex(StoreIndexDefinition{
				Name: "country", Paths: []string{"/profile/geo/country"},
			}); err != nil {
				t.Fatal(err)
			}
			if err := builder.CreateIndex(StoreIndexDefinition{
				Name: "country_active", Paths: []string{"/profile/geo/country", "/active"},
			}); err != nil {
				t.Fatal(err)
			}
			want := make(map[string]string)
			for i := 0; i < 257; i++ {
				key := fmt.Sprintf("key-%03d", i)
				doc := fmt.Sprintf(`{"id":%d,"profile":{"geo":{"country":"c%d"}},"active":%t}`, i, i%9, i%2 == 0)
				input := []byte(doc)
				if err := builder.Append(key, input); err != nil {
					t.Fatal(err)
				}
				clear(input)
				want[key] = doc
			}
			if builder.Len() != len(want) {
				t.Fatalf("builder Len = %d, want %d", builder.Len(), len(want))
			}
			store, err := builder.Build()
			if err != nil {
				t.Fatal(err)
			}
			if store.Len() != len(want) || store.Generation() != 1 {
				t.Fatalf("built Store Len/Generation = %d/%d", store.Len(), store.Generation())
			}
			checkStoreSnapshot(t, store.Snapshot(), want)

			indexes := store.Snapshot().AppendIndexes(nil)
			if len(indexes) != 2 || indexes[0].Name != "country" || indexes[1].Name != "country_active" ||
				indexes[0].State != StoreIndexReady || indexes[0].CoveredChunks != indexes[0].TotalChunks ||
				indexes[1].State != StoreIndexReady || indexes[1].CoveredChunks != indexes[1].TotalChunks {
				t.Fatalf("bulk index = %+v", indexes)
			}
			keys, err := store.Snapshot().AppendIndexRawKeys(nil, "country", []byte(`"c3"`))
			if err != nil {
				t.Fatal(err)
			}
			if len(keys) == 0 {
				t.Fatal("nested index found no c3 documents")
			}
			compound, err := store.Snapshot().AppendIndexRawKeys(nil, "country_active", []byte(`"c3"`), []byte(`false`))
			if err != nil || len(compound) == 0 {
				t.Fatalf("bulk compound index = (%v,%v)", compound, err)
			}

			before := store.Snapshot()
			if _, err := store.Put("key-003", []byte(`{"id":3,"profile":{"geo":{"country":"new"}}}`)); err != nil {
				t.Fatal(err)
			}
			if !store.Delete("key-004") || !store.SetTTL("key-005", 1) {
				t.Fatal("built Store mutation or TTL failed")
			}
			if raw, ok := before.GetRaw("key-003"); !ok || string(raw.Bytes()) != want["key-003"] {
				t.Fatal("post-build mutation changed retained snapshot")
			}
			oldKeys, err := before.AppendIndexRawKeys(nil, "country", []byte(`"c3"`))
			if err != nil || !containsString(oldKeys, "key-003") {
				t.Fatalf("retained bulk index lost old row: (%v,%v)", oldKeys, err)
			}
			newKeys, err := store.Snapshot().AppendIndexRawKeys(nil, "country", []byte(`"new"`))
			if err != nil || len(newKeys) != 1 || newKeys[0] != "key-003" {
				t.Fatalf("bulk index did not dual-maintain update: (%v,%v)", newKeys, err)
			}
		})
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestStoreBuilderCompactsKeyDirectory(t *testing.T) {
	builder, err := NewStoreBuilder(StoreOptions{ChunkDocuments: 2})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct{ key, json string }{
		{"", `0`},
		{"alpha", `1`},
		{"a-key-longer-than-the-first-arena-growth", `2`},
	} {
		if err := builder.Append(row.key, []byte(row.json)); err != nil {
			t.Fatal(err)
		}
	}
	store, err := builder.Build()
	if err != nil {
		t.Fatal(err)
	}
	state := store.state.Load()
	if state.keys != nil || state.baseKeys == nil || state.baseKeys.count != 3 ||
		state.baseKeys.sourceBlock == nil {
		t.Fatalf("published key directory retained builder graph: %+v", state)
	}
	state.chunks.each(func(_ uint32, chunk *storeChunk) bool {
		if chunk.keys != nil || chunk.keyBytes != nil || chunk.mappedKeys != state.baseKeys {
			t.Fatalf("chunk retained heap key storage: %+v", chunk)
		}
		return true
	})
	for key, want := range map[string]string{
		"": `0`, "alpha": `1`, "a-key-longer-than-the-first-arena-growth": `2`,
	} {
		if raw, ok := store.GetRaw(key); !ok || string(raw.Bytes()) != want {
			t.Fatalf("GetRaw(%q) = (%q,%v), want (%q,true)", key, raw.Bytes(), ok, want)
		}
	}
	if state.baseKeys.sourceBlock.OutsideHeap() && store.Stats().ExternalKeyBytes == 0 {
		t.Fatal("owned external key bytes not reported")
	}

	before := store.Snapshot()
	if !store.Delete("alpha") {
		t.Fatal("Delete(alpha) missed compact base")
	}
	if created, err := store.Put("later", []byte(`3`)); err != nil || !created {
		t.Fatalf("Put(later) = (%v,%v)", created, err)
	}
	if raw, ok := before.GetRaw("alpha"); !ok || string(raw.Bytes()) != `1` {
		t.Fatalf("retained compact snapshot = (%q,%v)", raw.Bytes(), ok)
	}
	if _, ok := store.GetRaw("alpha"); ok {
		t.Fatal("deleted compact-base key remained visible")
	}
}

func TestStoreBuilderErrorsAndEmptyStore(t *testing.T) {
	if _, err := NewStoreBuilder(StoreOptions{ChunkDocuments: 65}); err == nil {
		t.Fatal("invalid chunk bound accepted")
	}
	var nilBuilder *StoreBuilder
	if !errors.Is(nilBuilder.Append("k", []byte(`null`)), ErrStoreBuilderClosed) {
		t.Fatal("nil Append error")
	}
	if _, err := nilBuilder.Build(); !errors.Is(err, ErrStoreBuilderClosed) {
		t.Fatal("nil Build error")
	}

	builder, err := NewStoreBuilder(StoreOptions{ChunkDocuments: 3, ShapeTapes: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := builder.Append("bad", []byte(`{"broken"`)); err == nil {
		t.Fatal("invalid JSON accepted")
	}
	if builder.Len() != 0 {
		t.Fatalf("invalid append changed Len to %d", builder.Len())
	}
	if err := builder.Append("bad", []byte(`{"valid":true}`)); err != nil {
		t.Fatal(err)
	}
	if err := builder.Append("bad", []byte(`{"duplicate":true}`)); !errors.Is(err, ErrStoreDuplicateKey) {
		t.Fatalf("duplicate error = %v", err)
	}
	if builder.Len() != 1 {
		t.Fatalf("duplicate changed Len to %d", builder.Len())
	}
	if err := builder.CreateIndex(StoreIndexDefinition{Name: "", Paths: []string{"/valid"}}); err == nil {
		t.Fatal("invalid builder index accepted")
	}
	if err := builder.CreateIndex(StoreIndexDefinition{Name: "valid", Paths: []string{"/valid"}}); err != nil {
		t.Fatal(err)
	}
	if err := builder.CreateIndex(StoreIndexDefinition{Name: "valid", Paths: []string{"/other"}}); !errors.Is(err, ErrStoreIndexExists) {
		t.Fatalf("duplicate builder index error = %v", err)
	}
	store, err := builder.Build()
	if err != nil {
		t.Fatal(err)
	}
	if err := builder.Append("later", []byte(`null`)); !errors.Is(err, ErrStoreBuilderClosed) {
		t.Fatalf("Append after Build error = %v", err)
	}
	if _, err := builder.Build(); !errors.Is(err, ErrStoreBuilderClosed) {
		t.Fatalf("second Build error = %v", err)
	}
	if err := builder.CreateIndex(StoreIndexDefinition{Name: "later", Paths: []string{"/valid"}}); !errors.Is(err, ErrStoreBuilderClosed) {
		t.Fatalf("CreateIndex after Build error = %v", err)
	}
	if raw, ok := store.GetRaw("bad"); !ok || string(raw.Bytes()) != `{"valid":true}` {
		t.Fatalf("built value = (%q,%v)", raw.Bytes(), ok)
	}
	keys, err := store.Snapshot().AppendIndexRawKeys(nil, "valid", []byte(`true`))
	if err != nil || len(keys) != 1 || keys[0] != "bad" {
		t.Fatalf("built exact index = (%v,%v)", keys, err)
	}

	emptyBuilder, err := NewStoreBuilder(StoreOptions{ChunkDocuments: 2})
	if err != nil {
		t.Fatal(err)
	}
	empty, err := emptyBuilder.Build()
	if err != nil {
		t.Fatal(err)
	}
	if empty.Len() != 0 || empty.Generation() != 0 {
		t.Fatalf("empty Len/Generation = %d/%d", empty.Len(), empty.Generation())
	}
	if _, err := empty.Put("first", []byte(`1`)); err != nil {
		t.Fatal(err)
	}
}
