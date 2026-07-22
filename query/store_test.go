package query

import (
	"fmt"
	"testing"

	"github.com/thesyncim/simdjson"
)

func TestRunSnapshotDeclaredIndexesDifferential(t *testing.T) {
	docs := []string{
		`{"id":1,"tenant":"acme","status":"active","score":10,"nested":{"country":"PT"},"items":[{"sku":"A"}]}`,
		`{"id":2,"tenant":"acme","status":"idle","score":20,"nested":{"country":"US"},"items":[{"sku":"B"}]}`,
		`{"id":3,"tenant":"other","status":"active","score":30,"nested":{"country":"PT"},"items":[{"sku":"B"}]}`,
		`{"id":4,"tenant":"acme","status":"active","score":40,"nested":{"country":"PT"},"items":[{"sku":"A"}]}`,
		`{"id":5,"tenant":"other","status":"idle","score":50,"items":[]}`,
	}
	set := &simdjson.DocSet{ShapeTapes: true}
	store := simdjson.NewStore(simdjson.StoreOptions{ChunkDocuments: 2, ShapeTapes: true})
	for i, doc := range docs {
		if _, err := set.Append([]byte(doc)); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Put(fmt.Sprintf("k%d", i), []byte(doc)); err != nil {
			t.Fatal(err)
		}
	}

	queries := []*Query{
		Select(Path("id"), Path("nested.country")).Where(And(Cmp("tenant", Eq, "acme"), Cmp("status", Eq, "active"))),
		Select(Path("id")).Where(Cmp("nested.country", Eq, "PT")),
		Select(Path("id")).Where(Cmp("/items/0/sku", Eq, "B")),
		Select(Path("id")).Where(Or(Cmp("tenant", Eq, "other"), Cmp("status", Eq, "idle"))).OrderBy("id", Asc),
		Select(Path("id")).Where(Not(Cmp("status", Eq, "idle"))).OrderBy("id", Asc),
		Select(Count(), Sum("score")).Where(And(Cmp("tenant", Eq, "acme"), Cmp("score", Ge, 20))),
		Select(Path("tenant"), Sum("score")).Where(Cmp("nested.country", Eq, "PT")).GroupBy("tenant").OrderBy("tenant", Asc),
	}

	// Compiled plans precede DDL. While Building they take the dense exact path.
	if _, err := store.CreateIndex(simdjson.StoreIndexDefinition{Name: "tenant_status", Paths: []string{"/tenant", "/status"}}); err != nil {
		t.Fatal(err)
	}
	assertSnapshotQueriesEqual(t, queries, set, store.Snapshot(), "building")
	if info, err := store.BackfillIndex("tenant_status", 0); err != nil || info.State != simdjson.StoreIndexReady {
		t.Fatalf("BackfillIndex(tenant_status) = (%+v,%v)", info, err)
	}
	assertSnapshotQueriesEqual(t, queries, set, store.Snapshot(), "compound-ready")

	for _, def := range []simdjson.StoreIndexDefinition{
		{Name: "tenant", Paths: []string{"/tenant"}},
		{Name: "status", Paths: []string{"/status"}},
		{Name: "country", Paths: []string{"/nested/country"}},
		{Name: "sku", Paths: []string{"/items/0/sku"}},
	} {
		if _, err := store.CreateIndex(def); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"tenant", "status", "country", "sku"} {
		info, err := store.BackfillIndex(name, 0)
		if err != nil || info.State != simdjson.StoreIndexReady {
			t.Fatalf("BackfillIndex(%s) = (%+v,%v)", name, info, err)
		}
	}
	assertSnapshotQueriesEqual(t, queries, set, store.Snapshot(), "ready")
}

func assertSnapshotQueriesEqual(t *testing.T, queries []*Query, set *simdjson.DocSet, snapshot simdjson.Snapshot, phase string) {
	t.Helper()
	for i, q := range queries {
		want, err := q.Run(set)
		if err != nil {
			t.Fatalf("%s query %d DocSet: %v", phase, i, err)
		}
		got, err := q.RunSnapshot(snapshot)
		if err != nil {
			t.Fatalf("%s query %d Snapshot: %v", phase, i, err)
		}
		if gotKey, wantKey := resultKey(got), resultKey(want); gotKey != wantKey {
			t.Fatalf("%s query %d mismatch:\n got: %s\nwant: %s", phase, i, gotKey, wantKey)
		}
	}
}

func TestRunSnapshotIntoSteadyAllocs(t *testing.T) {
	store := simdjson.NewStore(simdjson.StoreOptions{ChunkDocuments: 8, ShapeTapes: true})
	for i := 0; i < 128; i++ {
		doc := fmt.Sprintf(`{"id":%d,"bucket":%d,"nested":{"country":"PT"}}`, i, i&7)
		if _, err := store.Put(fmt.Sprintf("k%03d", i), []byte(doc)); err != nil {
			t.Fatal(err)
		}
	}
	for _, def := range []simdjson.StoreIndexDefinition{
		{Name: "bucket", Paths: []string{"/bucket"}},
		{Name: "bucket_country", Paths: []string{"/bucket", "/nested/country"}},
	} {
		info, err := store.CreateIndex(def)
		if err != nil {
			t.Fatal(err)
		}
		for info.State != simdjson.StoreIndexReady {
			info, err = store.BackfillIndex(def.Name, 0)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	q := Select(Count(), Sum("id")).Where(And(Cmp("bucket", Eq, 3), Cmp("nested.country", Eq, "PT")))
	var result Result
	var workspace Workspace
	snapshot := store.Snapshot()
	if err := q.RunSnapshotInto(&result, snapshot, &workspace); err != nil {
		t.Fatal(err)
	}
	allocs := testing.AllocsPerRun(100, func() {
		if err := q.RunSnapshotInto(&result, snapshot, &workspace); err != nil {
			panic(err)
		}
	})
	if allocs != 0 {
		t.Fatalf("RunSnapshotInto allocated %.2f times, want 0", allocs)
	}
}
