package simd

import "testing"

func TestScannerAPINormalizesStart(t *testing.T) {
	src := []byte("plain<text\\\"\xe2\x80\xa8tail")
	starts := []int{-100, -1, 0, 5, len(src), len(src) + 1, len(src) + 100}
	for _, start := range starts {
		clamped := start
		if clamped < 0 {
			clamped = 0
		} else if clamped > len(src) {
			clamped = len(src)
		}

		if got, want := IndexStringSpecial(src, start), scanStringSpecialScalar(src, clamped); got != want {
			t.Errorf("IndexStringSpecial(start=%d) = %d, want %d", start, got, want)
		}
		if got, want := IndexStringSpecialLong(src, start), scanStringSpecialScalar(src, clamped); got != want {
			t.Errorf("IndexStringSpecialLong(start=%d) = %d, want %d", start, got, want)
		}
		if got, want := IndexStringSyntax(src, start), scanStringSyntaxScalar(src, clamped); got != want {
			t.Errorf("IndexStringSyntax(start=%d) = %d, want %d", start, got, want)
		}
		if got, want := IndexHTMLStringSpecial(src, start), scanEncodedHTMLSpecialScalar(src, clamped); got != want {
			t.Errorf("IndexHTMLStringSpecial(start=%d) = %d, want %d", start, got, want)
		}
		if got, want := IndexHTMLStringSyntax(src, start), scanEncodedHTMLSyntaxScalar(src, clamped); got != want {
			t.Errorf("IndexHTMLStringSyntax(start=%d) = %d, want %d", start, got, want)
		}
		if got, want := HasJSONLineSeparator(src, start), hasJSONLineSeparatorScalar(src, clamped); got != want {
			t.Errorf("HasJSONLineSeparator(start=%d) = %v, want %v", start, got, want)
		}

		gotNext, gotBad := ScanStringUnicodeRun(src, start)
		wantNext, wantBad := scanStringUnicodeRun(src, clamped)
		if gotNext != wantNext || gotBad != wantBad {
			t.Errorf("ScanStringUnicodeRun(start=%d) = (%d, %d), want (%d, %d)", start, gotNext, gotBad, wantNext, wantBad)
		}
	}
}

func TestScanUnicodeEscapeRunNormalizesStart(t *testing.T) {
	src := []byte(`\u1234\u5678\u9abc\udef0\u1234\u5678\u9abc\uef01`)
	for _, start := range []int{-100, -1, 0, len(src), len(src) + 1, len(src) + 100} {
		clamped := start
		if clamped < 0 {
			clamped = 0
		} else if clamped > len(src) {
			clamped = len(src)
		}
		end, ok := ScanUnicodeEscapeRun(src, start)
		wantEnd, wantOK := scanUnicodeEscapeRun(src, clamped)
		if end != wantEnd || ok != wantOK {
			t.Errorf("ScanUnicodeEscapeRun(start=%d) = (%d, %v), want (%d, %v)", start, end, ok, wantEnd, wantOK)
		}
	}
}
