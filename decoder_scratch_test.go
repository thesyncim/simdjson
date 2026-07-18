package simdjson

import (
	"fmt"
	"reflect"
	"runtime"
	"strconv"
	"sync"
	"testing"
)

type decoderScratchValue struct {
	Name   string
	Values []int
	Index  map[string]*int
	Next   *decoderScratchValue
}

type decoderScratchTextKey string

var retainedDecoderScratchTextKey *decoderScratchTextKey

func (k *decoderScratchTextKey) UnmarshalText(text []byte) error {
	*k = decoderScratchTextKey(text)
	retainedDecoderScratchTextKey = k
	return nil
}

type decoderScratchHook int

var retainedDecoderScratchHooks []*decoderScratchHook

func (v *decoderScratchHook) UnmarshalJSON(src []byte) error {
	value, err := strconv.Atoi(string(src))
	*v = decoderScratchHook(value)
	if retainedDecoderScratchHooks != nil {
		retainedDecoderScratchHooks = append(retainedDecoderScratchHooks, v)
	}
	return err
}

type decoderScratchNativeHook int

func (v *decoderScratchNativeHook) UnmarshalSimdJSON(cursor DecodeCursor) (DecodeCursor, error) {
	var value int
	err := cursor.Int(&value)
	*v = decoderScratchNativeHook(value)
	return cursor, err
}

type decoderScratchRecursive map[string]decoderScratchRecursive

type decoderScratchOversized [decoderMapScratchRetentionBytes]byte

type decoderScratchInlineOversized struct {
	Extra map[string]decoderScratchOversized `json:",inline"`
}

