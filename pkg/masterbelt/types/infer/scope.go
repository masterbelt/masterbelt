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
type funcScope struct {
	outer  scope
	params map[string]ir.Type
}

func (s funcScope) registry() *builtin.Registry { return s.outer.registry() }

func (s funcScope) universe() map[string]*ir.TypeDef { return s.outer.universe() }

func (s funcScope) qualified() func(namespace, name string) *ir.TypeDef { return s.outer.qualified() }

func (s funcScope) leaf(e ast.Expr) ir.Type {
	if id, ok := e.(*ast.Identifier); ok {
		if t, ok := s.params[id.Name]; ok {
			return t
		}
	}
	return s.outer.leaf(e)
}

func (s funcScope) conv(id *ast.Identifier) ir.Type {
	if _, ok := s.params[id.Name]; ok {
		return ir.Invalid // a parameter shadows a same-named type
	}
	return s.outer.conv(id)
}

func (s funcScope) fn(id *ast.Identifier) []*ast.FuncDecl {
	if _, ok := s.params[id.Name]; ok {
		return nil // a parameter shadows a same-named function
	}
	return s.outer.fn(id)
}

func (s funcScope) fnMember(m *ast.MemberExpr) []*ast.FuncDecl {
	if recv, ok := m.Receiver.(*ast.Identifier); ok {
		if _, isParam := s.params[recv.Name]; isParam {
			return nil // a parameter shadows a same-named namespace
		}
	}
	return s.outer.fnMember(m)
}

// constScope types a constant initializer: the context-specific forms are a
// value reference, whose type is its referent's, and a conversion, whose type
// is the type it names. Self, field access, and the null literal are not
// meaningful in a constant, so they are ir.Invalid.
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

func (s BodyScope) conv(id *ast.Identifier) ir.Type {
	if _, isParam := s.Params[id.Name]; isParam {
		return ir.Invalid // a parameter shadows a same-named type
	}
	return s.lookupType(id.Name)
}

func (s BodyScope) fn(id *ast.Identifier) []*ast.FuncDecl {
	if _, isParam := s.Params[id.Name]; isParam {
		return nil // a parameter shadows a same-named function
	}
	return s.Funcs[id.Name]
}

func (s BodyScope) fnMember(m *ast.MemberExpr) []*ast.FuncDecl {
	recv, ok := m.Receiver.(*ast.Identifier)
	if !ok || s.QualifiedFuncs == nil {
		return nil
	}
	if _, isParam := s.Params[recv.Name]; isParam {
		return nil // a parameter shadows a same-named namespace
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
		if t, ok := s.Params[e.Name]; ok {
			return t
		}
		return ir.Invalid
	case *ast.MemberExpr:
		// A member access used as a value is a record field access.
		return fieldType(exprType(e.Receiver, s), e.Member.Name)
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
