// Package eval is the value half of masterbelt's constant analysis: it folds a
// constant expression to its value (ir.Constant). It is the evaluation mirror of
// package types/infer — where infer derives an expression's type, eval derives
// its value — over the same desugared shape: a literal, a value reference, or a
// method call, whose value comes from the receiver type's native intrinsic in
// the builtin registry.
//
// Evaluation reads name resolution and referenced values through an Env, so it
// has no dependency on the semantic query engine: the engine supplies a
// memoizing Env (which also tracks dependencies and guards cycles), but the
// rules here are a pure function of the AST and that environment.
package eval

import (
	"math/big"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// Env is what evaluation needs from its driver: name resolution, the value of a
// referenced declaration, and the builtin registry (for the native operator
// implementations). Keeping it an interface lets the semantic engine supply a
// memoizing implementation that tracks dependencies and breaks cycles, while
// this package stays a pure set of rules.
type Env interface {
	// Resolve returns the declaration a value-position identifier refers to, or
	// nil if no declaration has that name.
	Resolve(id *ast.Identifier) *ast.ConstDecl
	// ValueOf returns a declaration's evaluated value, or nil when it cannot be
	// evaluated.
	ValueOf(decl *ast.ConstDecl) *ir.Constant
	// Registry returns the builtin registry the program evaluates against.
	Registry() *builtin.Registry
}

// Decl folds a declaration's value, or nil when it has no initializer. Overflow
// is intentionally not checked here — an integer literal is the arbitrary-
// precision int; the range check happens where the constant's concrete type is
// known.
func Decl(decl *ast.ConstDecl, env Env) *ir.Constant {
	if decl.Value == nil {
		return nil
	}
	return Expr(decl.Value, env)
}

// Expr folds an expression to its constant value, or nil when it cannot be
// evaluated. Reading references through env lets the engine track dependencies
// and reuse its cycle guard.
func Expr(e ast.Expr, env Env) *ir.Constant {
	return evalExpr(e, env, nil)
}

// evalExpr folds an expression, resolving an identifier first against locals —
// the parameter bindings of the enclosing function literals, nil at the top
// level — and then against the environment's declarations.
func evalExpr(e ast.Expr, env Env, locals map[string]*ir.Constant) *ir.Constant {
	switch e := e.(type) {
	case *ast.IntLit:
		n, ok := new(big.Int).SetString(e.Text, 10)
		if !ok {
			return nil
		}
		return ir.IntConstant(n)
	case *ast.StringLit:
		return ir.StringConstant(e.Value)
	case *ast.BoolLit:
		return ir.BoolConstant(e.Value)
	case *ast.CollectionLit:
		return collection(e, env, locals)
	case *ast.FuncLit:
		// A function literal folds to a closure over the bindings in scope, so it
		// can be applied later (by list.map) or stored in a constant.
		return ir.FuncConstant(e, locals)
	case *ast.Identifier:
		if v, ok := locals[e.Name]; ok {
			return v
		}
		if target := env.Resolve(e); target != nil {
			return env.ValueOf(target)
		}
		return nil
	case *ast.CallExpr:
		member, ok := e.Callee.(*ast.MemberExpr)
		if !ok {
			return nil
		}
		recv := evalExpr(member.Receiver, env, locals)
		args := make([]*ir.Constant, len(e.Arguments))
		for i, a := range e.Arguments {
			args[i] = evalExpr(a, env, locals)
		}
		return call(env, recv, member.Member.Name, args)
	default:
		return nil
	}
}

// collection folds a collection literal: each entry's value (and key, for a map)
// is folded, in order. It returns nil if any element is unevaluated, so a
// collection with an unfoldable element does not fold to a partial value.
func collection(e *ast.CollectionLit, env Env, locals map[string]*ir.Constant) *ir.Constant {
	entries := make([]ir.ConstEntry, 0, len(e.Entries))
	for _, entry := range e.Entries {
		var key *ir.Constant
		if entry.Key != nil {
			if key = evalExpr(entry.Key, env, locals); key == nil {
				return nil
			}
		}
		val := evalExpr(entry.Value, env, locals)
		if val == nil {
			return nil
		}
		entries = append(entries, ir.ConstEntry{Key: key, Value: val})
	}
	return ir.CollectionConstant(entries)
}

// call evaluates a method call: a collection receiver is handled here (the only
// foldable collection method is list.map), and a primitive receiver dispatches
// to its native intrinsic in the builtin registry, keyed on the receiver's value
// kind (every integer type shares one set of intrinsics, every boolean another).
// It returns nil when an operand is unevaluated, the method has no value for the
// receiver (only reachable for a type-incorrect program), or the intrinsic
// itself has no value (a division by zero).
func call(env Env, recv *ir.Constant, name string, args []*ir.Constant) *ir.Constant {
	if recv == nil {
		return nil
	}
	for _, a := range args {
		if a == nil {
			return nil
		}
	}
	if recv.Kind == ir.ConstCollection {
		return collectionMethod(env, recv, name, args)
	}
	var typeName string
	switch recv.Kind {
	case ir.ConstInt:
		typeName = "int"
	case ir.ConstBool:
		typeName = "bool"
	case ir.ConstString:
		typeName = "string"
	default:
		return nil
	}
	fn, ok := env.Registry().Intrinsic(typeName, name)
	if !ok {
		return nil
	}
	return fn(recv, args)
}

// collectionMethod folds a method on a list/map constant. The list collections
// are not natively backed in the registry, so their methods have no intrinsic;
// the one with a foldable value is list.map, which applies its function argument
// to each element and collects the results into a new list. Anything else (a map
// receiver, or a list method other than map) has no constant value here.
func collectionMethod(env Env, recv *ir.Constant, name string, args []*ir.Constant) *ir.Constant {
	if name != "map" || len(args) != 1 || args[0].Kind != ir.ConstFunc {
		return nil
	}
	out := make([]ir.ConstEntry, len(recv.Coll))
	for i, entry := range recv.Coll {
		if entry.Key != nil {
			return nil // map.map (keyed entries) is not foldable
		}
		v := apply(env, args[0], []*ir.Constant{entry.Value})
		if v == nil {
			return nil
		}
		out[i] = ir.ConstEntry{Value: v}
	}
	return ir.CollectionConstant(out)
}

// apply folds a function-value constant against the given arguments: it binds the
// parameters to the arguments over the closure's captured environment and folds
// the body's return statement. A body with no return, a wrong argument count, or
// an unfoldable return yields nil.
func apply(env Env, fn *ir.Constant, args []*ir.Constant) *ir.Constant {
	if fn.Fn == nil || len(args) != len(fn.Fn.Params) {
		return nil
	}
	locals := make(map[string]*ir.Constant, len(fn.Captured)+len(args))
	for k, v := range fn.Captured {
		locals[k] = v
	}
	for i, p := range fn.Fn.Params {
		locals[p.Name] = args[i]
	}
	for _, stmt := range fn.Fn.Body {
		if ret, ok := stmt.(*ast.ReturnStmt); ok {
			if ret.Value == nil {
				return nil
			}
			return evalExpr(ret.Value, env, locals)
		}
	}
	return nil
}
