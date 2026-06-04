package semantic

import (
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/ast"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/ir"
)

// lowerValue builds the resolved IR value for an expression: literals map to IR
// literals, a value reference binds to its declaration's *Const, and a method
// call becomes an ir.Call with its receiver and arguments lowered recursively.
func lowerValue(e ast.Expr, irOf map[*ast.ConstDecl]*ir.Const, q queries) ir.Value {
	switch e := e.(type) {
	case *ast.IntLit:
		return &ir.IntLiteral{Text: e.Text}
	case *ast.StringLit:
		return &ir.StringLiteral{Value: e.Value}
	case *ast.BoolLit:
		return &ir.BoolLiteral{Value: e.Value}
	case *ast.CollectionLit:
		entries := make([]ir.CollectionEntry, len(e.Entries))
		for i, entry := range e.Entries {
			var key ir.Value
			if entry.Key != nil {
				key = lowerValue(entry.Key, irOf, q)
			}
			entries[i] = ir.CollectionEntry{Key: key, Value: lowerValue(entry.Value, irOf, q)}
		}
		return &ir.CollectionLiteral{Entries: entries}
	case *ast.Identifier:
		if target := q.resolve(e); target != nil {
			return &ir.Reference{Target: irOf[target]}
		}
		return nil
	case *ast.CallExpr:
		member, ok := e.Callee.(*ast.MemberExpr)
		if !ok {
			return nil
		}
		args := make([]ir.Value, len(e.Arguments))
		for i, a := range e.Arguments {
			args[i] = lowerValue(a, irOf, q)
		}
		return &ir.Call{Receiver: lowerValue(member.Receiver, irOf, q), Method: member.Member.Name, Args: args}
	default:
		return nil
	}
}

// lowerBody lowers a method body to its IR statements (nil for an extern or
// empty body). params is the set of parameter names in scope, and tscope the
// generic-parameter names (for type conversions).
func (r *typeResolver) lowerBody(body []ast.Stmt, params, tscope map[string]bool) []ir.Stmt {
	var stmts []ir.Stmt
	for _, s := range body {
		switch s := s.(type) {
		case *ast.ReturnStmt:
			stmts = append(stmts, &ir.Return{Value: r.lowerBodyExpr(s.Value, params, tscope)})
		case *ast.ExprStmt:
			stmts = append(stmts, &ir.ExprStmt{Value: r.lowerBodyExpr(s.X, params, tscope)})
		}
	}
	return stmts
}

// lowerBodyExpr lowers a method-body expression to an IR value: self, a
// parameter reference, a literal, a record field access (recv.field), a type
// conversion (T(x), when the callee names a type), or a method call
// (recv.method(args), the form operators also desugar to).
func (r *typeResolver) lowerBodyExpr(e ast.Expr, params, tscope map[string]bool) ir.Value {
	switch e := e.(type) {
	case *ast.SelfExpr:
		return &ir.SelfValue{}
	case *ast.IntLit:
		return &ir.IntLiteral{Text: e.Text}
	case *ast.StringLit:
		return &ir.StringLiteral{Value: e.Value}
	case *ast.BoolLit:
		return &ir.BoolLiteral{Value: e.Value}
	case *ast.NullLit:
		return &ir.NullValue{}
	case *ast.CollectionLit:
		entries := make([]ir.CollectionEntry, len(e.Entries))
		for i, entry := range e.Entries {
			var key ir.Value
			if entry.Key != nil {
				key = r.lowerBodyExpr(entry.Key, params, tscope)
			}
			entries[i] = ir.CollectionEntry{Key: key, Value: r.lowerBodyExpr(entry.Value, params, tscope)}
		}
		return &ir.CollectionLiteral{Entries: entries}
	case *ast.Identifier:
		if params[e.Name] {
			return &ir.ParamRef{Name: e.Name}
		}
		return nil
	case *ast.MemberExpr:
		// A member access used as a value is a record field access.
		return &ir.FieldAccess{Receiver: r.lowerBodyExpr(e.Receiver, params, tscope), Field: e.Member.Name}
	case *ast.CallExpr:
		// A call whose callee names a type is a conversion T(x).
		if id, ok := e.Callee.(*ast.Identifier); ok && !params[id.Name] {
			if t := r.resolveNamedName(id.Name, tscope); t != ir.Invalid {
				var arg ir.Value
				if len(e.Arguments) > 0 {
					arg = r.lowerBodyExpr(e.Arguments[0], params, tscope)
				}
				return &ir.Conversion{Type: t, Value: arg}
			}
		}
		// A call whose callee is a member access is a method call.
		if member, ok := e.Callee.(*ast.MemberExpr); ok {
			args := make([]ir.Value, len(e.Arguments))
			for i, a := range e.Arguments {
				args[i] = r.lowerBodyExpr(a, params, tscope)
			}
			return &ir.Call{
				Receiver: r.lowerBodyExpr(member.Receiver, params, tscope),
				Method:   member.Member.Name,
				Args:     args,
			}
		}
		return nil
	default:
		return nil
	}
}
