package semantic

import (
	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/ast"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/ir"
)

// resolveTypes resolves the file's type declarations into ir.TypeDefs, in source
// order. A type reference resolves against the builtin registry (the primitives)
// and the other declarations in the file, so a declaration may refer to a type
// defined later in the file.
//
// Only the declarations' structure is resolved: the generic parameters and their
// bounds, the defined body type, and each method's signature. Method bodies are
// not lowered or type-checked here.
func resolveTypes(file *ast.File, reg *builtin.Registry) []*ir.TypeDef {
	if len(file.Types) == 0 {
		return nil
	}

	// First pass: a definition per declaration, by name, so references (including
	// forward ones) bind before any body is resolved.
	defs := make(map[string]*ir.TypeDef, len(file.Types))
	out := make([]*ir.TypeDef, len(file.Types))
	for i, td := range file.Types {
		def := &ir.TypeDef{Name: td.Name, Public: td.Public, Doc: td.Doc}
		out[i] = def
		if td.Name != "" {
			defs[td.Name] = def
		}
	}

	// Second pass: resolve parameters, body, and method signatures.
	r := &typeResolver{reg: reg, defs: defs}
	for i, td := range file.Types {
		r.resolveDecl(td, out[i])
	}
	return out
}

// typeResolver resolves type expressions against the builtin registry and the
// file's own type definitions.
type typeResolver struct {
	reg  *builtin.Registry
	defs map[string]*ir.TypeDef
}

// resolveDecl fills in def from the declaration: its generic parameters (whose
// names are in scope for the bounds, body, and methods), the body type, and the
// method signatures.
func (r *typeResolver) resolveDecl(td *ast.TypeDecl, def *ir.TypeDef) {
	scope := make(map[string]bool, len(td.Params))
	for _, p := range td.Params {
		scope[p.Name] = true
	}
	for _, p := range td.Params {
		var bound ir.Type
		if p.Constraint != nil {
			bound = r.resolveType(p.Constraint, scope)
		}
		def.Params = append(def.Params, &ir.TypeParam{Name: p.Name, Bound: bound})
	}
	// A `= builtin` body marks a primitive: its type is itself, and its native
	// semantics come from the registry rather than from a defining type.
	if _, ok := td.Body.(*ast.BuiltinType); ok {
		def.Builtin = true
		def.Body = &ir.Builtin{Name: td.Name}
	} else {
		def.Body = r.resolveType(td.Body, scope)
	}
	for _, m := range td.Methods {
		def.Methods = append(def.Methods, r.resolveMethod(m, scope))
	}
}

// resolveMethod resolves a method's signature (parameter types and result type)
// and lowers its body to an IR value. The body is not yet type-checked.
func (r *typeResolver) resolveMethod(m *ast.MethodDecl, scope map[string]bool) *ir.Method {
	method := &ir.Method{Name: m.Name, Public: m.Public, Extern: m.Extern}
	params := make(map[string]bool, len(m.Params))
	for _, p := range m.Params {
		method.Params = append(method.Params, ir.Param{Name: p.Name, Type: r.resolveType(p.Type, scope)})
		params[p.Name] = true
	}
	method.Result = r.resolveType(m.Result, scope)
	method.Body = r.lowerBody(m.Body, params, scope)
	return method
}

// lowerBody lowers a method body to an IR value. Only the single-return body
// form is modelled so far: the body lowers to its returned expression's value
// (nil for an extern or empty body). params is the set of parameter names in
// scope, and tscope the generic-parameter names (for type conversions).
func (r *typeResolver) lowerBody(body []ast.Stmt, params, tscope map[string]bool) ir.Value {
	for _, s := range body {
		if ret, ok := s.(*ast.ReturnStmt); ok {
			return r.lowerBodyExpr(ret.Value, params, tscope)
		}
	}
	return nil
}

