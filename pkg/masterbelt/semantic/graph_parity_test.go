// This file pins the IR interpreter against the published folds:
// every shared example's constant must fold to the same value through
// eval.Graph over the annotated value graph as the AST-driven folder published
// (Const.Eval) — the parity gate the migration holds while the consumers
// switch, and the running proof that the IR alone carries everything the fold
// needs.
package semantic

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/eval"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/project"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// moduleGraphEnv is the eval.GraphEnv a parity fold runs in: a referenced
// constant reads its published Eval off the shell (the graph is a pointer
// graph, so no resolution is needed), and type names resolve through the
// module set's own definitions over the prelude surface.
type moduleGraphEnv struct {
	defs map[string]*ir.TypeDef
}

func newModuleGraphEnv(modules ...*ir.Module) moduleGraphEnv {
	defs := map[string]*ir.TypeDef{}
	for name, def := range universe().prelude {
		defs[name] = def
	}
	for _, m := range modules {
		for _, def := range m.Types {
			defs[def.Name] = def
		}
	}
	return moduleGraphEnv{defs: defs}
}

func (e moduleGraphEnv) ConstValue(c *ir.Const) *ir.Constant { return c.Eval }
func (e moduleGraphEnv) LookupType(name string) *ir.TypeDef  { return e.defs[name] }
func (e moduleGraphEnv) Registry() *builtin.Registry         { return universe().reg }

// TestGraphFoldParity folds every shared example's constants through the IR
// interpreter and compares each against the published Eval. The interpreter
// reads only the annotated graph (and the registry through the env), so a
// disagreement means the IR is missing a fact the AST folder read from syntax
// — exactly what this gate exists to catch.
func TestGraphFoldParity(t *testing.T) {
	entries, err := os.ReadDir(sharedExamples)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		switch {
		case entry.IsDir():
			t.Run(name, func(t *testing.T) {
				proj, pdiags := project.Open(filepath.Join(sharedExamples, name))
				if pdiags.Len() > 0 {
					t.Fatalf("project diagnostics: %v", pdiags.Items())
				}
				docs := map[FileID]*abstract.Document{}
				uses := map[FileID]map[*ast.UseDecl]FileID{}
				for _, f := range proj.Files() {
					docs[FileID(f.ID)] = f.AST
					uses[FileID(f.ID)] = UsesOf(f.Uses)
				}
				modules, _ := AnalyzeProgram(docs, uses)
				all := make([]*ir.Module, 0, len(modules))
				for _, m := range modules {
					all = append(all, m)
				}
				env := newModuleGraphEnv(all...)
				for id, m := range modules {
					compareGraphFolds(t, string(id), m, env)
				}
			})
		case strings.HasSuffix(name, ".belt"):
			t.Run(name, func(t *testing.T) {
				src, err := os.ReadFile(filepath.Join(sharedExamples, name))
				if err != nil {
					t.Fatal(err)
				}
				module, _ := Analyze(abstract.NewDocument(src))
				compareGraphFolds(t, name, module, newModuleGraphEnv(module))
			})
		}
	}
}

// compareGraphFolds folds each constant's value graph and compares it with the
// published Eval.
func compareGraphFolds(t *testing.T, label string, m *ir.Module, env moduleGraphEnv) {
	t.Helper()
	for _, c := range m.Consts {
		if c == nil || c.Value == nil {
			continue
		}
		// The const's resolved type is the top expectation channel — what
		// DeclExpecting reads from the annotation: it tags a union-typed value
		// whose graph carries no Adapt (the value already flows at the union
		// type) and settles an empty collection's mapness.
		got := eval.GraphExpecting(c.Value, c.Type, env)
		if !ir.ConstantsEqual(got, c.Eval) {
			t.Errorf("%s: const %s: IR fold = %s, published Eval = %s", label, c.Name, got, c.Eval)
		}
	}
}
