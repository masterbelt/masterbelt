// This file collects the type queries the rest of the compiler reads a type's
// shape through: the enum a type carries (EnumDef), the element type a for-loop
// iterates (ForElement and its foldable helpers), and the unifier (Unify) with
// its same-shape predicates.
package types

import (
	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// EnumDef returns the enum definition a type carries, or nil when it carries
// none. A nominal enum (Rarity) gives its own definition; any other type is read
// through UnionType, so a union carrying an enum (Rarity | error), a named union
// alias of one, and a generic union alias (optional<Rarity>) all resolve to the
// enum exactly as a bare union does — a member of it being accepted where the
// type is expected. A union with several enums takes the first (a name no enum
// declares stays unresolved at the use site).
//
// It is the single channel a bare enum member resolves through wherever an
// annotation or syntactic static type is the expectation: the const/let/assoc
// initializer, the assignment target, the operator argument, the switch
// scrutinee. It reads only the resolved type — never a folded value — so the
// value query that feeds it stays independent of the type query.
func EnumDef(t ir.Type) *ir.TypeDef {
	if n, ok := t.(*ir.Named); ok && n.Def != nil && n.Def.Enum != nil {
		return n.Def
	}
	if u := UnionType(t); u != nil {
		for _, m := range u.Members {
			if n, ok := m.(*ir.Named); ok && n.Def != nil && n.Def.Enum != nil {
				return n.Def
			}
		}
	}
	return nil
}

// foldableInterfaceName is the prelude interface whose fold every for iterates
// over. A type is iterable exactly when it opts into it.
const foldableInterfaceName = "foldable"

// ForElement reports the type a for binds to its loop variable when iterating
// typ, and whether typ is iterable at all. Iterability is foldable, and a value
// is foldable two ways, both of which for accepts (mirroring how a method call
// reaches the value's fold):
//
//   - typ *is* the foldable interface — written in a type-requirement position
//     (c: foldable<int, V>) or reached through a bounded generic type parameter
//     (the T of fn f<T: foldable<int, V>>, whose bound is the interface). The
//     loop variable's type is read straight from the interface application's
//     arguments, the same arguments the bound fixes the methods against.
//   - typ *opts into* foldable at its definition site — a list<T>, a map<K, V>,
//     or a user type with impl foldable<K, V> (directly or through a nominal
//     type's underlying type). The loop variable's type is the impl's K/V with
//     the receiver's own type arguments substituted (list<int> binds int).
//
// An of-loop binds the value type V, an in-loop the key type K — a list<T> binds
// T for of and int (the index) for in, a map<K, V> binds V for of and K for in.
// ok is false when typ is not foldable (the for's not_iterable diagnostic); the
// element type is ir.Invalid then. It shares the interface-application reading
// Satisfies uses and the impl walk plus receiver substitution receiverSubst uses,
// so it agrees with conformance and method resolution on which types are foldable.
func ForElement(reg *builtin.Registry, typ ir.Type, of bool) (ir.Type, bool) {
	// typ itself is the foldable interface (a requirement-position interface, or a
	// bounded type parameter whose bound is one): take K/V from its arguments.
	if app, ok := foldableApp(typ); ok {
		return foldableArg(app, of), true
	}
	// typ opts into foldable at its definition site: take K/V from the impl, with
	// the receiver's type arguments substituted.
	subst := receiverSubst(reg, typ)
	seen := map[*ir.TypeDef]bool{}
	for def := defOf(reg, typ); def != nil && !seen[def]; {
		seen[def] = true
		for _, impl := range def.Impls {
			if app, ok := foldableApp(impl); ok {
				// The impl's argument carries the receiver's type variables (list<T>
				// impls foldable<int, T>), so substitute the receiver's bindings to
				// reach the concrete element type.
				return Substitute(foldableArg(app, of), subst), true
			}
		}
		if def.Builtin {
			break
		}
		def = defOf(reg, def.Body)
	}
	return ir.Invalid, false
}

// foldableApp returns the foldable<K, V> application t denotes — t itself when it
// is one, the bound's when t is a type parameter bounded by one — and whether t is
// foldable that way. It is the reading the requirement-position and bounded
// type-parameter forms share, the foldable twin of receiverSubst looking through a
// TypeVar's bound.
func foldableApp(t ir.Type) (*ir.App, bool) {
	if v, ok := t.(*ir.TypeVar); ok && v.Bound != nil {
		return foldableApp(v.Bound)
	}
	app, ok := t.(*ir.App)
	if !ok || len(app.Args) != 2 {
		return nil, false
	}
	idef := interfaceDefOf(app)
	if idef == nil || idef.Name != foldableInterfaceName {
		return nil, false
	}
	return app, true
}

// foldableArg picks the loop variable's type from a foldable<K, V> application:
// the value type V (Args[1]) for an of-loop, the key type K (Args[0]) for an
// in-loop.
func foldableArg(app *ir.App, of bool) ir.Type {
	if of {
		return app.Args[1] // V (the value)
	}
	return app.Args[0] // K (the key)
}

// implMatches reports whether an opt-in impl (foldable<int, T>) is the interface
// idef applied to the bound's arguments. The interface must be the same
// definition, and each of the bound's arguments must agree with the impl's
// (assignability, so a default-int bound matches an int8 impl element).
func implMatches(reg *builtin.Registry, impl ir.Type, idef *ir.TypeDef, bArgs []ir.Type) bool {
	if interfaceDefOf(impl) != idef {
		return false
	}
	var iArgs []ir.Type
	if app, ok := impl.(*ir.App); ok {
		iArgs = app.Args
	}
	if len(iArgs) != len(bArgs) {
		return false
	}
	for i := range bArgs {
		if !Assignable(reg, iArgs[i], bArgs[i]) && !Assignable(reg, bArgs[i], iArgs[i]) {
			return false
		}
	}
	return true
}

// Unify combines two operand types: the default integer adapts to the other
// integer operand, two equal types keep that type, and two applications of the
// same generic constructor (list<A> and list<B>) unify element-wise. Anything
// else is a mismatch (ir.Invalid). It is how an integer literal takes the type
// of the sized integer it is combined with, and how a collection literal's
// element type is inferred across its entries.
func Unify(reg *builtin.Registry, a, b ir.Type) ir.Type {
	switch {
	case a == b:
		return a
	case isDefaultInt(a) && IsInteger(reg, b):
		return b
	case isDefaultInt(b) && IsInteger(reg, a):
		return a
	case sameBuiltin(a, b), sameNamed(a, b):
		return a
	}
	if x, y, ok := sameAppShape(a, b); ok {
		args := make([]ir.Type, len(x.Args))
		for i := range args {
			if args[i] = Unify(reg, x.Args[i], y.Args[i]); args[i] == ir.Invalid {
				return ir.Invalid
			}
		}
		return &ir.App{Def: x.Def, Args: args}
	}
	return ir.Invalid
}

func sameBuiltin(a, b ir.Type) bool {
	x, ok := a.(*ir.Builtin)
	if !ok {
		return false
	}
	y, ok := b.(*ir.Builtin)
	return ok && x.Name == y.Name
}

func sameNamed(a, b ir.Type) bool {
	x, ok := a.(*ir.Named)
	if !ok {
		return false
	}
	y, ok := b.(*ir.Named)
	return ok && x.Def == y.Def
}

// sameAppShape reports whether a and b are both applications of the same generic
// constructor with the same number of arguments (e.g. both list<...> with one
// argument), returning the two applications so a caller can relate their
// arguments pairwise.
func sameAppShape(a, b ir.Type) (x, y *ir.App, ok bool) {
	x, oka := a.(*ir.App)
	y, okb := b.(*ir.App)
	if !oka || !okb || x.Def != y.Def || len(x.Args) != len(y.Args) {
		return nil, nil, false
	}
	return x, y, true
}
