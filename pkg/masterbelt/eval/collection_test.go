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

// wantFold folds e and asserts its rendered value.
func wantFold(t *testing.T, e ast.Expr, env Env, want string) {
	t.Helper()
	v := Expr(e, env)
	if v == nil || v.String() != want {
		t.Fatalf("fold = %v, want %s", v, want)
	}
}

// TestCollectionPushFold covers list.push: a new list with the value at the
// end, leaving the receiver unchanged; a map (keyed) has no push.
func TestCollectionPushFold(t *testing.T) {
	env := stubEnv{reg: builtin.Default()}
	wantFold(t, methodCall(listLit(intLit("1"), intLit("2")), "push", intLit("3")), env, "[1, 2, 3]")
	// The receiver is unchanged: push builds a new list.
	recv := listLit(intLit("1"))
	_ = Expr(methodCall(recv, "push", intLit("9")), env)
	if got := Expr(recv, env).String(); got != "[1]" {
		t.Errorf("receiver = %s, want [1] (push does not mutate it)", got)
	}
	// A map has no push.
	wantNil(t, methodCall(mapLit(strLit("a"), intLit("1")), "push", intLit("2")), env)
}

// TestCollectionUnshiftFold covers list.unshift — push's front-side mirror: a
// new list with the value first, the receiver unchanged; a map has none.
func TestCollectionUnshiftFold(t *testing.T) {
	env := stubEnv{reg: builtin.Default()}
	wantFold(t, methodCall(listLit(intLit("2"), intLit("3")), "unshift", intLit("1")), env, "[1, 2, 3]")
	wantFold(t, methodCall(listLit(), "unshift", intLit("1")), env, "[1]")
	wantNil(t, methodCall(mapLit(strLit("a"), intLit("1")), "unshift", intLit("2")), env)
}

// TestCollectionPopShiftFold covers the taking reads: pop folds to the last
// element, shift to the first, and both fold to null on an empty list — the
// optional<T> read is a value, not an error. A map has neither.
func TestCollectionPopShiftFold(t *testing.T) {
	env := stubEnv{reg: builtin.Default()}
	xs := func() *ast.CollectionLit { return listLit(intLit("1"), intLit("2"), intLit("3")) }
	wantFold(t, methodCall(xs(), "pop"), env, "3")
	wantFold(t, methodCall(xs(), "shift"), env, "1")
	wantFold(t, methodCall(listLit(), "pop"), env, "null")
	wantFold(t, methodCall(listLit(), "shift"), env, "null")
	// The receiver is unchanged: a taking read does not mutate it.
	recv := listLit(intLit("1"), intLit("2"))
	_ = Expr(methodCall(recv, "pop"), env)
	if got := Expr(recv, env).String(); got != "[1, 2]" {
		t.Errorf("receiver = %s, want [1, 2] (pop does not mutate it)", got)
	}
	// A spurious argument is not a pop/shift: it does not fold.
	wantNil(t, methodCall(xs(), "pop", intLit("0")), env)
	// A map has no pop/shift.
	wantNil(t, methodCall(mapLit(strLit("a"), intLit("1")), "pop"), env)
	wantNil(t, methodCall(mapLit(strLit("a"), intLit("1")), "shift"), env)
}

// TestCollectionAddFold covers the + fold re-deciding the overload by the
// operand's shape: a list of the receiver's elements concatenates (other:
// self), one element pushes (element: T) — including an element that is itself
// a list — and an operand both readings fit (an empty receiver with a list
// argument) stays unevaluated rather than guessed.
func TestCollectionAddFold(t *testing.T) {
	env := stubEnv{reg: builtin.Default()}
	// list + list of the same element shape: the concatenation.
	wantFold(t, methodCall(listLit(intLit("1"), intLit("2")), "add", listLit(intLit("3"), intLit("4"))), env, "[1, 2, 3, 4]")
	// list + element: the push.
	wantFold(t, methodCall(listLit(intLit("1"), intLit("2")), "add", intLit("3")), env, "[1, 2, 3]")
	// A nested receiver: [3] is one element of list<list<nint>>, so it pushes.
	nested := methodCall(listLit(listLit(intLit("1")), listLit(intLit("2"))), "add", listLit(intLit("3")))
	wantFold(t, nested, env, "[[1], [2], [3]]")
	// ...while another list-of-lists concatenates.
	concat := methodCall(listLit(listLit(intLit("1"))), "add", listLit(listLit(intLit("2"))))
	wantFold(t, concat, env, "[[1], [2]]")
	// An empty list argument concatenates: it cannot be a bare element.
	wantFold(t, methodCall(listLit(intLit("1")), "add", listLit()), env, "[1]")
	// An empty receiver with a non-list element can only push.
	wantFold(t, methodCall(listLit(), "add", intLit("3")), env, "[3]")
	// An empty receiver with a list argument fits both readings: do not guess.
	wantNil(t, methodCall(listLit(), "add", listLit(intLit("1"))), env)
	// A nested receiver with an empty list argument fits both readings too.
	wantNil(t, methodCall(listLit(listLit(intLit("1"))), "add", listLit()), env)
	// A map's + does not fold here.
	wantNil(t, methodCall(mapLit(strLit("a"), intLit("1")), "add", mapLit(strLit("b"), intLit("2"))), env)
}
