package diagnostic

import (
	"fmt"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/source"
)

func TestDiagnosticString(t *testing.T) {
	d := Diagnostic{
		Severity: Error,
		Code:     Code("belt.lexer.unterminated_block_comment"),
		Message:  "unterminated block comment",
		Offset:   12,
		Width:    8,
	}
	want := "error[belt.lexer.unterminated_block_comment]: unterminated block comment"
	if got := d.String(); got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestDiagnosticSpan(t *testing.T) {
	// The diagnostic stores only a byte range; Span resolves line/column from a
	// buffer on demand.
	buf := source.NewFile("t.belt", []byte("ab\ncdef"))
	d := Diagnostic{Offset: 4, Width: 2} // "de" on line 2

	if d.End() != 6 {
		t.Errorf("End() = %d, want 6", d.End())
	}
	span := d.Span(buf)
	if span.Start != (source.Position{ByteOffset: 4, Line: 2, Column: 2}) {
		t.Errorf("Span().Start = %+v", span.Start)
	}
	if span.End != (source.Position{ByteOffset: 6, Line: 2, Column: 4}) {
		t.Errorf("Span().End = %+v", span.End)
	}
}

func TestLocalize(t *testing.T) {
	// A diagnostic carries Code + Fields, so it can be re-rendered in any locale
	// regardless of the default-locale Message it was built with.
	d := Diagnostic{
		Code:    Code("belt.lexer.unexpected_character"),
		Message: "unexpected character: '#'",
		Fields:  map[string]fmt.Stringer{"char": Rune('#')},
	}
	if got, want := d.Localize("ja"), "予期しない文字: '#'"; got != want {
		t.Errorf("Localize(ja) = %q, want %q", got, want)
	}
	if got, want := d.Localize(DefaultLocale), d.Message; got != want {
		t.Errorf("Localize(en) = %q, want %q", got, want)
	}
}
