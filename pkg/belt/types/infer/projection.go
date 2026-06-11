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
	// A generic type parameter (T.member where T: I) projects off its bound: the
	// readable members come from the interface the bound requires, not from a
	// record body, so this is its own path — the bounded twin of the concrete
	// projections below.
	if tv, ok := head.(*ir.TypeVar); ok {
		return r.projectTypeVar(tv, member, node)
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
		return r.projectReadable(head, def, member, node, true) // not a field; a getter, else unknown
	}
	// A declared type whose body is not resolved yet — a forward or mutual
	// reference reached mid-pass: resolve the one field's type from the
	// declaration's syntax, guarded so a cycle with no grounding type is caught.
	if def != nil {
		if fieldType, ok := recordFieldSyntax(def, member); ok {
			return r.projectThroughSyntax(def, member, fieldType, node)
		}
		if recordSyntaxOf(def) != nil {
			return r.projectReadable(head, def, member, node, true) // a record, but no such field
		}
	}
	return r.projectReadable(head, def, member, node, false)
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
		return r.projectReadable(app, def, member, node, true) // not a field; a getter, else unknown
	}
	if def != nil {
		if fieldType, ok := recordFieldSyntax(def, member); ok {
			return r.projectGenericThroughSyntax(app, member, fieldType, node)
		}
		// A getter declared in the definition's own syntax projects even when the
		// body is an application rather than a record (so recordSyntaxOf is nil): the
		// getter is read and instantiated like the resolved alias-declared getter,
		// not rejected as a forward generic alias. A record shape with no such field
		// routes here too, to the unknown_field its declaration warrants.
		hasRecord := recordSyntaxOf(def) != nil
		if _, _, isGetter := getterDeclSyntax(def, member); isGetter || hasRecord {
			return r.projectReadable(app, def, member, node, hasRecord)
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

// projectGenericThroughSyntax resolves member's written type from the declaration
// syntax of a forward-referenced generic — its parameter scope in effect, so a
// type written in terms of a parameter (a field Box<T> = { value: T }, or a
// getter item(): T) resolves to that parameter — then substitutes the
// application's arguments for the definition's parameters (Box<string>.value ->
// string). The cycle guard keys on (def, member) exactly as projectThroughSyntax,
// so an ungrounded generic projection is reported rather than recursing forever.
// It is shared by the field path (directly) and the getter path
// (projectGenericGetterThroughSyntax, which substitutes self atop the result).
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

// projectGenericGetterThroughSyntax is the getter twin of
// projectGenericThroughSyntax: it instantiates a forward-referenced generic
// getter's result type the same way (parameter scope, then the application's
// arguments substituted), then replaces self with the receiver application
// throughout — so a forward generic getter returning self or list<self> projects
// the application (LateBox<long>) or list of it, not a type still carrying the
// receiver-only self marker. The parameter is bound before self, matching the
// resolved path (GetterResultType). The fields' path needs no self substitution
// — a field type carries no self — so only the getter wraps the shared core.
func (r *TypeResolver) projectGenericGetterThroughSyntax(app *ir.App, member string, result ast.TypeExpr, node ast.Node) ir.Type {
	t := r.projectGenericThroughSyntax(app, member, result, node)
	if t == ir.Invalid {
		return ir.Invalid // an arity mismatch or cycle was already handled
	}
	return types.SubstituteSelf(t, app)
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
	for i, p := range syn {
		scope[p.Name] = nil
		names[i] = p.Name
	}
	// Settle the forward generic's bounds the same two-pass way the declaration
	// does, so a bound that projects off another parameter (T: Box<U.x>) resolves
	// here too. Reporting is silenced: these bounds' diagnostics belong to the
	// generic's own declaration, not to a projection that reaches it early.
	report, projErr, boundV, arity := r.Report, r.ProjectionError, r.BoundViolation, r.ArityMismatch
	r.Report, r.ProjectionError, r.BoundViolation, r.ArityMismatch = nil, nil, nil, nil
	SettleBounds(r, syn, scope)
	r.Report, r.ProjectionError, r.BoundViolation, r.ArityMismatch = report, projErr, boundV, arity
	return scope, names
}

// isGenericDef reports whether a definition takes type parameters — read from its
// resolved parameters, or, for a shell not resolved yet (a forward reference),
// from its declaration syntax. A projection off a generic carries its arguments
// to instantiate the field; a non-generic head ignores stray arguments.
func isGenericDef(def *ir.TypeDef) bool {
	return len(def.Params) > 0 || (def.Syntax != nil && len(def.Syntax.Params) > 0)
}

// paramCount is the number of type parameters a definition declares — its
// resolved parameters, or, for a shell not resolved yet (a forward reference),
// the count from its declaration syntax. It is zero for a non-generic type and
// for a builtin generic, whose parameters are not tracked on the def — so a
// caller gating on paramCount > 0 checks only a declared generic's arity, the
// count being the same whichever side of its declaration the use sits on.
func paramCount(def *ir.TypeDef) int {
	if len(def.Params) > 0 {
		return len(def.Params)
	}
	if def.Syntax != nil {
		return len(def.Syntax.Params)
	}
	return 0
}

// failedProjection reports the right diagnostic for a projection that named no
// declared field and returns ir.Invalid: a value-or-method member is
// member_is_not_a_type, a record/master missing the field is unknown_field, and
// a fieldless receiver is type_has_no_fields.
func (r *TypeResolver) failedProjection(node ast.Node, head ir.Type, def *ir.TypeDef, member string, hasFields bool) ir.Type {
	switch {
	case def != nil && (types.ResolveMember(def, member).Kind != types.MemberNone || r.hasMethodMember(head, member)):
		// A value member, not a type: an enum member, associated constant, or
		// static fn (ResolveMember), or a plain method/setter (hasMethodMember,
		// which follows inheritance too). A getter is a readable member and was
		// already projected, so it does not reach here; what remains is a member
		// that is a value, not a type.
		r.reportProjection(node, ProjMemberNotType, head, member)
	case hasFields:
		r.reportProjection(node, ProjUnknownField, head, member)
	default:
		r.reportProjection(node, ProjNoFields, head, member)
	}
	return ir.Invalid
}

// hasMethodMember reports whether head has a callable method of the given name —
// declared directly or inherited — used to classify a projected non-field,
// non-getter member as a value (member_is_not_a_type) rather than an unknown
// field. It reads the registry-backed method lookup, so an inherited method is
// classified the same as a directly-declared one.
func (r *TypeResolver) hasMethodMember(head ir.Type, name string) bool {
	if r.Registry == nil {
		return false
	}
	_, _, ok := types.Candidates(r.Registry, head, name)
	return ok
}

// projectTypeVar resolves a bounded projection T.member: the head is a generic
// type parameter and member a readable member its bound interface requires. The
// projection is the member's required read type — a requirement x: nint projects
// to nint, exactly what reading v.x off a value of type T yields — with self
// resolving to the parameter (a me: self requirement projects to T) and the
// bound's generic arguments substituted (T: Box<string> requiring value: U
// projects to string). It reads the getter result directly, without the
// bare-generic guard projectReadable applies: self off a type parameter is that
// parameter, and a bound written in terms of another in-scope parameter projects
// to it — both legitimate types here, not the free variable that guard defers.
// A member the bound does not require as a readable member falls to the failure
// classification against the bound: a method is member_is_not_a_type, a name the
// bound does not require is unknown_field, and an unbounded parameter — with no
// bound to require anything — has no members at all (type_has_no_fields).
func (r *TypeResolver) projectTypeVar(tv *ir.TypeVar, member string, node ast.Node) ir.Type {
	if tv.Bound != nil && r.Registry != nil {
		if t, ok := types.GetterResultType(r.Registry, tv, member); ok {
			return t
		}
	}
	return r.failedProjection(node, tv, r.projectionDef(tv.Bound), member, tv.Bound != nil)
}

// projectReadable returns a getter projection off head when member names a
// getter — the second readable member, tried after the field paths in project
// did not match — and otherwise reports the field-projection failure. A getter
// on a type declared later (a forward reference whose methods are not attached
// yet) is read from the declaration syntax, mirroring the field forward path. So
// a getter projects to its result type, while a method or unknown name falls to
// the right diagnostic.
func (r *TypeResolver) projectReadable(head ir.Type, def *ir.TypeDef, member string, node ast.Node, hasFields bool) ir.Type {
	if r.Registry != nil {
		// A getter projects to its result type — but a result still carrying a free
		// type variable (a getter reached through an uninstantiated bare generic) is
		// not a concrete type, so it is not projected here; an application supplies
		// the arguments through the forward path below instead.
		if t, ok := types.GetterResultType(r.Registry, head, member); ok && !types.HasTypeVar(t) {
			return t
		}
	}
	if t, ok := r.projectForwardGetter(head, def, member, node); ok {
		return t
	}
	return r.failedProjection(node, head, def, member, hasFields)
}

// projectForwardGetter projects a getter read from def's declaration syntax — the
// forward reference whose methods are not attached to the resolved def yet — and
// reports whether a getter of that name was found (false falls to the field-
// projection failure). A generic application instantiates the getter with its
// arguments through the getter twin of the field's projectGenericThroughSyntax,
// supplying what the bare-generic guard in getterResultSyntax has none of; a
// non-application head takes the non-generic forward path, where a self-returning
// getter projects the receiver and any other result has the receiver substituted
// for self throughout (so list<self> projects to list<receiver>).
func (r *TypeResolver) projectForwardGetter(head ir.Type, def *ir.TypeDef, member string, node ast.Node) (ir.Type, bool) {
	if def == nil {
		return nil, false
	}
	if app, ok := head.(*ir.App); ok {
		result, _, ok := getterDeclSyntax(def, member)
		if !ok {
			return nil, false
		}
		return r.projectGenericGetterThroughSyntax(app, member, result, node), true
	}
	result, isSelf, ok := getterResultSyntax(def, member)
	if !ok {
		return nil, false
	}
	if isSelf {
		return head, true // a self-returning getter projects the receiver
	}
	return types.SubstituteSelf(r.projectThroughSyntax(def, member, result, node), head), true
}

// getterDeclSyntax returns the declared result type of a getter named member in
// def's declaration syntax — for the forward reference where the getter's method
// is not attached to the resolved def yet — and whether that result is exactly
// the self type. It reads the getter even from a generic declaration: the
// application path supplies the arguments to instantiate it. ok is false when
// def's syntax declares no getter of that name.
func getterDeclSyntax(def *ir.TypeDef, member string) (ast.TypeExpr, bool, bool) {
	methods, _, ok := declSyntaxMethods(def)
	if !ok {
		return nil, false, false
	}
	for _, m := range methods {
		if m.Kind == ast.MethodGetter && m.Name == member {
			if nt, ok := m.Result.(*ast.NamedType); ok && nt.Namespace == "" && nt.Name == selfTypeName {
				return m.Result, true, true
			}
			return m.Result, false, true
		}
	}
	return nil, false, false
}

// getterResultSyntax is getterDeclSyntax restricted to a non-generic declaration.
// A getter on a generic type declared later cannot be projected from syntax on
// the non-application path: a bare generic supplies no arguments to instantiate
// its parameters, so it is deferred — the getter twin of the bare-generic field
// guard. The application path reads the generic getter through getterDeclSyntax
// directly, carrying the arguments to projectGenericGetterThroughSyntax.
func getterResultSyntax(def *ir.TypeDef, member string) (ast.TypeExpr, bool, bool) {
	if _, generic, ok := declSyntaxMethods(def); !ok || generic {
		return nil, false, false
	}
	return getterDeclSyntax(def, member)
}

// declSyntaxMethods returns the impl-block methods carried by a def's declaration
// syntax for the forward-reference paths — a type declaration's, or an enum
// declaration's (an enum carries its syntax on EnumSyntax, not Syntax, so a
// forward getter on an enum declared later is read here too) — together with
// whether the declaration is generic (a type with parameters; an enum never is).
// ok is false when the def carries no declaration syntax to read.
func declSyntaxMethods(def *ir.TypeDef) (methods []*ast.MethodDecl, generic bool, ok bool) {
	switch {
	case def.Syntax != nil:
		return def.Syntax.Methods, len(def.Syntax.Params) > 0, true
	case def.EnumSyntax != nil:
		return def.EnumSyntax.Methods, false, true
	}
	return nil, false, false
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
