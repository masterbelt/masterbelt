package eval

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// collEnv resolves map<...>/list<...> annotations to their App types — the
// syntactic channel an empty collection literal settles its mapness through —
// and folds named consts through DeclExpecting so a reference to an empty
// collection-typed const carries the settled mapness. It is the eval-layer stand
// in for the semantic engine's evalEnv, scoped to what these tests reference.
type collEnv struct {
	reg     *builtin.Registry
	mapDef  *ir.TypeDef
	listDef *ir.TypeDef
	consts  map[string]*ast.ConstDecl
	funcs   map[string][]*ast.FuncDecl
	annType map[*ast.ConstDecl]ir.Type // each const's resolved annotation type
}

func newCollEnv() *collEnv {
	return &collEnv{
		reg:     builtin.Default(),
		mapDef:  &ir.TypeDef{Name: "map"},
		listDef: &ir.TypeDef{Name: "list"},
		consts:  map[string]*ast.ConstDecl{},
		funcs:   map[string][]*ast.FuncDecl{},
		annType: map[*ast.ConstDecl]ir.Type{},
	}
}

// withTopFunc registers a top-level function so a call to it folds (ResolveFunc).
func (e *collEnv) withTopFunc(fd *ast.FuncDecl) *collEnv {
	e.funcs[fd.Name] = append(e.funcs[fd.Name], fd)
	return e
}

// mapAnno / listAnno build a map<string,int> / list<int> annotation.
func mapAnno() ast.TypeExpr {
	return ast.NewNamedType("", "map", []ast.TypeExpr{intType(), intType()}, nil)
}
func listAnno() ast.TypeExpr {
	return ast.NewNamedType("", "list", []ast.TypeExpr{intType()}, nil)
}

// withCollConst registers a top-level const with the given annotation and value
// expression, so a reference to it folds through DeclExpecting (carrying the
// annotation's mapness channel) and resolves the const by name.
func (e *collEnv) withCollConst(name string, anno ast.TypeExpr, value ast.Expr) *collEnv {
	decl := ast.NewConstDecl(nil, true, name, anno, value, nil)
	e.consts[name] = decl
	e.annType[decl] = e.TypeExprType(anno)
	return e
}

func (e *collEnv) Resolve(id *ast.Identifier) *ast.ConstDecl { return e.consts[id.Name] }
func (e *collEnv) ResolveMember(*ast.MemberExpr) *ast.ConstDecl {
	return nil
}
func (e *collEnv) ResolveFunc(id *ast.Identifier) []*ast.FuncDecl    { return e.funcs[id.Name] }
func (e *collEnv) ResolveFuncMember(*ast.MemberExpr) []*ast.FuncDecl { return nil }
func (e *collEnv) ValueOf(decl *ast.ConstDecl) *ir.Constant {
	return DeclExpecting(decl, e.annType[decl], e)
}
func (e *collEnv) LookupType(name string) *ir.TypeDef {
	switch name {
	case "map":
		return e.mapDef
	case "list":
		return e.listDef
	}
	d, _ := e.reg.Lookup(name)
	return d
}
func (e *collEnv) Registry() *builtin.Registry { return e.reg }

// TypeExprDef resolves a nominal annotation to its def; a map/list App is not a
// nominal type, so it yields nil (the channel uses TypeExprType for those).
func (e *collEnv) TypeExprDef(t ast.TypeExpr) *ir.TypeDef {
	n, ok := t.(*ast.NamedType)
	if !ok || n.Namespace != "" || len(n.Args) > 0 {
		return nil
	}
	return e.LookupType(n.Name)
}

// TypeExprType resolves a map<...>/list<...> annotation to its App type — the
// channel CollKindOf reads the mapness from — and a bare nominal to its Named.
func (e *collEnv) TypeExprType(t ast.TypeExpr) ir.Type {
	n, ok := t.(*ast.NamedType)
	if !ok || n.Namespace != "" {
		return nil
	}
	if len(n.Args) > 0 {
		if def := e.LookupType(n.Name); def != nil {
			return &ir.App{Def: def}
		}
		return nil
	}
	if def := e.LookupType(n.Name); def != nil {
		return &ir.Named{Def: def}
	}
	return nil
}

// wantColl folds e and asserts its String() rendering, for a collection-valued
// expression.
func wantColl(t *testing.T, e ast.Expr, env Env, want string) {
	t.Helper()
	v := Expr(e, env)
	if v == nil {
		t.Fatalf("fold = nil, want %s", want)
	}
	if got := v.String(); got != want {
		t.Errorf("fold = %s, want %s", got, want)
	}
}

