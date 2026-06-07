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

// TestLinkCoversEveryValue drives the relink walk over every value form: the
// linker is an exhaustive switch guarded by a panic, and this pin turns an
// unhandled form into a failing test rather than a runtime crash on the first
// module that carries it. (Minimal instances have nil references, so the
// dispatch is what is exercised — exactly like the interpreter's coverage
// pin.)
func TestLinkCoversEveryValue(t *testing.T) {
	for _, v := range ValueKinds() {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("Link panicked on %T: %v", v, r)
				}
			}()
			m := &Module{Consts: []*Const{{Name: "X", Value: v}}}
			if err := m.Link(Resolver{}); err != nil {
				t.Errorf("Link(%T) = %v", v, err)
			}
		}()
	}
}

// TestLinkCoversEveryStmt is the statement half of the linker pin.
func TestLinkCoversEveryStmt(t *testing.T) {
	for _, s := range StmtKinds() {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("Link panicked on %T: %v", s, r)
				}
			}()
			m := &Module{Funcs: []*Function{{Name: "f", Body: []Stmt{s}}}}
			if err := m.Link(Resolver{}); err != nil {
				t.Errorf("Link(%T) = %v", s, err)
			}
		}()
	}
}

// TestTypeCodecCoversEveryType drives every Type form through the four
// hand-written dispatch switches — heading, marshal, decode, and relink — so
// a form added to the sealed interface without teaching the codec fails here
// instead of panicking on the first module that carries it.
func TestTypeCodecCoversEveryType(t *testing.T) {
	for _, typ := range TypeKinds() {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("the type codec panicked on %T: %v", typ, r)
				}
			}()
			text, err := typ.MarshalText()
			if err != nil {
				t.Errorf("MarshalText(%T): %v", typ, err)
				return
			}
			if len(text) == 0 {
				t.Errorf("MarshalText(%T) is empty", typ)
				return
			}
			m := &Module{Consts: []*Const{{Name: "X", Type: typ}}}
			data, err := m.MarshalText()
			if err != nil {
				t.Errorf("module marshal with a %T: %v", typ, err)
				return
			}
			var back Module
			if err := back.UnmarshalText(data); err != nil {
				t.Errorf("module with a %T does not unmarshal: %v", typ, err)
				return
			}
			if err := back.Link(Resolver{TypeDef: func(string) *TypeDef { return &TypeDef{} }}); err != nil {
				t.Errorf("module with a %T does not link: %v", typ, err)
			}
		}()
	}
}

// TestTypeKindsRegistryComplete asserts TypeKinds() lists exactly the types
// in this package that satisfy the Type interface, discovered by scanning the
// package source for typ() implementers — the same cross-check StmtKinds and
// ValueKinds carry, extended to the hand-written codec's sealed set.
func TestTypeKindsRegistryComplete(t *testing.T) {
	registered := map[string]bool{}
	for _, typ := range TypeKinds() {
		name := reflect.TypeOf(typ).Elem().Name() // *Builtin -> Builtin, *invalid -> invalid
		if registered[name] {
			t.Errorf("TypeKinds() lists %s more than once", name)
		}
		registered[name] = true
	}

	actual := typImplementersInSource(t)
	if len(actual) == 0 {
		t.Fatal("found no typ() implementers in the package source; the scan is broken")
	}
	for name := range actual {
		if !registered[name] {
			t.Errorf("type %s implements Type but is missing from TypeKinds() — add it so the codec pins cover it", name)
		}
	}
	for name := range registered {
		if !actual[name] {
			t.Errorf("TypeKinds() lists %s, which does not implement Type in the package source", name)
		}
	}
}

// typImplementersInSource parses the non-test .go files of this package and
// returns the set of type names with a pointer-receiver typ() method — the
// marker that seals a type into the Type interface.
func typImplementersInSource(t *testing.T) map[string]bool {
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
			if !ok || fn.Name.Name != "typ" || fn.Recv == nil || len(fn.Recv.List) != 1 {
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
