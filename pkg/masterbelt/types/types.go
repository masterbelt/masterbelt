// Package types is masterbelt's type algebra: the rules and operations over a
// type value. ir owns the type representation (ir.Type and its variants); this
// package owns everything that reasons about a type — the classification
// predicates (IsInteger, IsBoolean), the lookup of builtin types by name
// (Lookup), the value-range check (Fits), the operator-method type rules
// (MethodResult, built from BindReceiver, Match, and Substitute, which the
// bidirectional checker also drives directly), and
// assignability/compatibility.
//
// There is no "untyped" type: an integer literal has type int (the
// arbitrary-precision integer, which adapts to any sized integer and is
// range-checked at the boundary) and a boolean literal has type bool.
//
// None of these hardcode the set of primitives: every "is this an integer", its
// value range, and the result type of an operator method is derived from the
// builtin registry (package builtin) and the method signatures it carries, so a
// primitive added to the registry and the prelude is understood here with no
// change. The AST-driven half of the type system — inferring a type from an
// expression or declaration — lives in the subpackage types/infer.
package types

import (
	"maps"
	"math/big"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// IsInteger reports whether t is an integer type: an integer builtin (per the
// registry) or a named type whose underlying type is an integer (so a nominal
// type like `type Level = int8` is integer-like and derives int8's operators).
func IsInteger(reg *builtin.Registry, t ir.Type) bool {
	switch t := t.(type) {
	case *ir.Builtin:
		n, ok := reg.Native(t.Name)
		return ok && n.IsInteger()
	case *ir.Named:
		return t.Def != nil && IsInteger(reg, t.Def.Body)
	}
	return false
}

// IsBoolean reports whether t is a boolean type: the boolean builtin or a named
// type whose underlying type is boolean.
func IsBoolean(reg *builtin.Registry, t ir.Type) bool {
	switch t := t.(type) {
	case *ir.Builtin:
		n, ok := reg.Native(t.Name)
		return ok && n.IsBoolean()
	case *ir.Named:
		return t.Def != nil && IsBoolean(reg, t.Def.Body)
	}
	return false
}

// IsString reports whether t is a string type: the string builtin or a named
// type whose underlying type is a string.
func IsString(reg *builtin.Registry, t ir.Type) bool {
	switch t := t.(type) {
	case *ir.Builtin:
		n, ok := reg.Native(t.Name)
		return ok && n.IsString()
	case *ir.Named:
		return t.Def != nil && IsString(reg, t.Def.Body)
	}
	return false
}

// defaultInt is the type of an integer literal: the arbitrary-precision integer
// that adapts to any sized integer type.
const defaultInt = "int"

// isDefaultInt reports whether t is the literal/default integer type, which
// adapts to any other integer type.
func isDefaultInt(t ir.Type) bool {
	b, ok := t.(*ir.Builtin)
	return ok && b.Name == defaultInt
}

// Lookup resolves a type name in the registry (the builtin primitives and,
// once the prelude is installed, its aliases and collections), or false if the
// name is unknown. A primitive resolves to a Builtin, any other definition to a
// Named.
func Lookup(reg *builtin.Registry, name string) (ir.Type, bool) {
	if d, ok := reg.Lookup(name); ok {
		if d.Builtin {
			return &ir.Builtin{Name: name}, true
		}
		return &ir.Named{Def: d}, true
	}
	return ir.Invalid, false
}

// Fits reports whether v is within the value range of type t. Non-integer types
// — and integer types without a fixed range — accept any value.
func Fits(reg *builtin.Registry, t ir.Type, v *big.Int) bool {
	seen := map[*ir.TypeDef]bool{}
	for {
		switch x := t.(type) {
		case *ir.Builtin:
			if n, ok := reg.Native(x.Name); ok {
				return n.Fits(v)
			}
			return true
		case *ir.Named:
			// A named type's range is its underlying type's; the visited set
			// keeps a self-referential definition finite.
			if x.Def == nil || x.Def.Body == nil || seen[x.Def] {
				return true
			}
			seen[x.Def] = true
			t = x.Def.Body
		default:
			return true
		}
	}
}

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
	if u, ok := to.(*ir.Union); ok {
		// A union accepts a value of any of its member types; a union-typed
		// value flows in when every member it may hold is accepted.
		if fu, ok := from.(*ir.Union); ok {
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
	return sameBuiltin(from, to) || sameNamed(from, to)
}

// MethodResult is the type rule for a method call: it selects the one overload
// of the method the argument types fit (SelectOverload), unifying the
// self-typed operands (so the default integer adapts to a sized one), and
// returns the substituted result type — self for a self-returning method, the
// declared result otherwise. It returns ir.Invalid when the method does not
// exist on the receiver, the operands fit no overload, or they fit several
// (ambiguous), which the IR records as an Invalid type.
//
// Because the method signatures come from the registry's type definitions (and,
// once loaded, the prelude's), this one rule covers every operator on every
// primitive — there is no per-operator table.
//
// The AST-driven checker types calls through the same pieces (SelectOverload,
// Match, Substitute) bidirectionally; this purely type-level composition is
// kept for callers that have argument types but no syntax.
func MethodResult(reg *builtin.Registry, recv ir.Type, method string, args []ir.Type) ir.Type {
	matches, _ := SelectOverload(reg, recv, method, args)
	if len(matches) != 1 {
		return ir.Invalid
	}
	sel := matches[0]
	if _, isSelf := sel.Method.Result.(*ir.SelfType); isSelf {
		return sel.Operand
	}
	return Substitute(sel.Method.Result, sel.Subst)
}

// BindReceiver finds a method on the receiver's type and starts the
// substitution that instantiates the method's type variables: it is bound by
// the receiver's type arguments — a method on list<int> sees T = int — while
// the per-method variables (the R in map(func: fn(T): R): list<R>) stay
// unbound for the caller to solve from the arguments (Match). It reports false
// when the receiver has no such method. For an overloaded name it returns the
// first declaration; a caller selecting among overloads wants Candidates or
// SelectOverload instead.
func BindReceiver(reg *builtin.Registry, recv ir.Type, method string) (*ir.Method, map[string]ir.Type, bool) {
	ms, subst, ok := Candidates(reg, recv, method)
	if !ok {
		return nil, nil, false
	}
	return ms[0], subst, true
}

// Candidates returns the overload set of method on the receiver's type — every
// same-name method the receiver binds, the nearest declaring definition
// shadowing the same name derived from its underlying type — together with the
// substitution the receiver's type arguments pin. It reports false when the
// receiver has no method of that name.
func Candidates(reg *builtin.Registry, recv ir.Type, method string) ([]*ir.Method, map[string]ir.Type, bool) {
	def := defOf(reg, recv)
	if def == nil {
		return nil, nil, false
	}
	ms := findMethods(reg, def, method, map[*ir.TypeDef]bool{})
	if len(ms) == 0 {
		return nil, nil, false
	}
	return ms, receiverSubst(recv), true
}

// receiverSubst is the substitution a receiver's type arguments pin: a
// list<int> receiver binds the element parameter T = int. A bounded generic
// type parameter pins the bound interface's parameters from the bound's
// arguments — a receiver typed T where T: foldable<int, int> binds the
// interface's K = int, V = int, so fold's signature reads against them.
func receiverSubst(recv ir.Type) map[string]ir.Type {
	if v, ok := recv.(*ir.TypeVar); ok && v.Bound != nil {
		return receiverSubst(v.Bound)
	}
	subst := map[string]ir.Type{}
	if app, ok := recv.(*ir.App); ok && app.Def != nil && len(app.Args) == len(app.Def.Params) {
		for i, p := range app.Def.Params {
			subst[p.Name] = app.Args[i]
		}
	}
	return subst
}

// Overload is one resolution of an overloaded method call: the selected
// method, the substitution combining the receiver's bindings with what the
// argument matching solved, and the unified self operand — the receiver and
// the self-typed arguments combined, so the default integer adapts.
type Overload struct {
	Method  *ir.Method
	Subst   map[string]ir.Type
	Operand ir.Type
}

// SelectOverload resolves a method call against the receiver's overload set:
// of the same-name methods (Candidates), it keeps those the argument types
// fit. The arity must agree, and each known argument must fit its parameter —
// a self-typed parameter unifies with the receiver (the default integer
// adapts), a parameter pattern with method type variables matches structurally
// (binding them), and any other parameter takes assignability. A nil argument
// type (a function literal the caller checks bidirectionally after selecting)
// fits any parameter.
//
// The call is well-typed exactly when one overload survives: none means no
// overload matches, several mean the call is ambiguous — belt has no
// subtyping, so there is no most-specific tiebreak; the resolution is an
// annotation at the call site, never an implicit priority. found reports
// whether the receiver has the method at all, distinguishing an unknown
// method from an unmatched one.
func SelectOverload(reg *builtin.Registry, recv ir.Type, method string, args []ir.Type) (matches []Overload, found bool) {
	ms, base, ok := Candidates(reg, recv, method)
	if !ok {
		return nil, false
	}
	for _, m := range ms {
		if len(args) != len(m.Params) {
			continue
		}
		subst := maps.Clone(base) // each candidate solves its own variables
		operand := recv           // the unified type of the receiver and the self-typed args
		fits := true
		for i, p := range m.Params {
			if args[i] == nil {
				continue
			}
			pt := Substitute(p.Type, subst)
			if _, isSelf := pt.(*ir.SelfType); isSelf {
				if operand = Unify(reg, operand, args[i]); operand == ir.Invalid {
					fits = false
					break
				}
			} else if !Match(reg, pt, args[i], subst) {
				fits = false
				break
			}
		}
		if fits {
			matches = append(matches, Overload{Method: m, Subst: subst, Operand: operand})
		}
	}
	return matches, true
}

// ReceiverMethods returns every method a receiver type binds — its own and,
// for a nominal type, those derived from its underlying type, nearer
// declarations shadowing derived ones of the same name (a name's overloads at
// one level all appear; a farther level's same name does not) — together with
// the substitution the receiver's type arguments pin (a list<int> receiver
// binds the element parameter). It is the all-methods companion of
// Candidates, for an editor completing a member access.
func ReceiverMethods(reg *builtin.Registry, recv ir.Type) ([]*ir.Method, map[string]ir.Type, bool) {
	def := defOf(reg, recv)
	if def == nil {
		return nil, nil, false
	}

	var out []*ir.Method
	seenDefs := map[*ir.TypeDef]bool{}
	seenNames := map[string]bool{}
	collect := func(d *ir.TypeDef) {
		level := map[string]bool{} // the names this definition declares itself
		for _, m := range d.Methods {
			if !seenNames[m.Name] {
				out = append(out, m)
				level[m.Name] = true
			}
		}
		for name := range level {
			seenNames[name] = true
		}
	}
	for d := def; d != nil && !seenDefs[d]; {
		seenDefs[d] = true
		collect(d)
		// The provided methods of each interface the type opts into, unless a
		// nearer declaration already shadows the name.
		for _, impl := range d.Impls {
			if idef := defOf(reg, impl); idef != nil && idef.Interface != nil && !seenDefs[idef] {
				seenDefs[idef] = true
				collect(idef)
			}
		}
		// Derive from the underlying type, exactly as findMethods does.
		if d.Builtin {
			break
		}
		d = defOf(reg, d.Body)
	}
	return out, receiverSubst(recv), true
}

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
	case *ir.App:
		a, ok := arg.(*ir.App)
		if !ok || a.Def != p.Def || len(a.Args) != len(p.Args) {
			return false
		}
		for i := range p.Args {
			if !Match(reg, p.Args[i], a.Args[i], subst) {
				return false
			}
		}
		return true
	case *ir.Record:
		// A concrete record pattern (no variable to solve) keeps the old
		// assignability rule, so nothing about the non-generic path changes; a
		// pattern carrying a variable ({ v: T }) is matched field-by-field by
		// name, each pattern field's pattern against the argument's same-named
		// field — mirroring Substitute's field-wise recursion, so a variable
		// introduced inside a record parameter is also solved from the argument.
		// The argument may be a nominal record, looked through to its fields.
		if !hasFreeVar(p, subst) {
			return Assignable(reg, arg, pattern)
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
	case *ir.Union:
		// A concrete union pattern (no variable to solve) keeps the old
		// assignability rule — which already accepts a member value, a reordered
		// union, or a narrower union — so the non-generic path is unchanged. A
		// pattern carrying a variable (T | error — the central unwrap use-case)
		// solves it: a same-arity union argument pairs positionally (int | error
		// binds T = int), and any other argument is one member's value flowing in,
		// matched against the members concrete-first so an error matches the error
		// member without binding T while an int binds T = int.
		if !hasFreeVar(p, subst) {
			return Assignable(reg, arg, pattern)
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
	default:
		return Assignable(reg, arg, pattern)
	}
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

// Satisfies reports whether the concrete type typ satisfies the interface bound
// — the nominal-satisfaction rule of E-16/E-17: typ's definition must opt into
// the bound's interface at its own definition site (an entry in its Impls), and
// the impl's type arguments must agree with the bound's. A bound that is not an
// interface, or a type with no matching impl, does not satisfy. The check looks
// through a nominal type to its underlying definition's impls, so an alias of a
// foldable type is itself foldable.
func Satisfies(reg *builtin.Registry, typ, bound ir.Type) bool {
	idef := interfaceDefOf(bound)
	if idef == nil {
		return false
	}
	var bArgs []ir.Type
	if app, ok := bound.(*ir.App); ok {
		bArgs = app.Args
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

// defOf returns the type definition whose methods apply to a value of type t:
// the registry definition for a builtin, the referent for a named type, and —
// for a bounded generic type parameter (the T of fn f<T: foldable<int>>) — the
// definition of its bound interface, so the only methods in scope on the
// parameter are the interface's own (E-17: a bound fixes T to the interface's
// methods). An unbounded type parameter has no methods.
func defOf(reg *builtin.Registry, t ir.Type) *ir.TypeDef {
	switch t := t.(type) {
	case *ir.Builtin:
		if d, ok := reg.Lookup(t.Name); ok {
			return d
		}
	case *ir.Named:
		return t.Def
	case *ir.App:
		// A generic application (list<int>) carries the methods of its
		// constructor; the type arguments are bound in MethodResult.
		return t.Def
	case *ir.TypeVar:
		if t.Bound != nil {
			return interfaceDefOf(t.Bound)
		}
	}
	return nil
}

// interfaceDefOf returns the interface definition a bound resolved to, or nil
// when the bound is not an interface. A bound written with type arguments
// (foldable<int, int>) is an App; a bare one (comparable) is a Named.
func interfaceDefOf(t ir.Type) *ir.TypeDef {
	switch t := t.(type) {
	case *ir.App:
		if t.Def != nil && t.Def.Interface != nil {
			return t.Def
		}
	case *ir.Named:
		if t.Def != nil && t.Def.Interface != nil {
			return t.Def
		}
	}
	return nil
}

// findMethods collects every method named name on def — the overload set a
// call site selects from — deriving from the underlying type when def does not
// declare the name itself: a nominal type (type Level = int8) thus inherits
// the operator methods of its underlying type. A definition that declares the
// name at all (however many overloads) shadows every same-name method it
// would derive. The seen set guards against a cyclic definition.
func findMethods(reg *builtin.Registry, def *ir.TypeDef, name string, seen map[*ir.TypeDef]bool) []*ir.Method {
	if def == nil || seen[def] {
		return nil
	}
	seen[def] = true
	var out []*ir.Method
	for _, m := range def.Methods {
		if m.Name == name {
			out = append(out, m)
		}
	}
	if len(out) > 0 {
		return out
	}
	// An interface the type opts into supplies its provided methods: a type that
	// declares the name directly (above) overrides them, but otherwise the
	// interface's default is the method. The required methods are also on the
	// interface def, but the implementing type always declares them itself
	// (conformance demands it), so they are shadowed above; only the provided
	// ones reach here. The interface's own def carries the method signatures and
	// bodies, so they resolve through the same overload path.
	for _, impl := range def.Impls {
		if idef := defOf(reg, impl); idef != nil && idef.Interface != nil {
			if ms := findMethods(reg, idef, name, seen); len(ms) > 0 {
				out = append(out, ms...)
			}
		}
	}
	if len(out) > 0 {
		return out
	}
	// Derive from the underlying type, unless this is a primitive (whose body is
	// itself) or has no underlying definition.
	if !def.Builtin {
		if ud := defOf(reg, def.Body); ud != nil {
			return findMethods(reg, ud, name, seen)
		}
	}
	return nil
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