// TestEmptyMapConstSetFolds is the main case: an empty collection typed as a map
// (const m: map<string,int> = []) carries the map mapness, so a set on it is an
// upsert that folds — the upsert the old "empty reads as list" default would not
// fold.
func TestEmptyMapConstSetFolds(t *testing.T) {
	env := newCollEnv().withCollConst("m", mapAnno(), listLit())
	// The const itself folds to an empty map (rendered [:]).
	wantColl(t, id("m"), env, "[:]")
	// m.set("a", 1) upserts into the empty map: a single-entry map.
	wantColl(t, indexSet(id("m"), strLit("a"), intLit("1")), env, `["a": 1]`)
}

// TestEmptyListConstSetIsOOB covers the list side: an empty collection typed as a
// list (const l: list<int> = []) carries the list mapness, so a set at index 0 is
// an out-of-range list write that does not fold (the index_out_of_range case).
func TestEmptyListConstSetIsOOB(t *testing.T) {
	env := newCollEnv().withCollConst("l", listAnno(), listLit())
	wantColl(t, id("l"), env, "[]")
	wantNil(t, indexSet(id("l"), intLit("0"), intLit("1")), env)
}

// TestBareEmptySetDoesNotFold covers the channel-free case: a bare [] with no
// annotation is CollUnknown, so a set on it does not fold rather than guess
// between a list's out-of-range write and a map's upsert.
func TestBareEmptySetDoesNotFold(t *testing.T) {
	env := newCollEnv()
	wantNil(t, indexSet(listLit(), intLit("0"), intLit("1")), env)
	wantNil(t, indexSet(listLit(), strLit("a"), intLit("1")), env)
}

// TestBareEmptyMapnessIndependentFold covers the operations a bare [] (unknown
// mapness) still folds, since a list and a map read the same on an empty
// collection: len is 0, get is a miss, fold returns the accumulator.
func TestBareEmptyMapnessIndependentFold(t *testing.T) {
	env := newCollEnv()
	wantInt(t, methodCall(listLit(), "len"), env, 0)
	// get on an empty collection misses whichever kind it is.
	if v := Expr(indexGet(listLit(), intLit("0")), env); v == nil || v.Kind != ir.ConstError {
		t.Errorf("get([], 0) = %v, want a miss error", v)
	}
	if v := Expr(indexGet(listLit(), strLit("k")), env); v == nil || v.Kind != ir.ConstError {
		t.Errorf("get([], \"k\") = %v, want a miss error", v)
	}
	// fold over an empty collection returns the init accumulator.
	sum := funcLit([]string{"a", "k", "v"}, ret(binary(id("a"), "add", id("v"))))
	wantInt(t, methodCall(listLit(), "fold", intLit("7"), sum), env, 7)
}

// TestEmptyMapLetChannel covers the let-annotation channel: let m: map<...> = []
// folds to an empty map inside a function body, and a set on the let local
// upserts.
func TestEmptyMapLetChannel(t *testing.T) {
	env := newCollEnv()
	// fn() : map<string,int> { let m: map<string,int> = []; return m.set("a", 1) }
	letM := ast.NewLetStmt("m", mapAnno(), listLit(), nil)
	body := []ast.Stmt{letM, ret(indexSet(id("m"), strLit("a"), intLit("1")))}
	f := fn("build", nil, mapAnno(), body...)
	envF := env.withTopFunc(f)
	wantColl(t, callFn("build"), envF, `["a": 1]`)
}

// TestEmptyMapResultChannel covers the function-result-type channel: a return []
// in a map<...>-returning function folds to an empty map (and a list<...>-returning
// one to an empty list), so a caller folding the result sees the right mapness.
func TestEmptyMapResultChannel(t *testing.T) {
	env := newCollEnv()
	emptyMap := fn("emptyMap", nil, mapAnno(), ret(listLit()))
	emptyList := fn("emptyList", nil, listAnno(), ret(listLit()))
	envF := env.withTopFunc(emptyMap).withTopFunc(emptyList)
	wantColl(t, callFn("emptyMap"), envF, "[:]")
	wantColl(t, callFn("emptyList"), envF, "[]")
	// A set on the returned empty map upserts; on the returned empty list is OOB.
	wantColl(t, indexSet(callFn("emptyMap"), strLit("a"), intLit("1")), envF, `["a": 1]`)
	wantNil(t, indexSet(callFn("emptyList"), intLit("0"), intLit("1")), envF)
}

// TestMapnessPropagatesThroughSetChain covers that set preserves the receiver's
// mapness: chaining set on an empty map keeps each intermediate a map, so a
// multi-key build folds end to end.
func TestMapnessPropagatesThroughSetChain(t *testing.T) {
	env := newCollEnv().withCollConst("m", mapAnno(), listLit())
	// m.set("a", 1).set("b", 2)
	chain := indexSet(indexSet(id("m"), strLit("a"), intLit("1")), strLit("b"), intLit("2"))
	wantColl(t, chain, env, `["a": 1, "b": 2]`)
}
