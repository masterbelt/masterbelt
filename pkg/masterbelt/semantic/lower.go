package semantic

import (
	"github.com/masterbelt/masterbelt/pkg/masterbelt/lower"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/types/infer"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// constBinder lowers the leaves of a constant initializer: a value-position
// identifier — or a namespace member access (geo.Origin) — binds to its
// declaration's *Const (through the resolution queries and the program-wide
// IR-const table); no other leaf form lowers in a constant. The file is the
// one the initializer sits in, scoping its resolution.
type constBinder struct {
	q    queries
	file FileID
	irOf map[*ast.ConstDecl]*ir.Const
}

func (b constBinder) Leaf(e ast.Expr, _ func(ast.Expr) ir.Value) ir.Value {
	switch e := e.(type) {
	case *ast.Identifier:
		if target := b.q.resolve(b.file, e); target != nil {
			return &ir.Reference{Target: b.irOf[target]}
		}
	case *ast.MemberExpr:
		if target := b.q.resolveMember(b.file, e); target != nil {
			return &ir.Reference{Target: b.irOf[target]}
		}
	}
	return nil
}

func (b constBinder) EnterFunc(params []*ast.ParamDef) lower.Binder { return enterFunc(b, params) }

// bodyBinder lowers the leaves of a method body: self, a parameter reference,
// a record field access (recv.field), a type conversion (T(x), when the callee
// names a type), or nothing. The type-name resolution for a conversion is the
// resolver's; params and tscope are the parameter and generic-parameter names in
// scope.
type bodyBinder struct {
	r      *infer.TypeResolver
	params map[string]bool
	tscope map[string]bool
}

func (b bodyBinder) EnterFunc(params []*ast.ParamDef) lower.Binder { return enterFunc(b, params) }

func (b bodyBinder) Leaf(e ast.Expr, sub func(ast.Expr) ir.Value) ir.Value {
	switch e := e.(type) {
	case *ast.SelfExpr:
		return &ir.SelfValue{}
	case *ast.NullLit:
		return &ir.NullValue{}
	case *ast.Identifier:
		if b.params[e.Name] {
			return &ir.ParamRef{Name: e.Name}
		}
		return nil
	case *ast.MemberExpr:
		// A member access used as a value is a record field access.
		return &ir.FieldAccess{Receiver: sub(e.Receiver), Field: e.Member.Name}
	case *ast.CallExpr:
		// A call whose callee names a type is a conversion T(x).
		if id, ok := e.Callee.(*ast.Identifier); ok && !b.params[id.Name] {
			if t := b.r.ResolveName(id.Name, b.tscope); t != ir.Invalid {
				var arg ir.Value
				if len(e.Arguments) > 0 {
					arg = sub(e.Arguments[0])
				}
				return &ir.Conversion{Type: t, Value: arg}
			}
		}
		return nil
	default:
		return nil
	}
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
	return b.outer.Leaf(e, sub)
}

func (b funcBinder) EnterFunc(params []*ast.ParamDef) lower.Binder { return enterFunc(b, params) }
