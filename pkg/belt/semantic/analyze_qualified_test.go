// This test pins value-position field projection off a namespace-qualified type
// (geo.Item.id): a comptime expression can project an imported type's field and
// compare it, the cross-file twin of the local Item.id projection, consistent
// with the type-position qualified projection.
package semantic

import (
	"testing"

	"github.com/masterbelt/masterbelt/internal/belttest"
	"github.com/masterbelt/masterbelt/pkg/belt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/project"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
)

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
