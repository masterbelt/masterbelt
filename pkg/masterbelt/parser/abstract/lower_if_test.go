package abstract

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/source/ast"
)

// ifOf lowers src and returns the IfStmt that is the first statement of its
// first function's body, failing the test when that statement is not an if.
func ifOf(t *testing.T, src string) *ast.IfStmt {
	t.Helper()
	file, diags := Lower([]byte(src))
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if len(file.Funcs) == 0 {
		t.Fatal("no function lowered")
	}
	body := file.Funcs[0].Body
	if len(body) == 0 {
		t.Fatal("empty function body")
	}
	s, ok := body[0].(*ast.IfStmt)
	if !ok {
		t.Fatalf("first body statement is %T, want *ast.IfStmt", body[0])
	}
	return s
}

// TestLowerIfNoElse checks a bare guard: the condition, a one-statement then
// body, and no else branch at all (both ElseIf and Else nil).
func TestLowerIfNoElse(t *testing.T) {
	s := ifOf(t, "pub fn f(n: int): int {\n  if n > 0 {\n    return 1\n  }\n  return 0\n}\n")
	if s.Cond == nil {
		t.Fatal("condition is nil")
	}
	if len(s.Then) != 1 {
		t.Fatalf("then body has %d statements, want 1", len(s.Then))
	}
	if _, ok := s.Then[0].(*ast.ReturnStmt); !ok {
		t.Fatalf("then body is %T, want a return", s.Then[0])
	}
	if s.ElseIf != nil || s.Else != nil {
		t.Fatalf("else present (ElseIf=%v Else=%v), want none", s.ElseIf, s.Else)
	}
}

// TestLowerIfElseBlock checks an if with a plain else block: the then and else
// bodies are separate statement lists and there is no else-if chain.
func TestLowerIfElseBlock(t *testing.T) {
	s := ifOf(t, "pub fn f(b: bool): int {\n  if b {\n    return 1\n  } else {\n    return 0\n  }\n}\n")
	if len(s.Then) != 1 {
		t.Fatalf("then body has %d statements, want 1", len(s.Then))
	}
	if s.ElseIf != nil {
		t.Fatal("ElseIf is set, want nil for a plain else")
	}
	if len(s.Else) != 1 {
		t.Fatalf("else body has %d statements, want 1", len(s.Else))
	}
	if _, ok := s.Else[0].(*ast.ReturnStmt); !ok {
		t.Fatalf("else body is %T, want a return", s.Else[0])
	}
}

// TestLowerIfElseChain checks an else-if chain: the head if's else branch is a
// nested IfStmt (ElseIf), whose own else is the final block (Else).
func TestLowerIfElseChain(t *testing.T) {
	s := ifOf(t, "pub fn f(n: int): int {\n  if n < 0 {\n    return -1\n  } else if n > 0 {\n    return 1\n  } else {\n    return 0\n  }\n}\n")
	if s.ElseIf == nil {
		t.Fatal("ElseIf is nil, want the chained if")
	}
	if s.Else != nil {
		t.Fatal("head Else is set, want the else to live on the chained if")
	}
	chained := s.ElseIf
	if chained.Cond == nil || len(chained.Then) != 1 {
		t.Fatalf("chained if = %+v, want a condition and a one-statement then", chained)
	}
	if chained.ElseIf != nil {
		t.Fatal("chained ElseIf is set, want nil")
	}
	if len(chained.Else) != 1 {
		t.Fatalf("chained else body has %d statements, want 1", len(chained.Else))
	}
}

// TestLowerIfNested checks that an else branch may hold further statements,
// including a nested if, so control flow composes.
func TestLowerIfNested(t *testing.T) {
	s := ifOf(t, "pub fn f(n: int): string {\n  if n > 0 {\n    return \"p\"\n  } else {\n    if n < 0 {\n      return \"n\"\n    }\n    return \"z\"\n  }\n}\n")
	if len(s.Else) != 2 {
		t.Fatalf("else body has %d statements, want 2 (a nested if and a return)", len(s.Else))
	}
	if _, ok := s.Else[0].(*ast.IfStmt); !ok {
		t.Fatalf("first else statement is %T, want a nested *ast.IfStmt", s.Else[0])
	}
	if _, ok := s.Else[1].(*ast.ReturnStmt); !ok {
		t.Fatalf("second else statement is %T, want a return", s.Else[1])
	}
}

// TestLowerIfBodyStatements checks that an if body may mix a const-free
// statement set: a bare expression statement and a return both lower inside the
// then body.
func TestLowerIfBodyStatements(t *testing.T) {
	s := ifOf(t, "pub fn f(n: int): int {\n  if n > 0 {\n    log(n)\n    return 1\n  }\n  return 0\n}\n")
	if len(s.Then) != 2 {
		t.Fatalf("then body has %d statements, want 2", len(s.Then))
	}
	if _, ok := s.Then[0].(*ast.ExprStmt); !ok {
		t.Fatalf("first then statement is %T, want an ExprStmt", s.Then[0])
	}
	if _, ok := s.Then[1].(*ast.ReturnStmt); !ok {
		t.Fatalf("second then statement is %T, want a return", s.Then[1])
	}
}
