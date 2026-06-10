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
// anonymous record, a record alias's body (through any chain of named aliases),
// or a master's row — or nil when the head is not a (resolved) record. A nil
// return is either a fieldless type or a body not resolved yet; project tells
// them apart through the declaration syntax.
func resolvedRecord(head ir.Type, def *ir.TypeDef) *ir.Record {
	if rec := recordOf(head); rec != nil {
		return rec
	}
	if def != nil && def.Master != nil {
		return recordOf(def.Master.Row)
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
