package semantic

import (
	"maps"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/lower"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/types/infer"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
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
// order. A type reference resolves against the other declarations in the file
// (so a declaration may refer to a type defined later in the file), extern —
// everything beneath them: the file's imported type definitions over the
// prelude surface — and qualified, the lookup for namespace-qualified names
// (geo.Point; nil when no namespaces are in scope). reg supplies the native
// semantics a refinement predicate types and folds against.
//
// Only the declarations' structure is resolved: the generic parameters and their
// bounds, the defined body type, each method's signature, and the where-clause
// predicate. Method bodies are lowered to IR here (lower.Body) but not
// type-checked.
func resolveTypes(file *ast.File, at func(ast.Node) span, diags *diagnostic.List, reg *builtin.Registry, extern map[string]*ir.TypeDef, qualified func(namespace, name string) *ir.TypeDef, fns bodyFuncs) []*ir.TypeDef {
	if len(file.Types) == 0 {
		return nil
	}

	// First pass: a definition per declaration, by name, so references (including
	// forward ones) bind before any body is resolved. A redeclared name keeps the
	// first definition and is reported; shadowing an imported name is not a
	// redeclaration.
	defs := make(map[string]*ir.TypeDef, len(file.Types)+len(extern))
	maps.Copy(defs, extern)
	own := make(map[string]bool, len(file.Types))
	out := make([]*ir.TypeDef, len(file.Types))
	for i, td := range file.Types {
		def := &ir.TypeDef{Name: td.Name, Public: td.Public, Doc: td.Doc, Syntax: td}
		// The builtin mark is syntactic (`= builtin`), so set it here: a forward
		// reference to a same-file primitive must already resolve as ir.Builtin
		// (the spelling literals produce), not as a Named of an unmarked shell.
		if _, ok := td.Body.(*ast.BuiltinType); ok {
			def.Builtin = true
		}
		out[i] = def
		if td.Name == "" {
			continue
		}
		if own[td.Name] {
			if at != nil && diags != nil {
				s := at(td)
				diags.Add(newDuplicateDeclarationDiagnostic(s.offset, s.width, td.Name))
			}
		} else {
			own[td.Name] = true
			defs[td.Name] = def
		}
	}

	// Second pass: resolve parameters, body, and method signatures, reporting any
	// unknown type names.
	r := &infer.TypeResolver{Defs: defs, Qualified: qualified, Report: unknownTypeReporter(at, diags)}
	for i, td := range file.Types {
		resolveDecl(r, reg, td, out[i], at, diags, fns)
	}
	return out
}

// resolveDecl fills in def from the declaration: its generic parameters (whose
// names are in scope for the bounds, body, and methods), the body type, the
// method signatures, and the refinement predicate.
func resolveDecl(r *infer.TypeResolver, reg *builtin.Registry, td *ast.TypeDecl, def *ir.TypeDef, at func(ast.Node) span, diags *diagnostic.List, fns bodyFuncs) {
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
	resolveWhere(r, reg, td, def, at, diags)
	// Same-name methods are overloads — legal as long as their parameter
	// types differ. A signature that repeats an earlier one (the same name
	// and the same parameter-type list) is a true redeclaration: the first
	// wins, the repeat is dropped and reported, mirroring how a redeclared
	// type keeps its first definition. The signature key is built from the
	// resolved types, so both resolution passes (the silent memoized one and
	// the reporting one) drop identically.
	seen := make(map[string]bool, len(td.Methods))
	for _, m := range td.Methods {
		rm := resolveMethod(r, m, scope, fns)
		key := rm.Name + signatureKey(def, rm)
		if m.Name != "" && seen[key] {
			if at != nil && diags != nil {
				s := at(m)
				diags.Add(newDuplicateOverloadDiagnostic(s.offset, s.width, rm.Name, paramTypes(rm)))
			}
			continue
		}
		seen[key] = true
		def.Methods = append(def.Methods, rm)
	}
}

// resolveMethod resolves a method's signature (parameter types and result type)
// and lowers its body to IR; fns is the file's function shells by name, so a
// body may call a top-level function. The body is not yet type-checked.
func resolveMethod(r *infer.TypeResolver, m *ast.MethodDecl, scope map[string]bool, fns bodyFuncs) *ir.Method {
	method := &ir.Method{Name: m.Name, Public: m.Public, Extern: m.Extern, Effects: m.Effects, Doc: m.Doc, Syntax: m}

	// Method-introduced type variables: free type names appearing in a parameter
	// type that the enclosing type does not bind and that name no known type — the
	// R in map(func: fn(T): R): list<R>. They join the scope for this method's
	// signature so they resolve to ir.TypeVar instead of being reported unknown.
	// Only parameter positions are scanned: a variable must be inferable from an
	// argument, so an unknown name in the result alone (a typo like `Nope`) stays
	// an unknown-type error rather than becoming a silent, unsolvable variable.
	mscope := scope
	paramTypes := make([]ast.TypeExpr, 0, len(m.Params))
	for _, p := range m.Params {
		paramTypes = append(paramTypes, p.Type)
	}
	if vars := r.FreeTypeVars(scope, paramTypes...); len(vars) > 0 {
		mscope = make(map[string]bool, len(scope)+len(vars))
		for k := range scope {
			mscope[k] = true
		}
		for _, v := range vars {
			mscope[v] = true
		}
	}

	params := make(map[string]bool, len(m.Params))
	for _, p := range m.Params {
		method.Params = append(method.Params, ir.Param{Name: p.Name, Type: r.ResolveType(p.Type, mscope)})
		params[p.Name] = true
	}
	method.Result = r.ResolveType(m.Result, mscope)
	method.Body = lower.Body(m.Body, bodyBinder{r: r, params: params, tscope: mscope, funcs: fns, self: true})
	return method
}

// resolveFuncs resolves the file's function declarations into their identity
// shells, in source order: each signature's parameter and result types (with
// unknown type names reported through the resolver) and the lowered body — a
// method's resolution, minus the receiver. Same-name functions are overloads,
// legal as long as their parameter types differ; a signature that repeats an
// earlier one is reported (duplicate_func_overload) and dropped from the
// module, the first winning, exactly as a duplicate method overload is. The
// shells are filled in place; FuncCall values across the program point at
// them, exactly as References point at the constant shells.
func resolveFuncs(file *ast.File, at func(ast.Node) span, diags *diagnostic.List, universe map[string]*ir.TypeDef, qualified func(namespace, name string) *ir.TypeDef, qualifiedFuncs func(namespace, name string) []*ast.FuncDecl, shells map[*ast.FuncDecl]*ir.Function) []*ir.Function {
	if len(file.Funcs) == 0 {
		return nil
	}
	r := &infer.TypeResolver{Defs: universe, Qualified: qualified, Report: unknownTypeReporter(at, diags)}
	fns := bodyFuncs{local: funcShellsByName(file, shells), qualified: qualifiedFuncs, shells: shells}
	out := make([]*ir.Function, 0, len(file.Funcs))
	seen := make(map[string]bool, len(file.Funcs))
	for _, fd := range file.Funcs {
		fn := shells[fd]
		fn.Extern = fd.Extern
		fn.Effects = fd.Effects
		params := make(map[string]bool, len(fd.Params))
		fn.Params = make([]ir.Param, 0, len(fd.Params))
		for _, p := range fd.Params {
			fn.Params = append(fn.Params, ir.Param{Name: p.Name, Type: r.ResolveType(p.Type, nil)})
			params[p.Name] = true
		}
		fn.Result = r.ResolveType(fd.Result, nil)
		fn.Body = lower.Body(fd.Body, bodyBinder{r: r, params: params, funcs: fns})

		key := fn.Name + funcSignatureKey(fn)
		if fn.Name != "" && seen[key] {
			if at != nil && diags != nil {
				s := at(fd)
				diags.Add(newDuplicateFuncOverloadDiagnostic(s.offset, s.width, fn.Name, paramTypesOf(fn.Params)))
			}
			continue
		}
		seen[key] = true
		out = append(out, fn)
	}
	return out
}

// funcShellsByName indexes a file's function shells by name — each name
// carrying its overload set in source order — for the binders that lower
// calls.
func funcShellsByName(file *ast.File, shells map[*ast.FuncDecl]*ir.Function) map[string][]*ir.Function {
	if file == nil || len(file.Funcs) == 0 {
		return nil
	}
	fns := make(map[string][]*ir.Function, len(file.Funcs))
	for _, fd := range file.Funcs {
		if fd.Name != "" {
			fns[fd.Name] = append(fns[fd.Name], shells[fd])
		}
	}
	return fns
}
