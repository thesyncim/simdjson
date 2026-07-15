package simdjson

import (
	"bytes"
	"strings"
	"testing"
)

func TestBitmapUTF8RunReject(t *testing.T) {
	// Build a >64KiB indented doc that engages the bitmap engine.
	var b bytes.Buffer
	b.WriteString("{\n")
	for i := 0; i < 3000; i++ {
		b.WriteString(strings.Repeat(" ", 24))
		b.WriteString("\"k")
		for j := 0; j < 3; j++ {
			b.WriteByte(byte('a' + (i+j)%26))
		}
		b.WriteString("\": \"vé\",\n") // valid two-byte UTF-8 in values
	}
	b.WriteString(strings.Repeat(" ", 24))
	b.WriteString("\"end\": \"x\"\n}")
	good := b.Bytes()
	if len(good) < validBitmapMinBytes {
		t.Fatalf("doc too small: %d", len(good))
	}
	if ok, decided := validBitmap(good); !decided || !ok {
		t.Fatalf("expected engine accept: ok=%v decided=%v", ok, decided)
	}
	cases := map[string]func([]byte) []byte{
		"lone continuation":  func(d []byte) []byte { d[bytes.IndexByte(d, 0xc3)+1] = 'x'; return d },
		"truncated lead":     func(d []byte) []byte { d[bytes.IndexByte(d, 0xc3)] = 0xe2; return d },
		"overlong":           func(d []byte) []byte { i := bytes.IndexByte(d, 0xc3); d[i] = 0xc0; d[i+1] = 0xaf; return d },
		"bad at last block":  func(d []byte) []byte { i := bytes.LastIndexByte(d, 0xc3); d[i+1] = 0x20; return d },
		"lead at slice tail": func(d []byte) []byte { i := bytes.LastIndexByte(d, 0xc3); d[i] = 0xf0; return d },
	}
	for name, mut := range cases {
		bad := mut(append([]byte(nil), good...))
		if ok, decided := validBitmap(bad); decided && ok {
			t.Errorf("%s: engine accepted invalid UTF-8", name)
		}
		if Validate(bad) == nil {
			t.Errorf("%s: Validate accepted invalid UTF-8", name)
		}
	}
}
