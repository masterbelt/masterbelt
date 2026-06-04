// Package lower turns the abstract syntax tree into the resolved IR value graph.
// It is the single AST-to-IR walk: literals become IR literals, collection
// literals recurse, and a method call (the form every operator desugars to)
// becomes an ir.Call. The forms whose lowering depends on context — a value
// name, the receiver self, a record field access, a conversion, the null literal
// — are delegated to a Binder, so a constant initializer and a method body are
// the same walk over two binders.
//
// Lowering reaches resolution and type-name facts only through the Binder, so it
// has no dependency on the semantic query engine or the type resolver: it is a
// pure function of the AST and the binder.
package lower

import (
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/ast"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/ir"
)

// Binder lowers the context-specific leaf forms of an expression — value names,
// self, field access, conversions, the null literal — to IR values. The forms
// shared by every context (literals, collection literals, and method calls) are
// lowered by Value itself.
type Binder interface {
	// Leaf lowers a context-specific expression form, recursing through sub for
	// its sub-expressions. It returns nil when the form does not lower in this
	// context.
	Leaf(e ast.Expr, sub func(ast.Expr) ir.Value) ir.Value
	// EnterFunc returns the binder for a function literal's body, with the
	// literal's parameters bound (so they lower to ir.ParamRef) on top of this
	// binder's own scope.
	EnterFunc(params []*ast.ParamDef) Binder
}

// Value lowers an expression to its resolved IR value. The shared forms are
// lowered here; the context-specific leaves go through b.Leaf.
func Value(e ast.Expr, b Binder) ir.Value {
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
				key = Value(entry.Key, b)
			}
			entries[i] = ir.CollectionEntry{Key: key, Value: Value(entry.Value, b)}
		}
		return &ir.CollectionLiteral{Entries: entries}
	case *ast.CallExpr:
		// A call through a member access is a method call; any other callee is a
		// context-specific form (a conversion in a method body, otherwise nothing).
		if member, ok := e.Callee.(*ast.MemberExpr); ok {
			args := make([]ir.Value, len(e.Arguments))
			for i, a := range e.Arguments {
				args[i] = Value(a, b)
			}
			return &ir.Call{Receiver: Value(member.Receiver, b), Method: member.Member.Name, Args: args}
		}
		return b.Leaf(e, sub(b))
	case *ast.FuncLit:
		// The body lowers in a binder that binds the literal's parameters; its
		// own parameter values are supplied at evaluation, not here.
		names := make([]string, len(e.Params))
		for i, p := range e.Params {
			names[i] = p.Name
		}
		return &ir.FuncLiteral{Params: names, Body: Body(e.Body, b.EnterFunc(e.Params))}
	default:
		return b.Leaf(e, sub(b))
	}
}

// Body lowers a method body to its IR statements (nil for an extern or empty
// body), lowering each statement's expression through b.
func Body(body []ast.Stmt, b Binder) []ir.Stmt {
	var stmts []ir.Stmt
	for _, s := range body {
		switch s := s.(type) {
		case *ast.ReturnStmt:
			stmts = append(stmts, &ir.Return{Value: Value(s.Value, b)})
		case *ast.ExprStmt:
			stmts = append(stmts, &ir.ExprStmt{Value: Value(s.X, b)})
		}
	}
	return stmts
}

// sub returns the sub-expression lowering a Binder.Leaf recurses through: Value
// bound to the same binder.
func sub(b Binder) func(ast.Expr) ir.Value {
	return func(e ast.Expr) ir.Value { return Value(e, b) }
}
