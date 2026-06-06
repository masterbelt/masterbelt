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
	body := bodyOf(t, "pub fn f(n: nint): nint {\n  let x = n\n  return x\n}\n")
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
	body := bodyOf(t, "pub fn f(n: nint): nint {\n  let x: nint = n\n  return x\n}\n")
	s, ok := body[0].(*ast.LetStmt)
	if !ok {
		t.Fatalf("first statement is %T, want *ast.LetStmt", body[0])
	}
	if s.Type == nil {
		t.Fatal("annotated let has no type, want nint")
	}
	named, ok := s.Type.(*ast.NamedType)
	if !ok || named.Name != "nint" {
		t.Fatalf("let type = %v, want nint", s.Type)
	}
}

// TestLowerAssign checks a reassignment: the target identifier and the new value
// are lowered, and the value is desugared like any other expression (x + 1
// becomes a method call).
func TestLowerAssign(t *testing.T) {
	body := bodyOf(t, "pub fn f(n: nint): nint {\n  let x = n\n  x = x + 1\n  return x\n}\n")
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

// TestLowerIndexAssign checks an index write: xs[i] = v desugars to a rebind of
// the collection, xs = xs.set(i, v). The target becomes the bare collection
// identifier (the same let-local target a plain assignment has), and the value
// is a set call carrying the index and the new value as its two arguments.
func TestLowerIndexAssign(t *testing.T) {
	body := bodyOf(t, "pub fn f(): list<nint> {\n  let xs = [1, 2, 3]\n  xs[0] = 99\n  return xs\n}\n")
	s, ok := body[1].(*ast.AssignStmt)
	if !ok {
		t.Fatalf("second statement is %T, want *ast.AssignStmt", body[1])
	}
	// The rebind target is the collection name, not the index expression.
	id, ok := s.Target.(*ast.Identifier)
	if !ok || id.Name != "xs" {
		t.Fatalf("assign target = %v, want identifier xs", s.Target)
	}
	// The value is xs.set(0, 99): a call of set on the collection with the index
	// and the assigned value as arguments.
	call, ok := s.Value.(*ast.CallExpr)
	if !ok {
		t.Fatalf("assign value is %T, want a set call", s.Value)
	}
	member, ok := call.Callee.(*ast.MemberExpr)
	if !ok || member.Member.Name != "set" {
		t.Fatalf("assign value callee = %v, want a .set member access", call.Callee)
	}
	if recv, ok := member.Receiver.(*ast.Identifier); !ok || recv.Name != "xs" {
		t.Fatalf("set receiver = %v, want identifier xs", member.Receiver)
	}
	if len(call.Arguments) != 2 {
		t.Fatalf("set has %d arguments, want 2 (index, value)", len(call.Arguments))
	}
	if idx, ok := call.Arguments[0].(*ast.IntLit); !ok || idx.Text != "0" {
		t.Errorf("set arg 0 = %v, want IntLit 0 (the index)", call.Arguments[0])
	}
	if v, ok := call.Arguments[1].(*ast.IntLit); !ok || v.Text != "99" {
		t.Errorf("set arg 1 = %v, want IntLit 99 (the value)", call.Arguments[1])
	}
}
