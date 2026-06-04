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
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/ast"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/ir"
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
		return collection(e, env)
	case *ast.Identifier:
		if target := env.Resolve(e); target != nil {
			return env.ValueOf(target)
		}
		return nil
	case *ast.CallExpr:
		member, ok := e.Callee.(*ast.MemberExpr)
		if !ok {
			return nil
		}
		recv := Expr(member.Receiver, env)
		args := make([]*ir.Constant, len(e.Arguments))
		for i, a := range e.Arguments {
			args[i] = Expr(a, env)
		}
		return method(env.Registry(), recv, member.Member.Name, args)
	default:
		return nil
	}
}

// collection folds a collection literal: each entry's value (and key, for a map)
// is folded, in order. It returns nil if any element is unevaluated, so a
// collection with an unfoldable element does not fold to a partial value.
func collection(e *ast.CollectionLit, env Env) *ir.Constant {
	entries := make([]ir.ConstEntry, 0, len(e.Entries))
	for _, entry := range e.Entries {
		var key *ir.Constant
		if entry.Key != nil {
			if key = Expr(entry.Key, env); key == nil {
				return nil
			}
		}
		val := Expr(entry.Value, env)
		if val == nil {
			return nil
		}
		entries = append(entries, ir.ConstEntry{Key: key, Value: val})
	}
	return ir.CollectionConstant(entries)
}

// method evaluates an operator method by dispatching to its native
// implementation in the builtin registry, keyed on the receiver's value kind
// (every integer type shares one set of intrinsics, every boolean type another).
// It returns nil when an operand is unevaluated, the method has no intrinsic for
// the receiver kind (only reachable for a type-incorrect program), or the
// intrinsic itself has no value (a division by zero).
func method(reg *builtin.Registry, recv *ir.Constant, name string, args []*ir.Constant) *ir.Constant {
	if recv == nil {
		return nil
	}
	for _, a := range args {
		if a == nil {
			return nil
		}
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
	fn, ok := reg.Intrinsic(typeName, name)
	if !ok {
		return nil
	}
	return fn(recv, args)
}
