package ast

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"strings"
	"testing"
)

// TestStmtKindsRegistryComplete is the linchpin of the statement-walker guards:
// it asserts StmtKinds() lists exactly the types in this package that satisfy
// the Stmt interface. Adding a statement form (a new `stmt()`-implementing type)
// therefore forces adding it to StmtKinds(), which feeds it through every walker
// the tests below and in the dependent packages exercise — so the new kind
// cannot be quietly dropped by a walker that forgot to handle it.
//
// The implementer set is discovered two ways and cross-checked: by reflection
// over StmtKinds() (the registered set) and by parsing this package's own source
// for the types with a `stmt()` method (the actual set). They must match.
func TestStmtKindsRegistryComplete(t *testing.T) {
	registered := map[string]bool{}
	for _, s := range StmtKinds() {
		name := reflect.TypeOf(s).Elem().Name() // *ReturnStmt -> ReturnStmt
		if registered[name] {
			t.Errorf("StmtKinds() lists %s more than once", name)
		}
		registered[name] = true
	}

	actual := stmtImplementersInSource(t)
	if len(actual) == 0 {
		t.Fatal("found no stmt() implementers in the package source; the scan is broken")
	}

	for name := range actual {
		if !registered[name] {
			t.Errorf("type %s implements Stmt but is missing from StmtKinds() — add it so every walker's test covers it", name)
		}
	}
	for name := range registered {
		if !actual[name] {
			t.Errorf("StmtKinds() lists %s, which does not implement Stmt in the package source", name)
		}
	}
}

// stmtImplementersInSource parses the non-test .go files of this package and
// returns the set of type names with a pointer-receiver stmt() method — the
// marker that seals a type into the Stmt interface.
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
			// Receiver is *T; record T.
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

// TestWalkBodyExprsCoversEveryStmt drives the shared body-walk skeleton over a
// body holding one of every statement kind. The walk must not panic — the
// default panic fires only for a kind StmtKinds() registered but WalkBodyExprs
// has no case for, which is exactly the regression this guards. (The minimal
// instances carry nil operands, so the callback simply observes none; the test
// exercises dispatch, not operand recursion.)
func TestWalkBodyExprsCoversEveryStmt(t *testing.T) {
	for _, s := range StmtKinds() {
		body := []Stmt{s}
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("WalkBodyExprs panicked on %T: %v", s, r)
				}
			}()
			WalkBodyExprs(body, func(Expr) {})
		}()
	}
}

// TestUnhandledStmtNamesType pins the panic message to the offending type so a
// walker failure points straight at the unhandled kind.
func TestUnhandledStmtNamesType(t *testing.T) {
	msg := UnhandledStmt(&IfStmt{})
	if !strings.Contains(msg, "IfStmt") {
		t.Errorf("UnhandledStmt message %q does not name the kind", msg)
	}
}
