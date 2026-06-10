// This test pins value-position field projection off a namespace-qualified type
// (geo.Item.id): a comptime expression can project an imported type's field and
// compare it, the cross-file twin of the local Item.id projection, consistent
// with the type-position qualified projection.
package semantic

import (
	"testing"

	"github.com/masterbelt/masterbelt/internal/belttest"
	"github.com/masterbelt/masterbelt/pkg/belt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/project"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
)

// analyzeProject lays a multi-file project, opens it, and returns the union of
// its semantic diagnostics across files.
func analyzeProject(t *testing.T, files map[string]string) []diagnostic.Diagnostic {
	t.Helper()
	files["masterbelt.toml"] = "entry = \"main.belt\"\n"
	root := belttest.WriteFiles(t, files)
	proj, pdiags := project.Open(root)
	if pdiags.Len() > 0 {
		t.Fatalf("project diagnostics: %v", pdiags.Items())
	}
	docs := map[FileID]*abstract.Document{}
	uses := map[FileID]map[*ast.UseDecl]FileID{}
	for _, f := range proj.Files() {
		docs[FileID(f.ID)] = f.AST
		uses[FileID(f.ID)] = UsesOf(f.Uses)
	}
	_, diags := AnalyzeProgram(docs, uses)
	var out []diagnostic.Diagnostic
	for _, ds := range diags {
		out = append(out, ds...)
	}
	return out
}

func TestQualifiedTypeMemberValidated(t *testing.T) {
	// A member off a qualified type is validated like a member off a local type: a
	// missing field or enum member is reported rather than silently accepted.
	field := analyzeProject(t, map[string]string{
		"geometry.belt": "pub type Item = { id: long }\n",
		"main.belt":     "use geo from \"geometry.belt\"\nassert geo.Item.nope == long\n",
	})
	if !hasCode(field, CodeUnknownAssociatedConst) {
		t.Errorf("geo.Item.nope: want a member diagnostic, got %v", field)
	}
	enum := analyzeProject(t, map[string]string{
		"geometry.belt": "pub enum R { A }\n",
		"main.belt":     "use geo from \"geometry.belt\"\nassert geo.R.Bogus == geo.R.A\n",
	})
	if !hasCode(enum, CodeUnknownEnumMember) {
		t.Errorf("geo.R.Bogus: want unknown_enum_member, got %v", enum)
	}
}

func TestValueShadowsNamespaceProjection(t *testing.T) {
	// A value named like a namespace import shadows it: geo.Item.id reads the
	// fields of the const named geo, not the imported type's projection, so the
	// const settles to the field type rather than type_in_value_position.
	diags := analyzeProject(t, map[string]string{
		"geometry.belt": "pub type Item = { id: long }\n",
		"main.belt": "use geo from \"geometry.belt\"\n" +
			"pub type Box = { Item: { id: nint } }\n" +
			"const geo: Box = { Item: { id: 1 } }\n" +
			"const X = geo.Item.id\n",
	})
	if len(diags) != 0 {
		t.Fatalf("want clean (value shadows namespace), got %v", diags)
	}
}

func TestBareQualifiedTypeValue(t *testing.T) {
	// A namespace-qualified type name used as a value (geo.Item) reifies to a type
	// value — the qualified twin of a bare local type name (Item) — so geo.Item ==
	// geo.Item folds true and geo.Item != geo.Other folds true, by nominal
	// identity, with no trailing field projection.
	clean := analyzeProject(t, map[string]string{
		"geometry.belt": "pub type Item = { id: long }\npub type Other = { id: long }\n",
		"main.belt": "use geo from \"geometry.belt\"\n" +
			"assert geo.Item == geo.Item\n" +
			"assert geo.Item != geo.Other\n",
	})
	if len(clean) != 0 {
		t.Fatalf("want clean (bare qualified type value folds), got %v", clean)
	}
}

func TestBareQualifiedTypeValueShadowedByValue(t *testing.T) {
	// A value named like a namespace import shadows it: geo.Item reads the field
	// Item of the const named geo, not the imported type as a type value, so the
	// const settles to that field's type rather than reifying a metatype.
	diags := analyzeProject(t, map[string]string{
		"geometry.belt": "pub type Item = { id: long }\n",
		"main.belt": "use geo from \"geometry.belt\"\n" +
			"pub type Box = { Item: { id: nint } }\n" +
			"const geo: Box = { Item: { id: 1 } }\n" +
			"const X = geo.Item\n",
	})
	if len(diags) != 0 {
		t.Fatalf("want clean (value shadows namespace), got %v", diags)
	}
}

func TestBareQualifiedTypeValueShadowedInBody(t *testing.T) {
	// A top-level const named like a namespace import shadows it inside a function
	// or method body too, not only in a const initializer: geo.Item reads the
	// const's field rather than reifying the imported type, so the body type-checks
	// against the field type instead of the metatype. The qualified projection
	// geo.Item.id is shadowed the same way.
	body := "use geo from \"geometry.belt\"\n" +
		"pub type Box = { Item: { id: nint } }\n" +
		"const geo: Box = { Item: { id: 1 } }\n" +
		"pub fn f(): { id: nint } { return geo.Item }\n" +
		"pub fn g(): nint { return geo.Item.id }\n"
	if diags := analyzeProject(t, map[string]string{
		"geometry.belt": "pub type Item = { id: long }\n",
		"main.belt":     body,
	}); len(diags) != 0 {
		t.Fatalf("want clean (const shadows namespace in a body), got %v", diags)
	}
	// Without a shadowing const, a bare qualified type value still folds in a body.
	if diags := analyzeProject(t, map[string]string{
		"geometry.belt": "pub type Item = { id: long }\n",
		"main.belt": "use geo from \"geometry.belt\"\n" +
			"pub fn f(): nint {\n  assert geo.Item == geo.Item\n  return 1\n}\n",
	}); len(diags) != 0 {
		t.Fatalf("want clean (bare qualified type value folds in a body), got %v", diags)
	}
}

func TestQualifiedTypeValueProjection(t *testing.T) {
	root := belttest.WriteFiles(t, map[string]string{
		"masterbelt.toml": "entry = \"main.belt\"\n",
		"geometry.belt":   "pub type Item = { id: long }\n",
		"main.belt": "use geo from \"geometry.belt\"\n" +
			"assert geo.Item.id == long\n" +
			"assert geo.Item.id != sbyte\n",
	})
	proj, pdiags := project.Open(root)
	if pdiags.Len() > 0 {
		t.Fatalf("project diagnostics: %v", pdiags.Items())
	}
	docs := map[FileID]*abstract.Document{}
	uses := map[FileID]map[*ast.UseDecl]FileID{}
	for _, f := range proj.Files() {
		docs[FileID(f.ID)] = f.AST
		uses[FileID(f.ID)] = UsesOf(f.Uses)
	}
	_, diags := AnalyzeProgram(docs, uses)
	for id, ds := range diags {
		for _, d := range ds {
			t.Errorf("%s: unexpected diagnostic %s", id, d.Code)
		}
	}
}