func TestDecoderMapScratchEligibility(t *testing.T) {
	ordinary, err := CompileDecoder[map[string]decoderScratchValue](DecoderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if ordinary.root.decMapScratch == 0 || ordinary.scratch == nil {
		t.Fatal("ordinary map did not receive bounded decoder scratch")
	}

	textKey, err := CompileDecoder[map[decoderScratchTextKey]int](DecoderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if textKey.root.decMapScratch != 0 {
		t.Fatal("text-unmarshaler key received reusable reflection storage")
	}

	hook, err := CompileDecoder[map[string]decoderScratchHook](DecoderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if hook.root.decMapScratch == 0 {
		t.Fatal("detached standard-method element did not receive decoder scratch")
	}

	nativeHook, err := CompileDecoder[map[string]decoderScratchNativeHook](DecoderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if nativeHook.root.decMapScratch != 0 {
		t.Fatal("native-hook element received reusable reflection storage")
	}

	recursive, err := CompileDecoder[decoderScratchRecursive](DecoderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if recursive.root.decMapScratch == 0 {
		t.Fatal("ordinary recursive map did not receive decoder scratch")
	}

	oversized, err := CompileDecoder[map[string]decoderScratchOversized](DecoderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if oversized.root.decMapScratch != 0 || oversized.scratch != nil {
		t.Fatal("oversized map boxes may be retained")
	}

	inlineOversized, err := CompileDecoder[decoderScratchInlineOversized](DecoderOptions{InlineFields: true})
	if err != nil {
		t.Fatal(err)
	}
	if inlineOversized.root.inlineMap.decMapScratch != 0 || inlineOversized.scratch != nil {
		t.Fatal("oversized inline map boxes may be retained")
	}
}

func TestDecoderMapStandardReceiverScratchLifetime(t *testing.T) {
	retainedDecoderScratchHooks = make([]*decoderScratchHook, 0, 4)
	t.Cleanup(func() { retainedDecoderScratchHooks = nil })
	decoder, err := CompileDecoder[map[string]decoderScratchHook](DecoderOptions{ZeroCopy: true})
	if err != nil {
		t.Fatal(err)
	}
	var first map[string]decoderScratchHook
	if err := decoder.Decode([]byte(`{"a":1,"b":2}`), &first); err != nil {
		t.Fatal(err)
	}
	if len(retainedDecoderScratchHooks) != 2 || first["a"] != 1 || first["b"] != 2 {
		t.Fatalf("first decode: retained=%d map=%v", len(retainedDecoderScratchHooks), first)
	}
	firstReceivers := append([]*decoderScratchHook(nil), retainedDecoderScratchHooks...)
	retainedDecoderScratchHooks = retainedDecoderScratchHooks[:0]

	var second map[string]decoderScratchHook
	if err := decoder.Decode([]byte(`{"a":3,"b":4}`), &second); err != nil {
		t.Fatal(err)
	}
	if len(retainedDecoderScratchHooks) != 2 || second["a"] != 3 || second["b"] != 4 {
		t.Fatalf("second decode: retained=%d map=%v", len(retainedDecoderScratchHooks), second)
	}
	for i := range firstReceivers {
		if firstReceivers[i] == retainedDecoderScratchHooks[i] {
			t.Fatalf("receiver %d reused across operations", i)
		}
	}
	*firstReceivers[0] = 99
	if first["a"] != 1 || first["b"] != 2 || second["a"] != 3 || second["b"] != 4 {
		t.Fatal("retained standard receiver aliases a decoded map or later scratch")
	}

}

func TestDecoderMapStandardReceiverScratchAllocs(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector adds bookkeeping allocations")
	}
	decoder, err := CompileDecoder[map[string]decoderScratchHook](DecoderOptions{ZeroCopy: true})
	if err != nil {
		t.Fatal(err)
	}
	src := []byte(`{"a":3,"b":4}`)
	dst := make(map[string]decoderScratchHook, 2)
	if err := decoder.Decode(src, &dst); err != nil {
		t.Fatal(err)
	}
	allocs := testing.AllocsPerRun(1000, func() {
		if err := decoder.Decode(src, &dst); err != nil {
			panic(err)
		}
	})
	if allocs > 2 {
		t.Fatalf("two standard-method map entries allocated %.1f times per decode, want <=2", allocs)
	}
}

func TestDecoderMapScratchAllocs(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector adds bookkeeping allocations")
	}
	src := []byte(`{"alpha":1,"bravo":2,"charlie":3,"delta":4}`)
	for _, tc := range []struct {
		name string
		opts DecoderOptions
		want float64
	}{
		{name: "owned", opts: DecoderOptions{}, want: 1},
		{name: "zero-copy", opts: DecoderOptions{ZeroCopy: true}, want: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decoder, err := CompileDecoder[map[string]int](tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			dst := make(map[string]int, 4)
			if err := decoder.Decode(src, &dst); err != nil {
				t.Fatal(err)
			}
			allocs := testing.AllocsPerRun(1000, func() {
				if err := decoder.Decode(src, &dst); err != nil {
					panic(err)
				}
			})
			if allocs != tc.want {
				t.Fatalf("reused map allocated %.1f times per decode, want %.0f", allocs, tc.want)
			}
		})
	}
}

func TestDecoderMapScratchCleared(t *testing.T) {
	decoder, err := CompileDecoder[map[string]decoderScratchValue](DecoderOptions{ZeroCopy: true})
	if err != nil {
		t.Fatal(err)
	}
	src := []byte(`{"first":{"Name":"one","Values":[1,2],"Index":{"x":3},"Next":{"Name":"next"}}}`)
	var dst map[string]decoderScratchValue
	if err := decoder.Decode(src, &dst); err != nil {
		t.Fatal(err)
	}
	want := decoderScratchValue{
		Name:   "one",
		Values: []int{1, 2},
		Index:  map[string]*int{"x": new(int)},
		Next:   &decoderScratchValue{Name: "next"},
	}
	*want.Index["x"] = 3
	if !reflect.DeepEqual(dst["first"], want) {
		t.Fatalf("decoded value = %#v, want %#v", dst["first"], want)
	}

	assertClear := func() {
		state := decoder.scratch.take()
		defer decoder.scratch.release(state)
		slot := int(decoder.root.decMapScratch - 1)
		scratch := &state.operation.maps[slot]
		if scratch.inUse {
			t.Fatal("released map scratch remains in use")
		}
		if !scratch.key.IsValid() || !scratch.key.IsZero() {
			t.Fatal("released map key box retained a value")
		}
		if !scratch.element.IsValid() || !scratch.element.IsZero() {
			t.Fatal("released map element box retained a value")
		}
	}
	assertClear()

	if err := decoder.Decode([]byte(`{"bad":{"Values":[1,`), &dst); err == nil {
		t.Fatal("truncated map decoded without error")
	}
	assertClear()
	runtime.GC()
	if !reflect.DeepEqual(dst["first"], want) {
		t.Fatal("clearing pooled scratch corrupted a retained map value")
	}
}

func TestDecoderMapTextKeyReceiverNotReused(t *testing.T) {
	retainedDecoderScratchTextKey = nil
	t.Cleanup(func() { retainedDecoderScratchTextKey = nil })
	decoder, err := CompileDecoder[map[decoderScratchTextKey]int](DecoderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var first map[decoderScratchTextKey]int
	if err := decoder.Decode([]byte(`{"first":1}`), &first); err != nil {
		t.Fatal(err)
	}
	firstReceiver := retainedDecoderScratchTextKey
	if firstReceiver == nil || *firstReceiver != "first" {
		t.Fatalf("first retained key receiver = %v", firstReceiver)
	}
	var second map[decoderScratchTextKey]int
	if err := decoder.Decode([]byte(`{"second":2}`), &second); err != nil {
		t.Fatal(err)
	}
	if retainedDecoderScratchTextKey == firstReceiver {
		t.Fatal("text key receiver storage was reused across decode operations")
	}
	*firstReceiver = "changed"
	if first["first"] != 1 || second["second"] != 2 {
		t.Fatal("retained text key receiver aliases a decoded map key")
	}
}

func TestDecoderMapScratchRecursive(t *testing.T) {
	decoder, err := CompileDecoder[decoderScratchRecursive](DecoderOptions{ZeroCopy: true})
	if err != nil {
		t.Fatal(err)
	}
	var dst decoderScratchRecursive
	if err := decoder.Decode([]byte(`{"a":{"b":{"c":{}}},"d":{}}`), &dst); err != nil {
		t.Fatal(err)
	}
	if _, ok := dst["a"]["b"]["c"]; !ok {
		t.Fatalf("recursive map = %#v", dst)
	}
	if _, ok := dst["d"]; !ok {
		t.Fatalf("recursive map = %#v", dst)
	}
}

func TestDecoderMapScratchConcurrent(t *testing.T) {
	decoder, err := CompileDecoder[map[string]decoderScratchValue](DecoderOptions{ZeroCopy: true, Replace: true})
	if err != nil {
		t.Fatal(err)
	}
	src := []byte(`{"item":{"Name":"value","Values":[1,2,3],"Index":{"n":9}}}`)
	start := make(chan struct{})
	errs := make(chan error, 16)
	var wait sync.WaitGroup
	for worker := range 16 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			var dst map[string]decoderScratchValue
			for iteration := range 100 {
				if err := decoder.Decode(src, &dst); err != nil {
					errs <- err
					return
				}
				value := dst["item"]
				if value.Name != "value" || len(value.Values) != 3 || value.Index["n"] == nil || *value.Index["n"] != 9 {
					errs <- fmt.Errorf("worker %d iteration %d decoded %#v", worker, iteration, value)
					return
				}
			}
		}()
	}
	close(start)
	wait.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
