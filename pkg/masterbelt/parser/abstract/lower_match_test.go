package abstract

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/source/ast"
)

// matchOf lowers src and returns the MatchStmt that is the sole statement of its
// first function's body, failing the test when the body is not exactly one
// match.
func matchOf(t *testing.T, src string) *ast.MatchStmt {
	t.Helper()
	file, diags := Lower([]byte(src))
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if len(file.Funcs) == 0 {
		t.Fatal("no function lowered")
	}
	body := file.Funcs[0].Body
	if len(body) != 1 {
		t.Fatalf("body has %d statements, want one match", len(body))
	}
	m, ok := body[0].(*ast.MatchStmt)
	if !ok {
		t.Fatalf("body statement is %T, want *ast.MatchStmt", body[0])
	}
	return m
}

// namedArm fails unless the arm's pattern is the named type want with the
// binding bind ("" for no binding).
func namedArm(t *testing.T, arm *ast.MatchArm, want, bind string) {
	t.Helper()
	named, ok := arm.Type.(*ast.NamedType)
	if !ok {
		t.Fatalf("arm type is %T, want *ast.NamedType", arm.Type)
	}
	if named.Name != want {
		t.Fatalf("arm type name = %q, want %q", named.Name, want)
	}
	if arm.Bind != bind {
		t.Fatalf("arm binding = %q, want %q", arm.Bind, bind)
	}
}

// TestLowerMatchUnion checks a union match: the scrutinee, two type-pattern arms
// each with a binding and a return body, and no wildcard (Else is nil).
func TestLowerMatchUnion(t *testing.T) {
	m := matchOf(t, "pub fn d(v: V): string {\n  match v {\n    Coin c  -> return \"c\"\n    Level l -> return \"l\"\n  }\n}\n")
	if id, ok := m.Scrutinee.(*ast.Identifier); !ok || id.Name != "v" {
		t.Fatalf("scrutinee = %v, want identifier v", m.Scrutinee)
	}
	if len(m.Arms) != 2 {
		t.Fatalf("got %d arms, want 2", len(m.Arms))
	}
	namedArm(t, m.Arms[0], "Coin", "c")
	namedArm(t, m.Arms[1], "Level", "l")
	for i, arm := range m.Arms {
		if len(arm.Body) != 1 {
			t.Fatalf("arm %d body has %d statements, want 1", i, len(arm.Body))
		}
		if _, ok := arm.Body[0].(*ast.ReturnStmt); !ok {
			t.Fatalf("arm %d body is %T, want a return", i, arm.Body[0])
		}
	}
	if m.Else != nil {
		t.Fatalf("Else = %v, want nil for a match with no wildcard", m.Else)
	}
}

// TestLowerMatchNullAndBlock checks an optional match: a bound member arm, a
// null arm with no binding, and a block body lowered to its statements.
func TestLowerMatchNullAndBlock(t *testing.T) {
	m := matchOf(t, "pub fn a(v: Coin | null): nint {\n  match v {\n    Coin c -> return c.n\n    null   -> {\n      return 0\n    }\n  }\n}\n")
	if len(m.Arms) != 2 {
		t.Fatalf("got %d arms, want 2", len(m.Arms))
	}
	namedArm(t, m.Arms[0], "Coin", "c")
	namedArm(t, m.Arms[1], "null", "")
	if len(m.Arms[1].Body) != 1 {
		t.Fatalf("null arm body has %d statements, want 1", len(m.Arms[1].Body))
	}
}

// TestLowerMatchWildcard checks that the wildcard "_" arm is lifted into Else and
// binds nothing.
func TestLowerMatchWildcard(t *testing.T) {
	m := matchOf(t, "pub fn c(v: V): nint {\n  match v {\n    Coin c -> return 1\n    _      -> return 0\n  }\n}\n")
	if len(m.Arms) != 1 {
		t.Fatalf("got %d live arms, want 1 (the wildcard is Else)", len(m.Arms))
	}
	if m.Else == nil {
		t.Fatal("Else is nil, want the wildcard's body")
	}
	if len(m.Else) != 1 {
		t.Fatalf("Else has %d statements, want 1", len(m.Else))
	}
}

// TestLowerMatchAfterWildcard checks that an arm written after the wildcard is
// lifted out of the live arms into AfterElse (it can never run).
func TestLowerMatchAfterWildcard(t *testing.T) {
	m := matchOf(t, "pub fn c(v: V): nint {\n  match v {\n    Coin c -> return 1\n    _      -> return 0\n    Level l -> return 2\n  }\n}\n")
	if len(m.Arms) != 1 {
		t.Fatalf("live arms = %d, want 1 (the Coin arm)", len(m.Arms))
	}
	if m.Else == nil {
		t.Fatal("Else is nil, want the wildcard body")
	}
	if len(m.AfterElse) != 1 {
		t.Fatalf("AfterElse = %d, want the 1 arm written after the wildcard", len(m.AfterElse))
	}
	namedArm(t, m.AfterElse[0], "Level", "l")
}

// TestLowerMatchIndexScrutinee checks the E-18 use case: an index read scrutinee
// desugars to a get call and the error/value arms lower as type patterns.
func TestLowerMatchIndexScrutinee(t *testing.T) {
	m := matchOf(t, "pub fn f(xs: list<nint>): nint {\n  match xs[0] {\n    nint v   -> return v\n    error e -> return 0\n  }\n}\n")
	if _, ok := m.Scrutinee.(*ast.CallExpr); !ok {
		t.Fatalf("scrutinee is %T, want the desugared get call", m.Scrutinee)
	}
	namedArm(t, m.Arms[0], "nint", "v")
	namedArm(t, m.Arms[1], "error", "e")
}

// TestLowerMatchNested checks that a match arm body may itself be a match, so
// control flow nests.
func TestLowerMatchNested(t *testing.T) {
	m := matchOf(t, "pub fn c(v: V): string {\n  match v {\n    Coin c -> match v {\n      Level l -> return \"l\"\n    }\n  }\n}\n")
	if len(m.Arms) != 1 || len(m.Arms[0].Body) != 1 {
		t.Fatalf("outer arm body = %v, want one statement", m.Arms)
	}
	if _, ok := m.Arms[0].Body[0].(*ast.MatchStmt); !ok {
		t.Fatalf("nested arm body is %T, want *ast.MatchStmt", m.Arms[0].Body[0])
	}
}
