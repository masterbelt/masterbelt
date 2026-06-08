package infer

import (
	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/types"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// TypeResolver resolves a type expression to its ir.Type. Where Expr types a
// value expression, TypeResolver types a type expression — the other syntax-to-
// type direction. It resolves a name against Defs — the file's whole universe:
// its own type declarations shadowing its imports shadowing the prelude — and
// reports an unknown name through Report (nil to report nothing, as when
// resolving the prelude or a constant annotation before diagnostics run).
type TypeResolver struct {
	Defs map[string]*ir.TypeDef
	// Qualified resolves a namespace-qualified name (geo.Point) to the
	// definition the namespace's target exports, or nil. A nil func means no
	// namespaces are in scope (the prelude, a file without imports), so every
	// qualified name is unknown.
	Qualified func(namespace, name string) *ir.TypeDef
	Report    func(node ast.Node, name string)
	// Registry backs the bound check on a generic type application: a parameter
	// declared with a bound (map<K: comparable, V>) is satisfied by an argument
	// only when the argument opts into the bound's interface (types.Satisfies),
	// which reads the registry's impls. A nil Registry disables the check, so a
	// silent or pre-registry resolution (the prelude bootstrap) is unaffected.
	Registry *builtin.Registry
	// BoundViolation fires when a generic type application's argument does not
	// satisfy the parameter's declared bound, anchored at the argument's syntax.
	// It is nil wherever bound violations are not reported (a memoized resolution
	// without diagnostics); the App is still built so typing proceeds.
	BoundViolation func(arg ast.TypeExpr, argType ir.Type, param *ir.TypeParam)
}

func (r *TypeResolver) reportUnknown(node ast.Node, name string) {
	if r.Report != nil {
		r.Report(node, name)
	}
}

// TypeScope is the set of generic parameter names in effect while resolving a
// type expression, each mapped to its declared bound (nil for an unbounded
// parameter, or one whose bound is not yet known). A name's presence in the map
// — checked with comma-ok, not the value — marks it in scope; the bound is what
// the resolver attaches to the TypeVar it returns, so a parameter declared with
// a bound (map<K: comparable, V>) resolves to a TypeVar that already carries it.
// This makes the declaration-site bound check (app) see the parameter's bound
// instead of a free variable, the way BindTypeParamBounds rebinds bounds
// onto a body's parameter types — only here at resolution time, from the start.
type TypeScope = map[string]ir.Type

// ResolveType resolves a type expression to its ir.Type, with scope holding the
// generic parameter names in effect (each mapped to its bound, nil if none). A
// nil or unresolvable type is ir.Invalid.
func (r *TypeResolver) ResolveType(t ast.TypeExpr, scope TypeScope) ir.Type {
	switch t := t.(type) {
	case nil:
		return ir.Invalid
	case *ast.NamedType:
		return r.resolveNamed(t, scope)
	case *ast.UnionType:
		members := make([]ir.Type, len(t.Members))
		for i, m := range t.Members {
			members[i] = r.ResolveType(m, scope)
		}
		return &ir.Union{Members: members}
	case *ast.RecordType:
		fields := make([]ir.Field, len(t.Fields))
		for i, f := range t.Fields {
			fields[i] = ir.Field{Name: f.Name, Type: r.ResolveType(f.Type, scope)}
		}
		return &ir.Record{Fields: fields}
	case *ast.FuncType:
		params := make([]ir.Type, len(t.Params))
		for i, p := range t.Params {
			params[i] = r.ResolveType(p.Type, scope)
		}
		return &ir.Func{Params: params, Result: r.ResolveType(t.Result, scope)}
	default:
		return ir.Invalid
	}
}

// resolveNamed resolves a named type: a namespace-qualified type, the self
// type, a generic parameter in scope, a generic application, a builtin
// primitive, or a reference to a declared type.
func (r *TypeResolver) resolveNamed(t *ast.NamedType, scope TypeScope) ir.Type {
	if t.Namespace != "" {
		return r.resolveQualified(t, scope)
	}
	if t.Name == "self" {
		return &ir.SelfType{}
	}
	if bound, ok := scope[t.Name]; ok && len(t.Args) == 0 {
		return &ir.TypeVar{Name: t.Name, Bound: bound}
	}
	if len(t.Args) > 0 {
		def := r.lookup(t.Name)
		if def == nil {
			r.reportUnknown(t, t.Name)
			return ir.Invalid
		}
		return r.app(def, t.Args, scope)
	}
	def := r.lookup(t.Name)
	if def == nil {
		r.reportUnknown(t, t.Name)
		return ir.Invalid
	}
	if def.Builtin {
		return &ir.Builtin{Name: t.Name}
	}
	return &ir.Named{Def: def}
}

// resolveQualified resolves a namespace-qualified named type (geo.Point): the
// qualifier must name a namespace import and the name one of its target's
// exported types. The qualifier is opaque to the generic scope and the
// registry — only the import surface can satisfy it — and a local type never
// shadows it: types have no members, so the dotted form has exactly one
// meaning.
func (r *TypeResolver) resolveQualified(t *ast.NamedType, scope TypeScope) ir.Type {
	if t.Name == "" {
		return ir.Invalid // a recovered geo. — already a parse diagnostic
	}
	var def *ir.TypeDef
	if r.Qualified != nil {
		def = r.Qualified(t.Namespace, t.Name)
	}
	if def == nil {
		r.reportUnknown(t, t.Namespace+"."+t.Name)
		return ir.Invalid
	}
	if len(t.Args) > 0 {
		return r.app(def, t.Args, scope)
	}
	if def.Builtin {
		return &ir.Builtin{Name: def.Name}
	}
	return &ir.Named{Def: def}
}

// app builds a generic type application (def<args...>), resolving each argument
// in scope and — the type-application bound check — verifying it
// satisfies the matching parameter's declared bound. A parameter with a bound
// (map<K: comparable, V>) is satisfied only by an argument that opts into the
// bound's interface; a violation is reported through BoundViolation (when set)
// against the offending argument's syntax. The check is purely diagnostic: the
// App is built regardless, so typing proceeds with the written type. It applies
// uniformly to every declared generic type, not just map.
//
// An argument that is itself a type parameter (a TypeVar) carries the bound the
// resolver attached from the parameter scope, so the check is exact at the
// declaration site: a parameter declared with the required bound (K: comparable
// used as map<K, V>) satisfies it via Satisfies' interface-inheritance walk,
// while a parameter declared with no bound — or too weak a bound — fails it, the
// first line of defense against an unbounded variable smuggled into a bounded
// constructor (fn g<K>(m: map<K, nint>)).
func (r *TypeResolver) app(def *ir.TypeDef, argExprs []ast.TypeExpr, scope TypeScope) ir.Type {
	args := make([]ir.Type, len(argExprs))
	for i, a := range argExprs {
		args[i] = r.ResolveType(a, scope)
	}
	if r.Registry != nil && r.BoundViolation != nil {
		for i := range argExprs {
			if i >= len(def.Params) {
				break
			}
			p := def.Params[i]
			if p.Bound == nil || args[i] == ir.Invalid {
				continue
			}
			if !types.Satisfies(r.Registry, args[i], p.Bound) {
				r.BoundViolation(argExprs[i], args[i], p)
			}
		}
	}
	return &ir.App{Def: def, Args: args}
}

// ResolveName resolves a bare type name (a conversion's callee) to its type, or
// ir.Invalid if it is not a known type. A name in scope is a generic parameter.
func (r *TypeResolver) ResolveName(name string, scope TypeScope) ir.Type {
	if bound, ok := scope[name]; ok {
		return &ir.TypeVar{Name: name, Bound: bound}
	}
	if def := r.lookup(name); def != nil {
		if def.Builtin {
			return &ir.Builtin{Name: name}
		}
		return &ir.Named{Def: def}
	}
	return ir.Invalid
}

// FreeTypeVars returns the bare type names appearing in ts that are neither in
// scope nor a known type — the implicit type variables a method signature
// introduces (the R in map(func: fn(T): R): list<R>). They are returned in order
// of first appearance, so a caller can add them to the scope before resolving.
func (r *TypeResolver) FreeTypeVars(scope TypeScope, ts ...ast.TypeExpr) []string {
	var out []string
	seen := map[string]bool{}
	var walk func(t ast.TypeExpr)
	walk = func(t ast.TypeExpr) {
		switch t := t.(type) {
		case *ast.NamedType:
			// A qualified name (geo.Point) is never a type variable: it can
			// only mean a namespace's export, and resolves (or is reported)
			// there.
			if _, inScope := scope[t.Name]; t.Namespace == "" && len(t.Args) == 0 && t.Name != "self" && !inScope && !seen[t.Name] && r.lookup(t.Name) == nil {
				seen[t.Name] = true
				out = append(out, t.Name)
			}
			for _, a := range t.Args {
				walk(a)
			}
		case *ast.UnionType:
			for _, m := range t.Members {
				walk(m)
			}
		case *ast.RecordType:
			for _, f := range t.Fields {
				walk(f.Type)
			}
		case *ast.FuncType:
			for _, p := range t.Params {
				walk(p.Type)
			}
			walk(t.Result)
		}
	}
	for _, t := range ts {
		walk(t)
	}
	return out
}

// lookup finds the definition of a type name in the universe. The prelude's
// primitives are part of Defs like every other import, so there is no second
// source to consult.
func (r *TypeResolver) lookup(name string) *ir.TypeDef {
	return r.Defs[name]
}
