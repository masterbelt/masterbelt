package lsp

import (
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/parser/abstract"
)

// forSrc is the shared fixture: a for-of loop over a list, accumulating into a
// let, with a reference to the loop variable in the body.
const forSrc = "pub fn sum(xs: list<nint>): nint {\n  let total = 0\n  for x of xs {\n    total = total + x\n  }\n  return total\n}\n"

// TestForLoopVarHover checks that the loop variable hovers with its element type
// — at the binding in the for header and at a reference in the body.
func TestForLoopVarHover(t *testing.T) {
	doc := testView(forSrc)

	t.Run("at the loop variable binding", func(t *testing.T) {
		// The "x" in "for x of xs".
		h := hover(doc, strings.Index(forSrc, "for x of")+4)
		if h == nil {
			t.Fatal("no hover on the loop variable")
		}
		if !strings.Contains(h.Contents.Value, "x: nint") {
			t.Errorf("hover = %q, want x: nint", h.Contents.Value)
		}
	})

	t.Run("at a body reference", func(t *testing.T) {
		// The "x" in "total = total + x".
		h := hover(doc, strings.Index(forSrc, "total + x")+8)
		if h == nil {
			t.Fatal("no hover on the loop variable reference")
		}
		if !strings.Contains(h.Contents.Value, "x: nint") {
			t.Errorf("hover = %q, want x: nint", h.Contents.Value)
		}
	})
}

// TestForLoopVarHoverMapValue checks that an of-loop over a map binds the value
// type, and an in-loop binds the key type.
func TestForLoopVarHoverMapValue(t *testing.T) {
	src := "pub fn f(m: map<string, nint>): nint {\n  let total = 0\n  for v of m {\n    total = total + v\n  }\n  for k in m {\n    total = total + 1\n  }\n  return total\n}\n"
	doc := testView(src)

	if h := hover(doc, strings.Index(src, "for v of")+4); h == nil || !strings.Contains(h.Contents.Value, "v: nint") {
		t.Errorf("of-loop value hover = %v, want v: nint", h)
	}
	if h := hover(doc, strings.Index(src, "for k in")+4); h == nil || !strings.Contains(h.Contents.Value, "k: string") {
		t.Errorf("in-loop key hover = %v, want k: string", h)
	}
}

// TestSemanticTokensFor checks that the for, of, and in keywords colour as
// keywords, the same way switch/match/if do.
func TestSemanticTokensFor(t *testing.T) {
	src := "pub fn f(xs: list<nint>, m: map<string, nint>): nint {\n  for x of xs {\n    return x\n  }\n  for k in m {\n    return k\n  }\n  return 0\n}\n"
	doc := abstract.NewDocument([]byte(src))
	got := decode(semanticTokens(doc).Data)

	keywordAt := func(line, char, length int) bool {
		for _, tok := range got {
			if tok.line == line && tok.char == char && tok.length == length {
				return tok.tokenType == stKeyword
			}
		}
		return false
	}
	// "for" at line 1 col 2 width 3, "of" at line 1 col 8 width 2.
	if !keywordAt(1, 2, 3) {
		t.Error("for keyword not coloured as a keyword")
	}
	if !keywordAt(1, 8, 2) {
		t.Error("of keyword not coloured as a keyword")
	}
	// "in" at line 4 col 8 width 2.
	if !keywordAt(4, 8, 2) {
		t.Error("in keyword not coloured as a keyword")
	}
}
