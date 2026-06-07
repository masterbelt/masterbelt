// This file holds the concrete typing scopes the inference and checking walks
// run in: funcScope for a function literal's body, constScope for a constant
// initializer, and BodyScope for a method or function body. Each implements the
// scope interface — supplying the registry, the type universe, and the rules
// for the context-specific leaf forms — so the one walk (exprType / check) sees
// the right meaning for self, a name, a conversion, and a function call in
// whatever context it is invoked.
package infer

import (
	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// funcScope types a function literal's body: the literal's own parameters
// resolve to their (declared or pushed-down) types, and every other leaf is
// delegated to the enclosing scope — mirroring how funcBinder chains the
// lowering scopes for the same body. A nested literal just wraps another
// funcScope around this one.
//
// A lambda block body may also introduce let-bound block-locals; locals carries
// each one in scope to its settled type, read before a parameter (a let shadows
// a same-named parameter) and the body counterpart of params. It is nil in a
// body with no lets — the common arrow/return form — and grows as the body walk
// descends a block's lets, exactly as BodyScope.Locals does for a method body.
type funcScope struct {
	outer  scope
	params map[string]ir.Type
	locals map[string]ir.Type
}

// withLocal returns a copy of the scope with name bound to typ on top of its
// locals — used as a body walk descends past a let, so the statements after it
// (within the same block) resolve the new local. The map is copied so the
// binding never escapes back to an enclosing block.
func (s funcScope) withLocal(name string, typ ir.Type) funcScope {
	locals := make(map[string]ir.Type, len(s.locals)+1)
	for k, v := range s.locals {
		locals[k] = v
	}
	locals[name] = typ
	s.locals = locals
	return s
}

func (s funcScope) registry() *builtin.Registry { return s.outer.registry() }

func (s funcScope) universe() map[string]*ir.TypeDef { return s.outer.universe() }

func (s funcScope) qualified() func(namespace, name string) *ir.TypeDef { return s.outer.qualified() }

// shadows reports whether name is bound by a let local or a parameter of this
// literal — either shadows a same-named type or top-level function (and so a
// conversion or function call) reached through the enclosing scope.
func (s funcScope) shadows(name string) bool {
	if _, ok := s.locals[name]; ok {
		return true
	}
	_, ok := s.params[name]
	return ok
}

func (s funcScope) leaf(e ast.Expr) ir.Type {
	if id, ok := e.(*ast.Identifier); ok {
		// A let-bound local shadows a same-named parameter, so it is read first.
		if t, ok := s.locals[id.Name]; ok {
			return t
		}
		if t, ok := s.params[id.Name]; ok {
			return t
		}
	}
	return s.outer.leaf(e)
}

func (s funcScope) conv(id *ast.Identifier) ir.Type {
	if s.shadows(id.Name) {
		return ir.Invalid // a local or parameter shadows a same-named type
	}
	return s.outer.conv(id)
}

func (s funcScope) fn(id *ast.Identifier) []*ast.FuncDecl {
	if s.shadows(id.Name) {
		return nil // a local or parameter shadows a same-named function
	}
	return s.outer.fn(id)
}

func (s funcScope) fnMember(m *ast.MemberExpr) []*ast.FuncDecl {
	if recv, ok := m.Receiver.(*ast.Identifier); ok {
		if s.shadows(recv.Name) {
			return nil // a local or parameter shadows a same-named namespace
		}
	}
	return s.outer.fnMember(m)
}

// constScope types a constant initializer: the context-specific forms are a
// value reference, whose type is its referent's, and a conversion, whose type
// is the type it names. A field access reads a record-typed constant's field
// (Hero.lv); self and the null literal are not meaningful in a constant, so they
// are ir.Invalid.
type constScope struct{ env Env }

func (s constScope) registry() *builtin.Registry { return s.env.Registry() }

func (s constScope) universe() map[string]*ir.TypeDef { return s.env.Universe() }

func (s constScope) qualified() func(namespace, name string) *ir.TypeDef { return s.env.QualifiedType }

func (s constScope) leaf(e ast.Expr) ir.Type {
	switch e := e.(type) {
	case *ast.Identifier:
		if target := s.env.Resolve(e); target != nil {
			return s.env.TypeOf(target)
		}
	case *ast.MemberExpr:
		// A member access on a namespace import (geo.Origin) inherits the
		// referenced declaration's type.
		if target := s.env.ResolveMember(e); target != nil {
			return s.env.TypeOf(target)
		}
		// A member access whose receiver names an enum type (Rarity.Common) is
		// a value of that enum.
		if t := enumMemberType(s.universe(), e); t != ir.Invalid {
			return t
		}
		// A member access whose receiver names a type and whose member names one
		// of its associated constants (int8.Max, Level.Max) is that constant's
		// value — the same Type.Name path an enum member takes.
		if t := assocConstType(s.universe(), e); t != ir.Invalid {
			return t
		}
		// Otherwise the receiver is a value: a field access on a record-typed
		// constant (Hero.lv, and a nested path a.b.c) or a getter read
		// (Freezing.fahrenheit). The receiver types through the same const scope,
		// so it reaches another constant's record value (or this one's,
		// recursively).
		return memberReadType(s.registry(), exprType(e.Receiver, s), e.Member.Name)
	}
	return ir.Invalid
}

func (s constScope) conv(id *ast.Identifier) ir.Type {
	if d, ok := s.env.Universe()[id.Name]; ok {
		if d.Builtin {
			return &ir.Builtin{Name: id.Name}
		}
		return &ir.Named{Def: d}
	}
	return ir.Invalid
}

func (s constScope) fn(id *ast.Identifier) []*ast.FuncDecl { return s.env.ResolveFunc(id) }

func (s constScope) fnMember(m *ast.MemberExpr) []*ast.FuncDecl {
	return s.env.ResolveFuncMember(m)
}

// BodyScope types a method or function body: the receiver type (Self —
// ir.Invalid in a function, which has none), the parameter types (Params),
// the type universe (Universe) a conversion resolves against, and the
// top-level functions callable from the body (Funcs), alongside the registry.
type BodyScope struct {
	Reg      *builtin.Registry
	Universe map[string]*ir.TypeDef
	// Qualified is the namespace-qualified type lookup the body's annotations
	// and conversions resolve through, or nil when no namespaces are in scope.
	Qualified func(namespace, name string) *ir.TypeDef
	Self      ir.Type
	Params    map[string]ir.Type
	// TScope is the generic type-parameter scope in effect in the body — the
	// enclosing function's or method's type parameters, each mapped to its bound
	// (nil if unbounded). A type annotation in the body (a let, a match/switch arm
	// type) resolves a name through it, so a parameter T may be written there and
	// resolves to a TypeVar carrying its bound rather than an unknown type. It is
	// nil in a body with no type parameters (and in a refinement predicate).
	TScope TypeScope
	// Locals maps each let-bound block-local in scope to its settled type. A
	// reference to one resolves to that type, shadowing a same-named parameter or
	// type; it is nil in a body with no lets (and in a refinement predicate, which
	// has none). The checking walk grows it as it descends a block's lets.
	Locals map[string]ir.Type
	// Funcs is the file's top-level functions by name — each name carrying
	// its overload set in source order — or nil when none are in scope (a
	// refinement predicate).
	Funcs map[string][]*ast.FuncDecl
	// QualifiedFuncs is the namespace-qualified function lookup
	// (geo.area -> the target's exported overload set), or nil when no
	// namespaces are in scope.
	QualifiedFuncs func(namespace, name string) []*ast.FuncDecl
}

// Body infers the type of a method-body expression: self, a parameter, a
// literal, a record field access, a type conversion (T(x)), or a method call
// (the form operators desugar to). An unresolvable expression is ir.Invalid.
func Body(e ast.Expr, s BodyScope) ir.Type { return exprType(e, s) }

func (s BodyScope) registry() *builtin.Registry { return s.Reg }

func (s BodyScope) universe() map[string]*ir.TypeDef { return s.Universe }

func (s BodyScope) qualified() func(namespace, name string) *ir.TypeDef { return s.Qualified }

// shadows reports whether name is bound by a let local or a parameter — either
// shadows a same-named type or top-level function in value position.
func (s BodyScope) shadows(name string) bool {
	if _, ok := s.Locals[name]; ok {
		return true
	}
	_, isParam := s.Params[name]
	return isParam
}

func (s BodyScope) conv(id *ast.Identifier) ir.Type {
	if s.shadows(id.Name) {
		return ir.Invalid // a local or parameter shadows a same-named type
	}
	return s.lookupType(id.Name)
}

func (s BodyScope) fn(id *ast.Identifier) []*ast.FuncDecl {
	if s.shadows(id.Name) {
		return nil // a local or parameter shadows a same-named function
	}
	return s.Funcs[id.Name]
}

func (s BodyScope) fnMember(m *ast.MemberExpr) []*ast.FuncDecl {
	recv, ok := m.Receiver.(*ast.Identifier)
	if !ok || s.QualifiedFuncs == nil {
		return nil
	}
	if s.shadows(recv.Name) {
		return nil // a local or parameter shadows a same-named namespace
	}
	return s.QualifiedFuncs(recv.Name, m.Member.Name)
}

func (s BodyScope) leaf(e ast.Expr) ir.Type {
	switch e := e.(type) {
	case *ast.SelfExpr:
		return s.Self
	case *ast.NullLit:
		return &ir.Builtin{Name: "null"}
	case *ast.Identifier:
		// A let-bound local shadows a same-named parameter, so it is read first.
		if t, ok := s.Locals[e.Name]; ok {
			return t
		}
		if t, ok := s.Params[e.Name]; ok {
			return t
		}
		return ir.Invalid
	case *ast.MemberExpr:
		// A member access whose receiver names a type — an enum member
		// (Element.Fire) or an associated constant (int8.Max, Level.Max) — is a
		// value of that type; a local or parameter shadowing the type name takes
		// the record-field reading instead.
		if recv, ok := e.Receiver.(*ast.Identifier); ok {
			if !s.shadows(recv.Name) {
				if t := enumMemberType(s.Universe, e); t != ir.Invalid {
					return t
				}
				if t := assocConstType(s.Universe, e); t != ir.Invalid {
					return t
				}
			}
		}
		// A member access used as a value is a record field access or a getter read
		// (value.name) the receiver's type declares.
		return memberReadType(s.Reg, exprType(e.Receiver, s), e.Member.Name)
	default:
		return ir.Invalid
	}
}

// lookupType resolves a type name (a conversion callee) to its type against
// the body's universe — which carries the prelude beneath the declared and
// imported types, so there is no second source to consult.
func (s BodyScope) lookupType(name string) ir.Type {
	if d, ok := s.Universe[name]; ok {
		if d.Builtin {
			return &ir.Builtin{Name: name}
		}
		return &ir.Named{Def: d}
	}
	return ir.Invalid
}
