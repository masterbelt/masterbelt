package lsp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/semantic"
	protocol "github.com/owenrumney/go-lsp/lsp"
)

func TestDocumentHighlights(t *testing.T) {
	// M is declared once and referenced twice: one write (the declaration) and
	// two reads.
	doc := semantic.NewDocument([]byte("const M = 1\nconst z = M + M\n"))
	hls := documentHighlights(doc, 6) // on the declaration name M

	var writes, reads int
	for _, h := range hls {
		switch *h.Kind {
		case protocol.DocumentHighlightKindWrite:
			writes++
		case protocol.DocumentHighlightKindRead:
			reads++
		}
	}
	if writes != 1 || reads != 2 {
		t.Errorf("highlights = %d write / %d read, want 1 / 2 (total %d)", writes, reads, len(hls))
	}
}

func TestInlayHints(t *testing.T) {
	// A is un-annotated (gets a hint); B is annotated (gets none).
	doc := semantic.NewDocument([]byte("const A = 1\nconst B: int64 = 2\n"))
	buf := doc.Buffer()
	hints := inlayHints(doc, 0, buf.Len())

	if len(hints) != 1 {
		t.Fatalf("got %d inlay hints, want 1 (only the un-annotated A)", len(hints))
	}
	var label string
	if err := json.Unmarshal(hints[0].Label, &label); err != nil {
		t.Fatalf("hint label is not a JSON string: %v", err)
	}
	if label != ": int" {
		t.Errorf("hint label = %q, want %q", label, ": int")
	}
	// The hint sits just after A's name (offset 7) and carries the same edit.
	if hints[0].Position.Line != 0 || hints[0].Position.Character != 7 {
		t.Errorf("hint position = %+v, want line 0 char 7", hints[0].Position)
	}
	if len(hints[0].TextEdits) != 1 || hints[0].TextEdits[0].NewText != ": int" {
		t.Errorf("hint edit = %+v, want one inserting %q", hints[0].TextEdits, ": int")
	}
}

func TestCodeActionAddsTypeAnnotation(t *testing.T) {
	doc := semantic.NewDocument([]byte("const A = 1\n"))
	uri := protocol.DocumentURI("file:///x.belt")
	actions := codeActions(doc, 0, 11, uri) // range over the whole declaration

	if len(actions) != 1 {
		t.Fatalf("got %d code actions, want 1", len(actions))
	}
	a := actions[0]
	if !strings.Contains(a.Title, "int") {
		t.Errorf("action title = %q, want it to mention the inferred type", a.Title)
	}
	if a.Edit == nil || len(a.Edit.Changes[uri]) != 1 || a.Edit.Changes[uri][0].NewText != ": int" {
		t.Errorf("action edit = %+v, want one inserting %q", a.Edit, ": int")
	}
}

func TestCodeActionSkipsAnnotated(t *testing.T) {
	// An already-annotated constant offers no add-annotation action.
	doc := semantic.NewDocument([]byte("const A: int64 = 1\n"))
	if actions := codeActions(doc, 0, 18, protocol.DocumentURI("file:///x.belt")); len(actions) != 0 {
		t.Errorf("got %d code actions for an annotated const, want 0", len(actions))
	}
}
