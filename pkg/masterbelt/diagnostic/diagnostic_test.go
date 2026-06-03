package diagnostic

import (
	"fmt"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/source"
)

func TestDiagnosticString(t *testing.T) {
	d := Diagnostic{
		Severity: Error,
		Code:     Code("masterbelt.lexer.unterminated_block_comment"),
		Message:  "unterminated block comment",
		Span: source.Span{
			Start: source.Position{ByteOffset: 12, Line: 3, Column: 5},
			End:   source.Position{ByteOffset: 20, Line: 3, Column: 13},
		},
	}
	want := "error[masterbelt.lexer.unterminated_block_comment]: unterminated block comment (3:5)"
	if got := d.String(); got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestLocalize(t *testing.T) {
	// A diagnostic carries Code + Fields, so it can be re-rendered in any locale
	// regardless of the default-locale Message it was built with.
	d := Diagnostic{
		Code:    Code("masterbelt.lexer.unexpected_character"),
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
