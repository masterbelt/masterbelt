package semantic

import (
	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/lower"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/ast"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/ir"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/types/infer"
)

// unknownTypeReporter builds the callback the type resolver reports an unknown
// type name through, anchoring the diagnostic at the offending node. It returns
// nil when there is nowhere to report (the prelude, which carries no positions),
// so the resolver stays silent.
func unknownTypeReporter(at func(ast.Node) span, diags *diagnostic.List) func(ast.Node, string) {
	if at == nil || diags == nil {
		return nil
	}
	return func(node ast.Node, name string) {
		s := at(node)
		diags.Add(newUnknownTypeDiagnostic(s.offset, s.width, name))
	}
}

// resolveTypes resolves the file's type declarations into ir.TypeDefs, in source
// order. A type reference resolves against the builtin registry (the primitives)
// and the other declarations in the file, so a declaration may refer to a type
// defined later in the file.
//
// Only the declarations' structure is resolved: the generic parameters and their
// bounds, the defined body type, and each method's signature. Method bodies are
// lowered to IR here (lower.Body) but not type-checked.
func resolveTypes(file *ast.File, reg *builtin.Registry, at func(ast.Node) span, diags *diagnostic.List) []*ir.TypeDef {
	if len(file.Types) == 0 {
		return nil
	}

	// First pass: a definition per declaration, by name, so references (including
	// forward ones) bind before any body is resolved. A redeclared name keeps the
	// first definition and is reported.
	defs := make(map[string]*ir.TypeDef, len(file.Types))
	out := make([]*ir.TypeDef, len(file.Types))
	for i, td := range file.Types {
		def := &ir.TypeDef{Name: td.Name, Public: td.Public, Doc: td.Doc}
		out[i] = def
		if td.Name == "" {
			continue
		}
		if _, dup := defs[td.Name]; dup {
			if at != nil && diags != nil {
				s := at(td)
				diags.Add(newDuplicateDeclarationDiagnostic(s.offset, s.width, td.Name))
			}
		} else {
			defs[td.Name] = def
		}
	}

	// Second pass: resolve parameters, body, and method signatures, reporting any
	// unknown type names.
	r := &infer.TypeResolver{Reg: reg, Defs: defs, Report: unknownTypeReporter(at, diags)}
	for i, td := range file.Types {
		resolveDecl(r, td, out[i])
	}
	return out
}

// resolveDecl fills in def from the declaration: its generic parameters (whose
// names are in scope for the bounds, body, and methods), the body type, and the
// method signatures.
func resolveDecl(r *infer.TypeResolver, td *ast.TypeDecl, def *ir.TypeDef) {
	scope := make(map[string]bool, len(td.Params))
	for _, p := range td.Params {
		scope[p.Name] = true
	}
	for _, p := range td.Params {
		var bound ir.Type
		if p.Constraint != nil {
			bound = r.ResolveType(p.Constraint, scope)
		}
		def.Params = append(def.Params, &ir.TypeParam{Name: p.Name, Bound: bound})
	}
	// A `= builtin` body marks a primitive: its type is itself, and its native
	// semantics come from the registry rather than from a defining type.
	if _, ok := td.Body.(*ast.BuiltinType); ok {
		def.Builtin = true
		def.Body = &ir.Builtin{Name: td.Name}
	} else {
		def.Body = r.ResolveType(td.Body, scope)
	}
	for _, m := range td.Methods {
		def.Methods = append(def.Methods, resolveMethod(r, m, scope))
	}
}

// resolveMethod resolves a method's signature (parameter types and result type)
// and lowers its body to IR. The body is not yet type-checked.
func resolveMethod(r *infer.TypeResolver, m *ast.MethodDecl, scope map[string]bool) *ir.Method {
	method := &ir.Method{Name: m.Name, Public: m.Public, Extern: m.Extern}
	params := make(map[string]bool, len(m.Params))
	for _, p := range m.Params {
		method.Params = append(method.Params, ir.Param{Name: p.Name, Type: r.ResolveType(p.Type, scope)})
		params[p.Name] = true
	}
	method.Result = r.ResolveType(m.Result, scope)
	method.Body = lower.Body(m.Body, bodyBinder{r: r, params: params, tscope: scope})
	return method
}
