// This file holds the fold-totality side of the analyzer: the language rule is
// that a constant either folds to a value or carries an error — there is no
// silently unfolded const. foldFailure classifies the failure for the
// unfolded_const diagnostic (and the fold gate tests): "depth" when an
// evaluation budget guard refused the fold (the user's computation is too
// deep — fixable), "evaluator gap" for everything else (a missing fold rule —
// a compiler bug, never the user's).
package semantic

import (
	"github.com/masterbelt/masterbelt/pkg/masterbelt/eval"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
)

// foldFailure classifies why a constant declaration's fold produced no value,
// re-running the fold with eval's budget channel armed. It is only called on
// the error path (a diagnostic-free const whose Eval is nil), so the re-run
// costs nothing in the green case.
func foldFailure(file FileID, decl *ast.ConstDecl, q queries) string {
	return eval.DeclFailure(decl, annotationResolved(q, file, decl), evalEnv{q: q, file: file})
}

// exprFoldFailure is foldFailure for a bare expression — an assert condition.
func exprFoldFailure(file FileID, e ast.Expr, q queries) string {
	return eval.ExprFailure(e, evalEnv{q: q, file: file})
}
