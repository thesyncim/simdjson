package simdjson

import (
	"reflect"
	"strings"
	"sync"
	"testing"
)

type marshalHintRetentionValue struct {
	Payload string `json:"payload"`
}

func TestMarshalHintRecoversAfterOversizedValue(t *testing.T) {
	typ := reflect.TypeFor[marshalHintRetentionValue]()
	marshalEncoders.Delete(typ)
	t.Cleanup(func() { marshalEncoders.Delete(typ) })

	huge := marshalHintRetentionValue{Payload: strings.Repeat("x", int(marshalSizeHintMax)*4)}
	if _, err := Marshal(&huge); err != nil {
		t.Fatal(err)
	}
	entry, ok := marshalEncoders.Load(typ)
	if !ok {
		t.Fatal("Marshal did not cache its encoder")
	}
	hint := entry.(*cachedEncoder[marshalHintRetentionValue]).sizeHint.Load()
	wantOutlierHint := marshalSizeHintMin * marshalSizeHintGrowth
	if hint != wantOutlierHint {
		t.Fatalf("oversized observation stored hint %d, want %d", hint, wantOutlierHint)
	}

	tiny := marshalHintRetentionValue{}
	firstTiny, err := Marshal(&tiny)
	if err != nil {
		t.Fatal(err)
	}
	if cap(firstTiny) > int(wantOutlierHint) {
		t.Fatalf("first tiny result retained oversized capacity %d, budget %d", cap(firstTiny), wantOutlierHint)
	}
	secondTiny, err := Marshal(&tiny)
	if err != nil {
		t.Fatal(err)
	}
	if cap(secondTiny) > int(marshalSizeHintMin) {
		t.Fatalf("tiny result did not recover to minimum hint: cap=%d, want <= %d", cap(secondTiny), marshalSizeHintMin)
	}
}

func TestCodecMarshalHintRecoversAfterOversizedValue(t *testing.T) {
	codec, err := CompileCodec[marshalHintRetentionValue](CodecOptions{})
	if err != nil {
		t.Fatal(err)
	}
	huge := marshalHintRetentionValue{Payload: strings.Repeat("x", int(marshalSizeHintMax)*4)}
	if _, err := codec.Marshal(&huge); err != nil {
		t.Fatal(err)
	}
	wantOutlierHint := marshalSizeHintMin * marshalSizeHintGrowth
	if hint := codec.hint.Load(); hint != wantOutlierHint {
		t.Fatalf("oversized observation stored hint %d, want %d", hint, wantOutlierHint)
	}

	tiny := marshalHintRetentionValue{}
	if _, err := codec.Marshal(&tiny); err != nil {
		t.Fatal(err)
	}
	secondTiny, err := codec.Marshal(&tiny)
	if err != nil {
		t.Fatal(err)
	}
	if cap(secondTiny) > int(marshalSizeHintMin) {
		t.Fatalf("tiny result did not recover to minimum hint: cap=%d, want <= %d", cap(secondTiny), marshalSizeHintMin)
	}
}

func TestMarshalHintConcurrentGrowth(t *testing.T) {
	typ := reflect.TypeFor[marshalHintRetentionValue]()
	marshalEncoders.Delete(typ)
	t.Cleanup(func() { marshalEncoders.Delete(typ) })
	huge := marshalHintRetentionValue{Payload: strings.Repeat("x", int(marshalSizeHintMax)*4)}
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := Marshal(&huge); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	entry, ok := marshalEncoders.Load(typ)
	if !ok {
		t.Fatal("Marshal did not cache its encoder")
	}
	if got := entry.(*cachedEncoder[marshalHintRetentionValue]).sizeHint.Load(); got != marshalSizeHintMax {
		t.Fatalf("concurrent growth stored %d, want %d", got, marshalSizeHintMax)
	}
}
