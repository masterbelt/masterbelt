package semantic

import (
	"math/big"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/ast"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/ir"
)

// computeValue is the evaluation rule, shared by both query implementations.
// Overflow is intentionally not checked here — an integer literal is the
// arbitrary-precision int; the range check happens in assemble where the
// constant's concrete type is known.
func computeValue(decl *ast.ConstDecl, q queries) *ir.Constant {
	if decl.Value == nil {
		return nil
	}
	return evalExpr(decl.Value, q)
}

// evalExpr folds an expression to its constant value, or nil when it cannot be
// evaluated. Reading references through q lets the engine track dependencies and
// reuse its cycle guard.
func evalExpr(e ast.Expr, q queries) *ir.Constant {
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
		return evalCollection(e, q)
	case *ast.Identifier:
		if target := q.resolve(e); target != nil {
			return q.valueOf(target)
		}
		return nil
	case *ast.CallExpr:
		member, ok := e.Callee.(*ast.MemberExpr)
		if !ok {
			return nil
		}
		recv := evalExpr(member.Receiver, q)
		args := make([]*ir.Constant, len(e.Arguments))
		for i, a := range e.Arguments {
			args[i] = evalExpr(a, q)
		}
		return evalMethod(q.registry(), recv, member.Member.Name, args)
	default:
		return nil
	}
}

// evalCollection folds a collection literal: each entry's value (and key, for a
// map) is folded, in order. It returns nil if any element is unevaluated, so a
// collection with an unfoldable element does not fold to a partial value.
func evalCollection(e *ast.CollectionLit, q queries) *ir.Constant {
	entries := make([]ir.ConstEntry, 0, len(e.Entries))
	for _, entry := range e.Entries {
		var key *ir.Constant
		if entry.Key != nil {
			if key = evalExpr(entry.Key, q); key == nil {
				return nil
			}
		}
		val := evalExpr(entry.Value, q)
		if val == nil {
			return nil
		}
		entries = append(entries, ir.ConstEntry{Key: key, Value: val})
	}
	return ir.CollectionConstant(entries)
}

// evalMethod evaluates an operator method by dispatching to its native
// implementation in the builtin registry, keyed on the receiver's value kind
// (every integer type shares one set of intrinsics, every boolean type another).
// It returns nil when an operand is unevaluated, the method has no intrinsic for
// the receiver kind (only reachable for a type-incorrect program), or the
// intrinsic itself has no value (a division by zero).
func evalMethod(reg *builtin.Registry, recv *ir.Constant, method string, args []*ir.Constant) *ir.Constant {
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
	fn, ok := reg.Intrinsic(typeName, method)
	if !ok {
		return nil
	}
	return fn(recv, args)
}
