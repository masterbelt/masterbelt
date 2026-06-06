package lsp

import (
	"strings"
	"testing"

	protocol "github.com/owenrumney/go-lsp/lsp"
)

const assocConstSrc = "/// a capped level\npub type Level = sbyte impl {\n  /// the highest level\n  pub const Max = 100\n  pub const Min = 0\n}\nconst Top: Level = Level.Max\n"

// TestAssocConstCompletion checks that a type member access offers the type's
// associated constants, labelled with their value.
func TestAssocConstCompletion(t *testing.T) {
	doc := testView(assocConstSrc)
	offset := strings.Index(assocConstSrc, "= Level.") + len("= Level.")
	got := byLabel(completion(doc, offset).Items)
	for _, name := range []string{"Max", "Min"} {
		item, ok := got[name]
		if !ok {
			t.Errorf("associated-const completion missing %q; got %v", name, got)
			continue
		}
		if item.Kind == nil || *item.Kind != protocol.CompletionItemKindConstant {
			t.Errorf("%s kind = %v, want Constant", name, item.Kind)
		}
	}
	if got["Max"].Detail != "= 100" {
		t.Errorf("Max detail = %q, want = 100", got["Max"].Detail)
	}
	// Member completion is exclusive: the value namespace is not offered.
	if _, ok := got["Top"]; ok {
		t.Errorf("type member completion should not offer the value Top")
	}
}

// TestAssocConstBuiltinCompletion checks that a builtin primitive offers its
// bounds (int8.Max / int8.Min) as constant completions.
func TestAssocConstBuiltinCompletion(t *testing.T) {
	src := "const X = sbyte.\n"
	doc := testView(src)
	got := byLabel(completion(doc, strings.Index(src, "sbyte.")+len("sbyte.")).Items)
	for _, name := range []string{"Max", "Min"} {
		if _, ok := got[name]; !ok {
			t.Errorf("sbyte bound completion missing %q; got %v", name, got)
		}
	}
	if got["Max"].Detail != "= 127" {
		t.Errorf("sbyte.Max detail = %q, want = 127", got["Max"].Detail)
	}
}

// TestAssocConstHover checks the hover for an associated constant: the qualified
// name with its type and folded value, plus the doc comment.
func TestAssocConstHover(t *testing.T) {
	doc := testView(assocConstSrc)
	offset := strings.Index(assocConstSrc, "Level.Max") + len("Level.")
	h := hover(doc, offset)
	if h == nil {
		t.Fatal("no hover for the associated constant")
	}
	if !strings.Contains(h.Contents.Value, "Level.Max") || !strings.Contains(h.Contents.Value, "= 100") {
		t.Errorf("hover = %q, want it to show Level.Max ... = 100", h.Contents.Value)
	}
	if !strings.Contains(h.Contents.Value, "the highest level") {
		t.Errorf("hover = %q, want the doc comment", h.Contents.Value)
	}
}

// TestAssocConstBuiltinHover checks the hover for a builtin bound: int8.Max with
// its value 127.
func TestAssocConstBuiltinHover(t *testing.T) {
	src := "const X = sbyte.Max\n"
	doc := testView(src)
	h := hover(doc, strings.Index(src, "sbyte.Max")+len("sbyte."))
	if h == nil {
		t.Fatal("no hover for sbyte.Max")
	}
	if !strings.Contains(h.Contents.Value, "sbyte.Max") || !strings.Contains(h.Contents.Value, "127") {
		t.Errorf("hover = %q, want sbyte.Max ... 127", h.Contents.Value)
	}
}
