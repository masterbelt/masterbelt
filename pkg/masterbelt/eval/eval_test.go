package eval

import (
	"math/big"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// --- ast builders (nil syntax: evaluation never reads Syntax) ----------------

func intLit(text string) *ast.IntLit { return ast.NewIntLit(text, nil) }
func selfExpr() *ast.SelfExpr        { return ast.NewSelfExpr(nil) }

// binary builds the desugared form of an operator: recv.method(arg).
func binary(recv ast.Expr, method string, arg ast.Expr) *ast.CallExpr {
	m := ast.NewMemberExpr(recv, ast.NewIdentifier(method, nil), nil)
	return ast.NewCallExpr(m, []ast.Expr{arg}, nil)
}

// stubEnv resolves nothing: a refinement predicate references only self and
// literals, so the registry is all the environment it needs.
type stubEnv struct{ reg *builtin.Registry }

func (e stubEnv) Resolve(*ast.Identifier) *ast.ConstDecl       { return nil }
func (e stubEnv) ResolveMember(*ast.MemberExpr) *ast.ConstDecl { return nil }
func (e stubEnv) ValueOf(*ast.ConstDecl) *ir.Constant          { return nil }
func (e stubEnv) Registry() *builtin.Registry                  { return e.reg }

// portPredicate is the desugared form of: self >= 1 && self <= 65535.
func portPredicate() ast.Expr {
	return binary(
		binary(selfExpr(), "gteq", intLit("1")),
		"anan",
		binary(selfExpr(), "lteq", intLit("65535")),
	)
}

func TestPredicate(t *testing.T) {
	env := stubEnv{reg: builtin.Default()}
	cases := []struct {
		name string
		self int64
		want bool
	}{
		{"in range", 8080, true},
		{"low boundary", 1, true},
		{"high boundary", 65535, true},
		{"below", 0, false},
		{"above", 70000, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := Predicate(portPredicate(), ir.IntConstant(big.NewInt(tc.self)), env)
			if v == nil || v.Kind != ir.ConstBool {
				t.Fatalf("Predicate = %v, want a bool constant", v)
			}
			if v.Bool != tc.want {
				t.Errorf("Predicate(self=%d) = %t, want %t", tc.self, v.Bool, tc.want)
			}
		})
	}
}

func TestPredicateUnboundSelf(t *testing.T) {
	// Expr never binds self, so a predicate folded through it stays unfoldable.
	if v := Expr(portPredicate(), stubEnv{reg: builtin.Default()}); v != nil {
		t.Errorf("Expr = %v, want nil for an unbound self", v)
	}
}

func TestPredicateNotFoldable(t *testing.T) {
	// A user-defined method has no intrinsic: the predicate folds to nil.
	pred := binary(selfExpr(), "bogus", intLit("1"))
	if v := Predicate(pred, ir.IntConstant(big.NewInt(1)), stubEnv{reg: builtin.Default()}); v != nil {
		t.Errorf("Predicate = %v, want nil for a non-intrinsic method", v)
	}
}

func TestPredicateNonBool(t *testing.T) {
	// self + 1 folds to an int; deciding what a non-bool predicate means is the
	// caller's business (the semantic layer reports it on the declaration).
	pred := binary(selfExpr(), "add", intLit("1"))
	v := Predicate(pred, ir.IntConstant(big.NewInt(1)), stubEnv{reg: builtin.Default()})
	if v == nil || v.Kind != ir.ConstInt {
		t.Fatalf("Predicate = %v, want an int constant", v)
	}
	if v.Int.Int64() != 2 {
		t.Errorf("Predicate = %v, want 2", v.Int)
	}
}
