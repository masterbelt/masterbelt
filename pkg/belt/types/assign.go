// This file holds assignability and the structural-type queries it rests on:
// Assignable decides whether one type's values flow into another, reading a
// record's fields (recordType) and a union's members (UnionType).

package types

import (
	"github.com/masterbelt/masterbelt/pkg/belt/builtin"
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
		return assignableToUnion(reg, from, u)
	}
	// A base value flows into the nominal type that wraps it: a value whose type is
	// the underlying type itself (a string into type Tag = string, an nint into
	// type Level = int8, a list<string> into type Names = list<string>) adapts to
	// the named wrapper — the same adaptation the integer case has always had,
	// generalized to every builtin base. It fires only when from is not itself a
	// nominal type, so a nominal type never flows into a *different* nominal wrapper
	// of the same base (Celsius does not adapt to Fahrenheit) nor back to its base.
	// The union arm above runs first, so a value flowing into a named union still
	// selects a member rather than being matched against the union body wholesale.
	if adaptsToNamed(reg, from, to) {
		return true
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
	if assignableByInterface(reg, from, to) {
		return true
	}
	// The same generic type variable is assignable to itself — two occurrences of
	// one parameter (max<T>(a: T, b: T): T returns a T where T is expected) are
	// distinct TypeVar pointers, so the identity check above does not catch it; the
	// name is the variable's identity. The interface-inheritance arm above already
	// handled a bounded variable flowing to its bound interface (a T: orderable
	// usable where comparable is expected).
	return sameBuiltin(from, to) || sameNamed(from, to) || sameTypeVar(from, to)
}

