package semantic

import (
	"github.com/masterbelt/masterbelt/pkg/belt/types"
	"github.com/masterbelt/masterbelt/pkg/belt/types/infer"
	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// An assert statement requires its condition to hold where it stands. Unlike a
// top-level assert declaration — a compile-time, closed assertion folded once and
// checked for foldability here — an assert statement's condition is checked but
// not folded: it reads the self and locals in scope, so its value is known only
// where it runs (a master's validate each block folds it against each row in the
// data layer). The only check at this layer is that the condition types as a bool
// (assertion_not_bool), the statement twin of an if's condition_not_bool — the
// same shape checkIf uses.

// checkAssertStmt validates one assert statement: its condition must type as a
// bool. noSelf, when non-nil (a function body, where self is not bound), reports
// a self expression in the condition. A nil diagnostic list is the func-literal-
// types walk, which wants only the sink's findings, so the assertion_not_bool
// check is gated on it — exactly as checkIf gates condition_not_bool.
func checkAssertStmt(s *ast.AssertStmt, bs infer.BodyScope, noSelf func(ast.Node), sink *infer.Sink, at func(ast.Node) span, diags *diagnostic.List) {
	if s.Cond == nil {
		return
	}
	if noSelf != nil {
		checkNoSelf(s.Cond, noSelf)
	}
	condT := infer.CheckPredicate(s.Cond, bs, sink)
	if diags != nil && condT != ir.Invalid && !types.IsBoolean(bs.Reg, condT) {
		c := at(s.Cond)
		diags.Add(newAssertionNotBoolDiagnostic(c.offset, c.width, condT.String()))
	}
}
