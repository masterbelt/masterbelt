package semantic

import (
	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/eval"
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
func checkLet(s *ast.LetStmt, bs infer.BodyScope, noSelf func(ast.Node), sink *infer.Sink, at func(ast.Node) span, diags *diagnostic.List) infer.BodyScope {
	if s.Value != nil && noSelf != nil {
		checkNoSelf(s.Value, noSelf)
	}

	var typ ir.Type
	switch {
	case s.Type != nil:
		// An annotated let fixes its type; the value is checked against it, so a
		// value that does not fit is reported as an ordinary type mismatch.
		typ = resolveBodyType(bs, s.Type)
		if s.Value != nil {
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
func checkAssign(s *ast.AssignStmt, bs infer.BodyScope, env eval.Env, noSelf func(ast.Node), sink *infer.Sink, at func(ast.Node) span, diags *diagnostic.List) {
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

	id, ok := s.Target.(*ast.Identifier)
	if !ok {
		// A field access (item.field = ...) or any other non-name target is a
		// write to immutable data: there is no let local to update.
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
		c := at(s.Target)
		_, isParam := bs.Params[id.Name]
		switch {
		case isParam:
			// A parameter is immutable; the message points at let, the mutable
			// form, exactly as a const target does.
			diags.Add(newAssignToConstDiagnostic(c.offset, c.width, id.Name))
		case isConstName(env, id):
			diags.Add(newAssignToConstDiagnostic(c.offset, c.width, id.Name))
		default:
			diags.Add(newAssignToUndefinedDiagnostic(c.offset, c.width, id.Name))
		}
		if s.Value != nil {
			infer.CheckBody(s.Value, ir.Invalid, bs, sink)
		}
		return
	}

	// The target is a let local: the new value must stay assignable to its fixed
	// type. The value is synthesized (checked against ir.Invalid), not checked
	// against want — so a mismatch surfaces once, as assign_type_mismatch (which
	// names the local and its fixed type), rather than also as a bare
	// type_mismatch through the sink.
	if s.Value == nil {
		return
	}
	got := infer.CheckBody(s.Value, ir.Invalid, bs, sink)
	if want != ir.Invalid && got != ir.Invalid && !types.Assignable(bs.Reg, got, want) {
		c := at(s.Value)
		diags.Add(newAssignTypeMismatchDiagnostic(c.offset, c.width, id.Name, got.String(), want.String()))
	}
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
// uses — so list<int>, a named type, and a qualified name all resolve. It
// reports nothing (the type names a let already resolved through the body
// binder); an unknown name there yields ir.Invalid.
func resolveBodyType(bs infer.BodyScope, t ast.TypeExpr) ir.Type {
	r := &infer.TypeResolver{Defs: bs.Universe, Qualified: bs.Qualified}
	return r.ResolveType(t, nil)
}

// isConstName reports whether id names a top-level constant — so assigning to it
// is assign_to_const (a const is immutable) rather than assign_to_undefined. It
// resolves through the body's environment, the same lookup a value reference in
// the body folds through; a nil environment (a checking path that folds nothing)
// reports false, leaving the name undefined.
func isConstName(env eval.Env, id *ast.Identifier) bool {
	return env != nil && env.Resolve(id) != nil
}
