package eval

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
)

// funcLit builds a function-literal expression: fn(params...) { body... }. The
// params take no annotations (evaluation is value-blind), and the body is the
// statements given.
func funcLit(params []string, body ...ast.Stmt) *ast.FuncLit {
	ps := make([]*ast.ParamDef, len(params))
	for i, p := range params {
		ps[i] = ast.NewParamDef(p, nil, nil)
	}
	return ast.NewFuncLit(ps, nil, body, nil)
}

// methodCall builds recv.name(args...).
func methodCall(recv ast.Expr, name string, args ...ast.Expr) *ast.CallExpr {
	m := ast.NewMemberExpr(recv, ast.NewIdentifier(name, nil), nil)
	return ast.NewCallExpr(m, args, nil)
}

// TestCollectionLenFold covers the list.len and map.len intrinsics: both fold to
// the entry count, and a stray argument does not fold.
func TestCollectionLenFold(t *testing.T) {
	env := stubEnv{reg: builtin.Default()}
	wantInt(t, methodCall(listLit(intLit("1"), intLit("2"), intLit("3")), "len"), env, 3)
	wantInt(t, methodCall(listLit(), "len"), env, 0)
	wantInt(t, methodCall(mapLit(strLit("a"), intLit("1"), strLit("b"), intLit("2")), "len"), env, 2)
	// A spurious argument is not a len: it does not fold.
	wantNil(t, methodCall(listLit(intLit("1")), "len", intLit("0")), env)
}

// TestCollectionFoldList covers the native fold over a list: the step sees
// (acc, index, value), so summing the values and the indices both fold.
func TestCollectionFoldList(t *testing.T) {
	env := stubEnv{reg: builtin.Default()}
	// fold(0, fn(a, k, v) { return a + v }) over [1, 2, 3] = 6.
	sumV := funcLit([]string{"a", "k", "v"}, ret(binary(id("a"), "add", id("v"))))
	wantInt(t, methodCall(listLit(intLit("1"), intLit("2"), intLit("3")), "fold", intLit("0"), sumV), env, 6)
	// fold(0, fn(a, k, v) { return a + k }) sums the indices: 0 + 1 + 2 = 3.
	sumK := funcLit([]string{"a", "k", "v"}, ret(binary(id("a"), "add", id("k"))))
	wantInt(t, methodCall(listLit(intLit("10"), intLit("20"), intLit("30")), "fold", intLit("0"), sumK), env, 3)
}

// TestCollectionFoldMap covers the native fold over a map: the step's key is the
// entry key, so a fold over the values folds to their sum.
func TestCollectionFoldMap(t *testing.T) {
	env := stubEnv{reg: builtin.Default()}
	m := mapLit(strLit("a"), intLit("1"), strLit("b"), intLit("2"), strLit("c"), intLit("3"))
	sumV := funcLit([]string{"a", "k", "v"}, ret(binary(id("a"), "add", id("v"))))
	wantInt(t, methodCall(m, "fold", intLit("0"), sumV), env, 6)
}

// TestCollectionFoldNonFunc covers a fold whose step is not a function value: it
// does not fold, the conservative failure.
func TestCollectionFoldNonFunc(t *testing.T) {
	env := stubEnv{reg: builtin.Default()}
	wantNil(t, methodCall(listLit(intLit("1")), "fold", intLit("0"), intLit("9")), env)
}

// TestCollectionAppendFold covers list.append: a new list with the value at the
// end, leaving the receiver unchanged; a map (keyed) has no append.
func TestCollectionAppendFold(t *testing.T) {
	env := stubEnv{reg: builtin.Default()}
	v := Expr(methodCall(listLit(intLit("1"), intLit("2")), "append", intLit("3")), env)
	if v == nil || v.String() != "[1, 2, 3]" {
		t.Fatalf("append = %v, want [1, 2, 3]", v)
	}
	// The receiver is unchanged: append builds a new list.
	recv := listLit(intLit("1"))
	_ = Expr(methodCall(recv, "append", intLit("9")), env)
	if got := Expr(recv, env).String(); got != "[1]" {
		t.Errorf("receiver = %s, want [1] (append does not mutate it)", got)
	}
	// A map has no append.
	wantNil(t, methodCall(mapLit(strLit("a"), intLit("1")), "append", intLit("2")), env)
}
