package simdjson_test

import (
	"fmt"
	"time"

	"github.com/thesyncim/simdjson"
)

func ExampleStore() {
	var store simdjson.Store
	_, _ = store.Put("user:42", []byte(`{"name":"Ada","score":7}`))
	before := store.Snapshot()

	created, _ := store.Put("user:42", []byte(`{"name":"Ada","score":8}`))
	current, _ := store.GetRaw("user:42")
	old, _ := before.GetRaw("user:42")
	fmt.Printf("created=%v current=%s old=%s\n", created, current.Bytes(), old.Bytes())

	// Output:
	// created=false current={"name":"Ada","score":8} old={"name":"Ada","score":7}
}

func ExampleStore_SetDeadline() {
	var store simdjson.Store
	_, _ = store.Put("session", []byte(`{"user":42}`))
	deadline := time.Now().Add(time.Hour)
	store.SetDeadline("session", deadline)
	before := store.Snapshot()

	fmt.Println(store.ExpireDue(deadline.Add(time.Second), 0))
	_, current := store.GetRaw("session")
	_, old := before.GetRaw("session")
	fmt.Println(current, old)

	// Output:
	// 1
	// false true
}

func ExampleStore_AddIndex() {
	store := simdjson.NewStore(simdjson.StoreOptions{ChunkDocuments: 2, ShapeTapes: true})
	_, _ = store.Put("a", []byte(`{"team":"compiler"}`))
	_, _ = store.Put("b", []byte(`{"team":"runtime"}`))
	_, _ = store.Put("c", []byte(`{"team":"compiler"}`))

	info, _ := store.AddIndex("team-search", simdjson.StoreIndexPostings)
	for info.State != simdjson.StoreIndexReady {
		info, _ = store.BackfillIndex("team-search", 1)
	}

	src := []byte(`"compiler"`)
	need, _ := simdjson.RequiredIndexEntries(src)
	needle, _ := simdjson.BuildIndex(src, make([]simdjson.IndexEntry, 0, need))
	keys := store.AppendWhereContainsIndexKeys(make([]string, 0, store.Len()), "team", needle)
	fmt.Println(keys)

	// Output:
	// [a c]
}
