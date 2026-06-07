// This file is the completeness gate's mechanical half (F-3 §2.7): the
// interpreter must work from the IR (and the builtin registry's native table)
// alone, so this package's imports are pinned — pkg/source/ir, the registry,
// and the type algebra over IR data, nothing else. The moment a syntax import
// (pkg/source/ast, pkg/source/cst, the parsers, the semantic layer) appears in
// a non-test file, this test fails: "the AST carries no semantics" enforced in
// CI, not by convention. The exhaustiveness pins beside it keep the
// interpreter total over the IR's sealed forms.
package eval

import (
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// allowedImports is the interpreter's whole input surface: the IR (the data),
// the builtin registry (the native table), and the type algebra (rules over IR
// data, itself syntax-free — the companion assertion below pins that too).
var allowedImports = map[string]bool{
	"github.com/masterbelt/masterbelt/pkg/source/ir":          true,
	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin": true,
	"github.com/masterbelt/masterbelt/pkg/masterbelt/types":   true,
}

// TestImportBoundary pins this package's non-test imports to the allowed
// surface (standard-library imports aside): the running proof that the fold
// reads nothing but the IR.
func TestImportBoundary(t *testing.T) {
	for path := range packageImports(t, ".") {
		if !strings.Contains(path, ".") {
			continue // the standard library
		}
		if !allowedImports[path] {
			t.Errorf("eval imports %q — the interpreter's inputs are the IR, the registry, and the type algebra only", path)
		}
	}
}

// TestTypeAlgebraSyntaxFree pins the companion invariant: the type algebra
// this package leans on (pkg/masterbelt/types) is itself rules over IR data —
// no syntax import may appear there either, or the boundary above would leak
// through it.
func TestTypeAlgebraSyntaxFree(t *testing.T) {
	for path := range packageImports(t, "../types") {
		if strings.Contains(path, "/pkg/source/") && !strings.HasSuffix(path, "/pkg/source/ir") {
			t.Errorf("pkg/masterbelt/types imports %q — the type algebra must stay syntax-free", path)
		}
	}
}

// packageImports collects the import paths of every non-test Go file in dir.
func packageImports(t *testing.T, dir string) map[string]bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	fset := token.NewFileSet()
	out := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, dir+"/"+name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, imp := range file.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatalf("unquote %s: %v", imp.Path.Value, err)
			}
			out[path] = true
		}
	}
	return out
}

// TestInterpreterCoversEveryValue feeds one instance of every IR value form
// through the interpreter: a form it has no case for panics by design, so
// adding a value form without teaching the fold fails here — the
// exhaustiveness pin of §2.7. (The fold's verdict per form is the semantic
// gates' business; this pins only that every form is decided, never dropped.)
func TestInterpreterCoversEveryValue(t *testing.T) {
	env := newStubEnv()
	for _, v := range ir.ValueKinds() {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("the interpreter panicked on %T: %v", v, r)
				}
			}()
			graphValue(v, graphCtx{env: env})
		}()
	}
}

// TestInterpreterCoversEveryStmt is the statement half of the pin: every
// lowered statement form must be executed by the body interpreter.
func TestInterpreterCoversEveryStmt(t *testing.T) {
	env := newStubEnv()
	for _, s := range ir.StmtKinds() {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("the body interpreter panicked on %T: %v", s, r)
				}
			}()
			graphBody([]ir.Stmt{s}, graphCtx{env: env, locals: map[string]*ir.Constant{}})
		}()
	}
}
