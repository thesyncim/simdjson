package simdjson

import (
	"strconv"
	"strings"
	"testing"
)

type benchSmall struct {
	ID   int    `json:"id"`
	OK   bool   `json:"ok"`
	Name string `json:"name"`
}

type benchRecord struct {
	ID      int        `json:"id"`
	Active  bool       `json:"active"`
	Name    string     `json:"name"`
	Message string     `json:"message"`
	Scores  [3]float64 `json:"scores"`
}

type benchMeta struct {
	Count  int    `json:"count"`
	Source string `json:"source"`
}

type benchDocument struct {
	Items []benchRecord `json:"items"`
	Meta  benchMeta     `json:"meta"`
}

var benchSmallJSON = []byte(`{"id":1,"ok":true,"name":"sim"}`)

func benchRecordsJSON(count int) []byte {
	var out strings.Builder
	out.Grow(count * 128)
	out.WriteString(`{"items":[`)
	for i := 0; i < count; i++ {
		if i != 0 {
			out.WriteByte(',')
		}
		out.WriteString(`{"id":`)
		out.WriteString(strconv.Itoa(i))
		out.WriteString(`,"active":`)
		if i&1 == 0 {
			out.WriteString("true")
		} else {
			out.WriteString("false")
		}
		out.WriteString(`,"name":"record-`)
		out.WriteString(strconv.Itoa(i))
		out.WriteString(`","message":"plain ascii payload sized to exercise vector scanners","scores":[1,2.5,-3e4]}`)
	}
	out.WriteString(`],"meta":{"count":`)
	out.WriteString(strconv.Itoa(count))
	out.WriteString(`,"source":"benchmark"}}`)
	return []byte(out.String())
}

func BenchmarkDecodeSmall(b *testing.B) {
	decoder, err := CompileDecoder[benchSmall](DecoderOptions{ZeroCopy: true, CaseSensitive: true})
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(benchSmallJSON)))
	b.ReportAllocs()
	for range b.N {
		var dst benchSmall
		if err := decoder.Decode(benchSmallJSON, &dst); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDecodeMedium(b *testing.B) {
	src := benchRecordsJSON(32)
	decoder, err := CompileDecoder[benchDocument](DecoderOptions{ZeroCopy: true, CaseSensitive: true})
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	for range b.N {
		var dst benchDocument
		if err := decoder.Decode(src, &dst); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDecodeLarge(b *testing.B) {
	src := benchRecordsJSON(1024)
	decoder, err := CompileDecoder[benchDocument](DecoderOptions{ZeroCopy: true, CaseSensitive: true})
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	for range b.N {
		var dst benchDocument
		if err := decoder.Decode(src, &dst); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDecodeLargeReused(b *testing.B) {
	src := benchRecordsJSON(1024)
	decoder, err := CompileDecoder[benchDocument](DecoderOptions{ZeroCopy: true, CaseSensitive: true})
	if err != nil {
		b.Fatal(err)
	}
	dst := benchDocument{Items: make([]benchRecord, 0, 1024)}
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if err := decoder.Decode(src, &dst); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDecodeLargeIndented(b *testing.B) {
	compact := benchRecordsJSON(1024)
	src, err := Indent(compact, "", "  ")
	if err != nil {
		b.Fatal(err)
	}
	decoder, err := CompileDecoder[benchDocument](DecoderOptions{ZeroCopy: true, CaseSensitive: true})
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	for range b.N {
		var dst benchDocument
		if err := decoder.Decode(src, &dst); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDecodeLargeOwned(b *testing.B) {
	src := benchRecordsJSON(1024)
	decoder, err := CompileDecoder[benchDocument](DecoderOptions{CaseSensitive: true})
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	for range b.N {
		var dst benchDocument
		if err := decoder.Decode(src, &dst); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParseAnyLarge(b *testing.B) {
	src := benchRecordsJSON(1024)
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	for range b.N {
		if _, err := ParseAny(src); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParseLarge(b *testing.B) {
	src := benchRecordsJSON(1024)
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	for range b.N {
		if _, err := ParseOptions(src, Options{ZeroCopy: true, Preallocate: true}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDecodeLargeShuffledKeys(b *testing.B) {
	// Same schema as benchRecordsJSON, but record members are rotated so the
	// JSON order never matches the struct field order.
	var out strings.Builder
	count := 1024
	out.Grow(count * 128)
	out.WriteString(`{"items":[`)
	for i := 0; i < count; i++ {
		if i != 0 {
			out.WriteByte(',')
		}
		out.WriteString(`{"message":"plain ascii payload sized to exercise vector scanners","scores":[1,2.5,-3e4],"id":`)
		out.WriteString(strconv.Itoa(i))
		out.WriteString(`,"active":true,"name":"record-`)
		out.WriteString(strconv.Itoa(i))
		out.WriteString(`"}`)
	}
	out.WriteString(`],"meta":{"count":`)
	out.WriteString(strconv.Itoa(count))
	out.WriteString(`,"source":"benchmark"}}`)
	src := []byte(out.String())

	decoder, err := CompileDecoder[benchDocument](DecoderOptions{ZeroCopy: true, CaseSensitive: true})
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	for range b.N {
		var dst benchDocument
		if err := decoder.Decode(src, &dst); err != nil {
			b.Fatal(err)
		}
	}
}
