package ir

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"strings"
	"testing"
)

// TestStmtKindsRegistryComplete asserts StmtKinds() lists exactly the types in
// this package that implement Stmt, discovered by scanning the package source
// for `stmt()` methods. Adding a lowered statement form forces registering it,
// which feeds it through the dump coverage test below — so a new kind cannot be
// quietly dropped by a dump walker (rendering as empty in the .ir snapshot).
func TestStmtKindsRegistryComplete(t *testing.T) {
	registered := map[string]bool{}
	for _, s := range StmtKinds() {
		registered[reflect.TypeOf(s).Elem().Name()] = true
	}

	actual := stmtImplementersInSource(t)
	if len(actual) == 0 {
		t.Fatal("found no stmt() implementers in the package source; the scan is broken")
	}
	for name := range actual {
		if !registered[name] {
			t.Errorf("type %s implements Stmt but is missing from StmtKinds()", name)
		}
	}
	for name := range registered {
		if !actual[name] {
			t.Errorf("StmtKinds() lists %s, which does not implement Stmt in the source", name)
		}
	}
}

func stmtImplementersInSource(t *testing.T) map[string]bool {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	fset := token.NewFileSet()
	out := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name.Name != "stmt" || fn.Recv == nil || len(fn.Recv.List) != 1 {
				continue
			}
			star, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
			if !ok {
				continue
			}
			if id, ok := star.X.(*ast.Ident); ok {
				out[id.Name] = true
			}
		}
	}
	return out
}

// TestDumpInlineNeverEmpty checks the inline IR dump renders every lowered
// statement kind as something non-empty, so a kind cannot silently vanish from
// the snapshot oracle.
func TestDumpInlineNeverEmpty(t *testing.T) {
	for _, s := range StmtKinds() {
		if got := dumpStmtInline(s); got == "" {
			t.Errorf("dumpStmtInline(%T) = %q; a statement must never dump as empty", s, got)
		}
	}
}

// TestDumpStmtAtCoversEveryStmt drives the multi-line dump over every kind; the
// default panic fires only for a registered kind dumpStmtAt has no case for.
func TestDumpStmtAtCoversEveryStmt(t *testing.T) {
	for _, s := range StmtKinds() {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("dumpStmtAt panicked on %T: %v", s, r)
				}
			}()
			var b strings.Builder
			dumpStmtAt(&b, s, "")
		}()
	}
}
