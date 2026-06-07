package eval

import (
	"math/big"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// --- enum + method builders --------------------------------------------------

// boolType is the result/param annotation a method writes for a bool; selfType
// is the receiver-typed annotation (param other: self).
func boolType() ast.TypeExpr { return ast.NewNamedType("", "bool", nil, nil) }
func selfType() ast.TypeExpr { return ast.NewNamedType("", "self", nil, nil) }

func ret(e ast.Expr) ast.Stmt { return ast.NewReturnStmt(e, nil) }

func param(name string, t ast.TypeExpr) *ast.ParamDef { return ast.NewParamDef(name, t, nil) }

// method builds a non-extern, pure method declaration with the given body.
func method(name string, params []*ast.ParamDef, result ast.TypeExpr, body ...ast.Stmt) *ast.MethodDecl {
	return ast.NewMethodDecl(nil, true, false, nil, name, nil, params, result, body, nil)
}

// methodIR wraps an AST method declaration as the ir.Method the type def holds,
// the way resolveMethod does (Syntax links the body the folder reads).
func methodIR(md *ast.MethodDecl) *ir.Method {
	return &ir.Method{Name: md.Name, Public: md.Public, Extern: md.Extern, Effects: md.Effects, Syntax: md}
}

// externMethodIR is a body-less native method (the six enum comparisons): no
// Syntax, so the folder leaves it to the intrinsic path.
func externMethodIR(name string) *ir.Method {
	return &ir.Method{Name: name, Public: true, Extern: true}
}

// enumDef builds an int-based enum type def with the given member names (values
// 0..n-1) and the six native (body-less) comparison methods. A test appends its
// own user methods to def.Methods after the call.
func enumDef(name string, members []string) *ir.TypeDef {
	def := &ir.TypeDef{Name: name, Enum: &ir.EnumDef{Base: "nint"}}
	for i, m := range members {
		def.Enum.Members = append(def.Enum.Members, ir.EnumMember{Name: m, Value: ir.IntConstant(big.NewInt(int64(i)))})
	}
	for _, n := range []string{"eql", "neq", "lt", "lteq", "gt", "gteq"} {
		def.Methods = append(def.Methods, externMethodIR(n))
	}
	return def
}

// memberCall builds recv.name(args...).
func memberCall(recv ast.Expr, name string, args ...ast.Expr) *ast.CallExpr {
	m := ast.NewMemberExpr(recv, ast.NewIdentifier(name, nil), nil)
	return ast.NewCallExpr(m, args, nil)
}

// memberExpr builds the qualified access Type.Member, which folds to the enum
// member's value (the receiver of a method call in these tests).
func memberExpr(typeName, member string) *ast.MemberExpr {
	return ast.NewMemberExpr(ast.NewIdentifier(typeName, nil), ast.NewIdentifier(member, nil), nil)
}

// typeEnv resolves the given enum types and top-level functions by name, so a
// Type.Member access, a LookupType inside a body, and a function call from a
// method body all resolve. Everything else resolves to nothing — these tests
// reference only self, literals, members, methods, and named functions.
type typeEnv struct {
	reg     *builtin.Registry
	types   map[string]*ir.TypeDef
	funcs   map[string][]*ast.FuncDecl
	consts  map[string]*ast.ConstDecl
	values  map[*ast.ConstDecl]*ir.Constant
	resolve map[string]*ast.ConstDecl // identifier name -> its declaration
}

func newTypeEnv(defs ...*ir.TypeDef) typeEnv {
	m := map[string]*ir.TypeDef{}
	for _, d := range defs {
		m[d.Name] = d
	}
	return typeEnv{
		reg: builtin.Default(), types: m,
		funcs:   map[string][]*ast.FuncDecl{},
		consts:  map[string]*ast.ConstDecl{},
		values:  map[*ast.ConstDecl]*ir.Constant{},
		resolve: map[string]*ast.ConstDecl{},
	}
}

// withFuncs adds named top-level functions the env's ResolveFunc returns.
func (e typeEnv) withFuncs(fds ...*ast.FuncDecl) typeEnv {
	for _, fd := range fds {
		e.funcs[fd.Name] = append(e.funcs[fd.Name], fd)
	}
	return e
}

// withConst registers a top-level const of the given annotation type and value,
// so a reference to it folds (ValueOf) and resolves its static def from the
// annotation (the channel-2 receiver case). typeName is the annotation's type.
func (e typeEnv) withConst(name, typeName string, value *ir.Constant) typeEnv {
	decl := ast.NewConstDecl(nil, true, name, ast.NewNamedType("", typeName, nil, nil), nil, nil)
	e.consts[name] = decl
	e.resolve[name] = decl
	e.values[decl] = value
	return e
}

func (e typeEnv) Resolve(id *ast.Identifier) *ast.ConstDecl    { return e.resolve[id.Name] }
func (e typeEnv) ResolveMember(*ast.MemberExpr) *ast.ConstDecl { return nil }
func (e typeEnv) ResolveFunc(id *ast.Identifier) []*ast.FuncDecl {
	return e.funcs[id.Name]
}
func (e typeEnv) ResolveFuncMember(*ast.MemberExpr) []*ast.FuncDecl { return nil }
func (e typeEnv) ValueOf(decl *ast.ConstDecl) *ir.Constant          { return e.values[decl] }
func (e typeEnv) LookupType(name string) *ir.TypeDef {
	if d, ok := e.types[name]; ok {
		return d
	}
	d, _ := e.reg.Lookup(name)
	return d
}
func (e typeEnv) Registry() *builtin.Registry { return e.reg }

// TypeExprDef resolves a written annotation to its def by name, the syntactic
// type channel a nominal receiver folds through. It satisfies eval.ReceiverTyper.
func (e typeEnv) TypeExprDef(t ast.TypeExpr) *ir.TypeDef {
	n, ok := t.(*ast.NamedType)
	if !ok || n.Namespace != "" {
		return nil
	}
	return e.LookupType(n.Name)
}

// TypeExprType resolves a written annotation to its type: a nominal name to its
// Named type, otherwise nil (these tests use only nominal annotations). It
// satisfies eval.ReceiverTyper.
func (e typeEnv) TypeExprType(t ast.TypeExpr) ir.Type {
	if def := e.TypeExprDef(t); def != nil {
		return &ir.Named{Def: def}
	}
	return nil
}

// fn builds a non-extern, pure top-level function declaration.
func fn(name string, params []*ast.ParamDef, result ast.TypeExpr, body ...ast.Stmt) *ast.FuncDecl {
	return ast.NewFuncDecl(nil, true, false, nil, name, nil, params, result, body, nil)
}

// id builds an identifier expression.
func id(name string) ast.Expr { return ast.NewIdentifier(name, nil) }

// callFn builds a top-level function call name(args...).
func callFn(name string, args ...ast.Expr) *ast.CallExpr {
	return ast.NewCallExpr(ast.NewIdentifier(name, nil), args, nil)
}

// --- tests -------------------------------------------------------------------

// TestEnumMethodFold covers the central case: a user-defined enum method with a
// body folds when called on a constant receiver. isFire returns self ==
// Element.Fire, which itself folds through the enum comparison intrinsic — so a
// method whose body calls a native method folds end to end.
func TestEnumMethodFold(t *testing.T) {
	def := enumDef("Element", []string{"Fire", "Water", "Wind"})
	isFire := method("isFire", nil, boolType(),
		ret(memberCall(selfExpr(), "eql", memberExpr("Element", "Fire"))))
	def.Methods = append(def.Methods, methodIR(isFire))
	env := newTypeEnv(def)

	cases := []struct {
		member string
		want   bool
	}{
		{"Fire", true},
		{"Water", false},
		{"Wind", false},
	}
	for _, tc := range cases {
		t.Run(tc.member, func(t *testing.T) {
			v := Expr(memberCall(memberExpr("Element", tc.member), "isFire"), env)
			if v == nil || v.Kind != ir.ConstBool {
				t.Fatalf("fold = %v, want a bool constant", v)
			}
			if v.Bool != tc.want {
				t.Errorf("%s.isFire() = %t, want %t", tc.member, v.Bool, tc.want)
			}
		})
	}
}

func boolLit(b bool) ast.Expr { return ast.NewBoolLit(b, nil) }
func intType() ast.TypeExpr   { return ast.NewNamedType("", "nint", nil, nil) }

// wantBool folds e and asserts it is the given bool.
func wantBool(t *testing.T, e ast.Expr, env Env, want bool) {
	t.Helper()
	v := Expr(e, env)
	if v == nil || v.Kind != ir.ConstBool {
		t.Fatalf("fold = %v, want a bool constant", v)
	}
	if v.Bool != want {
		t.Errorf("fold = %t, want %t", v.Bool, want)
	}
}

// wantNil folds e and asserts it does not fold.
func wantNil(t *testing.T, e ast.Expr, env Env) {
	t.Helper()
	if v := Expr(e, env); v != nil {
		t.Errorf("fold = %v, want nil (does not fold)", v)
	}
}

// TestMethodWithParam covers a method that takes an argument: equals(other)
// returns self == other, so the argument folds in the caller's context and
// binds the parameter.
func TestMethodWithParam(t *testing.T) {
	def := enumDef("Color", []string{"Red", "Green", "Blue"})
	equals := method("equals", []*ast.ParamDef{param("other", selfType())}, boolType(),
		ret(memberCall(selfExpr(), "eql", id("other"))))
	def.Methods = append(def.Methods, methodIR(equals))
	env := newTypeEnv(def)

	wantBool(t, memberCall(memberExpr("Color", "Red"), "equals", memberExpr("Color", "Red")), env, true)
	wantBool(t, memberCall(memberExpr("Color", "Red"), "equals", memberExpr("Color", "Blue")), env, false)
}

// TestMethodLetBody covers a let-bearing method body: a local binds an
// intermediate value the return reads — the body runs through the same statement
// walker a function body does.
func TestMethodLetBody(t *testing.T) {
	def := enumDef("Element", []string{"Fire", "Water"})
	// isFire(): let hit = self == Element.Fire; return hit
	isFire := method("isFire", nil, boolType(),
		ast.NewLetStmt("hit", nil, memberCall(selfExpr(), "eql", memberExpr("Element", "Fire")), nil),
		ret(id("hit")))
	def.Methods = append(def.Methods, methodIR(isFire))
	env := newTypeEnv(def)

	wantBool(t, memberCall(memberExpr("Element", "Fire"), "isFire"), env, true)
	wantBool(t, memberCall(memberExpr("Element", "Water"), "isFire"), env, false)
}

// TestMethodIfBody covers an if-bearing method body: the taken branch returns,
// exactly as in a function body.
func TestMethodIfBody(t *testing.T) {
	def := enumDef("Element", []string{"Fire", "Water"})
	// classify(): if self == Element.Fire { return true } else { return false }
	classify := method("classify", nil, boolType(),
		ast.NewIfStmt(
			memberCall(selfExpr(), "eql", memberExpr("Element", "Fire")),
			[]ast.Stmt{ret(boolLit(true))},
			nil,
			[]ast.Stmt{ret(boolLit(false))},
			nil))
	def.Methods = append(def.Methods, methodIR(classify))
	env := newTypeEnv(def)

	wantBool(t, memberCall(memberExpr("Element", "Fire"), "classify"), env, true)
	wantBool(t, memberCall(memberExpr("Element", "Water"), "classify"), env, false)
}

// TestMethodSwitchBody covers a switch-bearing method body: the matching arm's
// value is returned, the wildcard last.
func TestMethodSwitchBody(t *testing.T) {
	def := enumDef("Element", []string{"Fire", "Water", "Wind"})
	// name(): switch self { Element.Fire: return true; _: return false }
	nameM := method("isFire", nil, boolType(),
		ast.NewSwitchStmt(selfExpr(),
			[]*ast.SwitchArm{ast.NewSwitchArm([]ast.Expr{memberExpr("Element", "Fire")}, []ast.Stmt{ret(boolLit(true))}, nil)},
			[]ast.Stmt{ret(boolLit(false))},
			nil, nil))
	def.Methods = append(def.Methods, methodIR(nameM))
	env := newTypeEnv(def)

	wantBool(t, memberCall(memberExpr("Element", "Fire"), "isFire"), env, true)
	wantBool(t, memberCall(memberExpr("Element", "Wind"), "isFire"), env, false)
}

// TestMethodCallsMethod covers a method that calls another user method on self:
// notFire() returns !isFire(), folding through the inner method body.
func TestMethodCallsMethod(t *testing.T) {
	def := enumDef("Element", []string{"Fire", "Water"})
	isFire := method("isFire", nil, boolType(),
		ret(memberCall(selfExpr(), "eql", memberExpr("Element", "Fire"))))
	// notFire(): return self.isFire().not()
	notFire := method("notFire", nil, boolType(),
		ret(memberCall(memberCall(selfExpr(), "isFire"), "not")))
	def.Methods = append(def.Methods, methodIR(isFire), methodIR(notFire))
	env := newTypeEnv(def)

	wantBool(t, memberCall(memberExpr("Element", "Fire"), "notFire"), env, false)
	wantBool(t, memberCall(memberExpr("Element", "Water"), "notFire"), env, true)
}

// TestMethodCallsFunc covers a method body that calls a top-level function: the
// method passes a value out to a pure function and folds its result.
func TestMethodCallsFunc(t *testing.T) {
	def := enumDef("Element", []string{"Fire", "Water"})
	// negate(b: bool): return b.not()  — a top-level function
	negate := fn("negate", []*ast.ParamDef{param("b", boolType())}, boolType(),
		ret(memberCall(id("b"), "not")))
	// isWater(): return negate(self.isFire())
	isFire := method("isFire", nil, boolType(),
		ret(memberCall(selfExpr(), "eql", memberExpr("Element", "Fire"))))
	isWater := method("isWater", nil, boolType(),
		ret(callFn("negate", memberCall(selfExpr(), "isFire"))))
	def.Methods = append(def.Methods, methodIR(isFire), methodIR(isWater))
	env := newTypeEnv(def).withFuncs(negate)

	wantBool(t, memberCall(memberExpr("Element", "Fire"), "isWater"), env, false)
	wantBool(t, memberCall(memberExpr("Element", "Water"), "isWater"), env, true)
}

// TestMethodOverloadAmbiguous covers the value-blind overload selection: two
// same-name methods whose single parameter accepts the same value kind (both
// take a self-typed argument) are indistinguishable to the folder, so the call
// does not fold — a wrong overload's body is never applied.
func TestMethodOverloadAmbiguous(t *testing.T) {
	def := enumDef("Color", []string{"Red", "Green"})
	// two with(other: self) overloads — same arity, same arg kind: undecidable.
	a := method("with", []*ast.ParamDef{param("other", selfType())}, boolType(), ret(boolLit(true)))
	b := method("with", []*ast.ParamDef{param("other", selfType())}, boolType(), ret(boolLit(false)))
	def.Methods = append(def.Methods, methodIR(a), methodIR(b))
	env := newTypeEnv(def)

	wantNil(t, memberCall(memberExpr("Color", "Red"), "with", memberExpr("Color", "Green")), env)
}

// TestMethodOverloadByKind covers a foldable overload set: two same-name methods
// whose parameter kinds differ (a bool vs an int) select by the argument's value
// kind, the way a function overload does.
func TestMethodOverloadByKind(t *testing.T) {
	def := enumDef("Color", []string{"Red", "Green"})
	onBool := method("tag", []*ast.ParamDef{param("x", boolType())}, boolType(), ret(boolLit(true)))
	onInt := method("tag", []*ast.ParamDef{param("x", intType())}, boolType(), ret(boolLit(false)))
	def.Methods = append(def.Methods, methodIR(onBool), methodIR(onInt))
	env := newTypeEnv(def)

	wantBool(t, memberCall(memberExpr("Color", "Red"), "tag", boolLit(true)), env, true)
	wantBool(t, memberCall(memberExpr("Color", "Red"), "tag", intLit("3")), env, false)
}

// TestMethodRecursionDirect covers the depth guard on a directly recursive
// method: loop() calls itself forever, so the fold bottoms out at the depth cap
// and yields nil — no hang, no stack overflow.
func TestMethodRecursionDirect(t *testing.T) {
	def := enumDef("Element", []string{"Fire"})
	// loop(): return self.loop()
	loop := method("loop", nil, boolType(), ret(memberCall(selfExpr(), "loop")))
	def.Methods = append(def.Methods, methodIR(loop))
	env := newTypeEnv(def)

	wantNil(t, memberCall(memberExpr("Element", "Fire"), "loop"), env)
}

// TestMethodRecursionMutual covers the depth guard on mutual recursion between a
// method and a function: ping() calls pong(self), which calls self.ping(), and
// so on. The shared depth counter caps the cycle, so it folds to nil rather than
// looping forever.
func TestMethodRecursionMutual(t *testing.T) {
	def := enumDef("Element", []string{"Fire"})
	// ping(): return pong(self)   pong(e: Element): return e.ping()
	ping := method("ping", nil, boolType(), ret(callFn("pong", selfExpr())))
	def.Methods = append(def.Methods, methodIR(ping))
	pong := fn("pong", []*ast.ParamDef{param("e", ast.NewNamedType("", "Element", nil, nil))}, boolType(),
		ret(memberCall(id("e"), "ping")))
	env := newTypeEnv(def).withFuncs(pong)

	wantNil(t, memberCall(memberExpr("Element", "Fire"), "ping"), env)
}

// TestExternMethodUnchanged covers the resolution order: a body-less extern
// method (the six enum comparisons) still folds through its intrinsic, not the
// user-method path — the new path only handles methods that have a body.
func TestExternMethodUnchanged(t *testing.T) {
	def := enumDef("Color", []string{"Red", "Green"}) // no user methods, just the six
	env := newTypeEnv(def)

	// Red == Red folds true through the enum-comparison intrinsic.
	wantBool(t, memberCall(memberExpr("Color", "Red"), "eql", memberExpr("Color", "Red")), env, true)
	wantBool(t, memberCall(memberExpr("Color", "Red"), "neq", memberExpr("Color", "Green")), env, true)
	wantBool(t, memberCall(memberExpr("Color", "Red"), "lt", memberExpr("Color", "Green")), env, true)
}

// TestUnknownMethodDoesNotFold covers a name that is neither a user method nor an
// intrinsic on the receiver: it does not fold (nil), the conservative failure.
func TestUnknownMethodDoesNotFold(t *testing.T) {
	def := enumDef("Color", []string{"Red"})
	env := newTypeEnv(def)
	wantNil(t, memberCall(memberExpr("Color", "Red"), "bogus"), env)
}

// TestMethodAritySkipped covers an arity mismatch: a body-bearing method of the
// right name but the wrong parameter count is not selected, so the call does not
// fold (no other overload fits).
func TestMethodAritySkipped(t *testing.T) {
	def := enumDef("Color", []string{"Red"})
	// f(x: bool) is the only "f": calling f() (zero args) fits nothing.
	f := method("f", []*ast.ParamDef{param("x", boolType())}, boolType(), ret(boolLit(true)))
	def.Methods = append(def.Methods, methodIR(f))
	env := newTypeEnv(def)
	wantNil(t, memberCall(memberExpr("Color", "Red"), "f"), env)
}

// --- nominal type (over a primitive) method folding --------------------------

// nominalDef builds a nominal type def over a primitive (type Name = prim) with
// the given methods.
func nominalDef(name, prim string, methods ...*ir.Method) *ir.TypeDef {
	def := &ir.TypeDef{Name: name, Body: &ir.Builtin{Name: prim}}
	def.Methods = append(def.Methods, methods...)
	return def
}

func intConst(n int64) *ir.Constant { return ir.IntConstant(big.NewInt(n)) }

// wantInt folds e and asserts it is the given integer.
func wantInt(t *testing.T, e ast.Expr, env Env, want int64) {
	t.Helper()
	v := Expr(e, env)
	if v == nil || v.Kind != ir.ConstInt {
		t.Fatalf("fold = %v, want an nint constant", v)
	}
	if v.Int.Int64() != want {
		t.Errorf("fold = %s, want %d", v.Int, want)
	}
}

// levelDef is the running example: type Level = int8 with increment(): self
// returning self + 1.
func levelDef() *ir.TypeDef {
	inc := method("increment", nil, selfType(), ret(memberCall(selfExpr(), "add", intLit("1"))))
	return nominalDef("Level", "sbyte", methodIR(inc))
}

// TestNominalConversionFold covers channel 1, a conversion call: Level(5)
// names its type directly, so increment() folds on it.
func TestNominalConversionFold(t *testing.T) {
	def := levelDef()
	env := newTypeEnv(def)
	// Level(5).increment() — the conversion's receiver type is Level.
	conv := ast.NewCallExpr(id("Level"), []ast.Expr{intLit("5")}, nil)
	wantInt(t, memberCall(conv, "increment"), env, 6)
}

// TestConversionRangeCheck pins the eval-side soundness fix: a sized-integer
// conversion whose argument is out of the target's range does not fold (nil), so
// no out-of-range constant is ever built — the value a tagged-union match would
// otherwise dispatch on wrongly. An in-range conversion still folds to its value.
// It covers the builtin path (short) and the nominal-over-integer path (Level =
// sbyte, range -128..127).
func TestConversionRangeCheck(t *testing.T) {
	env := newTypeEnv(levelDef())
	conv := func(typ, lit string) ast.Expr {
		return ast.NewCallExpr(id(typ), []ast.Expr{intLit(lit)}, nil)
	}
	// Out of range: no representable value, so the conversion does not fold.
	wantNil(t, conv("short", "70000"), env) // short max is 32767
	wantNil(t, conv("uint", "-1"), env)     // unsigned lower bound is 0
	wantNil(t, conv("Level", "70000"), env) // sbyte max is 127
	// In range: the conversion is the identity on the value.
	wantInt(t, conv("short", "20"), env, 20)
	wantInt(t, conv("Level", "5"), env, 5)
	wantInt(t, conv("sbyte", "127"), env, 127) // the inclusive boundary folds
}

// TestUnionMemberAdmitsFold pins the eval-side member-aware range refusal: a value
// flowing into a sized union member it cannot represent (an out-of-range integer)
// does not fold (nil), so a wrong value is never tagged and a later match never
// dispatches on it. An admitted value folds tagged with the member. It is the
// union twin of the conversion range check.
func TestUnionMemberAdmitsFold(t *testing.T) {
	env := newTypeEnv()
	// sbyte | error: an integer literal selects the sbyte member by kind backing.
	sbyteUnion := &ir.Union{Members: []ir.Type{&ir.Builtin{Name: "sbyte"}, &ir.Builtin{Name: "error"}}}

	// Out of sbyte range: no representable member value, so the fold is refused.
	if v := ExprExpecting(intLit("200"), sbyteUnion, env); v != nil {
		t.Errorf("200 into sbyte | error folded to %v, want nil (out of range)", v)
	}
	// An admitted value folds tagged with its member.
	if v := ExprExpecting(intLit("100"), sbyteUnion, env); v == nil || v.UnionTag == nil {
		t.Errorf("100 into sbyte | error = %v, want a tagged value", v)
	}
}

// TestMemberForSelection pins the MemberFor channel the semantic layer resolves
// its member-aware check through: a union returns the selected member, a non-union
// returns the type itself, and an unfoldable value falls back to the type.
func TestMemberForSelection(t *testing.T) {
	port := nominalDef("Port", "sbyte")
	port.Where = memberCall(selfExpr(), "gt", intLit("0"))
	env := newTypeEnv(port)
	sbyteUnion := &ir.Union{Members: []ir.Type{&ir.Builtin{Name: "sbyte"}, &ir.Builtin{Name: "error"}}}

	// A union resolves to the selected member.
	if m := MemberFor(intLit("5"), sbyteUnion, env); m == nil || m.String() != "sbyte" {
		t.Errorf("MemberFor(5, sbyte | error) = %v, want sbyte", m)
	}
	// A non-union returns the type itself.
	short := &ir.Builtin{Name: "short"}
	if m := MemberFor(intLit("5"), short, env); m != short {
		t.Errorf("MemberFor(5, short) = %v, want short itself", m)
	}
}

// TestNominalConstRefFold covers channel 2, a top-level const reference:
// const base: Level = 5; base.increment() reads base's annotation for the def.
func TestNominalConstRefFold(t *testing.T) {
	def := levelDef()
	env := newTypeEnv(def).withConst("base", "Level", intConst(5))
	wantInt(t, memberCall(id("base"), "increment"), env, 6)
}

// TestNominalSelfFold covers channel 3, self inside a method body: a helper
// method calls another method on self, the receiver def being the owning type.
func TestNominalSelfFold(t *testing.T) {
	inc := method("increment", nil, selfType(), ret(memberCall(selfExpr(), "add", intLit("1"))))
	// twice(): return self.increment().increment()
	twice := method("twice", nil, selfType(),
		ret(memberCall(memberCall(selfExpr(), "increment"), "increment")))
	def := nominalDef("Level", "sbyte", methodIR(inc), methodIR(twice))
	env := newTypeEnv(def).withConst("base", "Level", intConst(5))
	wantInt(t, memberCall(id("base"), "twice"), env, 7)
}

// TestNominalParamFold covers channel 4 via a parameter: a top-level function
// takes a Level parameter and calls increment() on it inside its body.
func TestNominalParamFold(t *testing.T) {
	def := levelDef()
	// bump(l: Level): Level { return l.increment() }
	bump := fn("bump", []*ast.ParamDef{param("l", ast.NewNamedType("", "Level", nil, nil))},
		ast.NewNamedType("", "Level", nil, nil),
		ret(memberCall(id("l"), "increment")))
	env := newTypeEnv(def).withFuncs(bump).withConst("base", "Level", intConst(5))
	wantInt(t, callFn("bump", id("base")), env, 6)
}

// TestNominalLetFold covers channel 4 via a let: a method body binds a let of an
// annotated nominal type and calls a method on it.
func TestNominalLetFold(t *testing.T) {
	inc := method("increment", nil, selfType(), ret(memberCall(selfExpr(), "add", intLit("1"))))
	// step(): let next: Level = self.increment(); return next.increment()
	step := method("step", nil, selfType(),
		ast.NewLetStmt("next", ast.NewNamedType("", "Level", nil, nil), memberCall(selfExpr(), "increment"), nil),
		ret(memberCall(id("next"), "increment")))
	def := nominalDef("Level", "sbyte", methodIR(inc), methodIR(step))
	env := newTypeEnv(def).withConst("base", "Level", intConst(5))
	wantInt(t, memberCall(id("base"), "step"), env, 7)
}

// TestNominalChainFold covers channel 5, a call result chain:
// getLevel().increment().increment() resolves each result type from the callee's
// declared result annotation (a self result keeps the type).
func TestNominalChainFold(t *testing.T) {
	def := levelDef()
	// getLevel(): Level { return Level(5) }  — result type Level
	getLevel := fn("getLevel", nil, ast.NewNamedType("", "Level", nil, nil),
		ret(ast.NewCallExpr(id("Level"), []ast.Expr{intLit("5")}, nil)))
	env := newTypeEnv(def).withFuncs(getLevel)
	chain := memberCall(memberCall(callFn("getLevel"), "increment"), "increment")
	wantInt(t, chain, env, 7)
}

// TestNominalOverload covers overload selection on a nominal type (E-6): merge
// has a self-typed and a bool-typed overload; the argument kind picks the
// right one, with the receiver kind deciding the self-typed parameter.
func TestNominalOverload(t *testing.T) {
	// merge(points: self): self { return self + points }
	mergeInt := method("merge", []*ast.ParamDef{param("points", selfType())}, selfType(),
		ret(memberCall(selfExpr(), "add", id("points"))))
	// merge(active: bool): bool { return active && self > 0 }
	mergeBool := method("merge", []*ast.ParamDef{param("active", boolType())}, boolType(),
		ret(memberCall(id("active"), "anan", memberCall(selfExpr(), "gt", intLit("0")))))
	def := nominalDef("Score", "int", methodIR(mergeInt), methodIR(mergeBool))
	env := newTypeEnv(def).withConst("base", "Score", intConst(100))

	wantInt(t, memberCall(id("base"), "merge", intLit("50")), env, 150)
	wantBool(t, memberCall(id("base"), "merge", boolLit(true)), env, true)
}

// TestNominalWhereTypeMethod covers a method on a refinement (where-clause) type:
// the where-clause does not change method folding — the def's underlying
// primitive still backs the value, so the method folds.
func TestNominalWhereTypeMethod(t *testing.T) {
	inc := method("increment", nil, selfType(), ret(memberCall(selfExpr(), "add", intLit("1"))))
	def := nominalDef("Percent", "sbyte", methodIR(inc))
	def.Where = memberCall(selfExpr(), "gteq", intLit("0")) // self >= 0 (a usable predicate)
	env := newTypeEnv(def).withConst("p", "Percent", intConst(50))
	wantInt(t, memberCall(id("p"), "increment"), env, 51)
}

// TestNominalKindMismatchSafe covers the value/def integration guard: a def read
// from an annotation whose underlying primitive does not back the receiver's
// value kind is rejected, so a method is never applied to a wrong-kind value.
// (This only arises in a malformed program; a well-typed one never mismatches.)
func TestNominalKindMismatchSafe(t *testing.T) {
	// A string-based def with a method, but the const's value is an int.
	greet := method("greet", nil, boolType(), ret(boolLit(true)))
	def := nominalDef("Name", "string", methodIR(greet))
	env := newTypeEnv(def).withConst("x", "Name", intConst(5)) // value kind is int, def is string-based
	wantNil(t, memberCall(id("x"), "greet"), env)
}

// TestNominalNoTyperFallsBack covers an Env without ReceiverTyper: a nominal
// receiver cannot be resolved syntactically, so the call does not fold (the
// enum-only behavior). The stubEnv (from eval_test.go) implements no typer.
func TestNominalNoTyperFallsBack(t *testing.T) {
	// stubEnv resolves nothing and has no TypeExprDef; an int receiver with a
	// "bogus" method folds to nil regardless.
	env := stubEnv{reg: builtin.Default()}
	wantNil(t, memberCall(intLit("5"), "increment"), env)
}

// TestNominalUnresolvedReceiverSafe covers a receiver whose static type cannot be
// read through any channel (a plain int literal with no annotation): the method
// does not fold, the conservative failure.
func TestNominalUnresolvedReceiverSafe(t *testing.T) {
	def := levelDef()
	env := newTypeEnv(def)
	// 5.increment() — a bare literal names no type; increment does not fold.
	wantNil(t, memberCall(intLit("5"), "increment"), env)
}

// TestNominalRecursionGuarded covers the depth guard on a self-recursive nominal
// method: loop() calls self.loop() forever (self resolves through the owning
// def), so the fold bottoms out at the cap and yields nil — no hang.
func TestNominalRecursionGuarded(t *testing.T) {
	loop := method("loop", nil, ast.NewNamedType("", "sbyte", nil, nil), ret(memberCall(selfExpr(), "loop")))
	def := nominalDef("Level", "sbyte", methodIR(loop))
	env := newTypeEnv(def).withConst("base", "Level", intConst(1))
	wantNil(t, memberCall(id("base"), "loop"), env)
}

// TestNominalLetBlockScoping covers that a let's static def is block-scoped: a
// let of a nominal type inside a taken if-branch does not leak its def to a
// later same-named binding in the outer scope. The method folds correctly
// because each binding reads its own annotation.
func TestNominalLetBlockScoping(t *testing.T) {
	inc := method("increment", nil, selfType(), ret(memberCall(selfExpr(), "add", intLit("1"))))
	// shadow(): {
	//   let x: Level = self.increment()   // x is a Level (outer)
	//   if true { let x: Other = ... }     // inner x shadows, a different def
	//   return x.increment()               // reads the outer Level def, folds
	// }
	other := nominalDef("Other", "sbyte") // a def with no increment method
	shadow := method("shadow", nil, selfType(),
		ast.NewLetStmt("x", ast.NewNamedType("", "Level", nil, nil), memberCall(selfExpr(), "increment"), nil),
		ast.NewIfStmt(boolLit(true),
			[]ast.Stmt{ast.NewLetStmt("x", ast.NewNamedType("", "Other", nil, nil), intLit("99"), nil)},
			nil, nil, nil),
		ret(memberCall(id("x"), "increment")))
	def := nominalDef("Level", "sbyte", methodIR(inc), methodIR(shadow))
	env := newTypeEnv(def, other).withConst("base", "Level", intConst(5))
	// self=5 -> x=6 (outer Level) -> the inner block shadows x then restores it
	// -> x.increment() reads the outer Level def -> 7.
	wantInt(t, memberCall(id("base"), "shadow"), env, 7)
}
