package ast

import (
	"strings"
	"testing"
)

// TestWalkExprs pins the shared traversal's contract: pre-order over operand
// positions, no descent into function-literal bodies, and false skipping a
// node's operands.
func TestWalkExprs(t *testing.T) {
	// f(a.b, [k: v], fn() { return hidden })
	expr := NewCallExpr(
		NewIdentifier("f", nil),
		[]Expr{
			NewMemberExpr(NewIdentifier("a", nil), NewIdentifier("b", nil), nil),
			NewCollectionLit([]*CollectionEntry{
				{Key: NewIdentifier("k", nil), Value: NewIdentifier("v", nil)},
			}, nil),
			NewFuncLit(nil, nil, []Stmt{
				NewReturnStmt(NewIdentifier("hidden", nil), nil),
			}, nil),
		},
		nil,
	)

	var order []string
	WalkExprs(expr, func(e Expr) bool {
		switch e := e.(type) {
		case *Identifier:
			order = append(order, e.Name)
		case *MemberExpr:
			order = append(order, ".")
		case *CallExpr:
			order = append(order, "call")
		case *CollectionLit:
			order = append(order, "lit")
		case *FuncLit:
			order = append(order, "fn")
		}
		return true
	})
	// Pre-order; the member's name (b) is not an operand, and the lambda body
	// (hidden) is its own scope.
	if got, want := strings.Join(order, ","), "call,f,.,a,lit,k,v,fn"; got != want {
		t.Errorf("visit order = %s, want %s", got, want)
	}

	// Returning false skips the node's operands.
	var pruned []string
	WalkExprs(expr, func(e Expr) bool {
		switch e := e.(type) {
		case *Identifier:
			pruned = append(pruned, e.Name)
		case *MemberExpr:
			pruned = append(pruned, ".")
			return false
		}
		return true
	})
	if got, want := strings.Join(pruned, ","), "f,.,k,v"; got != want {
		t.Errorf("pruned order = %s, want %s (false must skip the receiver)", got, want)
	}
}

// TestWalkExprsTernary pins that the ternary's three operands — condition,
// then, and else — are walked in source order, so name resolution and the
// editor's occurrence walks reach every reference inside a conditional value.
func TestWalkExprsTernary(t *testing.T) {
	// c ? t : e
	expr := NewTernaryExpr(
		NewIdentifier("c", nil),
		NewIdentifier("t", nil),
		NewIdentifier("e", nil),
		nil,
	)
	var order []string
	WalkExprs(expr, func(e Expr) bool {
		switch e := e.(type) {
		case *Identifier:
			order = append(order, e.Name)
		case *TernaryExpr:
			order = append(order, "?")
		}
		return true
	})
	if got, want := strings.Join(order, ","), "?,c,t,e"; got != want {
		t.Errorf("visit order = %s, want %s", got, want)
	}
}
