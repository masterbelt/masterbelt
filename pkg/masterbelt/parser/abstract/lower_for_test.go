package abstract

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/source/ast"
)

// forOf lowers src and returns the ForStmt that is the second statement of its
// first function's body (after a let accumulator), failing the test when the
// statement is not a for.
func forOf(t *testing.T, src string) *ast.ForStmt {
	t.Helper()
	file, diags := Lower([]byte(src))
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if len(file.Funcs) == 0 {
		t.Fatal("no function lowered")
	}
	body := file.Funcs[0].Body
	if len(body) < 2 {
		t.Fatalf("body has %d statements, want at least a let and a for", len(body))
	}
	f, ok := body[1].(*ast.ForStmt)
	if !ok {
		t.Fatalf("body statement is %T, want *ast.ForStmt", body[1])
	}
	return f
}

// TestLowerForOf checks an "of" loop: the loop variable, the ForOf kind, the
// iterated identifier, and a single-statement body.
func TestLowerForOf(t *testing.T) {
	f := forOf(t, "pub fn f(xs: list<nint>): nint {\n  let total = 0\n  for x of xs {\n    total = total + x\n  }\n  return total\n}\n")
	if f.Var != "x" {
		t.Fatalf("loop variable = %q, want x", f.Var)
	}
	if f.Kind != ast.ForOf {
		t.Fatalf("kind = %v, want of", f.Kind)
	}
	if id, ok := f.Iter.(*ast.Identifier); !ok || id.Name != "xs" {
		t.Fatalf("iter = %v, want identifier xs", f.Iter)
	}
	if len(f.Body) != 1 {
		t.Fatalf("body has %d statements, want 1", len(f.Body))
	}
	if _, ok := f.Body[0].(*ast.AssignStmt); !ok {
		t.Fatalf("body statement is %T, want an assignment", f.Body[0])
	}
}

// TestLowerForIn checks an "in" loop: the ForIn kind binds the key.
func TestLowerForIn(t *testing.T) {
	f := forOf(t, "pub fn f(m: map<string, nint>): string {\n  let out = \"\"\n  for k in m {\n    out = out + k\n  }\n  return out\n}\n")
	if f.Var != "k" {
		t.Fatalf("loop variable = %q, want k", f.Var)
	}
	if f.Kind != ast.ForIn {
		t.Fatalf("kind = %v, want in", f.Kind)
	}
	if id, ok := f.Iter.(*ast.Identifier); !ok || id.Name != "m" {
		t.Fatalf("iter = %v, want identifier m", f.Iter)
	}
}

// TestLowerForNested checks a nested for: the outer loop's body is itself a for.
func TestLowerForNested(t *testing.T) {
	f := forOf(t, "pub fn f(xs: list<nint>, ys: list<nint>): nint {\n  let n = 0\n  for x of xs {\n    for y of ys {\n      n = n + 1\n    }\n  }\n  return n\n}\n")
	if len(f.Body) != 1 {
		t.Fatalf("outer body has %d statements, want 1", len(f.Body))
	}
	inner, ok := f.Body[0].(*ast.ForStmt)
	if !ok {
		t.Fatalf("outer body statement is %T, want a nested *ast.ForStmt", f.Body[0])
	}
	if inner.Var != "y" || inner.Kind != ast.ForOf {
		t.Fatalf("inner for = %q %v, want y of", inner.Var, inner.Kind)
	}
}
