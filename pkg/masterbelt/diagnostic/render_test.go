package diagnostic

import (
	"fmt"
	"testing"
)

func TestRenderLocale(t *testing.T) {
	code := Code("masterbelt.lexer.unexpected_character")
	fields := map[string]fmt.Stringer{"char": Rune('#')}

	if got, want := Render(DefaultLocale, code, fields), "unexpected character: '#'"; got != want {
		t.Errorf("Render(en) = %q, want %q", got, want)
	}
	if got, want := Render("ja", code, fields), "予期しない文字: '#'"; got != want {
		t.Errorf("Render(ja) = %q, want %q", got, want)
	}
	// An unknown locale falls back to the default rendering.
	if got, want := Render("fr", code, fields), "unexpected character: '#'"; got != want {
		t.Errorf("Render(fr fallback) = %q, want %q", got, want)
	}
	// An unknown code falls back to the code string.
	if got, want := Render(DefaultLocale, Code("masterbelt.unknown"), nil), "masterbelt.unknown"; got != want {
		t.Errorf("Render(unknown code) = %q, want %q", got, want)
	}

	// A field-less code renders in each locale too.
	plain := Code("masterbelt.lexer.unterminated_block_comment")
	if got, want := Render(DefaultLocale, plain, nil), "unterminated block comment"; got != want {
		t.Errorf("Render(en plain) = %q, want %q", got, want)
	}
	if got, want := Render("ja", plain, nil), "ブロックコメントが閉じられていません"; got != want {
		t.Errorf("Render(ja plain) = %q, want %q", got, want)
	}
}
