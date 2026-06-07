// This file classifies why a fold produced no value. Evaluation itself returns
// a bare nil for every failure — the rules stay simple — so the classification
// re-runs the fold with the budget-guard channel armed and reads which verdict
// refused it. It only runs on the error path (a constant that should have
// folded and did not), so the second evaluation costs nothing in the green
// case.
package eval

import (
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// The two reasons a pure constant can fail to fold. There is no third: an
// effectful or extern call is unreachable in a const position (the purity and
// builtin-surface checks reject it with its own diagnostic), so what remains is
// either the evaluation budget (the user's program recursed too deep — fixable
// by making the computation shallower) or a fold rule the evaluator is missing
// (a compiler bug).
const (
	// FailureDepth: an evaluation budget guard fired — the function-application
	// depth (maxApplyDepth) or the range iteration cap (maxRangeIterations).
	FailureDepth = "depth"
	// FailureGap: no budget guard fired; the evaluator has no rule for some
	// shape in the expression. This is a compiler bug, never the user's.
	FailureGap = "evaluator gap"
)

// DeclFailure classifies why folding a declaration produced no value: it
// re-runs DeclExpecting's fold with the budget channel armed and reports
// FailureDepth when a budget guard refused it, FailureGap otherwise. want is
// the same resolved annotation type the original fold was given.
func DeclFailure(decl *ast.ConstDecl, want ir.Type, env Env) string {
	if decl.Value == nil {
		return FailureGap
	}
	return exprFailureExpecting(decl.Value, want, env)
}

// exprFailureExpecting classifies why folding an expression against an
// expected type produced no value.
func exprFailureExpecting(e ast.Expr, want ir.Type, env Env) string {
	hit := false
	evalExpr(e, expectingType(evalCtx{env: env, budgetHit: &hit}, want))
	if hit {
		return FailureDepth
	}
	return FailureGap
}

// noteBudget records that an evaluation budget guard — the application depth or
// the range iteration cap — refused to fold. The channel is armed only by the
// failure classifiers above; ordinary evaluation carries a nil pointer and
// pays one branch.
func (ctx evalCtx) noteBudget() {
	if ctx.budgetHit != nil {
		*ctx.budgetHit = true
	}
}
