// This file holds assignability and the structural-type queries it rests on:
// Assignable decides whether one type's values flow into another, reading a
// record's fields (recordType) and a union's members (UnionType).
package types

import (
	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// Assignable reports whether a value of type from may be used where type to is
// expected: the same type, the default integer flowing into any other integer
// type (range-checked at the boundary, so an overflow is reported separately),
// or a member value flowing into a union that carries it.
func Assignable(reg *builtin.Registry, from, to ir.Type) bool {
	if from == to {
		return true
	}
	if isDefaultInt(from) && IsInteger(reg, to) {
		return true
	}
	if u := UnionType(to); u != nil {
		// A union accepts a value of any of its member types; a union-typed
		// value flows in when every member it may hold is accepted. Both sides are
		// read through UnionType, so a nominal alias of a union (type GameValue =
		// Coin | Level) or a generic union alias (optional<int> = int | null)
		// behaves exactly like the bare union it stands for — a member value flows
		// into the named union, and the named union flows where its members are
		// expected.
		if fu := UnionType(from); fu != nil {
			for _, m := range fu.Members {
				if !Assignable(reg, m, u) {
					return false
				}
			}
			return true
		}
		for _, m := range u.Members {
			if Assignable(reg, from, m) {
				return true
			}
		}
		return false
	}
	if x, y, ok := sameAppShape(from, to); ok {
		// list<A> is assignable to list<B> when A is assignable to B (the same,
		// covariant, element-wise rule that lets list<int> flow into list<int8>).
		for i := range x.Args {
			if !Assignable(reg, x.Args[i], y.Args[i]) {
				return false
			}
		}
		return true
	}
	// An interface value flows to an ancestor interface: width subtyping along
	// the inheritance chain (an ordered value is usable where a comparable is
	// expected). Only the declared parent chain is walked — no other variance —
	// so a value typed as an interface keeps exactly the methods the target
	// interface fixes. A non-interface from or to leaves this untouched.
	if tdef := interfaceDefOf(to); tdef != nil && interfaceDefOf(from) != nil {
		var tArgs []ir.Type
		if app, ok := to.(*ir.App); ok {
			tArgs = app.Args
		}
		if interfaceInherits(from, tdef, tArgs, reg, map[*ir.TypeDef]bool{}) {
			return true
		}
	}
	return sameBuiltin(from, to) || sameNamed(from, to)
}

// recordType returns t as a record — the record itself, or the underlying
// record of a nominal type — or nil when t is neither. It looks through a
// nominal type's body once per level, guarding a self-referential definition.
func recordType(t ir.Type) *ir.Record {
	seen := map[*ir.TypeDef]bool{}
	for {
		switch x := t.(type) {
		case *ir.Record:
			return x
		case *ir.Named:
			if x.Def == nil || x.Def.Body == nil || seen[x.Def] {
				return nil
			}
			seen[x.Def] = true
			t = x.Def.Body
		default:
			return nil
		}
	}
}

// UnionType returns t as a union — the union itself, or the underlying union of
// a nominal alias (type GameValue = Coin | Level) — or nil when t is neither. It
// looks through a nominal type's body once per level, guarding a self-referential
// definition, the union twin of recordType. This is what lets a member value
// flow into a *named* union and a named union flow where its members are
// expected, the same way a bare union already does.
//
// A generic union alias is unwrapped through its application too: optional<int>
// (an *ir.App over `type optional<T> = T | null`) reads as the substituted union
// int | null, so the alias rides on exactly the named-union assignability above.
// A non-union application (list<int>, whose body is a builtin) substitutes to a
// non-union body and yields nil, leaving the collection App path untouched.
//
// It is exported so the AST-driven layers (the match exhaustiveness and
// narrowing checks) unwrap a named or generic union alias the same way
// assignability does.
func UnionType(t ir.Type) *ir.Union {
	seen := map[*ir.TypeDef]bool{}
	for {
		switch x := t.(type) {
		case *ir.Union:
			return x
		case *ir.Named:
			if x.Def == nil || x.Def.Body == nil || seen[x.Def] {
				return nil
			}
			seen[x.Def] = true
			t = x.Def.Body
		case *ir.App:
			// A generic alias' body is read with the application's type arguments
			// substituted for the definition's parameters: optional<int> reads
			// `T | null` as `int | null`. A `= builtin` constructor (list, map) has
			// no union body to expand, so it falls through to nil.
			if x.Def == nil || x.Def.Body == nil || x.Def.Builtin || seen[x.Def] {
				return nil
			}
			if len(x.Args) != len(x.Def.Params) {
				return nil
			}
			seen[x.Def] = true
			subst := make(map[string]ir.Type, len(x.Def.Params))
			for i, p := range x.Def.Params {
				subst[p.Name] = x.Args[i]
			}
			t = Substitute(x.Def.Body, subst)
		default:
			return nil
		}
	}
}
