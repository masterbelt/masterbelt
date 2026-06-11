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

	protocol "github.com/owenrumney/go-lsp/lsp"
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

// masterOccSrc references a top-level constant from inside a master's per-row
// method body, so the occurrence engines must walk master method bodies — like
// they walk type/enum/interface/function bodies — to see it.
const masterOccSrc = "const Base = 10\n" +
	"master Skill {\n" +
	"  record {\n    id: int,\n  } impl {\n" +
	"    pub bumped(): int {\n      return self.id + Base\n    }\n  }\n" +
	"  primary id\n}\n"

func TestMasterMethodBodyOccurrences(t *testing.T) {
	doc := testView(masterOccSrc)

	// From the Base declaration, including it: the declaration + the reference in
	// the master method body.
	if got := references(doc, strings.Index(masterOccSrc, "Base = 10"), true); len(got) != 2 {
		t.Fatalf("references(Base) = %d, want 2 (declaration + master-method-body use)", len(got))
	}
	// From the reference inside the method body, definition jumps to the
	// declaration — the cursor-resolution walk reaches the master body too.
	locs := definition(doc, strings.Index(masterOccSrc, "+ Base")+len("+ "))
	if len(locs) != 1 {
		t.Fatalf("definition(Base in method body) = %d, want 1", len(locs))
	}
	if locs[0].Range.Start.Line != 0 {
		t.Errorf("definition jumped to line %d, want 0 (the const Base)", locs[0].Range.Start.Line)
	}
}

// masterMemberSrc reads a master-typed value's row fields through member access.
// A master is opaque (no record literal), but its fields are still readable, so
// completion and hover must project the row record from Master.Row — the
// descriptor — since the opaque master's Body is nil. This is the same row path
// the checker's member projection takes (types.RecordOf(Master.Row)).
const masterMemberSrc = "master Skill {\n" +
	"  record {\n    id: int,\n    name: string,\n  }\n" +
	"  primary id\n}\n" +
	"fn describe(s: Skill): string {\n  return s.name\n}\n"

func TestMasterMemberCompletion(t *testing.T) {
	doc := testView(masterMemberSrc)

	// After "s." in the method body: the row's fields, each a Field labelled with
	// its type.
	got := byLabel(completion(doc, strings.Index(masterMemberSrc, "s.name")+len("s.")).Items)
	for _, f := range []struct{ name, detail string }{
		{"id", ": int"},
		{"name", ": string"},
	} {
		item, ok := got[f.name]
		if !ok {
			t.Errorf("master row field %q missing from member completion: %v", f.name, labels(got))
			continue
		}
		if item.Kind == nil || *item.Kind != protocol.CompletionItemKindField {
			t.Errorf("%s kind = %v, want Field", f.name, item.Kind)
		}
		if item.Detail != f.detail {
			t.Errorf("%s detail = %q, want %q", f.name, item.Detail, f.detail)
		}
	}
}

func TestMasterMemberHover(t *testing.T) {
	doc := testView(masterMemberSrc)

	h := hover(doc, strings.Index(masterMemberSrc, "s.name")+len("s."))
	if h == nil {
		t.Fatal("no hover on the master row field access")
	}
	if !strings.Contains(h.Contents.Value, "name: string") {
		t.Errorf("hover = %q, want it to contain name: string", h.Contents.Value)
	}
}

// TestMasterRecordLiteralStaysOpaque pins the opacity boundary the read paths
// must not erase: a master is not constructible as a record literal, so writing
// Skill{ } offers none of the row fields — even though reading s.field does. The
// member-read projection (memberRecordOf/memberFieldOf) is deliberately one-way,
// so the literal path keeps treating a master as fieldless.
func TestMasterRecordLiteralStaysOpaque(t *testing.T) {
	src := "master Skill {\n  record {\n    id: int,\n    name: string,\n  }\n  primary id\n}\n" +
		"const x = Skill{  }\n"
	doc := testView(src)

	got := byLabel(completion(doc, strings.Index(src, "Skill{  }")+len("Skill{ ")).Items)
	for _, f := range []string{"id", "name"} {
		if _, ok := got[f]; ok {
			t.Errorf("record-literal completion offered the row field %q; a master is opaque (not constructible): %v", f, labels(got))
		}
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
