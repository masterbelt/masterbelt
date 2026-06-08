package abstract

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/source/ast"
)

// switchOf lowers src and returns the SwitchStmt that is the sole statement of
// its first function's body, failing the test when the body is not exactly one
// switch.
func switchOf(t *testing.T, src string) *ast.SwitchStmt {
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
		t.Fatalf("body has %d statements, want one switch", len(body))
	}
	sw, ok := body[0].(*ast.SwitchStmt)
	if !ok {
		t.Fatalf("body statement is %T, want *ast.SwitchStmt", body[0])
	}
	return sw
}

// TestLowerSwitchEnum checks an exhaustive enum switch: the scrutinee, the three
// arms each with one value pattern and a return body, and the absence of a
// wildcard (Else is nil).
func TestLowerSwitchEnum(t *testing.T) {
	sw := switchOf(t, "pub fn c(r: R): string {\n  switch r {\n    A -> return \"a\"\n    B -> return \"b\"\n    C -> return \"c\"\n  }\n}\n")
	if id, ok := sw.Scrutinee.(*ast.Identifier); !ok || id.Name != "r" {
		t.Fatalf("scrutinee = %v, want identifier r", sw.Scrutinee)
	}
	if len(sw.Arms) != 3 {
		t.Fatalf("got %d arms, want 3", len(sw.Arms))
	}
	for i, name := range []string{"A", "B", "C"} {
		arm := sw.Arms[i]
		if len(arm.Values) != 1 {
			t.Fatalf("arm %d has %d values, want 1", i, len(arm.Values))
		}
		if id, ok := arm.Values[0].(*ast.Identifier); !ok || id.Name != name {
			t.Fatalf("arm %d value = %v, want %s", i, arm.Values[0], name)
		}
		if len(arm.Body) != 1 {
			t.Fatalf("arm %d body has %d statements, want 1", i, len(arm.Body))
		}
		if _, ok := arm.Body[0].(*ast.ReturnStmt); !ok {
			t.Fatalf("arm %d body is %T, want a return", i, arm.Body[0])
		}
	}
	if sw.Else != nil {
		t.Fatalf("Else = %v, want nil for an exhaustive enum switch", sw.Else)
	}
}

// TestLowerSwitchScalar checks a scalar switch with a multi-value arm and a "_"
// wildcard whose block body is lifted into Else.
func TestLowerSwitchScalar(t *testing.T) {
	sw := switchOf(t, "pub fn g(n: nint): string {\n  switch n {\n    0 -> return \"z\"\n    1, 2, 3 -> return \"l\"\n    _ -> {\n      return \"h\"\n    }\n  }\n}\n")
	if len(sw.Arms) != 2 {
		t.Fatalf("got %d arms, want 2 (the wildcard is Else)", len(sw.Arms))
	}
	if got := len(sw.Arms[1].Values); got != 3 {
		t.Fatalf("multi-value arm has %d values, want 3", got)
	}
	if sw.Else == nil {
		t.Fatal("Else is nil, want the wildcard's body")
	}
	if len(sw.Else) != 1 {
		t.Fatalf("Else has %d statements, want 1", len(sw.Else))
	}
	if _, ok := sw.Else[0].(*ast.ReturnStmt); !ok {
		t.Fatalf("Else body is %T, want a return", sw.Else[0])
	}
}

// TestLowerSwitchExprBody checks that an arm whose body is a bare expression
// lowers to an ExprStmt, not a value pattern.
func TestLowerSwitchExprBody(t *testing.T) {
	sw := switchOf(t, "pub fn c(r: R): string {\n  switch r {\n    A -> log(r)\n  }\n}\n")
	if len(sw.Arms) != 1 || len(sw.Arms[0].Values) != 1 {
		t.Fatalf("arms = %v, want one arm with one value", sw.Arms)
	}
	if len(sw.Arms[0].Body) != 1 {
		t.Fatalf("arm body has %d statements, want 1", len(sw.Arms[0].Body))
	}
	if _, ok := sw.Arms[0].Body[0].(*ast.ExprStmt); !ok {
		t.Fatalf("arm body is %T, want an ExprStmt", sw.Arms[0].Body[0])
	}
}

// TestLowerSwitchAfterWildcard checks that an arm written after the wildcard is
// lifted out of the live arms into AfterElse (it can never run).
func TestLowerSwitchAfterWildcard(t *testing.T) {
	sw := switchOf(t, "pub fn g(n: nint): string {\n  switch n {\n    0 -> return \"z\"\n    _ -> return \"h\"\n    1 -> return \"o\"\n  }\n}\n")
	if len(sw.Arms) != 1 {
		t.Fatalf("live arms = %d, want 1 (the 0 arm)", len(sw.Arms))
	}
	if sw.Else == nil {
		t.Fatal("Else is nil, want the wildcard body")
	}
	if len(sw.AfterElse) != 1 {
		t.Fatalf("AfterElse = %d, want the 1 arm written after the wildcard", len(sw.AfterElse))
	}
}

// TestLowerSwitchNested checks that a switch arm body may itself be a switch, so
// control flow nests.
func TestLowerSwitchNested(t *testing.T) {
	sw := switchOf(t, "pub fn c(r: R): string {\n  switch r {\n    A -> switch r {\n      B -> return \"b\"\n    }\n  }\n}\n")
	if len(sw.Arms) != 1 || len(sw.Arms[0].Body) != 1 {
		t.Fatalf("outer arm body = %v, want one statement", sw.Arms)
	}
	if _, ok := sw.Arms[0].Body[0].(*ast.SwitchStmt); !ok {
		t.Fatalf("nested arm body is %T, want *ast.SwitchStmt", sw.Arms[0].Body[0])
	}
}
