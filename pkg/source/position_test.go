package source

import "testing"

// multibyteLine is "a あ 😀 b\n" with no spaces:
//
//	'a'  1 byte  | 1 UTF-16 unit  | 1 code point
//	'あ' 3 bytes | 1 UTF-16 unit  | 1 code point
//	'😀' 4 bytes | 2 UTF-16 units | 1 code point
//	'b'  1 byte  | 1 UTF-16 unit  | 1 code point
//
// Byte offsets: a@0, あ@1, 😀@4, b@8, '\n'@9, size 10.
const multibyteLine = "aあ😀b\n"

func TestFileLineColumn(t *testing.T) {
	file := NewFile("u.belt", []byte(multibyteLine))

	tests := []struct {
		name       string
		offset     int
		enc        Encoding
		wantLine   int
		wantColumn int
	}{
		{"byte before b", 8, ByteEncoding, 0, 8},
		{"utf16 before b", 8, UTF16Encoding, 0, 4},
		{"utf32 before b", 8, UTF32Encoding, 0, 3},
		{"utf16 before emoji", 4, UTF16Encoding, 0, 2},
		{"utf16 at newline", 9, UTF16Encoding, 0, 5},
		{"utf32 at newline", 9, UTF32Encoding, 0, 4},
		{"start of file", 0, UTF16Encoding, 0, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			line, column := file.LineColumn(tt.offset, tt.enc)
			if line != tt.wantLine || column != tt.wantColumn {
				t.Errorf("LineColumn(%d, %s) = (%d, %d), want (%d, %d)",
					tt.offset, tt.enc, line, column, tt.wantLine, tt.wantColumn)
			}
		})
	}
}

func TestFileOffsetAt(t *testing.T) {
	file := NewFile("u.belt", []byte(multibyteLine))

	tests := []struct {
		name   string
		line   int
		column int
		enc    Encoding
		want   int
	}{
		{"utf16 col 2 -> emoji start", 0, 2, UTF16Encoding, 4},
		{"utf16 col 4 -> b", 0, 4, UTF16Encoding, 8},
		{"utf16 col 5 -> line end", 0, 5, UTF16Encoding, 9},
		{"utf32 col 3 -> b", 0, 3, UTF32Encoding, 8},
		{"byte col 8 -> b", 0, 8, ByteEncoding, 8},
		{"column past line clamps to end", 0, 99, UTF16Encoding, 9},
		{"negative line clamps to start", -1, 0, ByteEncoding, 0},
		{"line past end clamps to size", 5, 0, ByteEncoding, 10},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := file.OffsetAt(tt.line, tt.column, tt.enc); got != tt.want {
				t.Errorf("OffsetAt(%d, %d, %s) = %d, want %d",
					tt.line, tt.column, tt.enc, got, tt.want)
			}
		})
	}
}

// TestForwardReverseRoundTrip checks that Position and its inverse Offset round
// trip for every byte offset, across multibyte and multi-line sources.
func TestForwardReverseRoundTrip(t *testing.T) {
	for _, src := range []string{multibyteLine, "ab\ncd", "", "\n\n", "trailing\n"} {
		file := NewFile("rt.belt", []byte(src))
		for off := range file.Size() + 1 {
			p := file.Position(off)
			if got := file.Offset(p.Line, p.Column); got != off {
				t.Errorf("src %q: offset %d -> %+v -> %d", src, off, p, got)
			}
		}
	}
}

func TestFileSpan(t *testing.T) {
	file := NewFile("s.belt", []byte("ab\ncd"))
	span := file.Span(1, 4) // 'b' on line 1 .. 'd' on line 2

	if span.Start != (Position{ByteOffset: 1, Line: 1, Column: 2}) {
		t.Errorf("Start = %+v", span.Start)
	}
	if span.End != (Position{ByteOffset: 4, Line: 2, Column: 2}) {
		t.Errorf("End = %+v", span.End)
	}
	if got := span.Len(); got != 3 {
		t.Errorf("Len() = %d, want 3", got)
	}
}

func TestEncodingString(t *testing.T) {
	for enc, want := range map[Encoding]string{
		ByteEncoding:  "utf-8",
		UTF16Encoding: "utf-16",
		UTF32Encoding: "utf-32",
		Encoding(99):  "unknown",
	} {
		if got := enc.String(); got != want {
			t.Errorf("Encoding(%d).String() = %q, want %q", int(enc), got, want)
		}
	}
}
