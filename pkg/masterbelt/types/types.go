// Package types is masterbelt's type algebra: the rules and operations over a
// type value. ir owns the type representation (ir.Type and its variants); this
// package owns everything that reasons about a type — the classification
// predicates (IsInteger, IsBoolean), the lookup of builtin types by name
// (Lookup), the value-range check (Fits), the operator-method type rules
// (MethodResult), and assignability/compatibility.
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
	"math/big"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/ir"
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
	if b, ok := t.(*ir.Builtin); ok {
		if n, ok := reg.Native(b.Name); ok {
			return n.Fits(v)
		}
	}
	return true
}

// Compatible reports whether an annotation and an initializer's inferred type
// agree: both integer (so the default int adapts to any sized integer), both
// boolean, or the same concrete type — the last of which lets a string (or any
// non-numeric primitive) annotation accept a matching initializer. The value
// range is checked separately (Fits).
func Compatible(reg *builtin.Registry, annotation, expr ir.Type) bool {
	if (IsInteger(reg, annotation) && IsInteger(reg, expr)) ||
		(IsBoolean(reg, annotation) && IsBoolean(reg, expr)) ||
		sameBuiltin(annotation, expr) || sameNamed(annotation, expr) {
		return true
	}
	// Two applications of the same generic constructor agree when their
	// arguments do, so list<int> satisfies a list<int8> annotation (the
	// elements' value ranges are checked separately).
	if x, y, ok := sameAppShape(annotation, expr); ok {
		for i := range x.Args {
			if !Compatible(reg, x.Args[i], y.Args[i]) {
				return false
			}
		}
		return true
	}
	return false
}

// Assignable reports whether a value of type from may be used where type to is
// expected: the same type, or the default integer flowing into any other
// integer type (range-checked at the boundary, so an overflow is reported
// separately).
func Assignable(reg *builtin.Registry, from, to ir.Type) bool {
	if from == to {
		return true
	}
	if isDefaultInt(from) && IsInteger(reg, to) {
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
	return sameBuiltin(from, to) || sameNamed(from, to)
}

// MethodResult is the type rule for a method call: it finds the method on the
// receiver's type, unifies the self-typed operands (so the default integer
// adapts to a sized one), and returns the substituted result type — self for a
// self-returning method, the declared result otherwise. It returns ir.Invalid
// when the method does not exist on the receiver or the operands do not fit,
// which the IR records as an Invalid type.
//
// Because the method signatures come from the registry's type definitions (and,
// once loaded, the prelude's), this one rule covers every operator on every
// primitive — there is no per-operator table.
func MethodResult(reg *builtin.Registry, recv ir.Type, method string, args []ir.Type) ir.Type {
	def := defOf(reg, recv)
	if def == nil {
		return ir.Invalid
	}
	m := findMethod(reg, def, method)
	if m == nil || len(args) != len(m.Params) {
		return ir.Invalid
	}

	// The substitution that instantiates the method's type variables. It starts
	// bound by the receiver's type arguments — a method on list<int> sees T = int
	// — and the per-method variables (the R in map(func: fn(T): R): list<R>) are
	// solved by matching the parameter patterns against the argument types.
	subst := map[string]ir.Type{}
	if app, ok := recv.(*ir.App); ok && app.Def != nil && len(app.Args) == len(app.Def.Params) {
		for i, p := range app.Def.Params {
			subst[p.Name] = app.Args[i]
		}
	}

	operand := recv // the unified type of the receiver and the self-typed args
	for i, p := range m.Params {
		pt := substitute(p.Type, subst)
		if _, isSelf := pt.(*ir.SelfType); isSelf {
			operand = Unify(reg, operand, args[i])
			if operand == ir.Invalid {
				return ir.Invalid
			}
		} else if !match(reg, pt, args[i], subst) {
			return ir.Invalid
		}
	}
	if _, isSelf := m.Result.(*ir.SelfType); isSelf {
		return operand
	}
	return substitute(m.Result, subst)
}

// substitute replaces every bound type variable in t with its binding from
// subst, recursing through the composite types. An unbound variable is left as
// is, so a concrete type (no variables) is returned unchanged.
func substitute(t ir.Type, subst map[string]ir.Type) ir.Type {
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
			args[i] = substitute(a, subst)
		}
		return &ir.App{Def: t.Def, Args: args}
	case *ir.Func:
		params := make([]ir.Type, len(t.Params))
		for i, p := range t.Params {
			params[i] = substitute(p, subst)
		}
		return &ir.Func{Params: params, Result: substitute(t.Result, subst)}
	case *ir.Union:
		members := make([]ir.Type, len(t.Members))
		for i, m := range t.Members {
			members[i] = substitute(m, subst)
		}
		return &ir.Union{Members: members}
	case *ir.Record:
		fields := make([]ir.Field, len(t.Fields))
		for i, f := range t.Fields {
			fields[i] = ir.Field{Name: f.Name, Type: substitute(f.Type, subst)}
		}
		return &ir.Record{Fields: fields}
	default:
		return t
	}
}

// match matches a parameter pattern — which may contain still-unbound method
// type variables — against a concrete argument type, recording each variable it
// solves in subst. A bare variable binds to the argument (and, if already bound,
// must agree); a function or generic-application pattern matches structurally;
// anything else falls back to assignability, the same rule a non-generic
// parameter used before.
func match(reg *builtin.Registry, pattern, arg ir.Type, subst map[string]ir.Type) bool {
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
			if !match(reg, p.Params[i], a.Params[i], subst) {
				return false
			}
		}
		return match(reg, p.Result, a.Result, subst)
	case *ir.App:
		a, ok := arg.(*ir.App)
		if !ok || a.Def != p.Def || len(a.Args) != len(p.Args) {
			return false
		}
		for i := range p.Args {
			if !match(reg, p.Args[i], a.Args[i], subst) {
				return false
			}
		}
		return true
	default:
		return Assignable(reg, arg, pattern)
	}
}

// defOf returns the type definition whose methods apply to a value of type t:
// the registry definition for a builtin, the referent for a named type.
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
	}
	return nil
}

// findMethod looks up a method by name on def, deriving from the underlying type
// when def does not declare it itself: a nominal type (type Level = int8) thus
// inherits the operator methods of its underlying type. The seen set guards
// against a cyclic definition.
func findMethod(reg *builtin.Registry, def *ir.TypeDef, name string) *ir.Method {
	return findMethodSeen(reg, def, name, map[*ir.TypeDef]bool{})
}

func findMethodSeen(reg *builtin.Registry, def *ir.TypeDef, name string, seen map[*ir.TypeDef]bool) *ir.Method {
	if def == nil || seen[def] {
		return nil
	}
	seen[def] = true
	for _, m := range def.Methods {
		if m.Name == name {
			return m
		}
	}
	// Derive from the underlying type, unless this is a primitive (whose body is
	// itself) or has no underlying definition.
	if !def.Builtin {
		if ud := defOf(reg, def.Body); ud != nil {
			return findMethodSeen(reg, ud, name, seen)
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
