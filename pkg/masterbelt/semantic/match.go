package semantic

import (
	"slices"
	"strings"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/types/infer"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// A match is a type-dispatch statement: it branches on the runtime type of a
// union (or optional) scrutinee, narrowing the scrutinee to the arm's member
// type inside the arm body. Its analysis layers on top of the per-statement body
// walk:
//
//   - every arm's member type must be one of the scrutinee union's members
//     (arm_type_not_in_union);
//   - no two arms may name the same member type (duplicate_match_arm);
//   - the arms must cover every member of the union, or carry a wildcard "_"
//     (non_exhaustive_match); and
//   - an arm after the wildcard can never run (unreachable_arm).
//
// Return analysis treats an exhaustive match all of whose arms (and its wildcard)
// return as itself a return, so a match that covers a union keeps a function from
// tripping missing_return. The arm bodies are walked by the caller (checkStmts)
// in a scope where the arm's binding is narrowed to its member type.

// checkMatch validates one match statement: its scrutinee's union, each arm's
// member type and binding, its exhaustiveness, and its duplicate and unreachable
// arms. The arm bodies themselves are walked by the caller (checkStmts), which
// narrows each arm's binding and threads the result type into their returns.
func checkMatch(m *ast.MatchStmt, bs infer.BodyScope, at func(ast.Node) span, diags *diagnostic.List) {
	if m.Scrutinee == nil {
		return
	}
	scrutT := infer.Body(m.Scrutinee, bs)
	members := unionMembers(scrutT)

	// covered records which union members an arm's type accounts for, keyed by the
	// member's rendered type; seen records the arm types already named, so a
	// repeat is a duplicate. The wildcard (Else) makes the match exhaustive on its
	// own.
	covered := map[string]bool{}
	seen := map[string]bool{}
	for _, arm := range m.Arms {
		if arm.Type == nil {
			continue
		}
		armT := resolveBodyType(bs, arm.Type)
		if armT == ir.Invalid {
			continue // the arm names no type; a type-name diagnostic already fired
		}
		key := armT.String()
		if seen[key] {
			s := at(arm)
			diags.Add(newDuplicateMatchArmDiagnostic(s.offset, s.width, key))
			continue
		}
		seen[key] = true
		// The arm's member type must be one of the scrutinee union's members. A
		// non-union scrutinee (members is nil) has no members to be in, so any
		// typed arm on it is reported — a match needs a union to dispatch on.
		if !members[key] {
			s := at(arm)
			diags.Add(newArmTypeNotInUnionDiagnostic(s.offset, s.width, key, scrutT.String()))
			continue
		}
		covered[key] = true
	}

	// Any arm written after the wildcard can never run: the catch-all already
	// matched every remaining type.
	for _, arm := range m.AfterElse {
		s := at(arm)
		diags.Add(newUnreachableArmDiagnostic(s.offset, s.width))
	}

	if m.Else != nil {
		return // a wildcard covers every remaining member: always exhaustive
	}
	if len(members) == 0 {
		return // a non-union scrutinee: the per-arm diagnostics carry the report
	}
	var missing []string
	for key := range members {
		if !covered[key] {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		s := at(m)
		diags.Add(newNonExhaustiveMatchDiagnostic(s.offset, s.width, scrutT.String(), "missing "+strings.Join(missing, ", ")))
	}
}

// unionMembers returns the set of a scrutinee type's union members, keyed by
// each member's rendered type — the keying every membership and exhaustiveness
// check in this file uses, so an arm type matches a member exactly when their
// rendered forms agree. A nominal alias of a union (type GameValue = Coin | Level)
// is unwrapped to its underlying union, so a match over the alias dispatches on
// its members. A non-union type yields the empty set (a match needs a union to
// dispatch on), so its arms are reported and it is never exhaustive.
func unionMembers(t ir.Type) map[string]bool {
	u, ok := underlyingUnion(t).(*ir.Union)
	if !ok {
		return map[string]bool{}
	}
	out := make(map[string]bool, len(u.Members))
	for _, m := range u.Members {
		out[m.String()] = true
	}
	return out
}

// underlyingUnion unwraps a nominal alias to the type it is defined as, so a
// match scrutinee whose type is a named union (type GameValue = Coin | Level)
// reaches the union it stands for. A self-referential alias is made finite by the
// visited set; a non-alias (a bare union, a record, a primitive) is returned
// unchanged.
func underlyingUnion(t ir.Type) ir.Type {
	seen := map[*ir.TypeDef]bool{}
	for {
		named, ok := t.(*ir.Named)
		if !ok || named.Def == nil || named.Def.Body == nil || seen[named.Def] {
			return t
		}
		seen[named.Def] = true
		t = named.Def.Body
	}
}

// armNarrowedScope returns the body scope a match arm is checked in: the arm's
// binding (if any) bound to its resolved member type, so a reference to it inside
// the arm resolves at the narrowed type. A nameless arm or one whose type does
// not resolve narrows nothing. It is the checking-walk twin of the IR lowering's
// NarrowLocal and infer's narrowArmScope.
func armNarrowedScope(bs infer.BodyScope, arm *ast.MatchArm) infer.BodyScope {
	if arm.Bind == "" || arm.Type == nil {
		return bs
	}
	return withLocal(bs, arm.Bind, resolveBodyType(bs, arm.Type))
}

// matchReturns reports whether a match always returns: it must be exhaustive (a
// wildcard, or a union scrutinee every member of which an arm names) and every
// arm body — and the wildcard body — must itself return. The arm bodies are
// checked in the scope where the binding is narrowed, mirroring the checking
// walk, so a return that reads the binding resolves the same way.
func matchReturns(m *ast.MatchStmt, bs infer.BodyScope) bool {
	for _, arm := range m.Arms {
		if !bodyReturns(arm.Body, armNarrowedScope(bs, arm)) {
			return false
		}
	}
	if m.Else != nil {
		return bodyReturns(m.Else, bs)
	}
	// No wildcard: the match returns only if it is exhaustive over the scrutinee's
	// union (every member named).
	members := unionMembers(infer.Body(m.Scrutinee, bs))
	if len(members) == 0 {
		return false
	}
	covered := map[string]bool{}
	for _, arm := range m.Arms {
		if arm.Type == nil {
			continue
		}
		if armT := resolveBodyType(bs, arm.Type); armT != ir.Invalid {
			if key := armT.String(); members[key] {
				covered[key] = true
			}
		}
	}
	return len(covered) == len(members)
}
