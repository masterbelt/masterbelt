package std_test

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/belt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/belt/semantic"
	"github.com/masterbelt/masterbelt/pkg/belt/std"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
)

// TestModulesAnalyzeClean is the standard library's diagnostic-free pin, the
// analogue of the prelude's validation: every bundled module is analyzed
// unconditionally — under its real std: file id, so the builtin-surface trust
// bit applies and a module may legally declare extern — and asserted free of
// lex, parse, and semantic diagnostics. A broken std module is a compiler bug; a
// project that never imports it would otherwise never surface the breakage, so
// CI checks them all whether any project uses them or not.
func TestModulesAnalyzeClean(t *testing.T) {
	prog := semantic.NewProgram()
	docs := map[semantic.FileID]*abstract.Document{}
	for _, name := range std.List() {
		src, ok := std.Resolve(name)
		if !ok {
			t.Fatalf("std.List() named %q but Resolve does not know it", name)
		}
		doc := abstract.NewDocument(src)
		id := semantic.FileID(std.Locator(name))
		for _, d := range doc.Concrete().LexDiagnostics() {
			t.Errorf("%s: lex diagnostic: %s @%d: %s", id, d.Code, d.Offset, d.Message)
		}
		for _, d := range doc.Diagnostics() {
			t.Errorf("%s: parse diagnostic: %s @%d: %s", id, d.Code, d.Offset, d.Message)
		}
		docs[id] = doc
	}

	// Wire std-internal imports the way the loader does: a std module may only
	// `use` another std module, so a std: locator resolves to that module's id
	// and anything else is left unresolved (and would be a bug the analysis below
	// reports).
	for id, doc := range docs {
		uses := map[*ast.UseDecl]semantic.FileID{}
		for _, u := range doc.File().Uses {
			if target := semantic.FileID(u.Path); std.IsLocator(u.Path) && docs[target] != nil {
				uses[u] = target
			}
		}
		prog.SetFile(id, doc, uses)
	}
	prog.Refresh()

	for id := range docs {
		for _, d := range prog.Diagnostics(id) {
			t.Errorf("%s: semantic diagnostic: %s @%d: %s", id, d.Code, d.Offset, d.Message)
		}
	}
}
