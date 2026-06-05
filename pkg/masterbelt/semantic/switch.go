package semantic

import (
	"strings"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/eval"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/types/infer"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// A switch is a value-dispatch statement: it branches on the scrutinee's
// equality against compile-time constant values. Its analysis layers on top of
// the per-statement body walk:
//
//   - every arm value must have the scrutinee's type (arm_value_type_mismatch);
//   - the arms must cover every value — an enum's whole member set, or, for an
//     unbounded scalar, a wildcard "_" (non_exhaustive_switch);
//   - no two arms may name the same value (duplicate_switch_arm); and
//   - an arm none of whose values can ever be reached is unreachable_arm.
//
// Return analysis treats an exhaustive switch all of whose arms (and its
// wildcard) return as itself a return, so a switch that covers an enum keeps a
// function from tripping missing_return.

// checkSwitch validates one switch statement: its scrutinee and arm value
// types, its exhaustiveness, its duplicate and unreachable arms. The arm bodies
// themselves are walked by the caller (checkStmts), which threads the result
// type into their returns.
func checkSwitch(sw *ast.SwitchStmt, bs infer.BodyScope, env eval.Env, at func(ast.Node) span, diags *diagnostic.List) {
	if sw.Scrutinee == nil {
		return
	}
	scrutT := infer.Body(sw.Scrutinee, bs)
	enumDef := enumDefOf(scrutT)
	armSink := armValueSink(at, diags)

	// covered records, for an enum scrutinee, which member indices the arms
	// account for; seen records the folded values already matched, so a repeat
	// is a duplicate. The wildcard (Else) makes the switch exhaustive on its
	// own.
	covered := map[int]bool{}
	seen := map[string]bool{}
	for _, arm := range sw.Arms {
		armReachable := false
		dupReported := false
		for _, v := range arm.Values {
			// Check the value against the scrutinee's type — pushing it down so a
			// bare enum member resolves and an integer literal adapts — reporting
			// a mismatch as arm_value_type_mismatch.
			if scrutT != ir.Invalid {
				infer.CheckBody(v, scrutT, bs, armSink)
			}
			key, ok := armValueKey(v, enumDef, env)
			if !ok {
				armReachable = true // an unfoldable value is conservatively live
				continue
			}
			if seen[key] {
				s := at(v)
				diags.Add(newDuplicateSwitchArmDiagnostic(s.offset, s.width, armValueLabel(v, enumDef, env)))
				dupReported = true
				continue
			}
			seen[key] = true
			armReachable = true
			if enumDef != nil {
				if idx := enumValueIndex(v, enumDef, env); idx >= 0 {
					covered[idx] = true
				}
			}
		}
		// An arm none of whose values can match is unreachable — but when that
		// is only because every value duplicated an earlier arm, the per-value
		// duplicate_switch_arm already said so, so the arm-level diagnostic would
		// just be noise.
		if !armReachable && len(arm.Values) > 0 && !dupReported {
			s := at(arm)
			diags.Add(newUnreachableArmDiagnostic(s.offset, s.width))
		}
	}

	// Any arm written after the wildcard can never run: the catch-all already
	// matched every remaining value.
	for _, arm := range sw.AfterElse {
		s := at(arm)
		diags.Add(newUnreachableArmDiagnostic(s.offset, s.width))
	}

	if sw.Else != nil {
		return // a wildcard covers every remaining value: always exhaustive
	}
	switch {
	case enumDef != nil:
		var missing []string
		for i, m := range enumDef.Enum.Members {
			if !covered[i] {
				missing = append(missing, enumDef.Name+"."+m.Name)
			}
		}
		if len(missing) > 0 {
			s := at(sw)
			diags.Add(newNonExhaustiveSwitchDiagnostic(s.offset, s.width, scrutT.String(), "missing "+strings.Join(missing, ", ")))
		}
	case scrutT != ir.Invalid:
		// A scalar (or any non-enum) scrutinee ranges over an unbounded domain,
		// so it can only be exhausted by a wildcard.
		s := at(sw)
		diags.Add(newNonExhaustiveSwitchDiagnostic(s.offset, s.width, scrutT.String(), "add a _ arm for the remaining values"))
	}
}

// armValueSink reports an arm value whose type is not the scrutinee's as
// arm_value_type_mismatch, reusing the checking walk's Mismatch finding. The
// other findings (operator errors inside a value expression) keep their own
// diagnostics, so a malformed arm value still surfaces its real cause.
func armValueSink(at func(ast.Node) span, diags *diagnostic.List) *infer.Sink {
	sink := exprSink(at, diags)
	sink.Mismatch = func(node ast.Node, got, want ir.Type) {
		s := at(node)
		diags.Add(newArmValueTypeMismatchDiagnostic(s.offset, s.width, got.String(), want.String()))
	}
	return sink
}

// armValueKey returns a stable key identifying an arm value for duplicate
// detection, and whether it could be determined. An enum member keys on its
// index; any other value keys on its folded constant. An unfoldable value has
// no key (the second result is false), so it is neither a duplicate nor counts
// toward coverage.
func armValueKey(v ast.Expr, enumDef *ir.TypeDef, env eval.Env) (string, bool) {
	if enumDef != nil {
		if idx := enumValueIndex(v, enumDef, env); idx >= 0 {
			return "enum:" + enumDef.Name + ":" + itoa(idx), true
		}
		return "", false
	}
	c := eval.Expr(v, env)
	if c == nil {
		return "", false
	}
	return "const:" + c.String(), true
}

// armValueLabel renders an arm value for a diagnostic: an enum member by its
// qualified name, any other value by its folded constant or its surface form.
func armValueLabel(v ast.Expr, enumDef *ir.TypeDef, env eval.Env) string {
	if enumDef != nil {
		if idx := enumValueIndex(v, enumDef, env); idx >= 0 {
			return enumDef.Name + "." + enumDef.Enum.Members[idx].Name
		}
	}
	if c := eval.Expr(v, env); c != nil {
		return c.String()
	}
	return ast.Render(v)
}

// enumValueIndex returns the member index an arm value names within enumDef, or
// -1 when the value is not a member of it. It accepts both the bare form
// (Common) and the qualified form (Rarity.Common).
func enumValueIndex(v ast.Expr, enumDef *ir.TypeDef, env eval.Env) int {
	if enumDef == nil {
		return -1
	}
	switch e := v.(type) {
	case *ast.Identifier:
		return enumIndex(enumDef, e.Name)
	case *ast.MemberExpr:
		if recv, ok := e.Receiver.(*ast.Identifier); ok && recv.Name == enumDef.Name {
			return enumIndex(enumDef, e.Member.Name)
		}
	}
	if c := eval.Expr(v, env); c != nil && c.Kind == ir.ConstEnum && c.EnumDef == enumDef {
		return c.EnumIndex
	}
	return -1
}

// bodyReturns reports whether a statement body is guaranteed to return a value
// on every path. A return does; a bare expression does not; a switch does when
// it is exhaustive — it has a wildcard, or its arm values cover an enum — and
// every arm body (and the wildcard body) itself returns. This is the return
// analysis that keeps an exhaustive switch from tripping missing_return.
func bodyReturns(body []ast.Stmt, scrutEnum func(ast.Expr) *ir.TypeDef) bool {
	for _, s := range body {
		if stmtReturns(s, scrutEnum) {
			return true
		}
	}
	return false
}

// stmtReturns reports whether one statement guarantees a return on every path.
func stmtReturns(s ast.Stmt, scrutEnum func(ast.Expr) *ir.TypeDef) bool {
	switch s := s.(type) {
	case *ast.ReturnStmt:
		return s.Value != nil
	case *ast.SwitchStmt:
		return switchReturns(s, scrutEnum)
	default:
		return false
	}
}

// switchReturns reports whether a switch always returns: it must be exhaustive
// (a wildcard, or an enum scrutinee every member of which an arm names) and
// every arm body — and the wildcard body — must itself return.
func switchReturns(sw *ast.SwitchStmt, scrutEnum func(ast.Expr) *ir.TypeDef) bool {
	for _, arm := range sw.Arms {
		if !bodyReturns(arm.Body, scrutEnum) {
			return false
		}
	}
	if sw.Else != nil {
		return bodyReturns(sw.Else, scrutEnum)
	}
	// No wildcard: the switch returns only if it is exhaustive over an enum
	// (every member named) — a scalar without a wildcard never is.
	enumDef := scrutEnum(sw.Scrutinee)
	if enumDef == nil {
		return false
	}
	covered := map[int]bool{}
	for _, arm := range sw.Arms {
		for _, v := range arm.Values {
			if idx := enumMemberIndexOf(v, enumDef); idx >= 0 {
				covered[idx] = true
			}
		}
	}
	return len(covered) == len(enumDef.Enum.Members)
}

// enumMemberIndexOf returns the member index an arm value names within enumDef
// syntactically (the bare or qualified form), or -1. It is the eval-free form
// used by return analysis, which runs without an evaluation environment.
func enumMemberIndexOf(v ast.Expr, enumDef *ir.TypeDef) int {
	switch e := v.(type) {
	case *ast.Identifier:
		return enumIndex(enumDef, e.Name)
	case *ast.MemberExpr:
		if recv, ok := e.Receiver.(*ast.Identifier); ok && recv.Name == enumDef.Name {
			return enumIndex(enumDef, e.Member.Name)
		}
	}
	return -1
}

// itoa renders a small non-negative int without importing strconv at the call
// sites; the key strings only need to be stable, not pretty.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