// lowerBodyExpr lowers a method-body expression to an IR value: self, a
// parameter reference, a literal, a record field access (recv.field), a type
// conversion (T(x), when the callee names a type), or a method call
// (recv.method(args), the form operators also desugar to).
func (r *typeResolver) lowerBodyExpr(e ast.Expr, params, tscope map[string]bool) ir.Value {
	switch e := e.(type) {
	case *ast.SelfExpr:
		return &ir.SelfValue{}
	case *ast.IntLit:
		return &ir.IntLiteral{Text: e.Text}
	case *ast.BoolLit:
		return &ir.BoolLiteral{Value: e.Value}
	case *ast.NullLit:
		return &ir.NullValue{}
	case *ast.Identifier:
		if params[e.Name] {
			return &ir.ParamRef{Name: e.Name}
		}
		return nil
	case *ast.MemberExpr:
		// A member access used as a value is a record field access.
		return &ir.FieldAccess{Receiver: r.lowerBodyExpr(e.Receiver, params, tscope), Field: e.Member.Name}
	case *ast.CallExpr:
		// A call whose callee names a type is a conversion T(x).
		if id, ok := e.Callee.(*ast.Identifier); ok && !params[id.Name] {
			if t := r.resolveNamedName(id.Name, tscope); t != ir.Invalid {
				var arg ir.Value
				if len(e.Arguments) > 0 {
					arg = r.lowerBodyExpr(e.Arguments[0], params, tscope)
				}
				return &ir.Conversion{Type: t, Value: arg}
			}
		}
		// A call whose callee is a member access is a method call.
		if member, ok := e.Callee.(*ast.MemberExpr); ok {
			args := make([]ir.Value, len(e.Arguments))
			for i, a := range e.Arguments {
				args[i] = r.lowerBodyExpr(a, params, tscope)
			}
			return &ir.Call{
				Receiver: r.lowerBodyExpr(member.Receiver, params, tscope),
				Method:   member.Member.Name,
				Args:     args,
			}
		}
		return nil
	default:
		return nil
	}
}

// resolveNamedName resolves a bare type name (a conversion's callee) to its type,
// or ir.Invalid if it is not a known type.
func (r *typeResolver) resolveNamedName(name string, tscope map[string]bool) ir.Type {
	if tscope[name] {
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

// resolveType resolves a type expression to its ir.Type, with scope holding the
// generic parameter names in effect. A nil or unresolvable type is ir.Invalid.
func (r *typeResolver) resolveType(t ast.TypeExpr, scope map[string]bool) ir.Type {
	switch t := t.(type) {
	case nil:
		return ir.Invalid
	case *ast.NamedType:
		return r.resolveNamed(t, scope)
	case *ast.UnionType:
		members := make([]ir.Type, len(t.Members))
		for i, m := range t.Members {
			members[i] = r.resolveType(m, scope)
		}
		return &ir.Union{Members: members}
	case *ast.RecordType:
		fields := make([]ir.Field, len(t.Fields))
		for i, f := range t.Fields {
			fields[i] = ir.Field{Name: f.Name, Type: r.resolveType(f.Type, scope)}
		}
		return &ir.Record{Fields: fields}
	case *ast.FuncType:
		params := make([]ir.Type, len(t.Params))
		for i, p := range t.Params {
			params[i] = r.resolveType(p.Type, scope)
		}
		return &ir.Func{Params: params, Result: r.resolveType(t.Result, scope)}
	default:
		return ir.Invalid
	}
}

// resolveNamed resolves a named type: the self type, a generic parameter in
// scope, a generic application, a builtin primitive, or a reference to a
// declared type.
func (r *typeResolver) resolveNamed(t *ast.NamedType, scope map[string]bool) ir.Type {
	if t.Name == "self" {
		return &ir.SelfType{}
	}
	if len(t.Args) == 0 && scope[t.Name] {
		return &ir.TypeVar{Name: t.Name}
	}
	if len(t.Args) > 0 {
		def := r.lookup(t.Name)
		if def == nil {
			return ir.Invalid
		}
		args := make([]ir.Type, len(t.Args))
		for i, a := range t.Args {
			args[i] = r.resolveType(a, scope)
		}
		return &ir.App{Def: def, Args: args}
	}
	def := r.lookup(t.Name)
	if def == nil {
		return ir.Invalid
	}
	if def.Builtin {
		return &ir.Builtin{Name: t.Name}
	}
	return &ir.Named{Def: def}
}

// lookup finds the definition of a type name: a file declaration first, then a
// builtin primitive.
func (r *typeResolver) lookup(name string) *ir.TypeDef {
	if def, ok := r.defs[name]; ok {
		return def
	}
	if def, ok := r.reg.Lookup(name); ok {
		return def
	}
	return nil
}
