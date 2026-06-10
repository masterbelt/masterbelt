// This file is the field-type projection of the type resolver: T.member used in
// type position, resolved to member's declared field type (Character.level →
// Level, nominal identity preserved). Only a declared field projects; an
// associated constant, static fn, or enum member is a value, not a type.
// Resolution is lazy — a field's type is read from the declaration's syntax when
// its body is not resolved yet — and guarded against an ungrounded cyclic
// projection so a mutual reference with a concrete floor (Item.level → Level)
// resolves while one without (A.x: B.x ⇄ B.x: A.x) is rejected rather than
// looping.

package infer

import (
	"github.com/masterbelt/masterbelt/pkg/belt/types"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// projKey identifies one field-type-projection step — projecting member off the
// type defined by def — the unit the cycle guard keys on. An anonymous record
// has no def and never keys a cycle: it cannot refer back to itself by name.
type projKey struct {
	def    *ir.TypeDef
	member string
}

// ProjectionErrorKind classifies why a field-type projection (T.member in type
// position) did not resolve to a type.
type ProjectionErrorKind int

// The projection-error kinds, one per way a field-type projection fails.
const (
	ProjMemberNotType      ProjectionErrorKind = iota // the member is a value or method (member_is_not_a_type)
	ProjNoFields                                      // the receiver type has no fields at all (type_has_no_fields)
	ProjUnknownField                                  // a record/master receiver declares no such field (unknown_field)
	ProjCyclic                                        // an ungrounded cyclic projection (cyclic_type_projection)
	ProjGenericUnsupported                            // the receiver is a generic type (generic_type_projection)
)

// applyProjections projects each segment in turn off the running type, left to
// right: Order.customer.id projects customer off Order, then id off the result.
func (r *TypeResolver) applyProjections(head ir.Type, projections []string, node ast.Node) ir.Type {
	for _, member := range projections {
		head = r.project(head, member, node)
	}
	return head
}

// project resolves a single field-type projection T.member in type position to
// member's declared field type (Character.level → Level). T must be a record or
// a master; member must name one of its declared fields. Anything else is a
// reported error and ir.Invalid: a member that is an associated constant, static
// fn, or enum member is a value not a type (member_is_not_a_type); a record or
// master without the field is unknown_field; a fieldless type (a primitive,
// enum, union) is type_has_no_fields. A field whose type loops back to itself
// with no grounding type is cyclic_type_projection.
//
// Projecting off a generic type (Box<T>.value) is generic_type_projection: it is
// not supported yet — instantiating a parameterised field type belongs to the
// generics work — so it is reported rather than resolved to an unbound parameter.
func (r *TypeResolver) project(head ir.Type, member string, node ast.Node) ir.Type {
	if head == nil || head == ir.Invalid {
		return ir.Invalid // the head already failed and was reported; do not cascade
	}
	def := r.projectionDef(head)
	// A generic application (Box<string>) instantiates the projected field: the
	// field's parameterised type is read with the application's arguments
	// substituted for the definition's parameters (Box<string>.value -> string),
	// composing substitutions through an alias of an application (Box<T> =
	// Inner<T>). A bare generic type with no application falls past this to the
	// generic_type_projection guard below, having no arguments to substitute.
	if app, ok := head.(*ir.App); ok {
		return r.projectApp(app, def, member, node)
	}
	if def != nil && len(def.Params) > 0 {
		r.reportProjection(node, ProjGenericUnsupported, head, member)
		return ir.Invalid
	}
	// A resolved record — a record alias's body, an anonymous record, or a
	// master's row — yields the field's declared type directly.
	if rec := resolvedRecord(head, def); rec != nil {
		if f := fieldNamed(rec, member); f != nil {
			return f.Type
		}
		return r.failedProjection(node, head, def, member, true)
	}
	// A declared type whose body is not resolved yet — a forward or mutual
	// reference reached mid-pass: resolve the one field's type from the
	// declaration's syntax, guarded so a cycle with no grounding type is caught.
	if def != nil {
		if fieldType, ok := recordFieldSyntax(def, member); ok {
			return r.projectThroughSyntax(def, member, fieldType, node)
		}
		if recordSyntaxOf(def) != nil {
			return r.failedProjection(node, head, def, member, true) // a record, but no such field
		}
	}
	return r.failedProjection(node, head, def, member, false)
}

// projectApp resolves a field-type projection off a generic application
// (Box<string>.value): the instantiated record's field directly when the
// definition's body is settled, or — for a forward-referenced generic whose body
// is not settled yet — the field's type resolved from the declaration syntax in
// the definition's parameter scope, with the application's arguments substituted.
// An application whose record is resolvable by neither (a forward-referenced
// generic alias chain) is generic_type_projection rather than an unbound guess.
func (r *TypeResolver) projectApp(app *ir.App, def *ir.TypeDef, member string, node ast.Node) ir.Type {
	if rec := types.RecordOf(app); rec != nil {
		if f := fieldNamed(rec, member); f != nil {
			return f.Type
		}
		return r.failedProjection(node, app, def, member, true)
	}
	if def != nil {
		if fieldType, ok := recordFieldSyntax(def, member); ok {
			return r.projectGenericThroughSyntax(app, member, fieldType, node)
		}
		// The declaration's record shape is known but has no such field: a missing
		// field (unknown_field), the same diagnostic the resolved generic gives,
		// rather than reporting the receiver as unsupported.
		if recordSyntaxOf(def) != nil {
			return r.failedProjection(node, app, def, member, true)
		}
	}
	r.reportProjection(node, ProjGenericUnsupported, app, member)
	return ir.Invalid
}

// projectThroughSyntax resolves member's field type from def's declaration
// syntax, recording the step in the resolving set so a re-entry — a projection
// chain that comes back to this same (def, member) with no concrete type in
// between — is reported as cyclic rather than recursing forever. A grounded
// chain unwinds normally, clearing the step on the way out.
func (r *TypeResolver) projectThroughSyntax(def *ir.TypeDef, member string, fieldType ast.TypeExpr, node ast.Node) ir.Type {
	key := projKey{def: def, member: member}
	if r.resolving[key] {
		r.reportProjection(node, ProjCyclic, &ir.Named{Def: def}, member)
		return ir.Invalid
	}
	if r.resolving == nil {
		r.resolving = map[projKey]bool{}
	}
	r.resolving[key] = true
	t := r.ResolveType(fieldType, nil)
	delete(r.resolving, key)
	return t
}

// projectGenericThroughSyntax resolves member's field type from the declaration
// syntax of a forward-referenced generic — its parameter scope in effect, so a
// field type written in terms of a parameter (Box<T> = { value: T }) resolves to
// that parameter — then substitutes the application's arguments for the
// definition's parameters (Box<string>.value -> string). The cycle guard keys on
// (def, member) exactly as projectThroughSyntax, so an ungrounded generic
// projection is reported rather than recursing forever.
func (r *TypeResolver) projectGenericThroughSyntax(app *ir.App, member string, fieldType ast.TypeExpr, node ast.Node) ir.Type {
	def := app.Def
	scope, names := r.genericScope(def)
	if len(names) != len(app.Args) {
		return ir.Invalid // an arity mismatch has no consistent substitution
	}
	key := projKey{def: def, member: member}
	if r.resolving[key] {
		r.reportProjection(node, ProjCyclic, &ir.Named{Def: def}, member)
		return ir.Invalid
	}
	if r.resolving == nil {
		r.resolving = map[projKey]bool{}
	}
	r.resolving[key] = true
	t := r.ResolveType(fieldType, scope)
	delete(r.resolving, key)
	r.checkProjectionBounds(app, names, scope, node)
	subst := make(map[string]ir.Type, len(names))
	for i, name := range names {
		subst[name] = app.Args[i]
	}
	return types.Substitute(t, subst)
}

// checkProjectionBounds enforces a forward-referenced generic's parameter bounds
// on a projection's arguments. The normal app bound check is skipped when the
// application is built off a shell with no resolved Params (the forward
// reference), so a violating argument (Box.value<{x: nint}> against
// Box<T: comparable>) would otherwise pass; this re-checks against the bounds
// resolved from the declaration syntax, anchored at the offending argument, so a
// forward projection reports bound_not_satisfied exactly as the resolved one
// does. It is reached only on the forward-reference path, so it never double-
// reports a violation app already caught.
func (r *TypeResolver) checkProjectionBounds(app *ir.App, names []string, scope TypeScope, node ast.Node) {
	if r.Registry == nil || r.BoundViolation == nil {
		return
	}
	for i, name := range names {
		bound := scope[name]
		if bound == nil || i >= len(app.Args) || app.Args[i] == ir.Invalid {
			continue
		}
		if !types.Satisfies(r.Registry, app.Args[i], bound) {
			r.BoundViolation(projectionArgSyntax(node, i), app.Args[i], &ir.TypeParam{Name: name, Bound: bound})
		}
	}
}

// projectionArgSyntax returns the syntax of the i-th generic argument written on
// a projection (Box.value<string> -> the string type expression), for anchoring a
// bound violation. It falls back to the projection node itself when the
// per-argument syntax is not available (a chained or recovered projection).
func projectionArgSyntax(node ast.Node, i int) ast.TypeExpr {
	if nt, ok := node.(*ast.NamedType); ok && i < len(nt.Args) {
		return nt.Args[i]
	}
	if te, ok := node.(ast.TypeExpr); ok {
		return te
	}
	return nil
}

// genericScope is a generic definition's type-parameter scope — each parameter
// name mapped to its bound (nil if unbounded) — and the parameter names in
// declaration order, for resolving a field's type from the declaration syntax of
// a forward-referenced generic before the application's arguments are
// substituted. It prefers the resolved parameters; when the definition is a shell
// reached before its own resolution (a forward reference), it falls back to the
// declaration syntax, resolving each parameter's constraint the same two-pass way
// resolveDecl does so a bounded parameter used in the field type still resolves.
// Both returns are nil for a non-generic definition.
func (r *TypeResolver) genericScope(def *ir.TypeDef) (TypeScope, []string) {
	if len(def.Params) > 0 {
		scope := make(TypeScope, len(def.Params))
		names := make([]string, len(def.Params))
		for i, p := range def.Params {
			scope[p.Name] = p.Bound
			names[i] = p.Name
		}
		return scope, names
	}
	if def.Syntax == nil || len(def.Syntax.Params) == 0 {
		return nil, nil
	}
	syn := def.Syntax.Params
	scope := make(TypeScope, len(syn))
	names := make([]string, len(syn))
	for _, p := range syn {
		scope[p.Name] = nil
	}
	for i, p := range syn {
		var bound ir.Type
		if p.Constraint != nil {
			bound = r.ResolveType(p.Constraint, scope)
		}
		scope[p.Name] = bound
		names[i] = p.Name
	}
	return scope, names
}

// isGenericDef reports whether a definition takes type parameters — read from its
// resolved parameters, or, for a shell not resolved yet (a forward reference),
// from its declaration syntax. A projection off a generic carries its arguments
// to instantiate the field; a non-generic head ignores stray arguments.
func isGenericDef(def *ir.TypeDef) bool {
	return len(def.Params) > 0 || (def.Syntax != nil && len(def.Syntax.Params) > 0)
}

// failedProjection reports the right diagnostic for a projection that named no
// declared field and returns ir.Invalid: a value-or-method member is
// member_is_not_a_type, a record/master missing the field is unknown_field, and
// a fieldless receiver is type_has_no_fields.
func (r *TypeResolver) failedProjection(node ast.Node, head ir.Type, def *ir.TypeDef, member string, hasFields bool) ir.Type {
	switch {
	case def != nil && types.ResolveMember(def, member).Kind != types.MemberNone:
		r.reportProjection(node, ProjMemberNotType, head, member)
	case hasFields:
		r.reportProjection(node, ProjUnknownField, head, member)
	default:
		r.reportProjection(node, ProjNoFields, head, member)
	}
	return ir.Invalid
}

func (r *TypeResolver) reportProjection(node ast.Node, kind ProjectionErrorKind, typ ir.Type, member string) {
	if r.ProjectionError != nil {
		r.ProjectionError(node, kind, typ, member)
	}
}

// projectionDef returns the definition behind a projectable head — a declared
// type (Named), a generic application (App), or a primitive (Builtin, whose
// registry def carries its associated constants and methods, so int8.Max in
// type position is classified as a value rather than an unknown field) — or nil
// for an anonymous record or a type that names no definition.
func (r *TypeResolver) projectionDef(t ir.Type) *ir.TypeDef {
	switch t := t.(type) {
	case *ir.Named:
		return t.Def
	case *ir.App:
		return t.Def
	case *ir.Builtin:
		return r.lookup(t.Name)
	}
	return nil
}

// resolvedRecord returns the resolved record a head projects fields from — an
// anonymous record, a record alias's body (through any chain of named aliases,
// including one whose body is a generic application instantiated with its
// arguments, so a concrete alias of a generic — type StringBox = Box<string> —
// projects in type position exactly as it does in value position), or a master's
// row — or nil when the head is not a (resolved) record. A nil return is either a
// fieldless type or a body not resolved yet; project tells them apart through the
// declaration syntax.
func resolvedRecord(head ir.Type, def *ir.TypeDef) *ir.Record {
	if rec := types.RecordOf(head); rec != nil {
		return rec
	}
	if def != nil && def.Master != nil {
		return types.RecordOf(def.Master.Row)
	}
	return nil
}

// fieldNamed returns the record field of the given name, or nil.
func fieldNamed(rec *ir.Record, member string) *ir.Field {
	for i := range rec.Fields {
		if rec.Fields[i].Name == member {
			return &rec.Fields[i]
		}
	}
	return nil
}

// recordFieldSyntax returns the written type of member in def's record-typed
// declaration body, for the lazy forward-reference resolution — nil, false when
// def's body is not a record syntax or has no such field.
func recordFieldSyntax(def *ir.TypeDef, member string) (ast.TypeExpr, bool) {
	rec := recordSyntaxOf(def)
	if rec == nil {
		return nil, false
	}
	for _, f := range rec.Fields {
		if f.Name == member {
			return f.Type, true
		}
	}
	return nil, false
}

// recordSyntaxOf returns the record-type syntax a declaration's fields come from
// — a type declaration's record body, or a master's row record — for the lazy
// forward-reference resolution, when the body is not resolved yet. It is nil when
// the declaration carries no record syntax. A master is read here too, so a
// same-file master whose row is still a shell (its Master.Row nil) projects a
// declared field exactly as an already-resolved one does.
func recordSyntaxOf(def *ir.TypeDef) *ast.RecordType {
	if def == nil {
		return nil
	}
	if def.Syntax != nil {
		if rec, ok := def.Syntax.Body.(*ast.RecordType); ok {
			return rec
		}
	}
	if def.MasterSyntax != nil {
		if rec, ok := def.MasterSyntax.Record.(*ast.RecordType); ok {
			return rec
		}
	}
	return nil
}
