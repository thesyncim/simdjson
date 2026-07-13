package simd

import "testing"

func TestCopyStringPrefixPublicContract(t *testing.T) {
	clean := []byte("0123456789abcdef0123456789abcdef")
	dst := make([]byte, len(clean))
	if got := CopyStringPrefix(dst, clean); got != len(clean) || string(dst) != string(clean) {
		t.Fatalf("CopyStringPrefix(clean) = %d or changed bytes", got)
	}
	if got := CopyStringPrefix(make([]byte, len(clean)-1), clean); got != -1 {
		t.Fatalf("CopyStringPrefix(short dst) = %d, want -1", got)
	}
	if got := CopyStringPrefix(clean, clean); got != -1 {
		t.Fatalf("CopyStringPrefix(identical slices) = %d, want -1", got)
	}
	storage := make([]byte, len(clean)+8)
	copy(storage, clean)
	if got := CopyStringPrefix(storage[4:4+len(clean)], storage[:len(clean)]); got != -1 {
		t.Fatalf("CopyStringPrefix(overlap) = %d, want -1", got)
	}
	for _, special := range []byte{'"', '\\', 0, 0x1f, 0x80, 0xff} {
		dirty := append([]byte(nil), clean...)
		at := len(dirty) / 2
		dirty[at] = special
		if got := CopyStringPrefix(dst, dirty); got != at {
			t.Fatalf("CopyStringPrefix(byte %#02x) = %d, want %d", special, got, at)
		}
		if string(dst[:at]) != string(dirty[:at]) {
			t.Fatalf("CopyStringPrefix(byte %#02x) changed clean prefix", special)
		}
	}
}

func TestCopyHTMLStringPrefixPublicContract(t *testing.T) {
	clean := []byte("0123456789abcdef0123456789abcdef")
	dst := make([]byte, len(clean))
	if got := CopyHTMLStringPrefix(dst, clean); got != len(clean) || string(dst) != string(clean) {
		t.Fatalf("CopyHTMLStringPrefix(clean) = %d or changed bytes", got)
	}
	for _, special := range []byte{'"', '\\', '<', '>', '&', 0, 0x1f, 0x80, 0xff} {
		dirty := append([]byte(nil), clean...)
		at := len(dirty) / 2
		dirty[at] = special
		if got := CopyHTMLStringPrefix(dst, dirty); got != at {
			t.Fatalf("CopyHTMLStringPrefix(byte %#02x) = %d, want %d", special, got, at)
		}
	}
}
