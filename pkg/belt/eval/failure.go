// This file classifies why a fold produced no value. Evaluation itself returns
// a bare nil for every failure — the rules stay simple — so the classification
// (eval.GraphFailure) re-runs the fold with the budget-guard channel armed and
// reads which verdict refused it. It only runs on the error path (a constant
// that should have folded and did not), so the second evaluation costs nothing
// in the green case.

package eval

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
