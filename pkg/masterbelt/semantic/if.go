package semantic

import (
	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/types"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/types/infer"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// An if is a boolean control statement: it runs its then body when the condition
// holds, otherwise its else branch. It yields no value, so — unlike a switch —
// there is no exhaustiveness, no branch-type merge, and no else-required rule. The
// only condition check is that it types as a bool (condition_not_bool); the
// branch bodies are walked like any statement body, threading the result type
// into their returns.
//
// Return analysis treats an if whose then body and whose else branch both return
// (an if/else where every path returns) as itself a return, so a function whose
// body ends in such an if no longer trips missing_return.

// checkIf validates one if statement and recurses into its branches: the
// condition must type as a bool, and the then body, the else-if chain, and the
// else body are each checked against the declared result type want. noSelf,
// when non-nil (a function body), reports a self expression in the condition.
func checkIf(s *ast.IfStmt, want ir.Type, bs infer.BodyScope, env exprFolder, noSelf func(ast.Node), sink *infer.Sink, at func(ast.Node) span, diags *diagnostic.List) {
	if s.Cond != nil {
		if noSelf != nil {
			checkNoSelf(s.Cond, noSelf)
		}
		// Type the condition (reporting operator and reference errors through the
		// checking sink) and require a bool. A nil diagnostic list is the
		// func-literal-types walk, which wants only the sink's findings, so the
		// condition_not_bool check is gated on it — exactly as checkSwitch gates
		// its own diagnostics.
		condT := infer.CheckPredicate(s.Cond, bs, sink)
		if diags != nil && condT != ir.Invalid && !types.IsBoolean(bs.Reg, condT) {
			c := at(s.Cond)
			diags.Add(newConditionNotBoolDiagnostic(c.offset, c.width, condT.String()))
		}
	}
	checkStmts(s.Then, want, bs, env, noSelf, sink, at, diags)
	if s.ElseIf != nil {
		checkIf(s.ElseIf, want, bs, env, noSelf, sink, at, diags)
	}
	checkStmts(s.Else, want, bs, env, noSelf, sink, at, diags)
}

// ifReturns reports whether an if statement guarantees a return on every path:
// its then body must return, and it must have an else branch — a chained else-if
// (itself returning on every path) or a plain else body that returns. An if with
// no else cannot guarantee a return, since the condition may be false.
func ifReturns(s *ast.IfStmt, bs infer.BodyScope) bool {
	if !bodyReturns(s.Then, bs) {
		return false
	}
	switch {
	case s.ElseIf != nil:
		return ifReturns(s.ElseIf, bs)
	case s.Else != nil:
		return bodyReturns(s.Else, bs)
	default:
		return false // no else: the false path falls through without returning
	}
}
