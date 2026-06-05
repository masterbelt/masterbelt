package semantic

import (
	"github.com/masterbelt/masterbelt/pkg/masterbelt/lower"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/types/infer"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// constBinder lowers the leaves of a constant initializer: a value-position
// identifier — or a namespace member access (geo.Origin) — binds to its
// declaration's *Const, and a call whose callee names a top-level function
// binds to its *Function (both through the resolution queries and the
// program-wide shell tables); no other leaf form lowers in a constant. The
// file is the one the initializer sits in, scoping its resolution.
type constBinder struct {
	q    queries
	file FileID
	irOf map[*ast.ConstDecl]*ir.Const
	fnOf map[*ast.FuncDecl]*ir.Function
	// expected is the enum a bare member lowers through (the const's
	// annotation), or nil when there is none. It reaches only the initializer's
	// top leaf — a bare member is meaningful only as the const's whole value.
	expected *ir.TypeDef
}

func (b constBinder) Leaf(e ast.Expr, sub func(ast.Expr) ir.Value) ir.Value {
	switch e := e.(type) {
	case *ast.Identifier:
		if target := b.q.resolve(b.file, e); target != nil {
			return &ir.Reference{Target: b.irOf[target]}
		}
		// A bare member resolves through the expected enum (const Top: Rarity =
		// Legend). The expectation is the const's own annotation, so it only
		// reaches a bare name that is the whole initializer.
		if idx := enumIndex(b.expected, e.Name); idx >= 0 {
			return &ir.EnumMemberValue{Def: b.expected, Index: idx}
		}
	case *ast.MemberExpr:
		if target := b.q.resolveMember(b.file, e); target != nil {
			return &ir.Reference{Target: b.irOf[target]}
		}
		// A member access whose receiver names an enum type (Rarity.Common).
		if def, idx := enumMemberAccess(b.q.universe(b.file), e); idx >= 0 {
			return &ir.EnumMemberValue{Def: def, Index: idx}
		}
		// A member access whose receiver names a type and whose member names one
		// of its associated constants (int8.Max, Level.Max).
		if def, idx := assocConstAccess(b.q.universe(b.file), e); idx >= 0 {
			return &ir.AssocConstValue{Def: def, Index: idx}
		}
	case *ast.CallExpr:
		switch callee := e.Callee.(type) {
		case *ast.Identifier:
			// A call whose callee names a type is a conversion T(x) — the type
			// wins over a same-named function, exactly as in a body.
			if def, ok := b.q.universe(b.file)[callee.Name]; ok {
				var arg ir.Value
				if len(e.Arguments) > 0 {
					arg = sub(e.Arguments[0])
				}
				t := ir.Type(&ir.Named{Def: def})
				if def.Builtin {
					t = &ir.Builtin{Name: def.Name}
				}
				return &ir.Conversion{Type: t, Value: arg}
			}
			if cands := b.q.resolveFunc(b.file, callee); len(cands) > 0 {
				return funcCall(b.fnOf[pickOverload(cands, len(e.Arguments))], e.Arguments, sub)
			}
		case *ast.MemberExpr:
			if cands := b.q.resolveFuncMember(b.file, callee); len(cands) > 0 {
				return funcCall(b.fnOf[pickOverload(cands, len(e.Arguments))], e.Arguments, sub)
			}
		}
	}
	return nil
}

// pickOverload narrows an overload set to the call's target for the untyped
// value graph: the sole candidate whose arity matches, or — when the argument
// types are needed to decide — the set's first declaration as the
// representative (the set shares the name; a typed consumer re-selects).
func pickOverload(cands []*ast.FuncDecl, arity int) *ast.FuncDecl {
	var match *ast.FuncDecl
	n := 0
	for _, fd := range cands {
		if len(fd.Params) == arity {
			match = fd
			n++
		}
	}
	if n == 1 {
		return match
	}
	return cands[0]
}

func (b constBinder) EnterFunc(params []*ast.ParamDef) lower.Binder { return enterFunc(b, params) }

// enumIndex returns the index of the named member of an enum definition, or -1
// when def is not an enum or has no such member.
func enumIndex(def *ir.TypeDef, name string) int {
	if def == nil || def.Enum == nil {
		return -1
	}
	for i, m := range def.Enum.Members {
		if m.Name == name {
			return i
		}
	}
	return -1
}

// enumMemberAccess resolves a member access whose receiver names an enum type
// (Rarity.Common): the enum definition and the member's index, or (nil, -1)
// when the receiver is not an enum or the member is unknown.
func enumMemberAccess(universe map[string]*ir.TypeDef, m *ast.MemberExpr) (*ir.TypeDef, int) {
	recv, ok := m.Receiver.(*ast.Identifier)
	if !ok {
		return nil, -1
	}
	def, ok := universe[recv.Name]
	if !ok || def.Enum == nil {
		return nil, -1
	}
	return def, enumIndex(def, m.Member.Name)
}

// assocConstAccess resolves a member access whose receiver names a type and
// whose member names one of its associated constants (int8.Max, Level.Max): the
// owning definition and the constant's index, or (nil, -1) when the receiver
// names no type or has no such constant.
func assocConstAccess(universe map[string]*ir.TypeDef, m *ast.MemberExpr) (*ir.TypeDef, int) {
	recv, ok := m.Receiver.(*ast.Identifier)
	if !ok {
		return nil, -1
	}
	def, ok := universe[recv.Name]
	if !ok {
		return nil, -1
	}
	return def, assocConstIndex(def, m.Member.Name)
}

// assocConstIndex returns the index of the named associated constant of a type
// definition, or -1 when it has no such constant.
func assocConstIndex(def *ir.TypeDef, name string) int {
	if def == nil {
		return -1
	}
	for i, c := range def.Consts {
		if c.Name == name {
			return i
		}
	}
	return -1
}

// bodyFuncs is what a body binder needs to lower function calls: the file's
// own shells by name, the namespace-qualified declaration lookup (nil when no
// namespaces are in scope), and the program-wide shell table mapping a
// resolved declaration to its shell.
type bodyFuncs struct {
	local     map[string][]*ir.Function
	qualified func(namespace, name string) []*ast.FuncDecl
	shells    map[*ast.FuncDecl]*ir.Function
}

// bodyBinder lowers the leaves of a method or function body: self (methods
// only), a parameter reference, a record field access (recv.field), a type
// conversion (T(x), when the callee names a type), a call of a top-level
// function (by name or through a namespace import), or nothing. The type-name
// resolution for a conversion is the resolver's; params and tscope are the
// parameter and generic-parameter names in scope, and funcs the function
// surface callable from the body.
type bodyBinder struct {
	r      *infer.TypeResolver
	params map[string]bool
	// paramTypes maps each parameter to its resolved type, so a switch can read
	// the scrutinee's enum (the expected type its bare-member arms resolve
	// through) without consulting the type query. selfType is the receiver's
	// type (a method body), used the same way for a "switch self".
	paramTypes map[string]ir.Type
	selfType   ir.Type
	tscope     map[string]bool
	funcs      bodyFuncs
	self       bool // whether self has a value here (a method body; never a function's)
}

func (b bodyBinder) EnterFunc(params []*ast.ParamDef) lower.Binder { return enterFunc(b, params) }

// ExpectedEnum returns the enum definition a switch scrutinee's static type
// names, so its bare-member arms (Common rather than Rarity.Common) lower to
// enum-member values. It reads the type syntactically from the binder's scope
// — a parameter's resolved type, the receiver's type for self — without the
// type query, keeping value lowering independent of typing. A scrutinee whose
// enum cannot be read this way yields nil, and its bare members stay
// unresolved (the qualified form always works).
func (b bodyBinder) ExpectedEnum(scrutinee ast.Expr) *ir.TypeDef {
	switch e := scrutinee.(type) {
	case *ast.Identifier:
		if t, ok := b.paramTypes[e.Name]; ok {
			return enumDefOf(t)
		}
	case *ast.SelfExpr:
		if b.self {
			return enumDefOf(b.selfType)
		}
	}
	return nil
}

// EnumMember resolves a bare member name against an enum definition to its
// enum-member value, or nil when def has no such member — the bare-member rule
// a switch arm shares with a const initializer.
func (b bodyBinder) EnumMember(def *ir.TypeDef, name string) ir.Value {
	if idx := enumIndex(def, name); idx >= 0 {
		return &ir.EnumMemberValue{Def: def, Index: idx}
	}
	return nil
}

// enumDefOf returns the enum definition a type names, or nil when it is not a
// nominal enum.
func enumDefOf(t ir.Type) *ir.TypeDef {
	if n, ok := t.(*ir.Named); ok && n.Def != nil && n.Def.Enum != nil {
		return n.Def
	}
	return nil
}

func (b bodyBinder) Leaf(e ast.Expr, sub func(ast.Expr) ir.Value) ir.Value {
	switch e := e.(type) {
	case *ast.SelfExpr:
		if !b.self {
			return nil // a function body has no receiver
		}
		return &ir.SelfValue{}
	case *ast.NullLit:
		return &ir.NullValue{}
	case *ast.Identifier:
		if b.params[e.Name] {
			return &ir.ParamRef{Name: e.Name}
		}
		return nil
	case *ast.MemberExpr:
		// A member access whose receiver names a type — an enum member
		// (Element.Fire) or an associated constant (int8.Max) — is that type's
		// value; a parameter shadowing the type name takes the record-field
		// reading instead.
		if recv, ok := e.Receiver.(*ast.Identifier); ok && !b.params[recv.Name] {
			if def := b.r.Defs[recv.Name]; def != nil {
				if def.Enum != nil {
					if idx := enumIndex(def, e.Member.Name); idx >= 0 {
						return &ir.EnumMemberValue{Def: def, Index: idx}
					}
				}
				if idx := assocConstIndex(def, e.Member.Name); idx >= 0 {
					return &ir.AssocConstValue{Def: def, Index: idx}
				}
			}
		}
		// A member access used as a value is a record field access.
		return &ir.FieldAccess{Receiver: sub(e.Receiver), Field: e.Member.Name}
	case *ast.CallExpr:
		// A call whose callee names a type is a conversion T(x); one that
		// names a top-level function — by name, or through a namespace import
		// (geo.area(...)) — is a function call.
		switch callee := e.Callee.(type) {
		case *ast.Identifier:
			if b.params[callee.Name] {
				return nil
			}
			if t := b.r.ResolveName(callee.Name, b.tscope); t != ir.Invalid {
				var arg ir.Value
				if len(e.Arguments) > 0 {
					arg = sub(e.Arguments[0])
				}
				return &ir.Conversion{Type: t, Value: arg}
			}
			if cands := b.funcs.local[callee.Name]; len(cands) > 0 {
				return funcCall(pickShellOverload(cands, len(e.Arguments)), e.Arguments, sub)
			}
		case *ast.MemberExpr:
			recv, ok := callee.Receiver.(*ast.Identifier)
			if !ok || b.params[recv.Name] || b.funcs.qualified == nil {
				return nil
			}
			if cands := b.funcs.qualified(recv.Name, callee.Member.Name); len(cands) > 0 {
				return funcCall(b.funcs.shells[pickOverload(cands, len(e.Arguments))], e.Arguments, sub)
			}
		}
		return nil
	default:
		return nil
	}
}

// pickShellOverload is pickOverload over function shells: the arity reads off
// the declaration syntax, since a shell's signature may not be resolved yet
// when a method body lowers.
func pickShellOverload(cands []*ir.Function, arity int) *ir.Function {
	var match *ir.Function
	n := 0
	for _, f := range cands {
		if f.Syntax != nil && len(f.Syntax.Params) == arity {
			match = f
			n++
		}
	}
	if n == 1 {
		return match
	}
	return cands[0]
}

// funcCall lowers a resolved function call: the target and its lowered
// arguments.
func funcCall(target *ir.Function, args []ast.Expr, sub func(ast.Expr) ir.Value) ir.Value {
	out := make([]ir.Value, len(args))
	for i, a := range args {
		out[i] = sub(a)
	}
	return &ir.FuncCall{Target: target, Args: out}
}

// funcBinder lowers the body of a function literal: its own parameters lower to
// ir.ParamRef, and any other leaf is delegated to the enclosing binder — so a
// reference to an outer constant, a conversion, or self still lowers as it would
// outside the lambda. Nesting a literal wraps another funcBinder around this one,
// chaining the parameter scopes.
type funcBinder struct {
	outer  lower.Binder
	params map[string]bool
}

// enterFunc builds the binder for a function literal's body from the enclosing
// binder and the literal's parameters.
func enterFunc(outer lower.Binder, params []*ast.ParamDef) funcBinder {
	m := make(map[string]bool, len(params))
	for _, p := range params {
		m[p.Name] = true
	}
	return funcBinder{outer: outer, params: m}
}

func (b funcBinder) Leaf(e ast.Expr, sub func(ast.Expr) ir.Value) ir.Value {
	if id, ok := e.(*ast.Identifier); ok && b.params[id.Name] {
		return &ir.ParamRef{Name: id.Name}
	}
	if c, ok := e.(*ast.CallExpr); ok {
		if id, ok := c.Callee.(*ast.Identifier); ok && b.params[id.Name] {
			return nil // a call of a parameter: a literal's parameter shadows a function
		}
	}
	return b.outer.Leaf(e, sub)
}

func (b funcBinder) EnterFunc(params []*ast.ParamDef) lower.Binder { return enterFunc(b, params) }