// assignableToUnion reports whether a value of type from flows into the union u.
// A union accepts a value of any of its member types; a union-typed value flows
// in when every member it may hold is accepted. The from side is read through
// UnionType, so a nominal alias of a union (type GameValue = Coin | Level) or a
// generic union alias (optional<int> = int | null) behaves exactly like the bare
// union it stands for — a member value flows into the named union, and the named
// union flows where its members are expected.
func assignableToUnion(reg *builtin.Registry, from ir.Type, u *ir.Union) bool {
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

// assignableByInterface reports whether an interface value of type from flows to
// the ancestor interface to: width subtyping along the inheritance chain (an
// ordered value is usable where a comparable is expected). Only the declared
// parent chain is walked — no other variance — so a value typed as an interface
// keeps exactly the methods the target interface fixes. A non-interface from or
// to is false.
func assignableByInterface(reg *builtin.Registry, from, to ir.Type) bool {
	tdef := interfaceDefOf(to)
	if tdef == nil || interfaceDefOf(from) == nil {
		return false
	}
	var tArgs []ir.Type
	if app, ok := to.(*ir.App); ok {
		tArgs = app.Args
	}
	return interfaceInherits(from, tdef, tArgs, reg, map[*ir.TypeDef]bool{})
}

// adaptsToNamed reports whether a base value of type from adapts to the nominal
// type to: to is a nominal type (a *Named whose definition has a body) and from
// is not itself nominal, so from flows in when it is assignable to to's
// underlying type. This is the generalization of the default-integer-into-Level
// rule to every builtin base — a string into type Tag = string, a bool into type
// Flag = bool, a list<string> into type Names = list<string> (the body is an App
// reached through the covariant collection rule).
//
// to is peeled one nominal level at a time, so a chained alias (type B = A, type
// A = string) reaches the base; the visited set keeps a self-referential
// definition finite. The body must be non-nil, which structurally excludes an
// enum or an interface — neither carries a body (their base lives in Enum.Base /
// the interface contract) — so a base value never flows into one. from being
// non-nominal is what keeps a nominal type from adapting to a different nominal
// wrapper of the same base, or back to its own base.
func adaptsToNamed(reg *builtin.Registry, from, to ir.Type) bool {
	if _, ok := from.(*ir.Named); ok {
		return false
	}
	seen := map[*ir.TypeDef]bool{}
	for {
		n, ok := to.(*ir.Named)
		if !ok {
			return false
		}
		if n.Def == nil || n.Def.Body == nil || seen[n.Def] {
			return false
		}
		seen[n.Def] = true
		// A chained alias (type B = A, type A = string) is peeled in this loop, so
		// the guarded seen set spans every level — calling Assignable on a Named body
		// instead would re-enter with a fresh guard and a self-referential definition
		// (type T = T) would not terminate. A non-Named body is the base to match.
		if _, body := n.Def.Body.(*ir.Named); body {
			to = n.Def.Body
			continue
		}
		return Assignable(reg, from, n.Def.Body)
	}
}

// UnionSelection is the outcome of choosing which member of a union a value
// flows in as — the member that tags the value, the basis for a match's
// confident dispatch.
type UnionSelection int

const (
	// UnionNotAUnion reports that the expected type is not a union, so member
	// selection does not apply (the value flows in by ordinary assignability).
	UnionNotAUnion UnionSelection = iota
	// UnionNoMember reports that the value is a union but matches none of its
	// members — the type_mismatch case, handled by the caller's existing
	// assignability report.
	UnionNoMember
	// UnionUnique reports that exactly one member accepts the value; Member is it.
	UnionUnique
	// UnionAmbiguous reports that two or more members accept the value with no
	// exact tie-break — the ambiguous_union_member case, resolved by an explicit
	// conversion.
	UnionAmbiguous
)

// SelectUnionMember chooses which member of the union type to a value of static
// type from flows in as, the member that becomes the value's tag. It is the type
// layer's half of the tagged-union rule the value folder mirrors:
//
//   - an exact match wins outright: a member type-identical to from is the one,
//     so an nint literal into nint | error tags nint and the V | error /
//     optional<T> code that already type-checks keeps tagging the same member;
//   - failing an exact match, a single assignable member is chosen (a default-int
//     literal into short | error has one integer member);
//   - no assignable member is UnionNoMember (the caller's type_mismatch);
//   - two or more assignable members with no exact match is UnionAmbiguous (short
//     | byte and an nint literal) — the ambiguous_union_member diagnostic, fixed
//     by an explicit conversion (short(1)) that makes from exact.
//
// to that is not a union (read through UnionType, so a nominal or generic alias
// unwraps) is UnionNotAUnion, leaving the ordinary assignability path in charge.
func SelectUnionMember(reg *builtin.Registry, from, to ir.Type) (UnionSelection, ir.Type) {
	u := UnionType(to)
	if u == nil {
		return UnionNotAUnion, nil
	}
	// An exact (type-identical) member is the unambiguous choice, even when other
	// members would also accept the value: a member written as the value's own
	// type pins it. This is what keeps an nint literal into nint | error on nint
	// and an explicit conversion (short(1)) on short.
	for _, m := range u.Members {
		if sameType(from, m) {
			return UnionUnique, m
		}
	}
	var chosen ir.Type
	n := 0
	for _, m := range u.Members {
		if Assignable(reg, from, m) {
			chosen = m
			n++
		}
	}
	switch n {
	case 0:
		return UnionNoMember, nil
	case 1:
		return UnionUnique, chosen
	default:
		return UnionAmbiguous, nil
	}
}

// Identical reports whether two types are structurally identical — the same
// builtin, the same definition, the same application, member-wise the same
// union — the exported reading of the tagging rule's sameType. It is identity,
// not assignability: nint is not identical to short, which is exactly what an
// adaption-detection caller (the Adapt write-back) wants to know.
func Identical(a, b ir.Type) bool {
	return sameType(a, b)
}

// sameType reports whether two types are the same member for tagging purposes:
// the same builtin (by name), the same nominal type (by definition), the same
// generic application, or the same union (member-wise). It is structural
// identity, not assignability — a default-int literal is *not* exact against a
// sized integer (so short | byte and a bare literal stay ambiguous), and a sized
// integer is not the same as a wider one (so int8 | int16 with an int8-typed
// value still selects int8 by exactness, while a bare literal that fits both is
// ambiguous). An nint member against an nint value is exact through sameBuiltin
// directly, which is what keeps nint | error and optional<T> tagging by exactness.
func sameType(a, b ir.Type) bool {
	if a == b {
		return true
	}
	if sameBuiltin(a, b) || sameNamed(a, b) || sameTypeVar(a, b) {
		return true
	}
	if x, y, ok := sameAppShape(a, b); ok {
		for i := range x.Args {
			if !sameType(x.Args[i], y.Args[i]) {
				return false
			}
		}
		return true
	}
	if ua, ub := UnionType(a), UnionType(b); ua != nil && ub != nil {
		if len(ua.Members) != len(ub.Members) {
			return false
		}
		for i := range ua.Members {
			if !sameType(ua.Members[i], ub.Members[i]) {
				return false
			}
		}
		return true
	}
	return false
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
