package semantic

import (
	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/types"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/types/infer"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// A let introduces a mutable block-local; an assignment reassigns one. The two
// are the body's only mutation — the const-centred boundary holds everywhere
// else (top-level declarations and data stay immutable), so the rules here are
// the gate that keeps mutation confined to a let local:
//
//   - a let must be initialized (missing_initializer); its type is the
//     annotation when written, otherwise the value's inferred type, and is
//     fixed for the binding's life.
//   - an assignment's target must be a let local in scope: a const or parameter
//     is immutable (assign_to_const), an undefined name has no binding
//     (assign_to_undefined), and a field or element of immutable data cannot be
//     assigned at all (immutable_data) — build a new value instead.
//   - a reassignment must stay assignable to the let's fixed type
//     (assign_type_mismatch).

// checkLet validates a let binding and returns the body scope extended with the
// new local, which the statements after the let are checked against. It reports
// a missing initializer, types the value against the annotation (or infers it),
// and binds the local to its settled type so a later reference — and a later
// assignment's type check — resolves through it. A nil diagnostic list (the
// func-literal-types walk) still extends the scope and types the value through
// the sink, but reports no let-specific diagnostics.
func checkLet(s *ast.LetStmt, bs infer.BodyScope, env exprFolder, noSelf func(ast.Node), sink *infer.Sink, at func(ast.Node) span, diags *diagnostic.List) infer.BodyScope {
	if s.Value != nil && noSelf != nil {
		checkNoSelf(s.Value, noSelf)
	}

	var typ ir.Type
	switch {
	case s.Type != nil:
		// An annotated let fixes its type; the value is checked against it, so a
		// value that does not fit is reported as an ordinary type mismatch. A bare
		// member of the annotation's enum (let r: Rarity = Legend) resolves through
		// the checking walk; a name that is not a member is the unknown_enum_member
		// the const path reports, not a bare type mismatch.
		typ = resolveBodyType(bs, s.Type)
		if s.Value != nil {
			reportBareEnumMember(s.Value, enumDefOf(typ), bs, env, at, diags)
			infer.CheckBody(s.Value, typ, bs, sink)
		}
	case s.Value != nil:
		// An inferred let takes the value's synthesized type.
		typ = infer.CheckBody(s.Value, ir.Invalid, bs, sink)
	default:
		typ = ir.Invalid
	}

	if s.Value == nil && diags != nil {
		c := at(s)
		diags.Add(newMissingInitializerDiagnostic(c.offset, c.width, s.Name))
	}

	// A nameless let (recovered away by the parser) cannot be bound or referenced;
	// extend nothing.
	if s.Name == "" {
		return bs
	}
	return withLocal(bs, s.Name, typ)
}

// checkAssign validates a reassignment: its target must be a let local in scope,
// and the new value must stay assignable to that local's fixed type. A non-let
// target is rejected by kind — a const or parameter is immutable, an undefined
// name has no binding, and a field or element access is immutable data. A nil
// diagnostic list suppresses the assignment diagnostics but still types the
// value through the sink.
func checkAssign(s *ast.AssignStmt, bs infer.BodyScope, env exprFolder, noSelf func(ast.Node), sink *infer.Sink, at func(ast.Node) span, diags *diagnostic.List) {
	if s.Value != nil && noSelf != nil {
		checkNoSelf(s.Value, noSelf)
	}
	if diags == nil {
		// Type the value so the func-literal-types walk still sees its errors; the
		// assignment-target diagnostics are the reporting pass's.
		if s.Value != nil {
			infer.CheckBody(s.Value, ir.Invalid, bs, sink)
		}
		return
	}

	if m, ok := s.Target.(*ast.MemberExpr); ok {
		checkSetterAssign(s, m, bs, sink, at, diags)
		return
	}

	id, ok := s.Target.(*ast.Identifier)
	if !ok {
		// Any other non-name target (an index access already desugared away leaves a
		// member or identifier; anything else here) is a write to immutable data:
		// there is no let local to update.
		if s.Target != nil {
			c := at(s.Target)
			diags.Add(newImmutableDataDiagnostic(c.offset, c.width))
		}
		if s.Value != nil {
			infer.CheckBody(s.Value, ir.Invalid, bs, sink)
		}
		return
	}

	want, isLocal := bs.Locals[id.Name]
	if !isLocal {
		reportNonLocalAssign(s, id, bs, env, sink, at, diags)
		return
	}
	checkLocalAssign(s, id, want, bs, env, sink, at, diags)
}

// reportNonLocalAssign reports a reassignment whose name target is not a let
// local: a parameter or const is immutable (the message points at let, the
// mutable form), and any other name has no binding. The value is still typed so
// its own errors surface.
func reportNonLocalAssign(s *ast.AssignStmt, id *ast.Identifier, bs infer.BodyScope, env exprFolder, sink *infer.Sink, at func(ast.Node) span, diags *diagnostic.List) {
	c := at(s.Target)
	_, isParam := bs.Params[id.Name]
	switch {
	case isParam:
		diags.Add(newAssignToConstDiagnostic(c.offset, c.width, id.Name))
	case isConstName(env, id):
		diags.Add(newAssignToConstDiagnostic(c.offset, c.width, id.Name))
	default:
		diags.Add(newAssignToUndefinedDiagnostic(c.offset, c.width, id.Name))
	}
	if s.Value != nil {
		infer.CheckBody(s.Value, ir.Invalid, bs, sink)
	}
}

// checkLocalAssign checks a reassignment to a let local: the new value must
// stay assignable to its fixed type. The value is synthesized (checked against
// ir.Invalid), not checked against want — so a mismatch surfaces once, as
// assign_type_mismatch (which names the local and its fixed type), rather than
// also as a bare type_mismatch through the sink.
func checkLocalAssign(s *ast.AssignStmt, id *ast.Identifier, want ir.Type, bs infer.BodyScope, env exprFolder, sink *infer.Sink, at func(ast.Node) span, diags *diagnostic.List) {
	if s.Value == nil {
		return
	}
	// An empty collection literal has no type of its own — synthesis would
	// leave it Invalid and the rebinding unfoldable — so the local's fixed
	// type reaches in, exactly as a let annotation reaches its initializer:
	// the literal settles (and the typed graph records) the local's mapness,
	// so a reassignment m = [] of a map local stays a map for the fold and
	// the index-write check.
	if lit, ok := s.Value.(*ast.CollectionLit); ok && len(lit.Entries) == 0 && want != ir.Invalid {
		infer.CheckBody(s.Value, want, bs, sink)
		return
	}
	// A bare member of the target's enum (r = Common, where r is a Rarity let)
	// resolves through the local's static type; resolve it here first so the
	// synthesis below does not call a genuine member an unknown name.
	if enumDef := enumDefOf(want); enumDef != nil {
		if checkBareEnumAssign(s, enumDef, want, bs, env, sink, at, diags) {
			return
		}
	}
	got := infer.CheckBody(s.Value, ir.Invalid, bs, sink)
	if want == ir.Invalid || got == ir.Invalid {
		return
	}
	if !types.Assignable(bs.Reg, got, want) {
		c := at(s.Value)
		diags.Add(newAssignTypeMismatchDiagnostic(c.offset, c.width, id.Name, got.String(), want.String()))
		return
	}
	// The reassignment was accepted at the local's fixed type; a differing
	// value type is an implicit adaption (a width settle, a union inflow) the
	// IR makes explicit, exactly as a checked position's is.
	if sink != nil && sink.Adapted != nil && !types.Identical(got, want) {
		sink.Adapted(s.Value, want)
	}
}

// checkBareEnumAssign resolves a bare enum member assigned to an enum-typed
// local: a name that is not a member is the unknown_enum_member the const path
// reports. It returns true when the value was a genuine member (handled here),
// in which case a member flowing into a union local (an alias like
// optional<Rarity>) is the explicit adaption the IR records.
func checkBareEnumAssign(s *ast.AssignStmt, enumDef *ir.TypeDef, want ir.Type, bs infer.BodyScope, env exprFolder, sink *infer.Sink, at func(ast.Node) span, diags *diagnostic.List) bool {
	reportBareEnumMember(s.Value, enumDef, bs, env, at, diags)
	id, ok := s.Value.(*ast.Identifier)
	if !ok || enumIndex(enumDef, id.Name) < 0 {
		return false
	}
	if member := (&ir.Named{Def: enumDef}); sink != nil && sink.Adapted != nil && !types.Identical(member, want) {
		sink.Adapted(s.Value, want)
	}
	return true
}

// checkSetterAssign validates a property write p.name = v: the receiver must be a
// let local (the binding the write rebinds), and its type must declare a setter
// named name. The value is checked against the setter's parameter type — a
// literal pushes into it through the same bidirectional path a method argument
// does. When no setter matches, the write is immutable data, exactly as a field
// write was before accessors. The result type is self (the setter returns the
// next value), so the local's type is unchanged and no reassignment-type check is
// needed.
func checkSetterAssign(s *ast.AssignStmt, m *ast.MemberExpr, bs infer.BodyScope, sink *infer.Sink, at func(ast.Node) span, diags *diagnostic.List) {
	recv, ok := m.Receiver.(*ast.Identifier)
	var want ir.Type
	isLocal := false
	if ok {
		want, isLocal = bs.Locals[recv.Name]
	}
	// Only a let local can be rebound by a property write. A const/parameter/field
	// receiver, or a non-identifier receiver (a chained p.a.b = v), is immutable
	// data — the same finding a plain field write gives.
	var setter *ir.Method
	var subst map[string]ir.Type
	if isLocal && want != ir.Invalid {
		setter, subst, _ = types.Setter(bs.Reg, want, m.Member.Name)
	}
	if setter == nil {
		c := at(s.Target)
		diags.Add(newImmutableDataDiagnostic(c.offset, c.width))
		if s.Value != nil {
			infer.CheckBody(s.Value, ir.Invalid, bs, sink)
		}
		return
	}
	if s.Value == nil {
		return
	}
	// The value is checked against the setter's single parameter type — a self
	// parameter unifies with the receiver, anything else takes its declared type —
	// so a literal pushes in and a mismatch is the ordinary type_mismatch.
	paramT := ir.Invalid
	if len(setter.Params) == 1 {
		paramT = types.Substitute(setter.Params[0].Type, subst)
		if _, isSelf := paramT.(*ir.SelfType); isSelf {
			paramT = want
		}
	}
	infer.CheckBody(s.Value, paramT, bs, sink)
}

// reportBareEnumMember reports a bare identifier under an enum expectation that
// names no member of the enum as unknown_enum_member — the same finding the const
// path's reportRefIssues gives a bare member of a const's annotation. It fires
// only when enumDef is non-nil (the position names an enum) and the value is a
// bare name that resolves to nothing else in scope: a real member resolves, and a
// name that is a parameter, a let local, a top-level function, or a constant is a
// legitimate reference (its own type rules apply), not a mistyped member. A nil
// diagnostic list (the func-literal-types walk) reports nothing.
func reportBareEnumMember(value ast.Expr, enumDef *ir.TypeDef, bs infer.BodyScope, env exprFolder, at func(ast.Node) span, diags *diagnostic.List) {
	if diags == nil || enumDef == nil {
		return
	}
	id, ok := value.(*ast.Identifier)
	if !ok || id.Name == "" {
		return
	}
	if enumIndex(enumDef, id.Name) >= 0 {
		return // a real member: a resolved value, not an unknown one
	}
	if _, isParam := bs.Params[id.Name]; isParam {
		return
	}
	if _, isLocal := bs.Locals[id.Name]; isLocal {
		return
	}
	if _, isFunc := bs.Funcs[id.Name]; isFunc {
		return
	}
	if env.q != nil && env.q.resolve(env.file, id) != nil {
		return // a top-level constant: a legitimate reference
	}
	s := at(id)
	diags.Add(newUnknownEnumMemberDiagnostic(s.offset, s.width, enumDef.Name, id.Name))
}

// withLocal returns a copy of bs whose Locals carries name bound to typ on top
// of the scope's existing locals. The map is copied so the new binding reaches
// only the statements after the let, leaving an outer block's scope untouched —
// which is what gives let block scoping and lets an inner let shadow an outer.
func withLocal(bs infer.BodyScope, name string, typ ir.Type) infer.BodyScope {
	locals := make(map[string]ir.Type, len(bs.Locals)+1)
	for k, v := range bs.Locals {
		locals[k] = v
	}
	locals[name] = typ
	bs.Locals = locals
	return bs
}

// resolveBodyType resolves a let's type annotation against the body's universe
// and namespace-qualified lookup — the same resolution a parameter annotation
// uses — so list<int>, a named type, and a qualified name all resolve. The
// enclosing function's or method's type parameters are in scope (bs.TScope), so a
// match/switch arm or a let in a generic body may name a type parameter T and it
// resolves to a TypeVar carrying its bound rather than an unknown type. It
// reports nothing (the type names a let already resolved through the body
// binder); an unknown name there yields ir.Invalid.
func resolveBodyType(bs infer.BodyScope, t ast.TypeExpr) ir.Type {
	r := &infer.TypeResolver{Defs: bs.Universe, Qualified: bs.Qualified}
	return r.ResolveType(t, bs.TScope)
}

// isConstName reports whether id names a top-level constant — so assigning to it
// is assign_to_const (a const is immutable) rather than assign_to_undefined. It
// resolves through the folder's queries, the same lookup a value reference in
// the body folds through.
func isConstName(env exprFolder, id *ast.Identifier) bool {
	return env.q != nil && env.q.resolve(env.file, id) != nil
}
