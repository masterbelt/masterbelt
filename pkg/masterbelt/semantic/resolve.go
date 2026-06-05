package semantic

import (
	"math/big"
	"strconv"
	"strings"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/eval"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/lower"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/types"
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
func resolveTypes(file *ast.File, at func(ast.Node) span, diags *diagnostic.List, reg *builtin.Registry, extern map[string]*ir.TypeDef, qualified func(namespace, name string) *ir.TypeDef) []*ir.TypeDef {
	if len(file.Types) == 0 {
		return nil
	}

	// First pass: a definition per declaration, by name, so references (including
	// forward ones) bind before any body is resolved. A redeclared name keeps the
	// first definition and is reported; shadowing an imported name is not a
	// redeclaration.
	defs := make(map[string]*ir.TypeDef, len(file.Types)+len(extern))
	for name, def := range extern {
		defs[name] = def
	}
	own := make(map[string]bool, len(file.Types))
	out := make([]*ir.TypeDef, len(file.Types))
	for i, td := range file.Types {
		def := &ir.TypeDef{Name: td.Name, Public: td.Public, Doc: td.Doc, Syntax: td}
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
		resolveDecl(r, reg, td, out[i], at, diags)
	}
	return out
}

// resolveDecl fills in def from the declaration: its generic parameters (whose
// names are in scope for the bounds, body, and methods), the body type, the
// method signatures, and the refinement predicate.
func resolveDecl(r *infer.TypeResolver, reg *builtin.Registry, td *ast.TypeDecl, def *ir.TypeDef, at func(ast.Node) span, diags *diagnostic.List) {
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
		rm := resolveMethod(r, m, scope)
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

// signatureKey renders a method's parameter-type list as the duplicate-
// detection key: two same-name methods collide exactly when their resolved
// parameter types denote the same signature. Spellings of the same type are
// normalized — the enclosing type's own name reads as self (inside the impl
// they are the same type, and would otherwise both fit every call, making it
// permanently ambiguous), and the method's own type variables read by binding
// position (foo(a: T) and foo(a: U) are the same universal signature). The
// enclosing type's generic parameters keep their names: they are bound by the
// receiver, so distinct parameters are distinct types.
func signatureKey(def *ir.TypeDef, m *ir.Method) string {
	bound := make(map[string]bool, len(def.Params))
	for _, p := range def.Params {
		bound[p.Name] = true
	}
	vars := map[string]int{}
	parts := make([]string, len(m.Params))
	for i, p := range m.Params {
		parts[i] = normalizeKeyType(def, p.Type, bound, vars)
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

// normalizeKeyType renders one parameter type for signatureKey, recursing
// through the composites so a nested self or type variable normalizes too.
func normalizeKeyType(def *ir.TypeDef, t ir.Type, bound map[string]bool, vars map[string]int) string {
	switch t := t.(type) {
	case nil:
		return "<nil>"
	case *ir.SelfType:
		return "self"
	case *ir.Named:
		if t.Def == def {
			return "self"
		}
		return t.String()
	case *ir.TypeVar:
		if bound[t.Name] {
			return t.Name
		}
		n, ok := vars[t.Name]
		if !ok {
			n = len(vars)
			vars[t.Name] = n
		}
		return "%" + strconv.Itoa(n)
	case *ir.App:
		name := ""
		if t.Def != nil {
			name = t.Def.Name
		}
		args := make([]string, len(t.Args))
		for i, a := range t.Args {
			args[i] = normalizeKeyType(def, a, bound, vars)
		}
		return name + "<" + strings.Join(args, ", ") + ">"
	case *ir.Func:
		params := make([]string, len(t.Params))
		for i, p := range t.Params {
			params[i] = normalizeKeyType(def, p, bound, vars)
		}
		return "fn(" + strings.Join(params, ", ") + "): " + normalizeKeyType(def, t.Result, bound, vars)
	case *ir.Union:
		members := make([]string, len(t.Members))
		for i, m := range t.Members {
			members[i] = normalizeKeyType(def, m, bound, vars)
		}
		return strings.Join(members, " | ")
	case *ir.Record:
		fields := make([]string, len(t.Fields))
		for i, f := range t.Fields {
			fields[i] = f.Name + ": " + normalizeKeyType(def, f.Type, bound, vars)
		}
		return "{ " + strings.Join(fields, ", ") + " }"
	default:
		return t.String()
	}
}

// paramTypes renders a method's parameter types as "a, b" for the
// duplicate-overload diagnostic.
func paramTypes(m *ir.Method) string {
	parts := make([]string, len(m.Params))
	for i, p := range m.Params {
		parts[i] = p.Type.String()
	}
	return strings.Join(parts, ", ")
}

// resolveWhere type-checks the declaration's refinement predicate — self is the
// underlying body type, so the comparisons type against the body's operators —
// and keeps it on the definition only when it is a usable compile-time
// predicate: a bool that folds. An unusable predicate is reported here, once,
// at the declaration; the definition's Where stays nil so the per-constant
// check never fires for it (the ir.Invalid style of suppression). The silent
// pass (nil at/diags) decides usability identically and just skips the
// reporting, so the memoized definitions and the diagnostics never disagree.
func resolveWhere(r *infer.TypeResolver, reg *builtin.Registry, td *ast.TypeDecl, def *ir.TypeDef, at func(ast.Node) span, diags *diagnostic.List) {
	if td.Where == nil || def.Body == nil || ir.HasInvalid(def.Body) {
		return
	}
	report := at != nil && diags != nil
	var sink *infer.Sink
	if report {
		sink = exprSink(at, diags)
	}
	// The predicate types in a body scope with no parameters: self and literals
	// only, which is exactly what the evaluator can fold (a reference to a
	// constant would need a value the declaration does not have).
	bs := infer.BodyScope{Reg: reg, Universe: r.Defs, Qualified: r.Qualified, Self: def.Body}
	t := infer.CheckPredicate(td.Where, bs, sink)
	if t == ir.Invalid {
		return // the operator error was reported by the sink
	}
	if !types.IsBoolean(reg, t) {
		if report {
			s := at(td.Where)
			diags.Add(newRefinementNotBoolDiagnostic(s.offset, s.width, t.String()))
		}
		return
	}
	// The predicate must fold. A witness value of the body type stands in for
	// self — the fold is value-independent for everything the type rules let
	// through (intrinsic-backed methods over self and literals), so a witness
	// that folds proves every constant's check will.
	if v := eval.Predicate(td.Where, witness(reg, def.Body), predicateEnv{reg}); v == nil || v.Kind != ir.ConstBool {
		if report {
			s := at(td.Where)
			diags.Add(newRefinementNotConstantDiagnostic(s.offset, s.width))
		}
		return
	}
	def.Where = td.Where
}

// witness is a representative constant of t for the declaration-time probe
// fold: 1 for an integer (avoiding a divide-by-self zero), true for a boolean,
// the empty string for a string, nil — never foldable — for anything else.
func witness(reg *builtin.Registry, t ir.Type) *ir.Constant {
	switch {
	case types.IsInteger(reg, t):
		return ir.IntConstant(big.NewInt(1))
	case types.IsBoolean(reg, t):
		return ir.BoolConstant(true)
	case types.IsString(reg, t):
		return ir.StringConstant("")
	default:
		return nil
	}
}

// predicateEnv is the eval environment of a refinement predicate: just the
// registry. The type rules guarantee a usable predicate references nothing but
// self and literals, so resolution never happens.
type predicateEnv struct{ reg *builtin.Registry }

func (e predicateEnv) Resolve(*ast.Identifier) *ast.ConstDecl       { return nil }
func (e predicateEnv) ResolveMember(*ast.MemberExpr) *ast.ConstDecl { return nil }
func (e predicateEnv) ValueOf(*ast.ConstDecl) *ir.Constant          { return nil }
func (e predicateEnv) Registry() *builtin.Registry                  { return e.reg }

// resolveMethod resolves a method's signature (parameter types and result type)
// and lowers its body to IR. The body is not yet type-checked.
func resolveMethod(r *infer.TypeResolver, m *ast.MethodDecl, scope map[string]bool) *ir.Method {
	method := &ir.Method{Name: m.Name, Public: m.Public, Extern: m.Extern, Doc: m.Doc, Syntax: m}

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
	method.Body = lower.Body(m.Body, bodyBinder{r: r, params: params, tscope: mscope})
	return method
}
