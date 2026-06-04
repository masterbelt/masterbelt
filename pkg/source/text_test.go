package source

import (
	"bytes"
	"math/rand"
	"testing"
)

// naiveSplice is the obvious, slow reference implementation of an edit.
func naiveSplice(src []byte, start, end int, repl []byte) []byte {
	out := make([]byte, 0, len(src)-(end-start)+len(repl))
	out = append(out, src[:start]...)
	out = append(out, repl...)
	out = append(out, src[end:]...)
	return out
}

func TestTextBasics(t *testing.T) {
	text := NewText([]byte("ab\ncd"))

	if text.Len() != 5 {
		t.Errorf("Len() = %d, want 5", text.Len())
	}
	if got := text.Bytes(); !bytes.Equal(got, []byte("ab\ncd")) {
		t.Errorf("Bytes() = %q", got)
	}
	if got := text.Slice(1, 4); !bytes.Equal(got, []byte("b\nc")) {
		t.Errorf("Slice(1,4) = %q, want %q", got, "b\nc")
	}
	if got := text.Position(3); got != (Position{ByteOffset: 3, Line: 2, Column: 1}) {
		t.Errorf("Position(3) = %+v", got)
	}
}

// assertTextMatches checks that text behaves identically to a File built from
// the same bytes: same content, and the same position mapping for every offset
// in every encoding — which exercises the incrementally maintained line index.
func assertTextMatches(t *testing.T, text *Text, want []byte) {
	t.Helper()

	if got := text.Bytes(); !bytes.Equal(got, want) {
		t.Fatalf("Bytes() = %q, want %q", got, want)
	}
	if text.Len() != len(want) {
		t.Fatalf("Len() = %d, want %d", text.Len(), len(want))
	}

	file := NewFile("oracle", want)
	for off := range len(want) + 1 {
		if got, exp := text.Position(off), file.Position(off); got != exp {
			t.Fatalf("Position(%d) = %+v, want %+v (content %q)", off, got, exp, want)
		}
		for _, enc := range []Encoding{ByteEncoding, UTF16Encoding, UTF32Encoding} {
			gl, gc := text.LineColumn(off, enc)
			el, ec := file.LineColumn(off, enc)
			if gl != el || gc != ec {
				t.Fatalf("LineColumn(%d, %s) = (%d,%d), want (%d,%d) (content %q)",
					off, enc, gl, gc, el, ec, want)
			}
		}
	}
}

func TestTextPositionService(t *testing.T) {
	text := NewText([]byte(multibyteLine))
	file := NewFile("oracle", []byte(multibyteLine))

	if text.Len() != file.Len() {
		t.Fatalf("Len() = %d, want %d", text.Len(), file.Len())
	}

	// Position and its inverse Offset round trip for every byte offset.
	for off := range text.Len() + 1 {
		p := text.Position(off)
		if got := text.Offset(p.Line, p.Column); got != off {
			t.Errorf("offset %d -> %+v -> %d", off, p, got)
		}
	}

	// OffsetAt agrees with File across encodings.
	for _, enc := range []Encoding{ByteEncoding, UTF16Encoding, UTF32Encoding} {
		for col := 0; col <= 6; col++ {
			if got, want := text.OffsetAt(0, col, enc), file.OffsetAt(0, col, enc); got != want {
				t.Errorf("OffsetAt(0, %d, %s) = %d, want %d", col, enc, got, want)
			}
		}
	}

	if got, want := text.Span(1, 4), file.Span(1, 4); got != want {
		t.Errorf("Span(1,4) = %+v, want %+v", got, want)
	}
}

func TestTextEditsScripted(t *testing.T) {
	type edit struct {
		start, end int
		repl       string
	}
	steps := []edit{
		{6, 6, "X"},         // insert in the middle
		{0, 0, "// lead\n"}, // insert at the very start (adds a line)
		{3, 9, ""},          // delete a range spanning content
		{2, 4, "あ😀"},        // replace with multibyte runes
		{0, 0, ""},          // no-op edit
	}

	text := NewText([]byte("alpha\nbeta\ngamma"))
	want := []byte("alpha\nbeta\ngamma")
	assertTextMatches(t, text, want)

	for _, s := range steps {
		text.Edit(s.start, s.end, []byte(s.repl))
		want = naiveSplice(want, min(s.start, len(want)), min(s.end, len(want)), []byte(s.repl))
		assertTextMatches(t, text, want)
	}
}

func TestTextEditReversedRange(t *testing.T) {
	// A reversed range must behave the same as the normalised one.
	a := NewText([]byte("abcdef"))
	a.Edit(4, 2, []byte("XY"))

	b := NewText([]byte("abcdef"))
	b.Edit(2, 4, []byte("XY"))

	if !bytes.Equal(a.Bytes(), b.Bytes()) || !bytes.Equal(a.Bytes(), []byte("abXYef")) {
		t.Errorf("reversed-range edit = %q, want %q", a.Bytes(), "abXYef")
	}
}

func TestTextEditsFuzz(t *testing.T) {
	r := rand.New(rand.NewSource(0xBEEF))
	runes := []string{"a", "b", " ", "\n", "あ", "😀"}

	text := NewText([]byte("seed\ntext"))
	want := []byte("seed\ntext")

	for range 400 {
		start := r.Intn(len(want) + 1)
		end := start + r.Intn(len(want)-start+1)

		var repl []byte
		for n := r.Intn(5); n > 0; n-- {
			repl = append(repl, runes[r.Intn(len(runes))]...)
		}

		text.Edit(start, end, repl)
		want = naiveSplice(want, start, end, repl)
		assertTextMatches(t, text, want)
	}
}
