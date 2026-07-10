package simdjson_test

import (
	"errors"
	"fmt"

	"github.com/thesyncim/simdjson"
)

type exampleEvent struct {
	ID      int    `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

func ExampleUnmarshal() {
	var event exampleEvent
	if err := simdjson.Unmarshal([]byte(`{"id":7,"name":"launch","enabled":true}`), &event); err != nil {
		panic(err)
	}

	fmt.Printf("%d %s %t\n", event.ID, event.Name, event.Enabled)
	// Output: 7 launch true
}

func ExampleDecodeError() {
	type batch struct {
		Events []exampleEvent `json:"events"`
	}

	var dst batch
	err := simdjson.Unmarshal([]byte(`{"events":[{"id":1},{"id":"two"}]}`), &dst)

	var decodeErr *simdjson.DecodeError
	if errors.As(err, &decodeErr) {
		fmt.Println(decodeErr.Path)
		fmt.Println(decodeErr)
	}
	// Output:
	// events[1].id
	// simdjson: cannot decode JSON at byte 26 into int at events[1].id: expected number
}

func ExampleCompileDecoder() {
	decoder, err := simdjson.CompileDecoder[exampleEvent](simdjson.DecoderOptions{
		ZeroCopy:      true,
		CaseSensitive: true,
	})
	if err != nil {
		panic(err)
	}

	var event exampleEvent
	if err := decoder.Decode([]byte(`{"id":7,"name":"launch","enabled":true}`), &event); err != nil {
		panic(err)
	}

	fmt.Printf("%d %s %t\n", event.ID, event.Name, event.Enabled)
	// Output: 7 launch true
}

func ExampleBuildIndex() {
	src := []byte(`{"items":[{"id":7}]}`)
	var storage [8]simdjson.IndexEntry

	index, err := simdjson.BuildIndex(src, storage[:])
	if err != nil {
		panic(err)
	}
	id, ok, err := index.PointerCompiled(simdjson.MustCompilePointer("/items/0/id"))
	if err != nil {
		panic(err)
	}
	if !ok {
		panic("missing item id")
	}
	n, ok := id.Int64()
	if !ok {
		panic("item id is not an integer")
	}

	fmt.Println(n)
	// Output: 7
}
