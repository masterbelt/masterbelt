package lsp

import (
	"encoding/json"
	"strings"
	"testing"

	protocol "github.com/owenrumney/go-lsp/lsp"
)

func TestDocumentHighlights(t *testing.T) {
	// M is declared once and referenced twice: one write (the declaration) and
	// two reads.
	doc := testView("const M = 1\nconst z = M + M\n")
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
	doc := testView("const A = 1\nconst B: int64 = 2\n")
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

// hintLabels decodes the hints' labels, in order.
func hintLabels(t *testing.T, hints []protocol.InlayHint) []string {
	t.Helper()
	labels := make([]string, len(hints))
	for i, h := range hints {
		if err := json.Unmarshal(h.Label, &labels[i]); err != nil {
			t.Fatalf("hint %d label is not a JSON string: %v", i, err)
		}
	}
	return labels
}

func TestLambdaInlayHints(t *testing.T) {
	// The lambda's parameter and result types are solved from map's signature
	// and the body; the hints render them where the annotations would sit.
	src := "const Doubled = [1, 2].map(fn(x) { return x * 2 })\n"
	doc := testView(src)
	hints := inlayHints(doc, 0, doc.Buffer().Len())

	labels := hintLabels(t, hints)
	want := []string{": list<int>", ": int", ": int"} // Doubled, x, the result
	if strings.Join(labels, "|") != strings.Join(want, "|") {
		t.Fatalf("hint labels = %v, want %v", labels, want)
	}
	// The parameter hint sits just after x; the result hint just after ")".
	xEnd := strings.Index(src, "x)") + 1
	if at := hints[1].Position.Character; at != xEnd {
		t.Errorf("parameter hint at char %d, want %d", at, xEnd)
	}
	parenEnd := strings.Index(src, ") {") + 1
	if at := hints[2].Position.Character; at != parenEnd {
		t.Errorf("result hint at char %d, want %d", at, parenEnd)
	}
}

func TestLambdaInlayHintsSkipWritten(t *testing.T) {
	// Written annotations already show themselves: only the inferred result
	// gets a hint here.
	src := "const Tripled = [1, 2].map(fn(x: int) { return x * 3 })\n"
	doc := testView(src)
	labels := hintLabels(t, inlayHints(doc, 0, doc.Buffer().Len()))
	want := []string{": list<int>", ": int"} // Tripled and the result; x is written
	if strings.Join(labels, "|") != strings.Join(want, "|") {
		t.Fatalf("hint labels = %v, want %v", labels, want)
	}

	// A fully annotated literal needs no lambda hints at all.
	src = "const Twice: fn(x: int): int = fn(x: int): int { return x * 2 }\n"
	doc = testView(src)
	if hints := inlayHints(doc, 0, doc.Buffer().Len()); len(hints) != 0 {
		t.Fatalf("got %d hints, want none (everything is written)", len(hints))
	}
}

func TestLambdaInlayHintsInMethodBody(t *testing.T) {
	// The method's declared result type reaches the returned literal, so its
	// parameter and result infer and get hints.
	src := "pub type T = int8 impl {\n  pub f(): fn(x: int): int {\n    return fn(x) { return x }\n  }\n}\n"
	doc := testView(src)
	labels := hintLabels(t, inlayHints(doc, 0, doc.Buffer().Len()))
	want := []string{": int", ": int"} // the literal's x and result
	if strings.Join(labels, "|") != strings.Join(want, "|") {
		t.Fatalf("hint labels = %v, want %v", labels, want)
	}
}

func TestLambdaInlayHintsSkipUninferable(t *testing.T) {
	// An unsolvable signature renders no hint (the diagnostics carry the news).
	src := "const A = fn(x) { return x }\n"
	doc := testView(src)
	if hints := inlayHints(doc, 0, doc.Buffer().Len()); len(hints) != 0 {
		t.Fatalf("got %d hints, want none (nothing is solved)", len(hints))
	}
}

func TestCodeActionAddsTypeAnnotation(t *testing.T) {
	doc := testView("const A = 1\n")
	uri := doc.uri
	actions := codeActions(doc, 0, 11) // range over the whole declaration

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
	doc := testView("const A: int64 = 1\n")
	if actions := codeActions(doc, 0, 18); len(actions) != 0 {
		t.Errorf("got %d code actions for an annotated const, want 0", len(actions))
	}
}
