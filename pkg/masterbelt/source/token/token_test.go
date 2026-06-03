package token

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/source"
)

func TestKindString(t *testing.T) {
	if got := Const.String(); got != "Const" {
		t.Errorf("Const.String() = %q, want %q", got, "Const")
	}
	if got := Kind(999).String(); got != "Kind(999)" {
		t.Errorf("Kind(999).String() = %q, want %q", got, "Kind(999)")
	}
}

func TestLookup(t *testing.T) {
	cases := map[string]Kind{
		"const": Const,
		"pub":   Pub,
		"int64": Ident, // a type name is an ordinary identifier, not a keyword
		"x":     Ident,
	}
	for ident, want := range cases {
		if got := Lookup(ident); got != want {
			t.Errorf("Lookup(%q) = %s, want %s", ident, got, want)
		}
	}
}

func TestTokenResolution(t *testing.T) {
	file := source.NewFile("t.belt", []byte("const x\n"))
	tok := Token{Kind: Const, Offset: 0, Width: 5}

	if got := tok.End(); got != 5 {
		t.Errorf("End() = %d, want 5", got)
	}
	if got := tok.Text(file); got != "const" {
		t.Errorf("Text() = %q, want %q", got, "const")
	}
	if got := tok.String(); got != "Const@0+5" {
		t.Errorf("String() = %q, want %q", got, "Const@0+5")
	}

	span := tok.Span(file)
	if span.Start.Column != 1 || span.End.Column != 6 {
		t.Errorf("Span() columns = (%d, %d), want (1, 6)", span.Start.Column, span.End.Column)
	}
	if got := span.Len(); got != tok.Width {
		t.Errorf("Span().Len() = %d, want %d", got, tok.Width)
	}
}
