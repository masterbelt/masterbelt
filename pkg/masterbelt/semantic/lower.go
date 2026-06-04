package semantic

import (
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/ast"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/ir"
)

// constBinder lowers the leaves of a constant initializer: a value-position
// identifier binds to its declaration's *Const (through the resolution query and
// the IR-const table); no other leaf form lowers in a constant.
type constBinder struct {
	q    queries
	irOf map[*ast.ConstDecl]*ir.Const
}

func (b constBinder) Leaf(e ast.Expr, _ func(ast.Expr) ir.Value) ir.Value {
	if id, ok := e.(*ast.Identifier); ok {
		if target := b.q.resolve(id); target != nil {
			return &ir.Reference{Target: b.irOf[target]}
		}
	}
	return nil
}

// bodyBinder lowers the leaves of a method body: self, a parameter reference,
// a record field access (recv.field), a type conversion (T(x), when the callee
// names a type), or nothing. The type-name resolution for a conversion is the
// resolver's; params and tscope are the parameter and generic-parameter names in
// scope.
type bodyBinder struct {
	r      *typeResolver
	params map[string]bool
	tscope map[string]bool
}

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
			if t := b.r.resolveNamedName(id.Name, b.tscope); t != ir.Invalid {
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
