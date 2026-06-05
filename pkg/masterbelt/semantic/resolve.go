package semantic

import (
	"maps"
	"math/big"

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
func resolveTypes(env eval.Env, file *ast.File, at func(ast.Node) span, diags *diagnostic.List, reg *builtin.Registry, extern map[string]*ir.TypeDef, qualified func(namespace, name string) *ir.TypeDef, fns bodyFuncs) []*ir.TypeDef {
	if len(file.Types) == 0 && len(file.Enums) == 0 && len(file.Interfaces) == 0 {
		return nil
	}

	// First pass: a definition per declaration, by name, so references (including
	// forward ones) bind before any body is resolved. A redeclared name keeps the
	// first definition and is reported; shadowing an imported name is not a
	// redeclaration. Types, enums, and interfaces share one name space, so a name
	// collision across the kinds is a redeclaration too.
	defs := make(map[string]*ir.TypeDef, len(file.Types)+len(file.Enums)+len(file.Interfaces)+len(extern))
	maps.Copy(defs, extern)
	own := make(map[string]bool, len(file.Types)+len(file.Enums)+len(file.Interfaces))
	out := make([]*ir.TypeDef, len(file.Types))
	claim := func(name string, def *ir.TypeDef, anchor ast.Node) {
		if name == "" {
			return
		}
		if own[name] {
			if at != nil && diags != nil {
				s := at(anchor)
				diags.Add(newDuplicateDeclarationDiagnostic(s.offset, s.width, name))
			}
			return
		}
		own[name] = true
		defs[name] = def
	}
	for i, td := range file.Types {
		def := &ir.TypeDef{Name: td.Name, Public: td.Public, Doc: td.Doc, Syntax: td}
		// The builtin mark is syntactic (`= builtin`), so set it here: a forward
		// reference to a same-file primitive must already resolve as ir.Builtin
		// (the spelling literals produce), not as a Named of an unmarked shell.
		if _, ok := td.Body.(*ast.BuiltinType); ok {
			def.Builtin = true
		}
		out[i] = def
		claim(td.Name, def, td)
	}
	enumOut := make([]*ir.TypeDef, len(file.Enums))
	for i, ed := range file.Enums {
		def := &ir.TypeDef{Name: ed.Name, Public: ed.Public, Doc: ed.Doc, Enum: &ir.EnumDef{}, EnumSyntax: ed}
		enumOut[i] = def
		claim(ed.Name, def, ed)
	}
	ifaceOut := make([]*ir.TypeDef, len(file.Interfaces))
	for i, id := range file.Interfaces {
		def := &ir.TypeDef{Name: id.Name, Public: id.Public, Doc: id.Doc, Interface: &ir.InterfaceDef{}, InterfaceSyntax: id}
		ifaceOut[i] = def
		claim(id.Name, def, id)
	}

	// Second pass: resolve parameters, body, method signatures, enum bodies, and
	// interface members, reporting any unknown type names.
	r := &infer.TypeResolver{Defs: defs, Qualified: qualified, Report: unknownTypeReporter(at, diags)}
	for i, id := range file.Interfaces {
		resolveInterfaceDecl(r, reg, id, ifaceOut[i], fns)
	}
	for i, td := range file.Types {
		resolveDecl(env, r, reg, td, out[i], at, diags, fns)
	}
	for i, ed := range file.Enums {
		resolveEnumDecl(env, r, reg, ed, enumOut[i], at, diags, fns)
	}

	// Third pass: resolve each type's and enum's declared interface impls and,
	// when reporting, check conformance (every required method present) and the
	// orphan rule (the interface is implemented at the type's own definition
	// site, which it always is here — a third-party file cannot reach a type's
	// impl list). The interface applications are resolved against the same
	// universe the bodies were.
	for i, td := range file.Types {
		resolveImpls(r, td.Impls, out[i], at, diags)
	}
	for i, ed := range file.Enums {
		resolveImpls(r, ed.Impls, enumOut[i], at, diags)
	}

	out = append(out, enumOut...)
	return append(out, ifaceOut...)
}

// resolveInterfaceDecl fills in an interface definition: its generic parameters
// (whose names are in scope for the member signatures) and its members,
// resolved into ir.Methods on the definition (required and provided alike, so a
// value typed as the interface resolves them through the same overload path a
// concrete type's methods take). Interface.Required and Interface.Provided
// record which names are required versus provided, for the conformance check.
func resolveInterfaceDecl(r *infer.TypeResolver, reg *builtin.Registry, id *ast.InterfaceDecl, def *ir.TypeDef, fns bodyFuncs) {
	scope := make(map[string]bool, len(id.Params))
	for _, p := range id.Params {
		scope[p.Name] = true
	}
	for _, p := range id.Params {
		var bound ir.Type
		if p.Constraint != nil {
			bound = r.ResolveType(p.Constraint, scope)
		}
		def.Params = append(def.Params, &ir.TypeParam{Name: p.Name, Bound: bound})
	}
	for _, m := range id.Members {
		method := resolveInterfaceMember(r, reg, &ir.Named{Def: def}, m, scope, fns)
		def.Methods = append(def.Methods, method)
		if m.Provided() {
			def.Interface.Provided = append(def.Interface.Provided, m.Name)
		} else {
			def.Interface.Required = append(def.Interface.Required, m.Name)
		}
	}
}

// resolveInterfaceMember resolves one interface member into an ir.Method,
// mirroring resolveMethod: its explicit type variables (the A in fold<A>) and
// the implicit ones (free names in a parameter position) join the signature's
// scope, the parameters and result resolve against it, and a provided member's
// default body lowers to IR. A required member has no body.
func resolveInterfaceMember(r *infer.TypeResolver, reg *builtin.Registry, self ir.Type, m *ast.InterfaceMember, scope map[string]bool, fns bodyFuncs) *ir.Method {
	method := &ir.Method{Name: m.Name, Public: m.Public, Doc: m.Doc}

	// The member's own type variables join a fresh scope: the explicit ones
	// (the A in fold<A>) and the implicit free names appearing in a parameter
	// type (the R in map's fn(T): R), neither bound by the interface nor naming
	// a known type. A member with no own variables reuses the interface scope.
	mscope := scope
	var extra []string
	for _, tp := range m.TypeParams {
		extra = append(extra, tp.Name)
	}
	paramTypes := make([]ast.TypeExpr, 0, len(m.Params))
	for _, p := range m.Params {
		paramTypes = append(paramTypes, p.Type)
	}
	if free := r.FreeTypeVars(scope, paramTypes...); len(free) > 0 {
		extra = append(extra, free...)
	}
	if len(extra) > 0 {
		mscope = make(map[string]bool, len(scope)+len(extra))
		for k := range scope {
			mscope[k] = true
		}
		for _, n := range extra {
			mscope[n] = true
		}
	}

	params := make(map[string]bool, len(m.Params))
	resolvedParams := make(map[string]ir.Type, len(m.Params))
	for _, p := range m.Params {
		t := r.ResolveType(p.Type, mscope)
		method.Params = append(method.Params, ir.Param{Name: p.Name, Type: t})
		params[p.Name] = true
		resolvedParams[p.Name] = t
	}
	method.Result = r.ResolveType(m.Result, mscope)
	if m.Body != nil {
		method.Body = lower.Body(m.Body, bodyBinder{r: r, reg: reg, params: params, paramTypes: resolvedParams, selfType: self, tscope: mscope, funcs: fns, self: true})
	}
	return method
}

// resolveImpls resolves a type's interface-tag impls (impl foldable<int, T>)
// into the definition's Impls and, when reporting, checks each implemented
// interface for conformance. An impl whose tag does not name an interface is
// reported (not_an_interface); a conforming type must declare every required
// method of the interface itself (missing_required_method). The orphan rule is
// satisfied structurally: a type's impl list is reachable only at its own
// definition site, so any impl recorded here is non-orphan by construction.
func resolveImpls(r *infer.TypeResolver, impls []ast.TypeExpr, def *ir.TypeDef, at func(ast.Node) span, diags *diagnostic.List) {
	scope := make(map[string]bool, len(def.Params))
	for _, p := range def.Params {
		scope[p.Name] = true
	}
	for _, impl := range impls {
		t := r.ResolveType(impl, scope)
		idef := interfaceDefOf(t)
		if idef == nil {
			// The tag resolved to a non-interface (or failed to resolve). An
			// unknown name is already reported by the resolver; a known
			// non-interface is reported here.
			if at != nil && diags != nil && t != ir.Invalid {
				s := at(impl)
				diags.Add(newNotAnInterfaceDiagnostic(s.offset, s.width, t.String()))
			}
			continue
		}
		def.Impls = append(def.Impls, t)
		if at == nil || diags == nil {
			continue
		}
		// Conformance: every required method must be declared directly on the
		// type. Provided methods need not be — the interface supplies them.
		for _, name := range idef.Interface.Required {
			if !declaresMethod(def, name) {
				s := at(impl)
				diags.Add(newMissingRequiredMethodDiagnostic(s.offset, s.width, def.Name, idef.Name, name))
			}
		}
	}
}

// interfaceDefOf returns the interface definition an impl tag resolved to, or
// nil when the tag is not an interface. An interface used with type arguments is
// an App; a bare one is a Named.
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

// declaresMethod reports whether the definition declares a method of the given
// name directly (not through an interface or an underlying type) — what
// conformance demands for a required method.
func declaresMethod(def *ir.TypeDef, name string) bool {
	for _, m := range def.Methods {
		if m.Name == name {
			return true
		}
	}
	return false
}

// resolveDecl fills in def from the declaration: its generic parameters (whose
// names are in scope for the bounds, body, and methods), the body type, the
// associated constants, the refinement predicate, and the method signatures.
// env folds the associated-constant initializers (it is nil in callers that do
// not evaluate).
func resolveDecl(env eval.Env, r *infer.TypeResolver, reg *builtin.Registry, td *ast.TypeDecl, def *ir.TypeDef, at func(ast.Node) span, diags *diagnostic.List, fns bodyFuncs) {
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
	// The associated constants are resolved before the where-clause, so a
	// self-referential predicate (`where self <= Percent.Max`) can read them.
	resolveAssocConsts(env, r, reg, td, def, scope, at, diags)
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
		rm := resolveMethod(r, reg, &ir.Named{Def: def}, m, scope, fns)
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

// resolveAssocConsts resolves a type's associated constants — the impl block's
// `const` items, read as TypeName.Name — into ir.AssocConsts, in source order.
// Each carries its resolved type (the annotation when present, else the kind of
// its folded value) and its folded value (env folds the initializer; nil when
// no evaluator is supplied). A `= builtin` constant takes its value from the
// registry's value range — int8.Max/Min — and is rejected (no_bound) on a type
// with no bound on that side (the arbitrary-precision int). A duplicate name
// keeps the first and is reported. tscope holds the type's generic-parameter
// names, so an annotation may mention them.
func resolveAssocConsts(env eval.Env, r *infer.TypeResolver, reg *builtin.Registry, td *ast.TypeDecl, def *ir.TypeDef, tscope map[string]bool, at func(ast.Node) span, diags *diagnostic.List) {
	def.Consts = resolveAssocConstList(env, r, reg, def, td.Consts, tscope, at, diags)
}

// resolveAssocConstList is the shared resolution of a list of associated
// constants — used for both a type declaration's and an enum's impl block.
func resolveAssocConstList(env eval.Env, r *infer.TypeResolver, reg *builtin.Registry, def *ir.TypeDef, decls []*ast.ConstDecl, tscope map[string]bool, at func(ast.Node) span, diags *diagnostic.List) []*ir.AssocConst {
	if len(decls) == 0 {
		return nil
	}
	out := make([]*ir.AssocConst, 0, len(decls))
	seen := make(map[string]bool, len(decls))
	for _, c := range decls {
		if c.Name != "" {
			if seen[c.Name] {
				if at != nil && diags != nil {
					s := at(c)
					diags.Add(newDuplicateDeclarationDiagnostic(s.offset, s.width, c.Name))
				}
				continue
			}
			seen[c.Name] = true
		}

		ac := &ir.AssocConst{Name: c.Name, Public: c.Public, Doc: c.Doc, Builtin: c.Builtin, Syntax: c}

		// The annotation, when written, gives the constant's type directly.
		var annType ir.Type
		if c.Type != nil {
			annType = r.ResolveType(c.Type, tscope)
		}

		if c.Builtin {
			// A `= builtin` constant takes its value from the type's native value
			// range — Max/Min. A type with no bound on that side has no such
			// constant (the arbitrary-precision int): report no_bound.
			value, ok := builtinBound(reg, def.Name, c.Name)
			if !ok {
				if at != nil && diags != nil {
					s := at(c)
					diags.Add(newNoBoundDiagnostic(s.offset, s.width, def.Name, c.Name))
				}
				ac.Type = ir.Invalid
				out = append(out, ac)
				continue
			}
			ac.Value = value
			// A builtin bound is typed as the arbitrary-precision int (the type of
			// an integer literal), not the concrete sized type: int8.Max is the
			// value 127, and like the literal 127 it adapts to whatever sized
			// integer it is compared or assigned to (self <= int16.Max in an int32
			// refinement). An explicit annotation overrides this.
			if annType != nil {
				ac.Type = annType
			} else {
				ac.Type = &ir.Builtin{Name: "int"}
			}
			out = append(out, ac)
			continue
		}

		// An ordinary associated constant folds its initializer (when an
		// evaluator is supplied) and types as its annotation, or — without one —
		// as the kind of its folded value.
		if env != nil {
			ac.Value = eval.Decl(c, env)
		}
		switch {
		case annType != nil:
			ac.Type = annType
		case ac.Value != nil:
			ac.Type = constantType(ac.Value)
		default:
			ac.Type = ir.Invalid
		}
		out = append(out, ac)
	}
	return out
}

// builtinBound returns the value of a builtin associated constant — the named
// bound (Max or Min) of the named primitive's value range — and whether the
// primitive has that bound. A non-integer primitive, an unknown bound name, or
// an unbounded side (the arbitrary-precision int, or uint's missing upper
// bound) yields ok == false.
func builtinBound(reg *builtin.Registry, typeName, bound string) (*ir.Constant, bool) {
	native, ok := reg.Native(typeName)
	if !ok || !native.IsInteger() {
		return nil, false
	}
	min, max := native.Bounds()
	switch bound {
	case "Max":
		if max == nil {
			return nil, false
		}
		return ir.IntConstant(max), true
	case "Min":
		if min == nil {
			return nil, false
		}
		return ir.IntConstant(min), true
	default:
		return nil, false
	}
}

// constantType is the type an un-annotated associated constant takes from its
// folded value's kind — the same reading an un-annotated top-level constant
// gets. An enum value keeps its enum type; an unsupported kind has no type.
func constantType(v *ir.Constant) ir.Type {
	switch v.Kind {
	case ir.ConstInt:
		return &ir.Builtin{Name: "int"}
	case ir.ConstBool:
		return &ir.Builtin{Name: "bool"}
	case ir.ConstString:
		return &ir.Builtin{Name: "string"}
	case ir.ConstDatetime:
		return &ir.Builtin{Name: "datetime"}
	case ir.ConstDuration:
		return &ir.Builtin{Name: "duration"}
	case ir.ConstEnum:
		if v.EnumDef != nil {
			return &ir.Named{Def: v.EnumDef}
		}
		return ir.Invalid
	default:
		return ir.Invalid
	}
}

// resolveEnumDecl fills in an enum definition from its declaration: the base
// type (the integer family or string; the default int when omitted), the member
// values determined by the §3.5 rules, and the operator methods (the six
// comparisons every enum has, plus the impl block's). Diagnostics — an invalid
// base type, an out-of-range or duplicate value, a duplicate member name — are
// reported through diags (nil in the silent memoized pass). env folds the
// member initializers (a constant expression may reference a top-level const);
// it is nil in callers that do not evaluate.
func resolveEnumDecl(env eval.Env, r *infer.TypeResolver, reg *builtin.Registry, ed *ast.EnumDecl, def *ir.TypeDef, at func(ast.Node) span, diags *diagnostic.List, fns bodyFuncs) {
	// The base type: the annotation when present, else the default int. It must
	// resolve to an integer-family or string primitive — anything else (bool, a
	// user type, a composite) is rejected, and the enum falls back to int so the
	// rest of resolution stays well-formed.
	base := "int"
	baseType := ir.Type(&ir.Builtin{Name: "int"})
	if ed.Base != nil {
		bt := r.ResolveType(ed.Base, nil)
		if name, ok := integerOrStringBase(reg, bt); ok {
			base = name
			baseType = bt
		} else if at != nil && diags != nil {
			s := at(ed.Base)
			diags.Add(newInvalidEnumBaseTypeDiagnostic(s.offset, s.width, typeNameOf(bt)))
		}
	}
	def.Enum.Base = base

	native, _ := reg.Native(base)
	isString := native != nil && native.IsString()

	// Member values, determined in declaration order (§3.5): an explicit
	// initializer folds against the base type; an omitted one takes the previous
	// integer value plus one (zero for the first), or, for a string base, the
	// member's own name. The values are settled for the whole enum before
	// duplicate detection, so a diagnostic never leaves the value table in a
	// half-built state.
	def.Enum.Members = make([]ir.EnumMember, len(ed.Members))
	memberSeen := map[string]bool{}
	var prevInt *big.Int
	for i, m := range ed.Members {
		def.Enum.Members[i] = ir.EnumMember{Name: m.Name}

		// A duplicate member name is reported once, at the repeat.
		if m.Name != "" {
			if memberSeen[m.Name] {
				if at != nil && diags != nil {
					s := at(m)
					diags.Add(newDuplicateEnumMemberDiagnostic(s.offset, s.width, m.Name))
				}
			}
			memberSeen[m.Name] = true
		}

		value, nextInt := enumMemberValue(env, m, isString, baseType, prevInt)
		prevInt = nextInt
		def.Enum.Members[i].Value = value

		// The folded value must fit the base type's range; an integer base with a
		// fixed width rejects an out-of-range value (constant_overflow), reusing
		// the same diagnostic an over-large const gets.
		if value != nil && value.Kind == ir.ConstInt && !types.Fits(reg, baseType, value.Int) {
			if at != nil && diags != nil {
				s := enumValueSpan(at, m)
				diags.Add(newConstantOverflowDiagnostic(s.offset, s.width, value.String(), base))
			}
		}
		// A written initializer that does not type as the base (an int base given
		// a string, say) is a type mismatch, reported the same way a const's is.
		if m.Value != nil && value != nil && !valueFitsBaseKind(value, isString) {
			if at != nil && diags != nil {
				s := enumValueSpan(at, m)
				diags.Add(newTypeMismatchDiagnostic(s.offset, s.width, kindName(value.Kind), base))
			}
		}
	}

	// Duplicate values are forbidden outright (§3.5-5): two members whose
	// settled values coincide — explicit or defaulted — are an error, reported
	// at the second.
	checkDuplicateEnumValues(def, ed, at, diags)

	// The impl block's associated constants (read as EnumName.Name), the same
	// mechanism a type declaration's impl carries.
	def.Consts = resolveAssocConstList(env, r, reg, def, ed.Consts, nil, at, diags)

	// The operator methods: the six comparisons every enum carries, then the
	// impl block's own methods (which may shadow a comparison or add new ones).
	def.Methods = append(def.Methods, builtin.EnumComparisonMethods()...)
	scope := map[string]bool{}
	seen := make(map[string]bool, len(ed.Methods))
	for _, m := range ed.Methods {
		rm := resolveMethod(r, reg, &ir.Named{Def: def}, m, scope, fns)
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

// enumMemberValue determines one member's value and the running integer counter
// for the next member. An explicit initializer folds through env (nil when no
// evaluator is supplied); an omitted one is the member's own name for a string
// base, or the previous integer plus one (zero for the first) for an integer
// base. nextInt is the counter the following member continues from — the folded
// value when it is an integer, else prev+1 so auto-numbering survives an
// unevaluable explicit value.
func enumMemberValue(env eval.Env, m *ast.EnumMember, isString bool, baseType ir.Type, prevInt *big.Int) (value *ir.Constant, nextInt *big.Int) {
	if m.Value != nil {
		if env != nil {
			value = eval.ExprExpecting(m.Value, baseType, env)
		}
		if value != nil && value.Kind == ir.ConstInt {
			return value, new(big.Int).Add(value.Int, big.NewInt(1))
		}
		return value, nextIntCounter(prevInt)
	}
	if isString {
		// A string base defaults a member to its own name.
		if m.Name == "" {
			return nil, prevInt
		}
		return ir.StringConstant(m.Name), prevInt
	}
	// An integer base auto-numbers: zero for the first member, the previous
	// value plus one thereafter.
	n := nextIntCounter(prevInt)
	return ir.IntConstant(n), new(big.Int).Add(n, big.NewInt(1))
}

// nextIntCounter returns the next auto-numbering value: zero when there is no
// previous value, the previous plus one otherwise.
func nextIntCounter(prev *big.Int) *big.Int {
	if prev == nil {
		return big.NewInt(0)
	}
	return new(big.Int).Set(prev)
}

// checkDuplicateEnumValues reports a duplicate_enum_value for any member whose
// settled value coincides with an earlier member's — the no-alias rule (§3.5-5),
// which keeps Name and Value in bijection. A member with no settled value (an
// unevaluable initializer) is skipped: its duplication cannot be decided.
func checkDuplicateEnumValues(def *ir.TypeDef, ed *ast.EnumDecl, at func(ast.Node) span, diags *diagnostic.List) {
	if at == nil || diags == nil {
		return
	}
	seen := map[string]int{}
	for i, m := range def.Enum.Members {
		if m.Value == nil {
			continue
		}
		key := enumValueKey(m.Value)
		if key == "" {
			continue
		}
		if _, dup := seen[key]; dup {
			s := enumValueSpan(at, ed.Members[i])
			diags.Add(newDuplicateEnumValueDiagnostic(s.offset, s.width, m.Name, m.Value.String()))
			continue
		}
		seen[key] = i
	}
}

// enumValueKey is a comparison key for an enum member's value: its kind and
// canonical string, so two integer values compare numerically and two strings
// by content. An unsupported kind has no key.
func enumValueKey(v *ir.Constant) string {
	switch v.Kind {
	case ir.ConstInt:
		return "i:" + v.Int.String()
	case ir.ConstString:
		return "s:" + v.Str
	default:
		return ""
	}
}

// enumValueSpan anchors a member-value diagnostic at the member's value
// expression when it has one, else at the member itself.
func enumValueSpan(at func(ast.Node) span, m *ast.EnumMember) span {
	if m.Value != nil {
		return at(m.Value)
	}
	return at(m)
}

// integerOrStringBase reports whether t is a usable enum base type — an
// integer-family or string primitive — and returns its name.
func integerOrStringBase(reg *builtin.Registry, t ir.Type) (string, bool) {
	b, ok := t.(*ir.Builtin)
	if !ok {
		return "", false
	}
	n, ok := reg.Native(b.Name)
	if !ok || (!n.IsInteger() && !n.IsString()) {
		return "", false
	}
	return b.Name, true
}

// valueFitsBaseKind reports whether a folded initializer value matches the
// base's value kind — an integer for an integer base, a string for a string
// base. A value of any other kind is a type mismatch.
func valueFitsBaseKind(v *ir.Constant, isString bool) bool {
	if isString {
		return v.Kind == ir.ConstString
	}
	return v.Kind == ir.ConstInt
}

// typeNameOf renders a type for the invalid-base diagnostic, falling back to a
// readable form for the composite types an enum base may never be.
func typeNameOf(t ir.Type) string {
	if t == nil || t == ir.Invalid {
		return "invalid"
	}
	return t.String()
}

// kindName renders a constant kind for the type-mismatch diagnostic.
func kindName(k ir.ConstKind) string {
	switch k {
	case ir.ConstString:
		return "string"
	case ir.ConstBool:
		return "bool"
	case ir.ConstInt:
		return "int"
	default:
		return "value"
	}
}

// resolveMethod resolves a method's signature (parameter types and result type)
// and lowers its body to IR; fns is the file's function shells by name, so a
// body may call a top-level function. reg and self (the receiver type) let the
// body binder infer an inferred let's value type. The body is not yet
// type-checked.
func resolveMethod(r *infer.TypeResolver, reg *builtin.Registry, self ir.Type, m *ast.MethodDecl, scope map[string]bool, fns bodyFuncs) *ir.Method {
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
	resolvedParams := make(map[string]ir.Type, len(m.Params))
	for _, p := range m.Params {
		t := r.ResolveType(p.Type, mscope)
		method.Params = append(method.Params, ir.Param{Name: p.Name, Type: t})
		params[p.Name] = true
		resolvedParams[p.Name] = t
	}
	method.Result = r.ResolveType(m.Result, mscope)
	method.Body = lower.Body(m.Body, bodyBinder{r: r, reg: reg, params: params, paramTypes: resolvedParams, selfType: self, tscope: mscope, funcs: fns, self: true})
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
func resolveFuncs(file *ast.File, at func(ast.Node) span, diags *diagnostic.List, reg *builtin.Registry, universe map[string]*ir.TypeDef, qualified func(namespace, name string) *ir.TypeDef, qualifiedFuncs func(namespace, name string) []*ast.FuncDecl, shells map[*ast.FuncDecl]*ir.Function) []*ir.Function {
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
		// The function's generic type parameters join a scope, so a parameter or
		// the result whose type names one resolves to a TypeVar (carrying its
		// bound) rather than being reported as an unknown type. Every parameter's
		// name is in scope for every bound — a bound may name a later parameter
		// (fn first<T: foldable<U>, U>) — so the names are gathered first.
		tscope := infer.FuncTypeParamScope(fd.TypeParams)
		fn.TypeParams = infer.ResolveFuncTypeParams(r, fd.TypeParams, tscope)
		params := make(map[string]bool, len(fd.Params))
		paramTypes := make(map[string]ir.Type, len(fd.Params))
		fn.Params = make([]ir.Param, 0, len(fd.Params))
		for _, p := range fd.Params {
			t := r.ResolveType(p.Type, tscope)
			fn.Params = append(fn.Params, ir.Param{Name: p.Name, Type: t})
			params[p.Name] = true
			paramTypes[p.Name] = t
		}
		fn.Result = r.ResolveType(fd.Result, tscope)
		fn.Body = lower.Body(fd.Body, bodyBinder{r: r, reg: reg, params: params, paramTypes: paramTypes, tscope: tscope, funcs: fns})

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
