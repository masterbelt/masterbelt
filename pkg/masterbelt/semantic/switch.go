package semantic

import (
	"strings"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/eval"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/types"
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
	// A switch dispatches on the scrutinee's value equality, so the scrutinee
	// must be comparable — the same equality-driven discipline a map key obeys.
	// A record or union does not opt into comparable, so a switch over it is
	// rejected with a pointer to match (which is what branches on a record or
	// union type). The check is skipped when the type is already invalid (its
	// own error is reported elsewhere — no double report).
	if scrutT != ir.Invalid && !scrutineeComparable(bs.Reg, scrutT) {
		s := at(sw.Scrutinee)
		diags.Add(newScrutineeNotComparableDiagnostic(s.offset, s.width, scrutT.String()))
	}
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

// scrutineeComparable reports whether a switch scrutinee's type carries
// equality — the contract value dispatch requires. The single rule is
// types.Satisfies against the comparable interface: a concrete type qualifies
// when its definition opts into comparable (an enum and the scalars
// automatically, a nominal type via `impl comparable {}`), and — since Satisfies
// is generalized over interface inheritance — a bounded type parameter qualifies
// when its bound is comparable or any interface that inherits it (T: orderable
// dispatches on the equality comparable supplies). This replaces the former
// exact-match special case on the type parameter's bound, which only admitted a
// bound that was comparable itself; an unbounded T (Bound == nil) still carries
// no contract and is rejected, because Satisfies finds no definition to read.
// comparable is taken from the universe, the same source the map-key and
// enum-contract checks use.
func scrutineeComparable(reg *builtin.Registry, typ ir.Type) bool {
	cmp := universe().prelude["comparable"]
	if cmp == nil {
		return true // no comparable in scope: degrade rather than spuriously reject
	}
	return types.Satisfies(reg, typ, &ir.Named{Def: cmp})
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
//
// bs is the body scope in force at the start of the body: it grows as the walk
// descends a block's lets, so a switch over a let-bound enum local resolves its
// scrutinee enum from the local in scope, exactly as checkSwitch does. The
// growth is block-local — a copy per let — so an inner let does not leak to a
// sibling statement's resolution.
func bodyReturns(body []ast.Stmt, bs infer.BodyScope) bool {
	for _, s := range body {
		if l, ok := s.(*ast.LetStmt); ok {
			// A let in this block binds a local the statements after it (and the
			// switches among them) can switch over; carry its settled type forward.
			bs = bindReturnLocal(l, bs)
			continue
		}
		if stmtReturns(s, bs) {
			return true
		}
	}
	return false
}

// bindReturnLocal grows bs with a let's settled type so a later switch over the
// local resolves its scrutinee enum. The type is inferred the eval-free way
// (infer.Body sees the params and locals already in scope), mirroring the
// checking walk's checkLet without re-reporting; a nameless or unfoldable let
// extends nothing meaningful (ir.Invalid is not an enum).
func bindReturnLocal(s *ast.LetStmt, bs infer.BodyScope) infer.BodyScope {
	if s.Name == "" {
		return bs
	}
	typ := ir.Invalid
	switch {
	case s.Type != nil:
		typ = resolveBodyType(bs, s.Type)
	case s.Value != nil:
		typ = infer.Body(s.Value, bs)
	}
	return withLocal(bs, s.Name, typ)
}

// stmtReturns reports whether one statement guarantees a return on every path.
func stmtReturns(s ast.Stmt, bs infer.BodyScope) bool {
	switch s := s.(type) {
	case *ast.ReturnStmt:
		return s.Value != nil
	case *ast.SwitchStmt:
		return switchReturns(s, bs)
	case *ast.MatchStmt:
		return matchReturns(s, bs)
	case *ast.IfStmt:
		return ifReturns(s, bs)
	case *ast.ForStmt:
		// A for never guarantees a return: an empty collection skips the body
		// entirely, so even a body that always returns may not run. Control falls
		// through to the statement after the loop.
		return false
	case *ast.LetStmt, *ast.AssignStmt, *ast.ExprStmt:
		// None of these guarantees a return: control falls through to the next
		// statement. Listed explicitly rather than folded into a `default:
		// return false` so a statement kind added later (one that might end a
		// path, e.g. a throw) forces a decision here instead of being silently
		// assumed not to return.
		return false
	default:
		panic(ast.UnhandledStmt(s))
	}
}

// switchReturns reports whether a switch always returns: it must be exhaustive
// (a wildcard, or an enum scrutinee every member of which an arm names) and
// every arm body — and the wildcard body — must itself return.
func switchReturns(sw *ast.SwitchStmt, bs infer.BodyScope) bool {
	for _, arm := range sw.Arms {
		if !bodyReturns(arm.Body, bs) {
			return false
		}
	}
	if sw.Else != nil {
		return bodyReturns(sw.Else, bs)
	}
	// No wildcard: the switch returns only if it is exhaustive over an enum
	// (every member named) — a scalar without a wildcard never is.
	enumDef := scrutEnumOf(bs)(sw.Scrutinee)
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
