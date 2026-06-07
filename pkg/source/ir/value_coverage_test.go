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

// TestValueKindsRegistryComplete asserts ValueKinds() lists exactly the types
// in this package that implement Value, discovered by scanning the package
// source for `value()` methods — the Value twin of the Stmt registry guard, so
// a new value form cannot be quietly dropped by the dump, the walkers, or the
// interpreter's exhaustiveness pins.
func TestValueKindsRegistryComplete(t *testing.T) {
	registered := map[string]bool{}
	for _, v := range ValueKinds() {
		registered[reflect.TypeOf(v).Elem().Name()] = true
	}

	actual := valueImplementersInSource(t)
	if len(actual) == 0 {
		t.Fatal("found no value() implementers in the package source; the scan is broken")
	}
	for name := range actual {
		if !registered[name] {
			t.Errorf("type %s implements Value but is missing from ValueKinds()", name)
		}
	}
	for name := range registered {
		if !actual[name] {
			t.Errorf("ValueKinds() lists %s, which does not implement Value in the source", name)
		}
	}
}

func valueImplementersInSource(t *testing.T) map[string]bool {
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
			if !ok || fn.Name.Name != "value" || fn.Recv == nil || len(fn.Recv.List) != 1 {
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

// TestDumpValueCoversEveryValue drives the dump over every value form; a kind
// the renderer has no case for panics rather than vanishing from the snapshot
// oracle.
func TestDumpValueCoversEveryValue(t *testing.T) {
	for _, v := range ValueKinds() {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("dumpValue panicked on %T: %v", v, r)
				}
			}()
			if got := dumpValue(v); got == "" {
				t.Errorf("dumpValue(%T) = %q; a value must never dump as empty", v, got)
			}
		}()
	}
}

// TestWalkValuesCoversEveryValue drives the exhaustive walker over every value
// form; an unregistered kind panics there by design, so this pins the walker's
// switch complete.
func TestWalkValuesCoversEveryValue(t *testing.T) {
	for _, v := range ValueKinds() {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("WalkValues panicked on %T: %v", v, r)
				}
			}()
			WalkValues(v, func(Value) bool { return true })
		}()
	}
}

// TestTypeOfAndSyntaxOfCoverEveryValue pins the uniform readings: TypeOf and
// SyntaxOf must have a case for every value form.
func TestTypeOfAndSyntaxOfCoverEveryValue(t *testing.T) {
	for _, v := range ValueKinds() {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("TypeOf/SyntaxOf panicked on %T: %v", v, r)
				}
			}()
			TypeOf(v)
			SyntaxOf(v)
		}()
	}
}
