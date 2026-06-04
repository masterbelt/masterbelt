package lsp

import (
	"strings"
	"testing"
)

// References/rename test source. Offsets:
//
//	line 0: "const MaxLevel = 1"   name [6,14)
//	line 1: "const A = MaxLevel"   reference [29,37)
//	line 2: "const B = MaxLevel"   reference [48,56)
const refSrc = "const MaxLevel = 1\nconst A = MaxLevel\nconst B = MaxLevel\n"

func TestReferences(t *testing.T) {
	doc := testView(refSrc)

	// From the declaration name, including the declaration: decl + 2 references.
	if got := references(doc, 10, true); len(got) != 3 {
		t.Fatalf("references(decl, includeDecl) = %d, want 3", len(got))
	}
	// Excluding the declaration: just the 2 references.
	if got := references(doc, 10, false); len(got) != 2 {
		t.Fatalf("references(decl, !includeDecl) = %d, want 2", len(got))
	}
	// From a reference, it still finds all of them.
	if got := references(doc, 31, true); len(got) != 3 {
		t.Fatalf("references(reference) = %d, want 3", len(got))
	}
}

func TestRename(t *testing.T) {
	doc := testView("const MaxLevel = 1\nconst A = MaxLevel\n")
	we := rename(doc, 10, "Cap")
	if we == nil {
		t.Fatal("rename returned nil")
	}
	edits := we.Changes[doc.uri]
	if len(edits) != 2 {
		t.Fatalf("got %d edits, want 2 (declaration + 1 reference)", len(edits))
	}
	for _, e := range edits {
		if e.NewText != "Cap" {
			t.Errorf("edit NewText = %q, want Cap", e.NewText)
		}
	}

	if rename(doc, 10, "1bad") != nil {
		t.Error("an invalid identifier should be rejected")
	}
	if rename(doc, 10, "const") != nil {
		t.Error("a reserved word should be rejected")
	}
}

func TestPrepareRename(t *testing.T) {
	doc := testView("const MaxLevel = 1\nconst A = MaxLevel\n")

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

// References/rename inside an expression. Offsets:
//
//	line 0: "const M = 1"       name [6,7)
//	line 1: "const z = M + M"   refs at [22,23) and [26,27)
const exprRefSrc = "const M = 1\nconst z = M + M\n"

func TestReferencesInExpression(t *testing.T) {
	doc := testView(exprRefSrc)

	// From the declaration, including it: decl + 2 in-expression references.
	if got := references(doc, 6, true); len(got) != 3 {
		t.Fatalf("references(M decl) = %d, want 3", len(got))
	}
	// From a reference inside the expression, still all 3.
	if got := references(doc, 22, true); len(got) != 3 {
		t.Fatalf("references(M ref) = %d, want 3", len(got))
	}
	// Excluding the declaration: just the 2 references.
	if got := references(doc, 22, false); len(got) != 2 {
		t.Fatalf("references(!includeDecl) = %d, want 2", len(got))
	}
}

func TestRenameInExpression(t *testing.T) {
	doc := testView(exprRefSrc)

	we := rename(doc, 26, "N") // on the second M reference
	if we == nil {
		t.Fatal("rename returned nil")
	}
	if edits := we.Changes[doc.uri]; len(edits) != 3 { // declaration + 2 references
		t.Fatalf("got %d edits, want 3 (declaration + 2 references)", len(edits))
	}
}

func TestReferencesInAssertCondition(t *testing.T) {
	// References inside an assertion's condition participate exactly as
	// initializer references do: find-references sees them, and a rename
	// from either end rewrites them.
	src := "const Max = 100\nassert Max > 0\nconst Twice = Max + Max\n"
	doc := testView(src)

	// From the declaration: decl + the assert reference + 2 initializer refs.
	if got := references(doc, 6, true); len(got) != 4 {
		t.Fatalf("references(Max decl) = %d, want 4", len(got))
	}
	// From the reference inside the assert condition, the same set.
	if got := references(doc, 23, true); len(got) != 4 { // inside "Max" in the assert
		t.Fatalf("references(assert ref) = %d, want 4", len(got))
	}
}

func TestTypeReferences(t *testing.T) {
	src := "pub type Coin = int8\nconst a: Coin = 1\nconst b: list<Coin> = [2]\n"
	doc := testView(src)

	// From the annotation: the declaration + 2 references (one nested in a
	// generic argument).
	if got := references(doc, strings.Index(src, ": Coin")+3, true); len(got) != 3 {
		t.Fatalf("references(Coin) = %d, want 3", len(got))
	}
	// Excluding the declaration.
	if got := references(doc, strings.Index(src, ": Coin")+3, false); len(got) != 2 {
		t.Fatalf("references(!includeDecl) = %d, want 2", len(got))
	}
}

func TestTypeRenamePreludeRefused(t *testing.T) {
	// A prelude type is declared outside the workspace: renaming it would
	// orphan every other program, so the rename (and its prepare) refuse.
	src := "const a: int8 = 1\n"
	doc := testView(src)
	offset := strings.Index(src, "int8")
	if edit := rename(doc, offset, "tiny"); edit != nil {
		t.Errorf("rename(int8) = %+v, want nil", edit)
	}
	if pr := prepareRename(doc, offset); pr != nil {
		t.Errorf("prepareRename(int8) = %+v, want nil", pr)
	}
}

func TestTypeDocumentHighlights(t *testing.T) {
	src := "pub type Coin = int8\nconst a: Coin = 1\n"
	doc := testView(src)
	got := documentHighlights(doc, strings.Index(src, ": Coin")+3)
	// The declaration (write) and the annotation reference (read).
	if len(got) != 2 {
		t.Fatalf("highlights = %d, want 2", len(got))
	}
}
