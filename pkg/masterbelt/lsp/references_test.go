package lsp

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/semantic"
	protocol "github.com/owenrumney/go-lsp/lsp"
)

// References/rename test source. Offsets:
//
//	line 0: "const MaxLevel = 1"   name [6,14)
//	line 1: "const A = MaxLevel"   reference [29,37)
//	line 2: "const B = MaxLevel"   reference [48,56)
const refSrc = "const MaxLevel = 1\nconst A = MaxLevel\nconst B = MaxLevel\n"

func TestReferences(t *testing.T) {
	doc := semantic.NewDocument([]byte(refSrc))
	uri := protocol.DocumentURI("file:///x.belt")

	// From the declaration name, including the declaration: decl + 2 references.
	if got := references(doc, 10, uri, true); len(got) != 3 {
		t.Fatalf("references(decl, includeDecl) = %d, want 3", len(got))
	}
	// Excluding the declaration: just the 2 references.
	if got := references(doc, 10, uri, false); len(got) != 2 {
		t.Fatalf("references(decl, !includeDecl) = %d, want 2", len(got))
	}
	// From a reference, it still finds all of them.
	if got := references(doc, 31, uri, true); len(got) != 3 {
		t.Fatalf("references(reference) = %d, want 3", len(got))
	}
}

func TestRename(t *testing.T) {
	doc := semantic.NewDocument([]byte("const MaxLevel = 1\nconst A = MaxLevel\n"))
	uri := protocol.DocumentURI("file:///x.belt")

	we := rename(doc, 10, "Cap", uri)
	if we == nil {
		t.Fatal("rename returned nil")
	}
	edits := we.Changes[uri]
	if len(edits) != 2 {
		t.Fatalf("got %d edits, want 2 (declaration + 1 reference)", len(edits))
	}
	for _, e := range edits {
		if e.NewText != "Cap" {
			t.Errorf("edit NewText = %q, want Cap", e.NewText)
		}
	}

	if rename(doc, 10, "1bad", uri) != nil {
		t.Error("an invalid identifier should be rejected")
	}
	if rename(doc, 10, "const", uri) != nil {
		t.Error("a reserved word should be rejected")
	}
}

func TestPrepareRename(t *testing.T) {
	doc := semantic.NewDocument([]byte("const MaxLevel = 1\nconst A = MaxLevel\n"))

	pr := prepareRename(doc, 31) // on the reference in A
	if pr == nil {
		t.Fatal("prepareRename returned nil")
	}
	if pr.Placeholder != "MaxLevel" {
		t.Errorf("placeholder = %q, want MaxLevel", pr.Placeholder)
	}
	// The reference is on line 1, columns 10..18.
	if pr.Range.Start.Line != 1 || pr.Range.Start.Character != 10 || pr.Range.End.Character != 18 {
		t.Errorf("range = %+v, want line 1 cols 10..18", pr.Range)
	}

	if prepareRename(doc, 0) != nil { // on the "const" keyword, not a name
		t.Error("prepareRename should be nil off a symbol")
	}
}
