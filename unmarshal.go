package simdjson

import (
	"reflect"
	"sync"
)

// unmarshalDecoders caches one default-option decoder per destination type.
// Values are *cachedDecoder[T] stored under reflect.TypeFor[T]().
var unmarshalDecoders sync.Map

type cachedDecoder[T any] struct {
	decoder Decoder[T]
	err     error
}

// Unmarshal decodes src into dst like encoding/json.Unmarshal: decoded strings
// own their storage and field names match case-insensitively after an exact
// match. The decoder for each destination type is compiled once and cached for
// the lifetime of the process.
//
// Hot paths that decode one type repeatedly should call CompileDecoder once
// and reuse the returned Decoder; that also unlocks ZeroCopy and the other
// DecoderOptions.
func Unmarshal[T any](src []byte, dst *T) error {
	entry, ok := unmarshalDecoders.Load(reflect.TypeFor[T]())
	if !ok {
		entry = newCachedDecoder[T]()
	}
	cached := entry.(*cachedDecoder[T])
	if cached.err != nil {
		return cached.err
	}
	return cached.decoder.Decode(src, dst)
}

//go:noinline
func newCachedDecoder[T any]() any {
	decoder, err := CompileDecoder[T](DecoderOptions{})
	entry, _ := unmarshalDecoders.LoadOrStore(reflect.TypeFor[T](), &cachedDecoder[T]{decoder: decoder, err: err})
	return entry
}
