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
	def := &ir.TypeDef{Name: name, Enum: &ir.EnumDef{Base: "int"}}
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
	reg   *builtin.Registry
	types map[string]*ir.TypeDef
	funcs map[string][]*ast.FuncDecl
}

func newTypeEnv(defs ...*ir.TypeDef) typeEnv {
	m := map[string]*ir.TypeDef{}
	for _, d := range defs {
		m[d.Name] = d
	}
	return typeEnv{reg: builtin.Default(), types: m, funcs: map[string][]*ast.FuncDecl{}}
}

// withFuncs adds named top-level functions the env's ResolveFunc returns.
func (e typeEnv) withFuncs(fds ...*ast.FuncDecl) typeEnv {
	for _, fd := range fds {
		e.funcs[fd.Name] = append(e.funcs[fd.Name], fd)
	}
	return e
}

func (e typeEnv) Resolve(*ast.Identifier) *ast.ConstDecl       { return nil }
func (e typeEnv) ResolveMember(*ast.MemberExpr) *ast.ConstDecl { return nil }
func (e typeEnv) ResolveFunc(id *ast.Identifier) []*ast.FuncDecl {
	return e.funcs[id.Name]
}
func (e typeEnv) ResolveFuncMember(*ast.MemberExpr) []*ast.FuncDecl { return nil }
func (e typeEnv) ValueOf(*ast.ConstDecl) *ir.Constant               { return nil }
func (e typeEnv) LookupType(name string) *ir.TypeDef {
	if d, ok := e.types[name]; ok {
		return d
	}
	d, _ := e.reg.Lookup(name)
	return d
}
func (e typeEnv) Registry() *builtin.Registry { return e.reg }

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
func intType() ast.TypeExpr   { return ast.NewNamedType("", "int", nil, nil) }

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
