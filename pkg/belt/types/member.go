// This file is the single type-member resolver: T.member classified against a
// type's one member namespace. The value lowering, the type checker, and the
// reference diagnostics all read a member through ResolveMember, so exactly one
// member resolution exists — the separate enum-member, associated-constant, and
// static-fn lookups the layers used to each carry are gone, folded into this one
// classifier.

package types

import "github.com/masterbelt/masterbelt/pkg/source/ir"

// MemberKind classifies what a name resolves to in a type's single member
// namespace: an enum member, an associated constant, or a static fn. The three
// share one namespace — a collision across them is rejected at the declaration
// site (the static-decl collision check) — so a name resolves to at most one,
// returned here in a fixed precedence that never hides a second meaning.
type MemberKind int

// The member kinds, one per thing a name resolves to in a type's member
// namespace; see each kind's comment.
const (
	MemberNone   MemberKind = iota // the type declares no member of that name
	MemberEnum                     // an enum member (Member.Index in def.Enum.Members)
	MemberConst                    // an associated constant (Member.Index in def.Consts)
	MemberStatic                   // a static fn (its overload set selected at the call site)
)

// Member is a resolved type member: its kind and, for an enum member or
// associated constant, its index in the owning definition (-1 for a static fn,
// whose overload set the call site selects, and for MemberNone).
type Member struct {
	Kind  MemberKind
	Index int
}

// ResolveMember classifies name against def's single member namespace — the one
// place T.member is resolved, shared by the value lowering, the type checker, and
// the reference diagnostics. An enum member, an associated constant, and a static
// fn are the three member kinds, in that precedence; a collision across them is
// rejected at the declaration site, so the precedence never hides a second
// meaning. A name
// matching none (or a nil def) is MemberNone, which the read path takes as a
// record-field access and the call path as a missing static fn.
func ResolveMember(def *ir.TypeDef, name string) Member {
	if def == nil {
		return Member{Kind: MemberNone, Index: -1}
	}
	if def.Enum != nil {
		for i, m := range def.Enum.Members {
			if m.Name == name {
				return Member{Kind: MemberEnum, Index: i}
			}
		}
	}
	for i, c := range def.Consts {
		if c.Name == name {
			return Member{Kind: MemberConst, Index: i}
		}
	}
	for _, m := range def.Methods {
		if m.Kind == ir.MethodStatic && m.Name == name {
			return Member{Kind: MemberStatic, Index: -1}
		}
	}
	return Member{Kind: MemberNone, Index: -1}
}

// ProjectMemberType returns the declared type a type-position projection
// def.member resolves to, and whether the member exists. It runs the one member
// classifier and maps each kind to the member's declared type: an associated
// constant to its declared type, an enum member to the enum itself (a member is
// a value of the enum), a static fn to its function type, and a record field —
// the kind ResolveMember leaves as MemberNone — to the field's declared type.
// The returned type is the member's type as declared, which may itself be a
// projection the caller resolves in turn. A name the type does not declare
// returns (nil, false), which the caller reports.
func ProjectMemberType(def *ir.TypeDef, name string) (ir.Type, bool) {
	if def == nil {
		return nil, false
	}
	switch m := ResolveMember(def, name); m.Kind {
	case MemberConst:
		return def.Consts[m.Index].Type, true
	case MemberEnum:
		return &ir.Named{Def: def}, true
	case MemberStatic:
		return staticFnType(def, name), true
	case MemberNone:
		return recordFieldType(def, name)
	}
	return nil, false
}

// recordFieldType returns a record field's declared type on def, looking through
// the record a record/alias body or a master's row carries, or (nil, false)
// when def declares no such field.
func recordFieldType(def *ir.TypeDef, name string) (ir.Type, bool) {
	rec := recordOf(def)
	if rec == nil {
		return nil, false
	}
	for _, f := range rec.Fields {
		if f.Name == name {
			return f.Type, true
		}
	}
	return nil, false
}

// recordOf returns the record a type's values conform to: its body when that is
// a record or a one-step alias to one, or a master's row record. It is nil for a
// type with no record shape (a primitive, a union, an enum without fields).
func recordOf(def *ir.TypeDef) *ir.Record {
	t := def.Body
	if def.Master != nil {
		t = def.Master.Row
	}
	if n, ok := t.(*ir.Named); ok && n.Def != nil {
		t = n.Def.Body
	}
	rec, _ := t.(*ir.Record)
	return rec
}

// staticFnType builds the function type of a static fn member — the signature
// the call site reads, projected when the member appears in type position. A
// name that is not a static fn yields Invalid (ProjectMemberType only calls it
// for one).
func staticFnType(def *ir.TypeDef, name string) ir.Type {
	for _, m := range def.Methods {
		if m.Kind == ir.MethodStatic && m.Name == name {
			params := make([]ir.Type, len(m.Params))
			for i, p := range m.Params {
				params[i] = p.Type
			}
			return &ir.Func{Params: params, Result: m.Result}
		}
	}
	return ir.Invalid
}
