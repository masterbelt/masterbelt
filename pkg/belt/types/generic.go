// This file is the generic half of the type algebra: Substitute applies a
// type-variable solution, Match solves variables by structural matching, and
// Satisfies (with hasFreeVar) checks a type against an interface bound.

package types

import (
	"maps"

	"github.com/masterbelt/masterbelt/pkg/belt/builtin"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// Substitute replaces every bound type variable in t with its binding from
// subst, recursing through the composite types. An unbound variable is left as
// is, so a concrete type (no variables) is returned unchanged.
func Substitute(t ir.Type, subst map[string]ir.Type) ir.Type {
	if len(subst) == 0 {
		return t
	}
	switch t := t.(type) {
	case *ir.TypeVar:
		if b, ok := subst[t.Name]; ok {
			return b
		}
		return t
	case *ir.App:
		args := make([]ir.Type, len(t.Args))
		for i, a := range t.Args {
			args[i] = Substitute(a, subst)
		}
		return &ir.App{Def: t.Def, Args: args}
	case *ir.Func:
		params := make([]ir.Type, len(t.Params))
		for i, p := range t.Params {
			params[i] = Substitute(p, subst)
		}
		return &ir.Func{Params: params, Result: Substitute(t.Result, subst)}
	case *ir.Union:
		members := make([]ir.Type, len(t.Members))
		for i, m := range t.Members {
			members[i] = Substitute(m, subst)
		}
		return &ir.Union{Members: members}
	case *ir.Record:
		fields := make([]ir.Field, len(t.Fields))
		for i, f := range t.Fields {
			fields[i] = ir.Field{Name: f.Name, Type: Substitute(f.Type, subst)}
		}
		return &ir.Record{Fields: fields}
	default:
		return t
	}
}

// Match matches a parameter pattern — which may contain still-unbound method
// type variables — against a concrete argument type, recording each variable it
// solves in subst. A bare variable binds to the argument (and, if already bound,
// must agree); a function or generic-application pattern matches structurally;
// anything else falls back to assignability, the same rule a non-generic
// parameter used before.
func Match(reg *builtin.Registry, pattern, arg ir.Type, subst map[string]ir.Type) bool {
	if v, ok := pattern.(*ir.TypeVar); ok {
		if bound, ok := subst[v.Name]; ok {
			return arg == bound || Assignable(reg, arg, bound)
		}
		subst[v.Name] = arg
		return true
	}
	switch p := pattern.(type) {
	case *ir.Func:
		return matchFunc(reg, p, arg, subst)
	case *ir.App:
		return matchApp(reg, p, arg, subst)
	case *ir.Record:
		return matchRecord(reg, p, arg, subst)
	case *ir.Union:
		return matchUnion(reg, p, arg, subst)
	default:
		return Assignable(reg, arg, pattern)
	}
}

// matchFunc matches a function pattern structurally: same parameter count, each
// parameter and the result matched in turn.
func matchFunc(reg *builtin.Registry, p *ir.Func, arg ir.Type, subst map[string]ir.Type) bool {
	a, ok := arg.(*ir.Func)
	if !ok || len(a.Params) != len(p.Params) {
		return false
	}
	for i := range p.Params {
		if !Match(reg, p.Params[i], a.Params[i], subst) {
			return false
		}
	}
	return Match(reg, p.Result, a.Result, subst)
}

// matchApp matches a generic-application pattern. See Match for the rule.
func matchApp(reg *builtin.Registry, p *ir.App, arg ir.Type, subst map[string]ir.Type) bool {
	// Two applications of the same constructor match argument-wise — list<T>
	// against list<int> binds T = int — the structural rule that also solves a
	// generic collection parameter.
	if a, ok := arg.(*ir.App); ok && a.Def == p.Def && len(a.Args) == len(p.Args) {
		for i := range p.Args {
			if !Match(reg, p.Args[i], a.Args[i], subst) {
				return false
			}
		}
		return true
	}
	// A generic union alias (optional<T> = T | null) matches through its
	// expansion: the application's arguments are substituted into the
	// definition's body, and the resulting union solves the same way a bare
	// union pattern does — a member value flowing in (5 into optional<int>)
	// binds the alias' parameter (T = int). A non-union application (list<int>)
	// has no union body, so UnionType returns nil and the match fails, leaving
	// the collection path unchanged.
	if u := UnionType(p); u != nil {
		return Match(reg, u, arg, subst)
	}
	// The constructor did not match (or the argument is not an application).
	// When the pattern carries no free variable left to solve, there is
	// nothing generic to infer here, so fall back to assignability — the same
	// rule the non-generic path uses. This is what lets a child interface flow
	// to a generic parent application (an intBox value to a box<nint> position):
	// the expected box<nint> is a concrete App, the argument intBox a Named, so
	// the constructor check above misses and assignability's interface
	// width-subtyping decides it. The no-free-var guard keeps generic inference
	// untouched — a still-open box<T> never reaches assignability — mirroring
	// the concrete-pattern guards the record and union cases already use.
	if !hasFreeVar(p, subst) {
		return Assignable(reg, arg, p)
	}
	return false
}

// matchRecord matches a record pattern. See Match for the rule.
func matchRecord(reg *builtin.Registry, p *ir.Record, arg ir.Type, subst map[string]ir.Type) bool {
	// A concrete record pattern (no variable to solve) keeps the old
	// assignability rule, so nothing about the non-generic path changes; a
	// pattern carrying a variable ({ v: T }) is matched field-by-field by
	// name, each pattern field's pattern against the argument's same-named
	// field — mirroring Substitute's field-wise recursion, so a variable
	// introduced inside a record parameter is also solved from the argument.
	// The argument may be a nominal record, looked through to its fields.
	if !hasFreeVar(p, subst) {
		return Assignable(reg, arg, p)
	}
	a := recordType(arg)
	if a == nil {
		return false
	}
	fields := make(map[string]ir.Type, len(a.Fields))
	for _, f := range a.Fields {
		fields[f.Name] = f.Type
	}
	for _, pf := range p.Fields {
		af, ok := fields[pf.Name]
		if !ok || !Match(reg, pf.Type, af, subst) {
			return false
		}
	}
	return true
}

// matchUnion matches a union pattern. See Match for the rule.
func matchUnion(reg *builtin.Registry, p *ir.Union, arg ir.Type, subst map[string]ir.Type) bool {
	// A concrete union pattern (no variable to solve) keeps the old
	// assignability rule — which already accepts a member value, a reordered
	// union, or a narrower union — so the non-generic path is unchanged. A
	// pattern carrying a variable (T | error — the central unwrap use-case)
	// solves it: a same-arity union argument pairs positionally (int | error
	// binds T = int), and any other argument is one member's value flowing in,
	// matched against the members concrete-first so an error matches the error
	// member without binding T while an int binds T = int.
	if !hasFreeVar(p, subst) {
		return Assignable(reg, arg, p)
	}
	if a, ok := arg.(*ir.Union); ok && len(a.Members) == len(p.Members) {
		trial := maps.Clone(subst)
		paired := true
		for i := range p.Members {
			if !Match(reg, p.Members[i], a.Members[i], trial) {
				paired = false
				break
			}
		}
		if paired {
			maps.Copy(subst, trial)
			return true
		}
	}
	for _, free := range []bool{false, true} {
		for _, m := range p.Members {
			if hasFreeVar(m, subst) != free {
				continue
			}
			trial := maps.Clone(subst)
			if Match(reg, m, arg, trial) {
				maps.Copy(subst, trial)
				return true
			}
		}
	}
	return false
}

// hasFreeVar reports whether t still contains a type variable not yet bound in
// subst — the part Match could still solve. It guides a union pattern to try its
// solvable members before its concrete ones.
func hasFreeVar(t ir.Type, subst map[string]ir.Type) bool {
	switch t := t.(type) {
	case *ir.TypeVar:
		_, bound := subst[t.Name]
		return !bound
	case *ir.App:
		for _, a := range t.Args {
			if hasFreeVar(a, subst) {
				return true
			}
		}
	case *ir.Func:
		for _, p := range t.Params {
			if hasFreeVar(p, subst) {
				return true
			}
		}
		return hasFreeVar(t.Result, subst)
	case *ir.Union:
		for _, m := range t.Members {
			if hasFreeVar(m, subst) {
				return true
			}
		}
	case *ir.Record:
		for _, f := range t.Fields {
			if hasFreeVar(f.Type, subst) {
				return true
			}
		}
	}
	return false
}

// Satisfies reports whether the type typ satisfies the interface bound — the
// nominal-satisfaction rule, generalized over interface
// inheritance:
//
//   - a concrete type satisfies a bound its definition opts into at its own
//     definition site (an entry in its Impls) with agreeing type arguments. The
//     materialize pass records a type's inherited interfaces on its Impls too, so
//     a type that impls only a child satisfies the child's ancestors here with no
//     extra walk;
//   - an interface (or a type parameter bounded by one) satisfies a bound that is
//     the interface itself or any of its ancestors — the contract implication
//     (orderable means comparable), reached by walking the interface's parent
//     closure. This is the path the TypeVar-bounded forms (T: orderable) take,
//     where there is no concrete Impls list to read.
//
// A bound that is not an interface, or a type with no matching impl or ancestor,
// does not satisfy. The check looks through a nominal type to its underlying
// definition's impls, so an alias of a foldable type is itself foldable; the
// seen set guards a cyclic definition or inheritance graph.
func Satisfies(reg *builtin.Registry, typ, bound ir.Type) bool {
	idef := interfaceDefOf(bound)
	if idef == nil {
		return false
	}
	var bArgs []ir.Type
	if app, ok := bound.(*ir.App); ok {
		bArgs = app.Args
	}
	// An interface type (directly, or as a type parameter's bound) satisfies a
	// bound it is or inherits: walk its parent closure. defOf maps a TypeVar to
	// its bound's interface, so T: orderable reaches orderable here.
	if def := defOf(reg, typ); def != nil && def.Interface != nil {
		ifaceType := typ
		if v, ok := typ.(*ir.TypeVar); ok {
			ifaceType = v.Bound
		}
		if interfaceInherits(ifaceType, idef, bArgs, reg, map[*ir.TypeDef]bool{}) {
			return true
		}
	}
	seen := map[*ir.TypeDef]bool{}
	for def := defOf(reg, typ); def != nil && !seen[def]; {
		seen[def] = true
		for _, impl := range def.Impls {
			if implMatches(reg, impl, idef, bArgs) {
				return true
			}
		}
		if def.Builtin {
			break
		}
		def = defOf(reg, def.Body)
	}
	return false
}

// interfaceInherits reports whether the interface application iface is, or
// inherits, the interface idef applied to bArgs: iface itself matches, or some
// ancestor reached by walking iface's parents (with iface's arguments
// substituted into a generic parent) matches. It is the contract-implication
// walk — orderable inherits comparable — the bound-satisfaction and switch
// checks share. The seen set guards a cyclic inheritance graph.
func interfaceInherits(iface ir.Type, idef *ir.TypeDef, bArgs []ir.Type, reg *builtin.Registry, seen map[*ir.TypeDef]bool) bool {
	def := interfaceDefOf(iface)
	if def == nil || seen[def] {
		return false
	}
	seen[def] = true
	if implMatches(reg, iface, idef, bArgs) {
		return true
	}
	subst := interfaceParamSubst(iface, def)
	for _, parent := range def.Interface.Parents {
		if interfaceInherits(Substitute(parent, subst), idef, bArgs, reg, seen) {
			return true
		}
	}
	return false
}

// interfaceParamSubst maps an interface definition's parameters to an
// application's type arguments, so a generic parent reached through the
// application is read with the application's bindings. A bare interface (a Named,
// or an argument-count mismatch) substitutes nothing.
func interfaceParamSubst(iface ir.Type, def *ir.TypeDef) map[string]ir.Type {
	app, ok := iface.(*ir.App)
	if !ok || len(app.Args) != len(def.Params) {
		return nil
	}
	subst := make(map[string]ir.Type, len(def.Params))
	for i, p := range def.Params {
		subst[p.Name] = app.Args[i]
	}
	return subst
}

// HasTypeVar reports whether t still contains a type variable anywhere in its
// structure — a generic part no context has pinned to a concrete type. It is
// the one walk the checker's inference holes and the interpreter's
// generic-arm refusals both read, so the two cannot drift as composite type
// forms are added.
func HasTypeVar(t ir.Type) bool {
	switch t := t.(type) {
	case *ir.TypeVar:
		return true
	case *ir.App:
		for _, a := range t.Args {
			if HasTypeVar(a) {
				return true
			}
		}
	case *ir.Func:
		for _, p := range t.Params {
			if HasTypeVar(p) {
				return true
			}
		}
		return HasTypeVar(t.Result)
	case *ir.Union:
		for _, m := range t.Members {
			if HasTypeVar(m) {
				return true
			}
		}
	case *ir.Record:
		for _, f := range t.Fields {
			if HasTypeVar(f.Type) {
				return true
			}
		}
	}
	return false
}
