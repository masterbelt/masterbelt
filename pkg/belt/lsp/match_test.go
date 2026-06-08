package lsp

import (
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/belt/parser/abstract"
)

// matchSrc is the shared fixture: a union and a match that narrows its binding.
const matchSrc = "pub type Coin = { amount: nint }\npub type Level = { rank: nint }\npub type GameValue = Coin | Level\n" +
	"pub fn worth(v: GameValue): nint {\n  match v {\n    Coin c -> return c.amount\n    Level l -> return l.rank\n  }\n}\n"

// TestMatchBindingHover checks that a match arm's narrowed binding hovers with
// its member type — at the binding name in the pattern and at a reference to it
// in the arm body.
func TestMatchBindingHover(t *testing.T) {
	doc := testView(matchSrc)

	t.Run("at the pattern binding", func(t *testing.T) {
		// The "c" in "Coin c".
		h := hover(doc, strings.Index(matchSrc, "Coin c -> ")+5)
		if h == nil {
			t.Fatal("no hover on the match arm binding")
		}
		if !strings.Contains(h.Contents.Value, "c: Coin") {
			t.Errorf("hover = %q, want c: Coin", h.Contents.Value)
		}
	})

	t.Run("at a body reference", func(t *testing.T) {
		// The "c" in "return c.amount".
		h := hover(doc, strings.Index(matchSrc, "return c.amount")+7)
		if h == nil {
			t.Fatal("no hover on the binding reference")
		}
		if !strings.Contains(h.Contents.Value, "c: Coin") {
			t.Errorf("hover = %q, want c: Coin", h.Contents.Value)
		}
	})

	t.Run("the other arm narrows to its own type", func(t *testing.T) {
		h := hover(doc, strings.Index(matchSrc, "Level l -> ")+6)
		if h == nil {
			t.Fatal("no hover on the Level arm binding")
		}
		if !strings.Contains(h.Contents.Value, "l: Level") {
			t.Errorf("hover = %q, want l: Level", h.Contents.Value)
		}
	})
}

// TestSemanticTokensMatch checks that the match keyword colours as a keyword,
// the same way switch and if do.
func TestSemanticTokensMatch(t *testing.T) {
	src := "pub fn worth(v: GameValue): nint {\n  match v {\n    Coin c -> return c.amount\n  }\n}\n"
	doc := abstract.NewDocument([]byte(src))
	got := decode(semanticTokens(doc).Data)

	var sawMatchKeyword bool
	for _, tok := range got {
		// "match" sits at line 1, column 2, width 5.
		if tok.line == 1 && tok.char == 2 && tok.length == 5 {
			if tok.tokenType != stKeyword {
				t.Errorf("match keyword token = %+v, want stKeyword", tok)
			}
			sawMatchKeyword = true
		}
	}
	if !sawMatchKeyword {
		t.Error("no semantic token for the match keyword")
	}
}
