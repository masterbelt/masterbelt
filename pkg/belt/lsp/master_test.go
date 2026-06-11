package lsp

// Master types are wired into the editor features exactly as the other type
// kinds are: a master resolves to a TypeDef whose declaration backpointer lives
// on MasterSyntax (Body is nil for it), so definition/references/rename/
// highlight read the declaration through the shared declSyntax helper — the same
// helper that lets enum and interface type names, which carry their own
// backpointers too, navigate consistently.

import (
	"strings"
	"testing"
)

// masterNavSrc declares an enum, a master whose row references it, and a
// function whose parameter is typed by the master — so Skill has a declaration
// and a reference (the parameter annotation) to navigate between.
const masterNavSrc = "enum SkillKind {\n  active\n  passive\n}\n" +
	"master Skill {\n" +
	"  record {\n    id: int,\n    name: string,\n    kind: SkillKind,\n  }\n" +
	"  primary id\n}\n" +
	"fn describe(s: Skill): string {\n  return s.name\n}\n"

// skillParamOffset points at the "Skill" of the function parameter annotation
// "(s: Skill)". The bare ": Skill" cannot be searched for: the row field
// "kind: SkillKind" contains it as a prefix, so the parameter list disambiguates.
func skillParamOffset() int { return strings.Index(masterNavSrc, "(s: Skill)") + len("(s: ") }

func TestMasterDefinition(t *testing.T) {
	doc := testView(masterNavSrc)

	// From the parameter annotation, jump to the master declaration's name. The
	// declaration is line 4 ("master Skill {"); the name "Skill" follows the
	// "master " keyword (7 columns) and spans cols 7..12.
	locs := definition(doc, skillParamOffset())
	if len(locs) != 1 {
		t.Fatalf("definition(Skill) = %d locations, want 1", len(locs))
	}
	r := locs[0].Range
	if r.Start.Line != 4 {
		t.Errorf("definition jumped to line %d, want the master declaration line 4", r.Start.Line)
	}
	if r.Start.Character != 7 || r.End.Character != 12 {
		t.Errorf("definition range cols = %d..%d, want 7..12 (the name Skill)", r.Start.Character, r.End.Character)
	}
}

func TestMasterReferences(t *testing.T) {
	doc := testView(masterNavSrc)

	// From the annotation, including the declaration: the declaration name + the
	// one annotation reference.
	if got := references(doc, skillParamOffset(), true); len(got) != 2 {
		t.Fatalf("references(Skill, includeDecl) = %d, want 2", len(got))
	}
	// Excluding the declaration: just the annotation reference.
	if got := references(doc, skillParamOffset(), false); len(got) != 1 {
		t.Fatalf("references(Skill, !includeDecl) = %d, want 1", len(got))
	}
}

func TestMasterRename(t *testing.T) {
	doc := testView(masterNavSrc)

	we := rename(doc, skillParamOffset(), "Ability")
	if we == nil {
		t.Fatal("rename(Skill) returned nil")
	}
	edits := we.Changes[doc.uri]
	if len(edits) != 2 { // the declaration name + the parameter annotation
		t.Fatalf("rename(Skill) = %d edits, want 2 (declaration + annotation)", len(edits))
	}
	for _, e := range edits {
		if e.NewText != "Ability" {
			t.Errorf("edit NewText = %q, want Ability", e.NewText)
		}
	}
}

func TestMasterPrepareRename(t *testing.T) {
	doc := testView(masterNavSrc)

	pr := prepareRename(doc, skillParamOffset())
	if pr == nil {
		t.Fatal("prepareRename(Skill) returned nil")
	}
	if pr.Placeholder != "Skill" {
		t.Errorf("placeholder = %q, want Skill", pr.Placeholder)
	}
}

func TestMasterDocumentHighlights(t *testing.T) {
	doc := testView(masterNavSrc)

	got := documentHighlights(doc, skillParamOffset())
	// The declaration name (write) and the annotation reference (read).
	if len(got) != 2 {
		t.Fatalf("highlights(Skill) = %d, want 2", len(got))
	}
}

// TestEnumTypeDefinition and TestInterfaceTypeDefinition pin the consistency the
// shared declSyntax helper buys: an enum and an interface carry their declaration
// backpointer on EnumSyntax / InterfaceSyntax (not Syntax), exactly as a master
// carries it on MasterSyntax, so navigating to a type name now works for all
// three kinds, not just plain `type` declarations.
func TestEnumTypeDefinition(t *testing.T) {
	src := "enum Rarity {\n  common\n  rare\n}\nconst c: Rarity = Rarity.common\n"
	doc := testView(src)

	locs := definition(doc, strings.Index(src, ": Rarity")+3)
	if len(locs) != 1 {
		t.Fatalf("definition(Rarity) = %d locations, want 1", len(locs))
	}
	if locs[0].Range.Start.Line != 0 {
		t.Errorf("definition jumped to line %d, want 0 (the enum declaration)", locs[0].Range.Start.Line)
	}
}

func TestInterfaceTypeDefinition(t *testing.T) {
	src := "interface Named {\n  name(): string\n}\nfn label(n: Named): string {\n  return n.name()\n}\n"
	doc := testView(src)

	locs := definition(doc, strings.Index(src, ": Named")+3)
	if len(locs) != 1 {
		t.Fatalf("definition(Named) = %d locations, want 1", len(locs))
	}
	if locs[0].Range.Start.Line != 0 {
		t.Errorf("definition jumped to line %d, want 0 (the interface declaration)", locs[0].Range.Start.Line)
	}
}
