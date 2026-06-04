package lsp

import (
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/semantic"
	protocol "github.com/owenrumney/go-lsp/lsp"
)

// completionSrc has a documented, annotated constant and a second constant whose
// initializer is a value position; "int64" is the only type position.
const completionSrc = "/// the maximum\nconst Max: int64 = 100\nconst Cur = Max\n"

func byLabel(items []protocol.CompletionItem) map[string]protocol.CompletionItem {
	out := map[string]protocol.CompletionItem{}
	for _, it := range items {
		out[it.Label] = it
	}
	return out
}

func TestCompletionInValuePosition(t *testing.T) {
	doc := semantic.NewDocument([]byte(completionSrc))

	// Inside the "Max" reference in "const Cur = Max" — a value position.
	offset := strings.Index(completionSrc, "= Max") + 3
	items := completion(doc, offset).Items
	got := byLabel(items)

	for _, want := range []string{"Max", "Cur", "true", "false", "null"} {
		if _, ok := got[want]; !ok {
			t.Errorf("value completion missing %q", want)
		}
	}

	// A constant carries its inferred type as detail and its doc comment.
	if d := got["Max"].Detail; d != ": int64" {
		t.Errorf("Max detail = %q, want %q", d, ": int64")
	}
	if doc := got["Max"].Documentation; doc == nil || !strings.Contains(doc.Value, "maximum") {
		t.Errorf("Max documentation = %v, want the doc comment", got["Max"].Documentation)
	}
	if k := got["Max"].Kind; k == nil || *k != protocol.CompletionItemKindConstant {
		t.Errorf("Max kind = %v, want Constant", k)
	}
}

func TestCompletionSuppressedInTypePosition(t *testing.T) {
	doc := semantic.NewDocument([]byte(completionSrc))

	// Inside the "int64" annotation — a type position, where a constant name
	// would be wrong.
	offset := strings.Index(completionSrc, "int64") + 2
	if items := completion(doc, offset).Items; len(items) != 0 {
		t.Errorf("type-position completion = %d items, want 0", len(items))
	}
}

func TestCompletionDedupesNames(t *testing.T) {
	// A redeclared name contributes a single completion item.
	doc := semantic.NewDocument([]byte("const A = 1\nconst A = 2\nconst B = A\n"))
	offset := strings.Index("const A = 1\nconst A = 2\nconst B = A\n", "= A") + 3
	n := 0
	for _, it := range completion(doc, offset).Items {
		if it.Label == "A" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("got %d completion items for A, want 1", n)
	}
}
