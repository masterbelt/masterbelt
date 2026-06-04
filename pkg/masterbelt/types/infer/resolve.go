package infer

import (
	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/ast"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/ir"
)

// TypeResolver resolves a type expression to its ir.Type. Where Expr types a
// value expression, TypeResolver types a type expression — the other syntax-to-
// type direction. It resolves a name against a set of named definitions (a
// file's own type declarations) and then the builtin registry, and reports an
// unknown name through Report (nil to report nothing, as when resolving the
// prelude or a constant annotation before diagnostics run).
type TypeResolver struct {
	Reg    *builtin.Registry
	Defs   map[string]*ir.TypeDef
	Report func(node ast.Node, name string)
}

func (r *TypeResolver) reportUnknown(node ast.Node, name string) {
	if r.Report != nil {
		r.Report(node, name)
	}
}

// ResolveType resolves a type expression to its ir.Type, with scope holding the
// generic parameter names in effect. A nil or unresolvable type is ir.Invalid.
func (r *TypeResolver) ResolveType(t ast.TypeExpr, scope map[string]bool) ir.Type {
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

// resolveNamed resolves a named type: the self type, a generic parameter in
// scope, a generic application, a builtin primitive, or a reference to a
// declared type.
func (r *TypeResolver) resolveNamed(t *ast.NamedType, scope map[string]bool) ir.Type {
	if t.Name == "self" {
		return &ir.SelfType{}
	}
	if len(t.Args) == 0 && scope[t.Name] {
		return &ir.TypeVar{Name: t.Name}
	}
	if len(t.Args) > 0 {
		def := r.lookup(t.Name)
		if def == nil {
			r.reportUnknown(t, t.Name)
			return ir.Invalid
		}
		args := make([]ir.Type, len(t.Args))
		for i, a := range t.Args {
			args[i] = r.ResolveType(a, scope)
		}
		return &ir.App{Def: def, Args: args}
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

// ResolveName resolves a bare type name (a conversion's callee) to its type, or
// ir.Invalid if it is not a known type. A name in scope is a generic parameter.
func (r *TypeResolver) ResolveName(name string, scope map[string]bool) ir.Type {
	if scope[name] {
		return &ir.TypeVar{Name: name}
	}
	if def := r.lookup(name); def != nil {
		if def.Builtin {
			return &ir.Builtin{Name: name}
		}
		return &ir.Named{Def: def}
	}
	return ir.Invalid
}

// lookup finds the definition of a type name: a named definition first, then a
// builtin primitive.
func (r *TypeResolver) lookup(name string) *ir.TypeDef {
	if def, ok := r.Defs[name]; ok {
		return def
	}
	if def, ok := r.Reg.Lookup(name); ok {
		return def
	}
	return nil
}
