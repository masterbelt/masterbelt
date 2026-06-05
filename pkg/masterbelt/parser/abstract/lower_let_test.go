package abstract

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/source/ast"
)

// bodyOf lowers src and returns the first function's statement body, failing
// the test on a diagnostic or an empty body.
func bodyOf(t *testing.T, src string) []ast.Stmt {
	t.Helper()
	file, diags := Lower([]byte(src))
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if len(file.Funcs) == 0 {
		t.Fatal("no function lowered")
	}
	return file.Funcs[0].Body
}

// TestLowerLetInferred checks an inferred-type let: the bound name and value are
// lowered and there is no annotation.
func TestLowerLetInferred(t *testing.T) {
	body := bodyOf(t, "pub fn f(n: int): int {\n  let x = n\n  return x\n}\n")
	s, ok := body[0].(*ast.LetStmt)
	if !ok {
		t.Fatalf("first statement is %T, want *ast.LetStmt", body[0])
	}
	if s.Name != "x" {
		t.Fatalf("let name = %q, want x", s.Name)
	}
	if s.Type != nil {
		t.Fatalf("inferred let has a type annotation %v, want none", s.Type)
	}
	if s.Value == nil {
		t.Fatal("let value is nil")
	}
}

// TestLowerLetAnnotated checks an annotated let: the type expression is lowered
// alongside the name and value.
func TestLowerLetAnnotated(t *testing.T) {
	body := bodyOf(t, "pub fn f(n: int): int {\n  let x: int = n\n  return x\n}\n")
	s, ok := body[0].(*ast.LetStmt)
	if !ok {
		t.Fatalf("first statement is %T, want *ast.LetStmt", body[0])
	}
	if s.Type == nil {
		t.Fatal("annotated let has no type, want int")
	}
	named, ok := s.Type.(*ast.NamedType)
	if !ok || named.Name != "int" {
		t.Fatalf("let type = %v, want int", s.Type)
	}
}

// TestLowerAssign checks a reassignment: the target identifier and the new value
// are lowered, and the value is desugared like any other expression (x + 1
// becomes a method call).
func TestLowerAssign(t *testing.T) {
	body := bodyOf(t, "pub fn f(n: int): int {\n  let x = n\n  x = x + 1\n  return x\n}\n")
	s, ok := body[1].(*ast.AssignStmt)
	if !ok {
		t.Fatalf("second statement is %T, want *ast.AssignStmt", body[1])
	}
	id, ok := s.Target.(*ast.Identifier)
	if !ok || id.Name != "x" {
		t.Fatalf("assign target = %v, want identifier x", s.Target)
	}
	if _, ok := s.Value.(*ast.CallExpr); !ok {
		t.Fatalf("assign value is %T, want a desugared call (x + 1)", s.Value)
	}
}
