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

// FieldProjection returns the declared type of a record field of def — the type
// a field-type projection (T.member) yields, with nominal identity preserved
// (Character.level → Level, not the int8 it unwraps to). def must carry a record
// body (a record alias, or a nominal type over one) or a master row; it reads
// the resolved body, so it is for use after type resolution (the value-position
// projection that produces a type value, where the body is settled). ok is false
// for a member that is not a declared field, or a def with no record at all,
// which the caller takes as a different member reading.
func FieldProjection(def *ir.TypeDef, name string) (ir.Type, bool) {
	rec := recordBody(def, map[*ir.TypeDef]bool{})
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

// recordBody returns the record a definition ultimately carries — its own record
// body, the record of a nominal type it aliases (through the chain), or a
// master's row record — or nil for a def with no record. seen guards a cyclic
// alias chain (reported elsewhere) from looping.
func recordBody(def *ir.TypeDef, seen map[*ir.TypeDef]bool) *ir.Record {
	if def == nil || seen[def] {
		return nil
	}
	seen[def] = true
	switch b := def.Body.(type) {
	case *ir.Record:
		return b
	case *ir.Named:
		return recordBody(b.Def, seen)
	case *ir.App:
		return recordBody(b.Def, seen)
	}
	if def.Master != nil {
		switch row := def.Master.Row.(type) {
		case *ir.Record:
			return row
		case *ir.Named:
			return recordBody(row.Def, seen)
		}
	}
	return nil
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
