package lsp

import (
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/parser/abstract"
)

// TestLetHover checks that a let-bound local hovers with its type, both at the
// binding name and at a reference in the body, with the type inferred from the
// initializer.
func TestLetHover(t *testing.T) {
	src := "pub fn f(n: int): int {\n  let total = n\n  total = total + 1\n  return total\n}\n"
	doc := testView(src)

	t.Run("at the binding", func(t *testing.T) {
		h := hover(doc, strings.Index(src, "let total")+5)
		if h == nil {
			t.Fatal("no hover on the let binding")
		}
		if !strings.Contains(h.Contents.Value, "total: int") {
			t.Errorf("hover = %q, want total: int", h.Contents.Value)
		}
	})

	t.Run("at a reference", func(t *testing.T) {
		h := hover(doc, strings.Index(src, "return total")+8)
		if h == nil {
			t.Fatal("no hover on the let reference")
		}
		if !strings.Contains(h.Contents.Value, "total: int") {
			t.Errorf("hover = %q, want total: int", h.Contents.Value)
		}
	})
}

// TestLetHoverAnnotated checks that an annotated let hovers with the annotated
// type.
func TestLetHoverAnnotated(t *testing.T) {
	src := "pub fn f(n: int): int {\n  let acc: int = n\n  return acc\n}\n"
	doc := testView(src)
	h := hover(doc, strings.Index(src, "let acc")+5)
	if h == nil {
		t.Fatal("no hover on the annotated let")
	}
	if !strings.Contains(h.Contents.Value, "acc: int") {
		t.Errorf("hover = %q, want acc: int", h.Contents.Value)
	}
}

// TestSemanticTokensLet checks the colouring of a let statement: the let keyword
// is a keyword, its bound name a variable declaration (not readonly — a let is
// mutable), and the initializer reads as usual.
func TestSemanticTokensLet(t *testing.T) {
	doc := abstract.NewDocument([]byte("pub fn f(n: int): int {\n  let x = n\n  return x\n}\n"))
	got := decode(semanticTokens(doc).Data)

	var sawLetKeyword, sawLetName bool
	for _, tok := range got {
		// "let" sits at line 1, column 2, width 3.
		if tok.line == 1 && tok.char == 2 && tok.length == 3 {
			if tok.tokenType != stKeyword {
				t.Errorf("let keyword token = %+v, want stKeyword", tok)
			}
			sawLetKeyword = true
		}
		// "x" (the bound name) sits at line 1, column 6, width 1.
		if tok.line == 1 && tok.char == 6 && tok.length == 1 {
			if tok.tokenType != stVariable || tok.mods != smDeclaration {
				t.Errorf("let name token = %+v, want stVariable+smDeclaration (mutable)", tok)
			}
			sawLetName = true
		}
	}
	if !sawLetKeyword {
		t.Error("the let keyword was not coloured")
	}
	if !sawLetName {
		t.Error("the let binding name was not coloured")
	}
}
