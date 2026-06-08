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
		default:
			// Any other highlight kind (a plain text occurrence) is neither a
			// write nor a read: it is not counted toward the asserted tally.
		}
	}
	if writes != 1 || reads != 2 {
		t.Errorf("highlights = %d write / %d read, want 1 / 2 (total %d)", writes, reads, len(hls))
	}
}

func TestInlayHints(t *testing.T) {
	// A is un-annotated (gets a hint); B is annotated (gets none).
	doc := testView("const A = 1\nconst B: long = 2\n")
	buf := doc.Buffer()
	hints := inlayHints(doc, 0, buf.Len())

	if len(hints) != 1 {
		t.Fatalf("got %d inlay hints, want 1 (only the un-annotated A)", len(hints))
	}
	var label string
	if err := json.Unmarshal(hints[0].Label, &label); err != nil {
		t.Fatalf("hint label is not a JSON string: %v", err)
	}
	if label != ": nint" {
		t.Errorf("hint label = %q, want %q", label, ": nint")
	}
	// The hint sits just after A's name (offset 7) and carries the same edit.
	if hints[0].Position.Line != 0 || hints[0].Position.Character != 7 {
		t.Errorf("hint position = %+v, want line 0 char 7", hints[0].Position)
	}
	if len(hints[0].TextEdits) != 1 || hints[0].TextEdits[0].NewText != ": nint" {
		t.Errorf("hint edit = %+v, want one inserting %q", hints[0].TextEdits, ": nint")
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
	want := []string{": list<nint>", ": nint", ": nint"} // Doubled, x, the result
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
	src := "const Tripled = [1, 2].map(fn(x: nint) { return x * 3 })\n"
	doc := testView(src)
	labels := hintLabels(t, inlayHints(doc, 0, doc.Buffer().Len()))
	want := []string{": list<nint>", ": nint"} // Tripled and the result; x is written
	if strings.Join(labels, "|") != strings.Join(want, "|") {
		t.Fatalf("hint labels = %v, want %v", labels, want)
	}

	// A fully annotated literal needs no lambda hints at all.
	src = "const Twice: fn(x: nint): nint = fn(x: nint): nint { return x * 2 }\n"
	doc = testView(src)
	if hints := inlayHints(doc, 0, doc.Buffer().Len()); len(hints) != 0 {
		t.Fatalf("got %d hints, want none (everything is written)", len(hints))
	}
}

func TestLambdaInlayHintsInMethodBody(t *testing.T) {
	// The method's declared result type reaches the returned literal, so its
	// parameter and result infer and get hints.
	src := "pub type T = sbyte impl {\n  pub f(): fn(x: nint): nint {\n    return fn(x) { return x }\n  }\n}\n"
	doc := testView(src)
	labels := hintLabels(t, inlayHints(doc, 0, doc.Buffer().Len()))
	want := []string{": nint", ": nint"} // the literal's x and result
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
	// pub so the only action is the annotation refactor, not the unused-
	// declaration delete a private unreferenced const would also offer.
	doc := testView("pub const A = 1\n")
	uri := doc.uri
	actions := codeActions(doc, 0, 15) // range over the whole declaration

	if len(actions) != 1 {
		t.Fatalf("got %d code actions, want 1", len(actions))
	}
	a := actions[0]
	if !strings.Contains(a.Title, "nint") {
		t.Errorf("action title = %q, want it to mention the inferred type", a.Title)
	}
	if a.Edit == nil || len(a.Edit.Changes[uri]) != 1 || a.Edit.Changes[uri][0].NewText != ": nint" {
		t.Errorf("action edit = %+v, want one inserting %q", a.Edit, ": nint")
	}
}

func TestCodeActionDeletesUnused(t *testing.T) {
	// An unused private constant offers a delete quick-fix that removes its whole
	// line, with the diagnostic it repairs attached.
	doc := testView("pub const Api = 1\nconst dead = 2\n")
	buf := doc.Buffer()
	start := fromPosition(buf, protocol.Position{Line: 1, Character: 0})
	end := fromPosition(buf, protocol.Position{Line: 1, Character: 5})

	actions := codeActions(doc, start, end)
	var del *protocol.CodeAction
	for i := range actions {
		if actions[i].Title == "Delete unused declaration" {
			del = &actions[i]
		}
	}
	if del == nil {
		t.Fatal("no delete action offered for the unused declaration")
	}
	if del.Kind == nil || *del.Kind != protocol.CodeActionQuickFix {
		t.Errorf("kind = %v, want quickfix", del.Kind)
	}
	if len(del.Diagnostics) != 1 || del.Diagnostics[0].Source != "masterbelt" {
		t.Errorf("want the repaired diagnostic attached, got %+v", del.Diagnostics)
	}
	edits := del.Edit.Changes[doc.uri]
	if len(edits) != 1 || edits[0].NewText != "" {
		t.Fatalf("edit = %+v, want one whole-line deletion", edits)
	}
	// The deletion spans the second line entirely: line 1, col 0 up to the start
	// of line 2 (the const declaration plus its trailing newline).
	r := edits[0].Range
	if r.Start.Line != 1 || r.Start.Character != 0 || r.End.Line != 2 || r.End.Character != 0 {
		t.Errorf("delete range = %+v, want all of line 1", r)
	}
}

func TestCodeActionSkipsAnnotated(t *testing.T) {
	// An already-annotated constant offers no add-annotation action (pub, so the
	// unused-declaration delete action does not appear either).
	doc := testView("pub const A: long = 1\n")
	if actions := codeActions(doc, 0, 22); len(actions) != 0 {
		t.Errorf("got %d code actions for an annotated const, want 0", len(actions))
	}
}
