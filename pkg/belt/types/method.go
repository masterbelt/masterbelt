// This file is the method-table machinery of the type algebra: resolving a
// method on a receiver (findMethods, ReceiverMethods, Candidates, BindReceiver),
// the operator-method result rule (MethodResult), and overload selection
// (SelectOverload), together with the impl/interface substitution they thread.

package types

import (
	"maps"

	"github.com/masterbelt/masterbelt/pkg/belt/builtin"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// IsMetatypeMethod reports whether name is an instance method of the metatype
// `type` — its equality, eql/neq — given the metatype's definition (the universe
// entry for `type`, or the registry's). A call on a bare type name (Level ==
// long, desugared to Level.eql(long)) resolves to one of these: the receiver is
// the reified type value, not a static-call namespace, so the static-call rule
// and the reference walk both defer such a call to the type-value method path. A
// nil def (no metatype in scope) yields false.
func IsMetatypeMethod(metatype *ir.TypeDef, name string) bool {
	if metatype == nil {
		return false
	}
	for _, m := range metatype.Methods {
		if m.Name == name && m.Kind == ir.MethodNormal {
			return true
		}
	}
	return false
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

// Candidates returns the instance-method overload set of method on the
// receiver's type — every same-name ordinary method the receiver binds, the
// nearest declaring definition shadowing the same name derived from its
// underlying type — together with the substitution the receiver's type arguments
// pin. It reports false when the receiver has no instance method of that name.
// Accessors and static fns live in their own name spaces (Getter, the static
// path), so they are not returned here: an instance-method call resolves only
// against instance methods.
func Candidates(reg *builtin.Registry, recv ir.Type, method string) ([]*ir.Method, map[string]ir.Type, bool) {
	return candidatesOfKind(reg, recv, method, ir.MethodNormal)
}

// Getter returns the getter named name on the receiver's type — the property
// read value.name folds to — together with the receiver's substitution, or
// false when the receiver has no such getter. A getter takes no overloads (it
// has no parameters), so the slice holds at most one method; it is returned as
// a slice for symmetry with Candidates and to carry the (rare) derived-getter
// case a nominal type inherits from its base.
func Getter(reg *builtin.Registry, recv ir.Type, name string) (*ir.Method, map[string]ir.Type, bool) {
	ms, subst, ok := candidatesOfKind(reg, recv, name, ir.MethodGetter)
	if !ok {
		return nil, nil, false
	}
	return ms[0], subst, true
}

// GetterResultType returns the type a getter read recv.name produces: the
// getter's result, with self resolving to the receiver (a getter that returns
// self yields the receiver's type), and the receiver's generic substitution
// applied. ok is false when the receiver declares no getter of that name. It is
// the one place a getter's read/projection type is computed, shared by the
// value-position read and the type-position projection so the two cannot drift.
func GetterResultType(reg *builtin.Registry, recv ir.Type, name string) (ir.Type, bool) {
	m, subst, ok := Getter(reg, recv, name)
	if !ok {
		return nil, false
	}
	if _, isSelf := m.Result.(*ir.SelfType); isSelf {
		return recv, true
	}
	return Substitute(m.Result, subst), true
}

// Setter returns the setter named name on the receiver's type — the accessor a
// property write value.name = v computes the next value through — together with
// the receiver's substitution, or false when the receiver has no such setter. A
// setter takes no overloads (one parameter, result self) in the MVP, so the
// slice holds at most one method.
func Setter(reg *builtin.Registry, recv ir.Type, name string) (*ir.Method, map[string]ir.Type, bool) {
	ms, subst, ok := candidatesOfKind(reg, recv, name, ir.MethodSetter)
	if !ok {
		return nil, nil, false
	}
	return ms[0], subst, true
}

// candidatesOfKind is the kind-filtered overload lookup behind Candidates and
// Getter: it collects the receiver's same-name methods of the given kind,
// shadowing by name within each kind independently (an accessor and an ordinary
// method of one name never shadow each other — they are different name spaces).
func candidatesOfKind(reg *builtin.Registry, recv ir.Type, method string, kind ir.MethodKind) ([]*ir.Method, map[string]ir.Type, bool) {
	def := defOf(reg, recv)
	if def == nil {
		return nil, nil, false
	}
	ms := findMethods(reg, def, method, kind, map[*ir.TypeDef]bool{})
	if len(ms) == 0 {
		return nil, nil, false
	}
	return ms, receiverSubst(reg, recv), true
}

// receiverSubst is the substitution a receiver's type arguments pin: a
// list<int> receiver binds the element parameter T = int. A bounded generic
// type parameter pins the bound interface's parameters from the bound's
// arguments — a receiver typed T where T: foldable<int, int> binds the
// interface's K = int, V = int, so fold's signature reads against them.
//
// A receiver that opts into an interface also binds that interface's own
// parameters from its impl tag: a list<int> with impl foldable<int, T> binds
// the foldable parameters K = int and V = int (T pinned to int by the receiver),
// so a provided method whose signature reads against K or V — keys(): list<K>,
// values(): list<V> — instantiates to the receiver's element type rather than
// leaving K/V free.
func receiverSubst(reg *builtin.Registry, recv ir.Type) map[string]ir.Type {
	if v, ok := recv.(*ir.TypeVar); ok && v.Bound != nil {
		return receiverSubst(reg, v.Bound)
	}
	subst := map[string]ir.Type{}
	def := defOf(reg, recv)
	if app, ok := recv.(*ir.App); ok && def != nil && len(app.Args) == len(def.Params) {
		for i, p := range def.Params {
			subst[p.Name] = app.Args[i]
		}
	}
	addImplSubst(reg, def, subst, map[*ir.TypeDef]bool{})
	return subst
}

// addImplSubst records, into subst, the interface parameters each impl the
// definition opts into binds — composing with what subst already binds, so an
// impl tag foldable<int, T> over a receiver that pinned T = int binds V = int.
// It walks the underlying type as well (a nominal type inherits its base's
// impls), with seen guarding a cyclic definition. An interface parameter only
// appears in a provided method's signature, so adding these bindings never
// disturbs a name the receiver's own parameters already pinned.
func addImplSubst(reg *builtin.Registry, def *ir.TypeDef, subst map[string]ir.Type, seen map[*ir.TypeDef]bool) {
	if def == nil || seen[def] {
		return
	}
	seen[def] = true
	for _, impl := range def.Impls {
		idef := defOf(reg, impl)
		if idef == nil || idef.Interface == nil {
			continue
		}
		app, ok := impl.(*ir.App)
		if !ok || len(app.Args) != len(idef.Params) {
			continue
		}
		for i, p := range idef.Params {
			if _, pinned := subst[p.Name]; pinned {
				continue // a nearer binding wins; do not overwrite it
			}
			subst[p.Name] = Substitute(app.Args[i], subst)
		}
	}
	if !def.Builtin {
		addImplSubst(reg, defOf(reg, def.Body), subst, seen)
	}
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
	return out, receiverSubst(reg, recv), true
}

// defOf returns the type definition whose methods apply to a value of type t:
// the registry definition for a builtin, the referent for a named type, and —
// for a bounded generic type parameter (the T of fn f<T: foldable<int>>) — the
// definition of its bound interface, so the only methods in scope on the
// parameter are the interface's own (a bound fixes T to the interface's
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

// findMethods collects every method named name and of the given kind on def —
// the overload set a call site selects from — deriving from the underlying type
// when def does not declare the name itself: a nominal type (type Level = int8)
// thus inherits the operator methods (and any getters/setters) of its underlying
// type. A definition that declares the name in that kind at all (however many
// overloads) shadows every same-name, same-kind method it would derive; a
// different kind is a different name space, so it neither contributes nor
// shadows. The seen set guards against a cyclic definition.
func findMethods(reg *builtin.Registry, def *ir.TypeDef, name string, kind ir.MethodKind, seen map[*ir.TypeDef]bool) []*ir.Method {
	if def == nil || seen[def] {
		return nil
	}
	seen[def] = true
	var out []*ir.Method
	for _, m := range def.Methods {
		if m.Name == name && m.Kind == kind {
			out = append(out, m)
		}
	}
	if len(out) > 0 {
		return out
	}
	out = append(out, findImplMethods(reg, def, name, kind, seen)...)
	out = append(out, findParentMethods(reg, def, name, kind, seen)...)
	if len(out) > 0 {
		return out
	}
	// Derive from the underlying type, unless this is a primitive (whose body is
	// itself) or has no underlying definition.
	if !def.Builtin {
		if ud := defOf(reg, def.Body); ud != nil {
			return findMethods(reg, ud, name, kind, seen)
		}
	}
	return nil
}

// findImplMethods collects the provided methods of every interface def opts
// into: a type that declares the name directly overrides them, but otherwise the
// interface's default is the method. The required methods are also on the
// interface def, but the implementing type always declares them itself
// (conformance demands it), so they are shadowed by the caller; only the
// provided ones reach here. The interface's own def carries the method
// signatures and bodies, so they resolve through the same overload path.
func findImplMethods(reg *builtin.Registry, def *ir.TypeDef, name string, kind ir.MethodKind, seen map[*ir.TypeDef]bool) []*ir.Method {
	var out []*ir.Method
	for _, impl := range def.Impls {
		if idef := defOf(reg, impl); idef != nil && idef.Interface != nil {
			if ms := findMethods(reg, idef, name, kind, seen); len(ms) > 0 {
				out = append(out, ms...)
			}
		}
	}
	return out
}

// findParentMethods collects the members an interface inherits from its parents
// — required and provided alike — so a method named on an ancestor resolves
// through the child too. This is the contract-implication path for a type
// parameter bounded by a child interface (T: orderable reaching comparable's
// eql): the bound's def carries only orderable's own members, and its parents
// carry the rest. A child that redeclared the name is rejected elsewhere
// (interface_member_override), so the walk does not have to choose between a
// child and an ancestor signature.
func findParentMethods(reg *builtin.Registry, def *ir.TypeDef, name string, kind ir.MethodKind, seen map[*ir.TypeDef]bool) []*ir.Method {
	if def.Interface == nil {
		return nil
	}
	var out []*ir.Method
	for _, parent := range def.Interface.Parents {
		if pdef := interfaceDefOf(parent); pdef != nil {
			if ms := findMethods(reg, pdef, name, kind, seen); len(ms) > 0 {
				out = append(out, ms...)
			}
		}
	}
	return out
}
