package simdjson_test

import (
	"bytes"
	"errors"
	"fmt"
	"time"

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

func ExampleMarshal() {
	event := exampleEvent{ID: 7, Name: "launch", Enabled: true}
	data, err := simdjson.Marshal(&event)
	if err != nil {
		panic(err)
	}

	fmt.Println(string(data))
	// Output: {"id":7,"name":"launch","enabled":true}
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

func ExampleGetRaw() {
	src := []byte(`{"user":{"name":"ada","tags":["admin","ops"]}}`)

	tag, ok, err := simdjson.GetRaw(src, "/user/tags/1")
	if err != nil {
		panic(err)
	}
	if !ok {
		panic("missing tag")
	}
	text, _, err := tag.Text()
	if err != nil {
		panic(err)
	}

	fmt.Println(text)
	// Output: ops
}

func ExampleUnmarshal_dynamic() {
	var value any
	if err := simdjson.Unmarshal([]byte(`{"name":"ada","scores":[1,2.5]}`), &value); err != nil {
		panic(err)
	}

	object := value.(map[string]any)
	fmt.Println(object["name"], object["scores"].([]any)[1])
	// Output: ada 2.5
}

func ExampleValid() {
	fmt.Println(simdjson.Valid([]byte(`{"strict":true}`)))
	fmt.Println(simdjson.Valid([]byte(`{"trailing":1,}`)))
	// Output:
	// true
	// false
}

func ExampleAppendCompact() {
	compact, err := simdjson.AppendCompact(nil, []byte("{\n  \"a\": [1, 2]\n}"))
	if err != nil {
		panic(err)
	}

	fmt.Println(string(compact))
	// Output: {"a":[1,2]}
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

func ExampleDecoderOptions() {
	decoder, err := simdjson.CompileDecoder[exampleEvent](simdjson.DecoderOptions{Replace: true})
	if err != nil {
		panic(err)
	}

	// Replace resets fields the document does not mention; the default
	// merges like encoding/json and would keep Name.
	event := exampleEvent{ID: 1, Name: "stale", Enabled: true}
	if err := decoder.Decode([]byte(`{"id":2}`), &event); err != nil {
		panic(err)
	}

	fmt.Printf("%d %q %t\n", event.ID, event.Name, event.Enabled)
	// Output: 2 "" false
}

func ExampleCompileEncoder() {
	type stamped struct {
		Name string    `json:"name"`
		At   time.Time `json:"at"`
	}
	encoder, err := simdjson.CompileEncoder[stamped](simdjson.EncoderOptions{})
	if err != nil {
		panic(err)
	}

	value := stamped{Name: "launch", At: time.Date(2026, 7, 11, 1, 30, 0, 0, time.UTC)}
	out, err := encoder.AppendJSON(nil, &value)
	if err != nil {
		panic(err)
	}

	fmt.Println(string(out))
	// Output: {"name":"launch","at":"2026-07-11T01:30:00Z"}
}

func ExampleCompileCodec() {
	codec, err := simdjson.CompileCodec[exampleEvent](simdjson.CodecOptions{})
	if err != nil {
		panic(err)
	}

	var stream bytes.Buffer
	w := simdjson.NewWriter(&stream)
	for _, event := range []exampleEvent{{ID: 1, Name: "boot"}, {ID: 2, Name: "run"}} {
		if err := codec.EncodeTo(w, &event); err != nil {
			panic(err)
		}
		w.Newline()
	}
	if err := w.Close(); err != nil {
		panic(err)
	}

	r := simdjson.NewReader(&stream)
	var event exampleEvent
	for simdjson.DecodeNext(r, codec.Decoder(), &event) {
		fmt.Println(event.ID, event.Name)
	}
	if err := r.Err(); err != nil {
		panic(err)
	}
	// Output:
	// 1 boot
	// 2 run
}

// examplePoint decodes itself through a DecodeCursor, reading the members it
// models and skipping the rest.
type examplePoint struct {
	X, Y int64
}

func (p *examplePoint) UnmarshalSimdJSON(c *simdjson.DecodeCursor) error {
	if err := c.BeginObject("examplePoint"); err != nil {
		return err
	}
	for first := true; ; first = false {
		key, ok, err := c.NextField(first)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		switch key {
		case "x":
			err = c.Int64(&p.X)
		case "y":
			err = c.Int64(&p.Y)
		default:
			err = c.Skip()
		}
		if err != nil {
			return err
		}
	}
}

func ExampleUnmarshalerSimd() {
	var point examplePoint
	if err := simdjson.Unmarshal([]byte(`{"x":3,"note":"ignored","y":4}`), &point); err != nil {
		panic(err)
	}

	fmt.Println(point.X, point.Y)
	// Output: 3 4
}
