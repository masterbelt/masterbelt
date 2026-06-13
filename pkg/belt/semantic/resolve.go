package semantic

import (
	"maps"
	"math/big"
	"slices"

	"github.com/masterbelt/masterbelt/pkg/belt/builtin"
	"github.com/masterbelt/masterbelt/pkg/belt/eval"
	"github.com/masterbelt/masterbelt/pkg/belt/lower"
	"github.com/masterbelt/masterbelt/pkg/belt/types"
	"github.com/masterbelt/masterbelt/pkg/belt/types/infer"
	"github.com/masterbelt/masterbelt/pkg/diagnostic"
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

// boundViolationReporter builds the callback the type resolver reports a
// type-application bound violation through, anchoring the diagnostic at the
// offending argument and naming the argument's type and the unsatisfied bound.
// It returns nil when there is nowhere to report (the prelude, a memoized
// resolution), so the resolver leaves the check off.
func boundViolationReporter(at func(ast.Node) span, diags *diagnostic.List) func(ast.TypeExpr, ir.Type, *ir.TypeParam) {
	if at == nil || diags == nil {
		return nil
	}
	return func(arg ast.TypeExpr, argType ir.Type, param *ir.TypeParam) {
		s := at(arg)
		diags.Add(newBoundNotSatisfiedDiagnostic(s.offset, s.width, argType.String(), param.Bound.String()))
	}
}

// deferredBoundChecks builds a BoundViolation callback that collects each
// generic-application bound violation instead of reporting it, paired with a
// recheck function that re-runs every collected check and reports only those still
// unmet. The body pass resolves a type before its impls are attached, so a
// user-implemented argument fails its bound there; rechecking once every impl is
// attached drops those false positives while a genuinely unmet bound (an argument
// that opts into no matching interface) is still reported. The collector is nil
// when there is nowhere to report, leaving the check off as the immediate reporter
// does.
func deferredBoundChecks(at func(ast.Node) span, diags *diagnostic.List, reg *builtin.Registry) (func(ast.TypeExpr, ir.Type, *ir.TypeParam), func()) {
	if at == nil || diags == nil {
		return nil, func() {}
	}
	type pending struct {
		argType ir.Type
		param   *ir.TypeParam
		at      span
	}
	var deferred []pending
	collect := func(arg ast.TypeExpr, argType ir.Type, param *ir.TypeParam) {
		deferred = append(deferred, pending{argType: argType, param: param, at: at(arg)})
	}
	recheck := func() {
		for _, p := range deferred {
			if !types.Satisfies(reg, p.argType, p.param.Bound) {
				diags.Add(newBoundNotSatisfiedDiagnostic(p.at.offset, p.at.width, p.argType.String(), p.param.Bound.String()))
			}
		}
	}
	return collect, recheck
}

// arityMismatchReporter builds the callback the type resolver reports a
// type-application arity mismatch through, anchoring the diagnostic at the
// offending argument and naming the applied type with the expected and given
// counts. It returns nil when there is nowhere to report (the prelude, a memoized
// resolution), so the resolver leaves the check off.
func arityMismatchReporter(at func(ast.Node) span, diags *diagnostic.List) func(ast.Node, string, int, int) {
	if at == nil || diags == nil {
		return nil
	}
	return func(node ast.Node, name string, actual, expected int) {
		s := at(node)
		diags.Add(newTypeArityMismatchDiagnostic(s.offset, s.width, name, actual, expected))
	}
}

// mentionsMetatype reports whether a resolved type is — or contains, anywhere in
// a composite — the metatype `type` (the type a reified type value carries). A
// type value is comptime-only and may not be stored, so any storage slot whose
// resolved type mentions the metatype is rejected; the recursive check closes
// the escape a bare top-level test would miss (list<type>, fn(type): type).
func mentionsMetatype(t ir.Type) bool {
	switch t := t.(type) {
	case *ir.Builtin:
		return t.Name == builtin.NameType
	case *ir.App:
		return slices.ContainsFunc(t.Args, mentionsMetatype)
	case *ir.Union:
		return slices.ContainsFunc(t.Members, mentionsMetatype)
	case *ir.Record:
		return slices.ContainsFunc(t.Fields, func(f ir.Field) bool { return mentionsMetatype(f.Type) })
	case *ir.Func:
		return slices.ContainsFunc(t.Params, mentionsMetatype) || mentionsMetatype(t.Result)
	default:
		return false
	}
}

// sigType packs a signature's parameter and result types into an ir.Func, so a
// single metatype test over it reports one diagnostic per signature however many
// of its slots are a type value.
func sigType(params []ir.Param, result ir.Type) *ir.Func {
	ts := make([]ir.Type, len(params))
	for i, p := range params {
		ts[i] = p.Type
	}
	return &ir.Func{Params: ts, Result: result}
}

// reportMetatypeSlot reports type_in_value_position when a storage slot's
// resolved type mentions the metatype — a const, let, record field, function
// parameter, or result whose type is (or carries) `type`. A type value is a
// comptime value and cannot be stored; the alias form (type X = Character.level)
// is how a projected type is named. It anchors at node and is a no-op without a
// reporter (a memoized resolution) or when the slot type is metatype-free.
func reportMetatypeSlot(at func(ast.Node) span, diags *diagnostic.List, node ast.Node, t ir.Type) {
	if at == nil || diags == nil || node == nil || !mentionsMetatype(t) {
		return
	}
	s := at(node)
	diags.Add(newTypeInValuePositionDiagnostic(s.offset, s.width))
}

// projectionErrorReporter builds the callback the type resolver reports a failed
// field-type projection (T.member in type position) through, anchored at the
// offending type expression: a value-or-method member (member_is_not_a_type), a
// fieldless receiver (type_has_no_fields), a record/master missing the field
// (unknown_field), or an ungrounded cyclic projection (cyclic_type_projection).
// It returns nil when there is nowhere to report (the prelude, a memoized
// resolution), so the resolver resolves projections silently for the IR.
func projectionErrorReporter(at func(ast.Node) span, diags *diagnostic.List) func(ast.Node, infer.ProjectionErrorKind, ir.Type, string) {
	if at == nil || diags == nil {
		return nil
	}
	return func(node ast.Node, kind infer.ProjectionErrorKind, typ ir.Type, member string) {
		s := at(node)
		switch kind {
		case infer.ProjMemberNotType:
			diags.Add(newMemberIsNotATypeDiagnostic(s.offset, s.width, typ.String(), member))
		case infer.ProjNoFields:
			diags.Add(newTypeHasNoFieldsDiagnostic(s.offset, s.width, typ.String(), member))
		case infer.ProjUnknownField:
			// unknown_field is "{typ} has no field {field}": the receiver type is
			// typ, the missing field is member.
			diags.Add(newUnknownFieldDiagnostic(s.offset, s.width, member, typ.String()))
		case infer.ProjCyclic:
			diags.Add(newCyclicTypeProjectionDiagnostic(s.offset, s.width, typ.String(), member))
		case infer.ProjGenericUnsupported:
			diags.Add(newGenericTypeProjectionDiagnostic(s.offset, s.width, typ.String()))
		}
	}
}

// typeParamValueReporter returns the callback BodyScope fires when a generic
// type parameter is consumed as a value (a bare T, or the receiver of a value
// read T.x), anchored at the offending identifier. It returns nil when there is
// nowhere to report (a sink-only settling walk passes a nil diags), so that walk
// types the value-position use silently rather than reporting it twice.
func typeParamValueReporter(at func(ast.Node) span, diags *diagnostic.List) func(ast.Node, string) {
	if at == nil || diags == nil {
		return nil
	}
	// The value leaf is reached more than once per offending identifier: the
	// checking walk streams a member receiver's type and the leaf re-derives it for
	// the field read, and the bare-enum-argument walk shares the same body scope —
	// each carries this reporter. Reporting is keyed by identifier node so each
	// offending use yields one diagnostic, not one per walk.
	seen := map[ast.Node]bool{}
	return func(node ast.Node, name string) {
		if seen[node] {
			return
		}
		seen[node] = true
		s := at(node)
		diags.Add(newTypeParamInValuePositionDiagnostic(s.offset, s.width, name))
	}
}

// selfMemberClashReporter builds the body leaf's reach to the self-member-clash
// diagnostic: a bare name that is at once a readable member of self (a field or
// getter) and a local or parameter, an ambiguity the self-omission rule forbids.
// It mirrors typeParamValueReporter, keying by identifier node so the several body walks
// that share a scope (the type walk, the checking walk re-deriving a member
// receiver, the bare-enum-argument walk) yield one diagnostic per offending use,
// not one per walk. It is nil when there is nothing to report through, leaving
// the leaf's silent ir.Invalid reading.
func selfMemberClashReporter(at func(ast.Node) span, diags *diagnostic.List) func(ast.Node, string) {
	if at == nil || diags == nil {
		return nil
	}
	seen := map[ast.Node]bool{}
	return func(node ast.Node, name string) {
		if seen[node] {
			return
		}
		seen[node] = true
		s := at(node)
		diags.Add(newSelfMemberNameClashDiagnostic(s.offset, s.width, name))
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
func resolveTypes(folder exprFolder, file *ast.File, at func(ast.Node) span, diags *diagnostic.List, res *callResolutions, reg *builtin.Registry, extern map[string]*ir.TypeDef, qualified func(namespace, name string) *ir.TypeDef, fns bodyFuncs) []*ir.TypeDef {
	if len(file.Types) == 0 && len(file.Enums) == 0 && len(file.Interfaces) == 0 && len(file.Masters) == 0 {
		return nil
	}

	// First pass: a definition per declaration, by name, so references (including
	// forward ones) bind before any body is resolved.
	defs, out, enumOut, ifaceOut, masterOut := declareTypeShells(file, extern, at, diags)

	// Second pass: resolve parameters, body, method signatures, enum bodies, and
	// interface members, reporting any unknown type names. A generic application's
	// bound check (Box<UserImpl> where Box<T: I>) is deferred rather than reported
	// here: a user-implemented argument opts into its bound through an impl block
	// that the third pass attaches, so checking the bound now — while the body is
	// first resolved — would reject a valid argument whose impls are not yet there.
	// The violations are collected and re-checked once every impl is attached.
	deferBound, recheckBounds := deferredBoundChecks(at, diags, reg)
	r := &infer.TypeResolver{
		Defs:            defs,
		Qualified:       qualified,
		Report:          unknownTypeReporter(at, diags),
		Registry:        reg,
		BoundViolation:  deferBound,
		ArityMismatch:   arityMismatchReporter(at, diags),
		ProjectionError: projectionErrorReporter(at, diags),
	}
	for i, id := range file.Interfaces {
		resolveInterfaceDecl(r, reg, id, ifaceOut[i], at, diags, fns)
	}
	// With every interface's parents resolved, check the inheritance graph: a
	// cycle (A: B, B: A), a child re-declaring an ancestor's member (override),
	// and a name two unrelated ancestors both contribute (conflict). These read
	// the whole graph, so they run once all parents are populated.
	checkInterfaceInheritance(file.Interfaces, ifaceOut, at, diags)
	for i, td := range file.Types {
		resolveDecl(r, reg, td, out[i], at, diags, res, fns)
	}
	for i, ed := range file.Enums {
		resolveEnumDecl(folder, defs, r, reg, ed, enumOut[i], at, diags, fns)
	}
	for i, md := range file.Masters {
		resolveMasterDecl(r, reg, md, masterOut[i], at, diags, fns)
	}

	// Third pass: resolve each type's and enum's declared interface impls and,
	// when reporting, check conformance (every required method present) and the
	// orphan rule (the interface is implemented at the type's own definition
	// site, which it always is here — a third-party file cannot reach a type's
	// impl list). The interface applications are resolved against the same
	// universe the bodies were.
	for i, td := range file.Types {
		resolveImpls(r, reg, td.Impls, out[i], at, diags)
	}
	for i, md := range file.Masters {
		resolveImpls(r, reg, md.Impls, masterOut[i], at, diags)
	}
	for i, ed := range file.Enums {
		resolveImpls(r, reg, ed.Impls, enumOut[i], at, diags)
		// Every enum is comparable and orderable: it carries the six comparison
		// methods (equality by index, order by base value), so it opts into both
		// contracts automatically — a generic bound of either is satisfied by an
		// enum, and the hover impl list shows them. The author may also write the
		// tag; addEnumContracts skips a contract already present so it is not
		// duplicated.
		addEnumContracts(r, enumOut[i])
	}

	// Every impl is now attached, so the generic-application bound checks deferred
	// during the body pass can run: a user-implemented argument that opts into its
	// bound is accepted, and one that does not is reported.
	recheckBounds()

	// Fourth pass: fold the associated-constant initializers, deferred until
	// every type, enum, and impl of the file has resolved so a cross-type
	// reference (another type's enum member, associated constant, or static
	// fn) folds — the in-pass eager fold ran before its targets existed. The
	// fold reads the just-built defs directly rather than the universe query,
	// which is this very computation and would cycle-guard to nothing.
	foldOwners := append(append([]*ir.TypeDef{}, out...), enumOut...)
	foldOwners = append(foldOwners, masterOut...)
	foldAssocConsts(folder, defs, foldOwners)

	// An associated constant may not store a type value either, the impl-block
	// twin of the top-level const rule: the check runs after the fold settles each
	// constant's type, so it catches both an annotated slot (const C: type) and
	// one inferred from a type-value initializer (const C = sbyte).
	for _, owner := range foldOwners {
		for _, ac := range owner.Consts {
			reportMetatypeSlot(at, diags, ac.Syntax, ac.Type)
		}
	}

	out = append(out, enumOut...)
	out = append(out, ifaceOut...)
	return append(out, masterOut...)
}

// declareTypeShells is resolveTypes' first pass: it builds a definition shell
// per type, enum, and interface declaration, by name, so references (including
// forward ones) bind before any body is resolved. A redeclared name keeps the
// first definition and is reported; shadowing an imported name is not a
// redeclaration. Types, enums, and interfaces share one name space, so a name
// collision across the kinds is a redeclaration too. It returns the universe
// (extern names plus the file's own) and the per-kind definition slices.
func declareTypeShells(file *ast.File, extern map[string]*ir.TypeDef, at func(ast.Node) span, diags *diagnostic.List) (defs map[string]*ir.TypeDef, out, enumOut, ifaceOut, masterOut []*ir.TypeDef) {
	defs = make(map[string]*ir.TypeDef, len(file.Types)+len(file.Enums)+len(file.Interfaces)+len(file.Masters)+len(extern))
	maps.Copy(defs, extern)
	own := make(map[string]bool, len(file.Types)+len(file.Enums)+len(file.Interfaces)+len(file.Masters))
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
	out = make([]*ir.TypeDef, len(file.Types))
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
	enumOut = make([]*ir.TypeDef, len(file.Enums))
	for i, ed := range file.Enums {
		def := &ir.TypeDef{Name: ed.Name, Public: ed.Public, Doc: ed.Doc, Enum: &ir.EnumDef{}, EnumSyntax: ed}
		enumOut[i] = def
		claim(ed.Name, def, ed)
	}
	ifaceOut = make([]*ir.TypeDef, len(file.Interfaces))
	for i, id := range file.Interfaces {
		def := &ir.TypeDef{Name: id.Name, Public: id.Public, Doc: id.Doc, Interface: &ir.InterfaceDef{}, InterfaceSyntax: id}
		ifaceOut[i] = def
		claim(id.Name, def, id)
	}
	// A master shares the one type name space: a name a type, enum, or interface
	// already claims is a redeclaration, reported by the same claim.
	masterOut = make([]*ir.TypeDef, len(file.Masters))
	for i, md := range file.Masters {
		def := &ir.TypeDef{Name: md.Name, Public: md.Public, Doc: md.Doc, Master: &ir.MasterDef{}, MasterSyntax: md}
		masterOut[i] = def
		claim(md.Name, def, md)
	}
	return defs, out, enumOut, ifaceOut, masterOut
}

// assocGraphEnv is the post-resolution fold environment the associated
// constants (and enum member initializers) interpret in: referenced values and
// the registry come from the queries, while the type-name channel reads the
// file's just-resolved definitions — the defs map resolveTypes built — so the
// fold sees every type and enum of the file where the in-flight universe query
// could not supply them.
type assocGraphEnv struct {
	q    queries
	defs map[string]*ir.TypeDef
}

func (e assocGraphEnv) ConstValue(c *ir.Const) *ir.Constant {
	if c.Syntax == nil {
		return nil
	}
	return e.q.valueOf(c.Syntax)
}
func (e assocGraphEnv) LookupType(name string) *ir.TypeDef { return e.defs[name] }
func (e assocGraphEnv) Registry() *builtin.Registry        { return e.q.registry() }

// foldAssocConsts folds every owner's ordinary associated constants (the
// `= builtin` ones were supplied from the registry during resolution) and
// settles their types: the annotation when written, the folded value's kind
// otherwise. Chains between associated constants fold by fixpoint — each round
// folds what the previous rounds settled, stopping when a round makes no
// progress — so declaration order never decides foldability; a genuine cycle
// or an unresolvable reference stays unfolded, for the reference diagnostics
// and the totality gate to report. A folder with no queries (a resolution
// with no evaluator) folds nothing, exactly as before.
func foldAssocConsts(folder exprFolder, defs map[string]*ir.TypeDef, owners []*ir.TypeDef) {
	if folder.q == nil {
		return
	}
	fenv := assocGraphEnv{q: folder.q, defs: defs}
	for progress := true; progress; {
		progress = false
		for _, def := range owners {
			for _, ac := range def.Consts {
				if foldOneAssocConst(folder, defs, fenv, ac) {
					progress = true
				}
			}
		}
	}
	// A constant that never settled a type — unannotated and unfolded — is
	// invalid, the same verdict the in-pass rule gave.
	for _, def := range owners {
		for _, ac := range def.Consts {
			if ac.Type == nil {
				ac.Type = ir.Invalid
			}
		}
	}
}

// foldOneAssocConst folds one ordinary associated constant's initializer and
// settles its type (the annotation when written, the folded value's kind
// otherwise), reporting whether it made progress. An already-folded, builtin,
// or syntax-less constant is skipped, as is one whose written annotation failed
// to resolve — the latter withholds the fold here, inside the memoized
// resolution, so every file sharing this definition sees the same absence (the
// publication rule's soundness side, decided at the source).
func foldOneAssocConst(folder exprFolder, defs map[string]*ir.TypeDef, fenv assocGraphEnv, ac *ir.AssocConst) bool {
	if ac.Value != nil || ac.Builtin || ac.Syntax == nil || ac.Syntax.Value == nil {
		return false
	}
	if ac.Syntax.Type != nil && ac.Type != nil && ir.HasInvalid(ac.Type) {
		return false
	}
	binder := folder.binder(enumDefOf(ac.Type))
	binder.universe = defs
	// Keep the resolved graph for reachability before folding it away: it
	// carries the references (a top-level const an assoc const reads) the folded
	// value drops.
	ac.ValueGraph = lower.Value(ac.Syntax.Value, binder)
	v := eval.GraphExpecting(ac.ValueGraph, ac.Type, fenv)
	if v == nil {
		return false
	}
	ac.Value = v
	if ac.Type == nil {
		ac.Type = constantType(v)
	}
	return true
}

// resolveInterfaceDecl fills in an interface definition: its generic parameters
// (whose names are in scope for the member signatures), its parents (the
// supertraits it inherits from), and its members, resolved into ir.Methods on
// the definition (required and provided alike, so a value typed as the interface
// resolves them through the same overload path a concrete type's methods take).
// Interface.Required and Interface.Provided record which names are required
// versus provided, for the conformance check; Interface.Parents records the
// resolved parent applications, for the inheritance closure.
func resolveInterfaceDecl(r *infer.TypeResolver, reg *builtin.Registry, id *ast.InterfaceDecl, def *ir.TypeDef, at func(ast.Node) span, diags *diagnostic.List, fns bodyFuncs) {
	// Resolve every parameter bound through the two-pass settle, so a bound may
	// name a later parameter or project off one (T: Box<U.x>); the resolved bounds
	// are back-filled into the scope, so a member signature naming a parameter as a
	// bounded constructor's argument sees the bound on its TypeVar.
	scope := make(infer.TypeScope, len(id.Params))
	for _, p := range id.Params {
		scope[p.Name] = nil
	}
	bounds := infer.SettleBounds(r, id.Params, scope)
	for i, p := range id.Params {
		def.Params = append(def.Params, &ir.TypeParam{Name: p.Name, Bound: bounds[i]})
	}
	// The parents (supertraits): each must resolve to an interface, the way an
	// impl tag must. A parent that is not an interface is reported here
	// (not_an_interface), exactly as a non-interface impl tag is; an unknown name
	// is already reported by the resolver. The resolved applications carry the
	// child's own type variables, so a generic parent (foldable<nint, T>) keeps
	// them for the closure to substitute.
	for _, p := range id.Parents {
		t := r.ResolveType(p, scope)
		if interfaceDefOf(t) == nil {
			if at != nil && diags != nil && t != ir.Invalid {
				s := at(p)
				diags.Add(newNotAnInterfaceDiagnostic(s.offset, s.width, t.String()))
			}
			continue
		}
		def.Interface.Parents = append(def.Interface.Parents, t)
	}
	seenReadable := map[string]bool{}
	for _, m := range id.Members {
		method := resolveInterfaceMember(r, reg, &ir.Named{Def: def}, m, scope, fns)
		// An interface member's parameter or result may not be a type value, the
		// same storage rule a concrete method obeys — so an interface cannot expose
		// a type-valued runtime slot.
		reportMetatypeSlot(at, diags, m, sigType(method.Params, method.Result))
		def.AttachMethods(method)
		reportInterfaceMemberIssues(m, seenReadable, at, diags)
		// A readable member and a static requirement are always required (neither
		// carries a usable default); only a method with a body is provided.
		if m.Provided() && !m.Readable && !m.Static {
			def.Interface.Provided = append(def.Interface.Provided, m.Name)
		} else {
			def.Interface.Required = append(def.Interface.Required, m.Name)
		}
	}
}

// reportInterfaceMemberIssues reports the per-member diagnostics of an interface
// declaration: a duplicate readable requirement, a readable member written with a
// body or its own type parameters (neither of which it can use), and the static-fn
// requirement faults — no parameter list, a provided body, or type parameters (a
// static is not generic). seenReadable carries the readable names seen so far, so
// the second of a duplicate pair is the one reported.
func reportInterfaceMemberIssues(m *ast.InterfaceMember, seenReadable map[string]bool, at func(ast.Node) span, diags *diagnostic.List) {
	if at == nil || diags == nil {
		return
	}
	s := at(m)
	if m.Readable && !m.Static {
		// Two readable requirements of one name are a duplicate, not an overload — a
		// readable member takes no arguments to distinguish them, so the second only
		// contradicts the first (value: string then value: nint can satisfy no
		// implementor at once).
		if seenReadable[m.Name] {
			diags.Add(newDuplicateDeclarationDiagnostic(s.offset, s.width, m.Name))
		}
		seenReadable[m.Name] = true
		// A readable member carries no default, so a written body would let any
		// implementor satisfy it vacuously; and read as value.X with no call, its own
		// type parameters could never be instantiated. Both are reported.
		if m.HasBody {
			diags.Add(newReadableMemberHasBodyDiagnostic(s.offset, s.width, m.Name))
		}
		if len(m.TypeParams) > 0 {
			diags.Add(newReadableMemberTypeParamsDiagnostic(s.offset, s.width, m.Name))
		}
	}
	if !m.Static {
		return
	}
	// A static-fn requirement needs a parameter list (static X(): T); without one the
	// parser still sets the modifier (static X: T), leaving a member that is both
	// static and readable. A provided default static is unsupported, so a body is
	// reported. And a static fn is not generic, so type parameters are reported — the
	// bound-call signature carries none to instantiate.
	if m.Readable {
		diags.Add(newStaticMemberNeedsParamsDiagnostic(s.offset, s.width, m.Name))
	}
	if m.HasBody {
		diags.Add(newStaticMemberHasBodyDiagnostic(s.offset, s.width, m.Name))
	}
	if len(m.TypeParams) > 0 {
		diags.Add(newGenericStaticDiagnostic(s.offset, s.width, m.Name))
	}
}

// checkInterfaceInheritance validates the interface inheritance graph once every
// interface's parents are resolved. It reports three problems, each anchored at
// the offending site:
//
//   - a cycle in the parent graph (A: B, B: A) — cyclic_reference at the
//     interface declaration, reusing the existing code the type/const cycle uses
//     (an interface is a nominal type, so a self-involving parent chain is the
//     same kind of fault);
//   - a child re-declaring a member an ancestor already carries (required or
//     provided) — interface_member_override at the child's member, since
//     inheritance carries the contract whole and a child may only add to it; and
//   - a member name two unrelated ancestors both contribute — a name reached
//     through a single shared ancestor is deduped by definition identity (the
//     diamond), but the same name declared independently by two distinct
//     ancestors is ambiguous and is reported as interface_member_conflict at the
//     child declaration.
func checkInterfaceInheritance(decls []*ast.InterfaceDecl, defs []*ir.TypeDef, at func(ast.Node) span, diags *diagnostic.List) {
	if at == nil || diags == nil {
		return
	}
	for i, def := range defs {
		if def.Interface == nil {
			continue
		}
		checkOneInterfaceInheritance(decls[i], def, at, diags)
	}
}

// checkOneInterfaceInheritance validates one interface's inheritance: a cycle
// (reported once at the declaration; the override and conflict checks below walk
// the same graph and short-circuit a cyclic chain through interfaceClosure's own
// seen set), a child member overriding an ancestor's, and a name two unrelated
// ancestors both contribute.
func checkOneInterfaceInheritance(decl *ast.InterfaceDecl, def *ir.TypeDef, at func(ast.Node) span, diags *diagnostic.List) {
	if interfaceHasCycle(def) {
		s := at(decl)
		diags.Add(newCyclicReferenceDiagnostic(s.offset, s.width, def.Name))
		return
	}
	checkConflictingAncestors(decl, def, at, diags)
	contributors := interfaceContributors(def)
	// Override: a child member an ancestor already carries — matched by name and
	// kind, so a child method name() does not override an ancestor readable name (a
	// readable member and a method are distinct members, as a field and a method of
	// the same name are on a concrete type).
	for _, m := range decl.Members {
		if anc := contributors[memberKeyOf(m)]; len(anc) > 0 {
			s := at(m)
			diags.Add(newInterfaceMemberOverrideDiagnostic(s.offset, s.width, def.Name, m.Name, anc[0].Name))
		}
	}
	// Conflict: a member two unrelated ancestors both declare, which the child does
	// not itself redeclare (an override is reported above instead).
	own := map[memberKey]bool{}
	for _, m := range decl.Members {
		own[memberKeyOf(m)] = true
	}
	for _, k := range interfaceMemberKeys(def) {
		own[k] = true // the IR members agree with the decl's; belt and braces
	}
	for k, anc := range contributors {
		if len(anc) >= 2 && !own[k] {
			s := at(decl)
			diags.Add(newInterfaceMemberConflictDiagnostic(s.offset, s.width, def.Name, k.name, anc[0].Name, anc[1].Name))
		}
	}
}

// memberKey identifies an interface member by name and kind — a readable member
// (a getter) and a method that share a name are distinct members, so the
// inheritance checks must not collapse them.
type memberKey struct {
	name string
	kind ir.MethodKind
}

// memberKeyOf is the key of a declared interface member: a readable-member
// requirement is a getter, a static-fn requirement a static, every other member a
// method.
func memberKeyOf(m *ast.InterfaceMember) memberKey {
	kind := ir.MethodNormal
	switch {
	case m.Readable:
		kind = ir.MethodGetter
	case m.Static:
		kind = ir.MethodStatic
	}
	return memberKey{name: m.Name, kind: kind}
}

// checkConflictingAncestors reports a generic interface inherited through two
// incompatible applications (D<X, Y>: A<X>, A<Y>), which would make an inherited
// member's type depend on the order the parents are written. It gathers every
// ancestor application reachable through the parents (each parent's closure with
// its arguments substituted) and flags a definition reached with two
// non-identical applications. A diamond that reaches one ancestor with the same
// application twice is consistent and not reported.
func checkConflictingAncestors(decl *ast.InterfaceDecl, def *ir.TypeDef, at func(ast.Node) span, diags *diagnostic.List) {
	first := map[*ir.TypeDef]ir.Type{}
	for _, parent := range def.Interface.Parents {
		for _, anc := range interfaceClosure(parent) {
			adef := interfaceDefOf(anc)
			if adef == nil {
				continue
			}
			prev, ok := first[adef]
			if !ok {
				first[adef] = anc
				continue
			}
			if !types.Identical(prev, anc) {
				s := at(decl)
				diags.Add(newConflictingGenericAncestorDiagnostic(s.offset, s.width, def.Name, adef.Name, prev.String(), anc.String()))
				return
			}
		}
	}
}

// interfaceContributors maps each member an ancestor of def declares — keyed by
// name and kind — to the distinct ancestor definitions that declare it. A member
// from a single shared ancestor lands once (the closure dedups by identity); one
// from two unrelated ancestors lands twice.
func interfaceContributors(def *ir.TypeDef) map[memberKey][]*ir.TypeDef {
	contributors := map[memberKey][]*ir.TypeDef{}
	for _, parent := range def.Interface.Parents {
		for _, anc := range interfaceClosure(parent) {
			adef := interfaceDefOf(anc)
			if adef == nil {
				continue
			}
			for _, k := range interfaceMemberKeys(adef) {
				contributors[k] = appendDistinct(contributors[k], adef)
			}
		}
	}
	return contributors
}

// interfaceHasCycle reports whether the interface reaches itself through its
// parent graph. It is a depth-first walk from the interface's own definition; a
// back edge to the start (or, conservatively, to any node already on the active
// path) is a cycle.
func interfaceHasCycle(def *ir.TypeDef) bool {
	onPath := map[*ir.TypeDef]bool{}
	var walk func(d *ir.TypeDef) bool
	walk = func(d *ir.TypeDef) bool {
		if d == nil || d.Interface == nil {
			return false
		}
		if onPath[d] {
			return true
		}
		onPath[d] = true
		for _, parent := range d.Interface.Parents {
			if walk(interfaceDefOf(parent)) {
				return true
			}
		}
		onPath[d] = false
		return false
	}
	return walk(def)
}

// interfaceMemberKeys returns the name-and-kind keys of an interface's own
// members (required and provided), in declaration order, for the override and
// conflict checks — so a readable member and a method of the same name are kept
// distinct.
func interfaceMemberKeys(def *ir.TypeDef) []memberKey {
	if def.Interface == nil {
		return nil
	}
	keys := make([]memberKey, 0, len(def.Methods))
	for _, m := range def.Methods {
		keys = append(keys, memberKey{name: m.Name, kind: m.Kind})
	}
	return keys
}

// appendDistinct appends def to defs unless it is already present, so a single
// ancestor reached two ways (a diamond) counts once.
func appendDistinct(defs []*ir.TypeDef, def *ir.TypeDef) []*ir.TypeDef {
	for _, d := range defs {
		if d == def {
			return defs
		}
	}
	return append(defs, def)
}

// resolveInterfaceMember resolves one interface member into an ir.Method,
// mirroring resolveMethod: its explicit type variables (the A in fold<A>) and
// the implicit ones (free names in a parameter position) join the signature's
// scope, the parameters and result resolve against it, and a provided member's
// default body lowers to IR. A required member has no body.
func resolveInterfaceMember(r *infer.TypeResolver, reg *builtin.Registry, self ir.Type, m *ast.InterfaceMember, scope infer.TypeScope, fns bodyFuncs) *ir.Method {
	method := &ir.Method{Name: m.Name, Public: m.Public, Doc: m.Doc}
	// A readable-member requirement (X: T, no parameter list) is the signature of a
	// getter — read as x.X yielding T — so it is carried as a getter, which is what
	// conformance branches on to demand a field or getter rather than a method. A
	// static-fn requirement (static X(): T) is carried as a static, which conformance
	// branches on to demand a static fn and which a bounded parameter's T.X() call
	// resolves through.
	switch {
	case m.Static:
		method.Kind = ir.MethodStatic
	case m.Readable:
		method.Kind = ir.MethodGetter
	}

	// The member's own type variables join a fresh scope: the explicit ones
	// (the A in fold<A>) and the implicit free names appearing in a parameter
	// type (the R in map's fn(T): R), neither bound by the interface nor naming
	// a known type. A member with no own variables reuses the interface scope.
	// An explicit member type parameter carries its own bound (fold<A: orderable>),
	// an implicit free name is unbounded.
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
		mscope = make(infer.TypeScope, len(scope)+len(extra))
		for k, v := range scope {
			mscope[k] = v
		}
		for _, n := range extra {
			mscope[n] = nil
		}
		// Resolve each explicit member type parameter's bound in the full member
		// scope and back-fill it, so a member naming it carries the bound. The
		// two-pass settle lets a bound project off another parameter (T: Box<U.x>).
		infer.SettleBounds(r, m.TypeParams, mscope)
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
	if m.Body != nil && !m.Readable && !m.Static {
		method.Body = lower.Body(m.Body, bodyBinder{r: r, reg: reg, params: params, paramTypes: resolvedParams, selfType: self, tscope: mscope, funcs: fns, self: true})
		// A provided member carries an AST syntax link, the way a concrete method
		// does, so the constant folder reaches its body: it folds a provided method
		// call (a list's count/keys/...) by evaluating this body with self bound to
		// the receiver, exactly as it folds a concrete method. A required member
		// (no body) keeps a nil Syntax — its implementation is the implementor's.
		method.Syntax = ast.NewMethodDecl(m.Doc, m.Public, false, ast.MethodNormal, nil, m.Name, m.TypeParams, m.Params, m.Result, m.Body, nil)
	}
	return method
}

// resolveImpls resolves a type's interface-tag impls (impl foldable<int, T>)
// into the definition's Impls and, when reporting, checks each implemented
// interface for conformance. An impl whose tag does not name an interface is
// reported (not_an_interface); a conforming type must declare every required
// method of the interface and of every ancestor it inherits
// (missing_required_method). The orphan rule is satisfied structurally: a
// type's impl list is reachable only at its own definition site, so any impl
// recorded here is non-orphan by construction.
//
// Opting into an interface opts into all its ancestors: each impl's ancestor
// closure (with the impl's type arguments substituted into a generic parent) is
// materialized onto Impls too, deduped by interface identity. This is what lets
// every path that reads Impls — Satisfies, the switch and map-key checks, the
// hover card — see the inherited contracts through the child alone, with no
// special-casing for inheritance.
func resolveImpls(r *infer.TypeResolver, reg *builtin.Registry, impls []ast.TypeExpr, def *ir.TypeDef, at func(ast.Node) span, diags *diagnostic.List) {
	scope := make(infer.TypeScope, len(def.Params))
	for _, p := range def.Params {
		scope[p.Name] = p.Bound
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
		// Materialize the impl and its whole ancestor closure: the author may have
		// written some ancestors explicitly, so each is added only if its interface
		// is not already present.
		for _, anc := range interfaceClosure(t) {
			addImpl(def, anc)
		}
		if at == nil || diags == nil {
			continue
		}
		// Conformance over the closure: every required method of the interface and
		// of every ancestor it inherits must be supplied by the type — its own
		// declaration, or one derived from its underlying type. A nominal type
		// (type Level = int impl comparable {}) inherits its base's comparison
		// methods, so an empty impl tag opts it in. Provided methods need not be
		// supplied — the interface itself carries them. The diagnostic anchors at
		// the declared impl tag, naming the interface whose contract is unmet.
		for _, anc := range interfaceClosure(t) {
			adef := interfaceDefOf(anc)
			if adef == nil {
				continue
			}
			subst := interfaceArgSubst(anc, adef)
			for _, req := range adef.Methods {
				// A required member has no default body; iterating the resolved members
				// (not their names) keeps each requirement's kind, so a readable and a
				// method requirement that share a name are checked as the distinct
				// requirements they are.
				if req.Body != nil {
					continue
				}
				if d, unmet := unmetRequirement(reg, def, adef, req, subst, at(impl)); unmet {
					diags.Add(d)
				}
			}
		}
	}
}

// addImpl records an interface application on the definition's Impls unless an
// application of the same interface is already present. Identity is the
// interface definition (not the full application), so an author-written tag and
// a materialized ancestor of the same interface are not both kept, and a diamond
// shares its common ancestor once.
func addImpl(def *ir.TypeDef, impl ir.Type) {
	idef := interfaceDefOf(impl)
	if idef == nil {
		return
	}
	for _, existing := range def.Impls {
		if interfaceDefOf(existing) == idef {
			return
		}
	}
	def.Impls = append(def.Impls, impl)
}

// interfaceClosure returns the interface application iface together with every
// ancestor it inherits, deduped by interface identity, in a stable order (the
// interface itself first, then its parents' closures left to right). A generic
// parent carries the child's type variables, so the parent application is
// substituted with the child application's arguments — foldable<nint, T> reached
// through a child applied to int yields foldable<nint, int>. It guards a cycle
// in the inheritance graph with the seen set, so a malformed A: B, B: A graph
// terminates rather than looping.
func interfaceClosure(iface ir.Type) []ir.Type {
	var out []ir.Type
	seen := map[*ir.TypeDef]bool{}
	var walk func(t ir.Type)
	walk = func(t ir.Type) {
		idef := interfaceDefOf(t)
		if idef == nil || seen[idef] {
			return
		}
		seen[idef] = true
		out = append(out, t)
		// A generic interface binds its parents against its own arguments, so
		// substitute the application's arguments into each parent before recursing.
		subst := interfaceSubst(t, idef)
		for _, parent := range idef.Interface.Parents {
			walk(types.Substitute(parent, subst))
		}
	}
	walk(iface)
	return out
}

// interfaceSubst builds the substitution from an interface definition's
// parameters to an application's arguments, so a generic parent reached through
// the application is read with the application's bindings. A bare interface (a
// Named, or an App with a mismatched argument count) substitutes nothing.
func interfaceSubst(t ir.Type, idef *ir.TypeDef) map[string]ir.Type {
	app, ok := t.(*ir.App)
	if !ok || len(app.Args) != len(idef.Params) {
		return nil
	}
	subst := make(map[string]ir.Type, len(idef.Params))
	for i, p := range idef.Params {
		subst[p.Name] = app.Args[i]
	}
	return subst
}

// enumContractNames are the operator contracts every enum opts into: an enum
// carries equality (comparable) and a total order (orderable) by construction.
var enumContractNames = []string{"comparable", "orderable"}

// addEnumContracts records comparable and orderable on an enum's Impls — the
// automatic opt-in every enum has from its six comparison methods. A contract
// the enum already lists (the author wrote the tag) is left alone, so it is
// never duplicated, and a contract name that does not resolve to an interface
// (a degraded universe where the prelude failed to load) is skipped rather than
// recorded as a non-interface. Each contract's ancestor closure is materialized
// too, deduped by interface identity, so orderable pulling in comparable does
// not record comparable twice. The impls are the bare interface (a Named), the
// no-argument form the conformance and Satisfies checks expect.
func addEnumContracts(r *infer.TypeResolver, def *ir.TypeDef) {
	for _, name := range enumContractNames {
		t := r.ResolveName(name, nil)
		if interfaceDefOf(t) == nil {
			continue
		}
		for _, anc := range interfaceClosure(t) {
			addImpl(def, anc)
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

// suppliesMethod reports whether the definition supplies a method of the given
// name — declared directly, or derived from its underlying type — which is what
// conformance demands for a required method. Walking the underlying type lets a
// nominal type satisfy an interface through its base's methods (type Level = int
// impl comparable {} inherits int's eql/neq), so the empty impl tag opts the
// type in. An interface the type itself opts into is deliberately not consulted:
// the interface being checked must not satisfy its own requirement, and another
// interface's provided default is not a real implementation either. The seen set
// guards a cyclic definition.
func suppliesMethod(reg *builtin.Registry, def *ir.TypeDef, name string, seen map[*ir.TypeDef]bool) bool {
	if def == nil || seen[def] {
		return false
	}
	seen[def] = true
	for _, m := range def.Methods {
		// A method requirement is met by a method only: a getter (read as x.X, not
		// callable as x.X()), a setter, or a static is the wrong kind, so the kind is
		// checked rather than the name alone.
		if m.Name == name && m.Kind == ir.MethodNormal {
			return true
		}
	}
	// A primitive's body is itself; only a nominal type has a distinct
	// underlying definition to inherit from.
	if def.Builtin {
		return false
	}
	if ud := bodyDef(reg, def.Body); ud != nil {
		return suppliesMethod(reg, ud, name, seen)
	}
	return false
}

// suppliesReadable reports whether def supplies a readable member of the given
// name and read type — a field of that type, or a getter whose result is that
// type — which is what a readable-member requirement (X: T) demands. The field is
// read from the receiver's fully instantiated record, and the getter is read off
// the type and its underlying chain with the receiver's generic arguments and self
// substituted in, but deliberately not through the registry: that lookup would
// find the very requirement being checked on the implemented interface and satisfy
// it with itself. The read type must match the requirement's type by identity.
func suppliesReadable(reg *builtin.Registry, def *ir.TypeDef, name string, want ir.Type) bool {
	recv := ir.Type(&ir.Named{Def: def})
	// A field of the requirement's type — the record is instantiated, so a field
	// reached through a generic alias (StringBox = Box<string>) carries the
	// application's argument (value: string, not the parameter T).
	if rec := types.RecordOf(recv); rec != nil {
		for i := range rec.Fields {
			if rec.Fields[i].Name == name {
				return types.Identical(types.SubstituteSelf(rec.Fields[i].Type, recv), want)
			}
		}
	}
	if t, ok := readableGetterType(reg, recv, def, name, nil, map[*ir.TypeDef]bool{}); ok {
		return types.Identical(t, want)
	}
	return false
}

// readableGetterType returns the result type of a getter named name declared on
// def or the type underlying it, with the generic substitution accumulated down
// the alias chain and self resolved to recv (the original receiver). It walks the
// body — an application binds its arguments to the next definition's parameters,
// so a getter inherited through StringBox = Box<string> reads string, not the
// parameter T — without consulting def's interfaces. A method or setter of the
// same name is the wrong kind and is skipped.
func readableGetterType(reg *builtin.Registry, recv ir.Type, def *ir.TypeDef, name string, subst map[string]ir.Type, seen map[*ir.TypeDef]bool) (ir.Type, bool) {
	if def == nil || seen[def] {
		return nil, false
	}
	seen[def] = true
	for _, m := range def.Methods {
		if m.Name == name && m.Kind == ir.MethodGetter {
			return types.SubstituteSelf(types.Substitute(m.Result, subst), recv), true
		}
	}
	if def.Builtin {
		return nil, false
	}
	switch b := def.Body.(type) {
	case *ir.App:
		if b.Def != nil && len(b.Def.Params) == len(b.Args) {
			next := make(map[string]ir.Type, len(b.Args))
			for i, p := range b.Def.Params {
				next[p.Name] = types.Substitute(b.Args[i], subst)
			}
			return readableGetterType(reg, recv, b.Def, name, next, seen)
		}
	case *ir.Named:
		return readableGetterType(reg, recv, b.Def, name, subst, seen)
	case *ir.Builtin:
		if d, ok := reg.Lookup(b.Name); ok {
			return readableGetterType(reg, recv, d, name, subst, seen)
		}
	}
	return nil, false
}

// unmetRequirement reports the diagnostic for a required member def does not
// supply, and whether one is unmet. A readable-member requirement (carried as a
// getter) demands a field or getter of the required read type; a method
// requirement demands a method. The interface's own type arguments are
// substituted into the required type, and self is resolved to the implementing
// type, so a generic interface (Box<T> requiring value: T) checks against the
// application's argument and a self-typed requirement (me: self) against the
// implementor. The span anchors the diagnostic at the impl tag.
func unmetRequirement(reg *builtin.Registry, def, adef *ir.TypeDef, req *ir.Method, subst map[string]ir.Type, s span) (diagnostic.Diagnostic, bool) {
	if req.Kind == ir.MethodGetter {
		want := types.SubstituteSelf(types.Substitute(req.Result, subst), &ir.Named{Def: def})
		if !suppliesReadable(reg, def, req.Name, want) {
			return newMissingReadableMemberDiagnostic(s.offset, s.width, def.Name, adef.Name, req.Name, want.String()), true
		}
		return diagnostic.Diagnostic{}, false
	}
	if req.Kind == ir.MethodStatic {
		// A static-fn requirement demands a static fn of the name whose signature
		// matches the requirement: a call T.foo() through a bounded parameter is typed
		// against the requirement, so an implementor whose static has a different arity
		// or result would be called with the wrong signature.
		if !suppliesStatic(def, req, subst) {
			return newMissingRequiredStaticDiagnostic(s.offset, s.width, def.Name, adef.Name, req.Name), true
		}
		return diagnostic.Diagnostic{}, false
	}
	if !suppliesMethod(reg, def, req.Name, map[*ir.TypeDef]bool{}) {
		return newMissingRequiredMethodDiagnostic(s.offset, s.width, def.Name, adef.Name, req.Name), true
	}
	return diagnostic.Diagnostic{}, false
}

// suppliesStatic reports whether def declares a static fn meeting the requirement
// req: a static of the same name whose parameter and result types match the
// requirement's, with the interface's arguments substituted and self resolved to
// the implementing type (a static make(): self is met by static fn make(): Widget
// on Widget). A static is read off the named type itself — the static-call path
// reads only the type's own methods, never an underlying alias — so conformance
// reads only def's own statics, the two staying consistent.
func suppliesStatic(def *ir.TypeDef, req *ir.Method, subst map[string]ir.Type) bool {
	recv := ir.Type(&ir.Named{Def: def})
	for _, m := range def.Methods {
		if m.Name != req.Name || m.Kind != ir.MethodStatic {
			continue
		}
		if staticSigMatches(m, req, subst, recv) {
			return true
		}
	}
	return false
}

// staticSigMatches reports whether a candidate static fn m has the parameter and
// result types the requirement req demands, with req's types instantiated — the
// interface's arguments substituted and self resolved to the implementing receiver
// — and compared by identity, the way a static call site would resolve them.
func staticSigMatches(m, req *ir.Method, subst map[string]ir.Type, recv ir.Type) bool {
	if len(m.Params) != len(req.Params) {
		return false
	}
	for i := range req.Params {
		want := types.SubstituteSelf(types.Substitute(req.Params[i].Type, subst), recv)
		if !types.Identical(m.Params[i].Type, want) {
			return false
		}
	}
	want := types.SubstituteSelf(types.Substitute(req.Result, subst), recv)
	return types.Identical(m.Result, want)
}

// interfaceArgSubst maps an interface's type parameters to the arguments of an
// application of it (Box<string> against interface Box<T> binds T to string), so a
// requirement written in terms of a parameter is checked against the concrete
// argument. A bare (non-generic) interface, or one reached as a Named with no
// arguments, yields an empty substitution.
func interfaceArgSubst(iface ir.Type, def *ir.TypeDef) map[string]ir.Type {
	app, ok := iface.(*ir.App)
	if !ok || len(app.Args) != len(def.Params) {
		return nil
	}
	subst := make(map[string]ir.Type, len(def.Params))
	for i, p := range def.Params {
		subst[p.Name] = app.Args[i]
	}
	return subst
}

// bodyDef returns the type definition an underlying-type reference resolves to:
// the registry definition for a builtin, the referent for a named or applied
// type. It mirrors the type algebra's defOf for the kinds an underlying body
// takes, without reaching across to that package's unexported helper.
func bodyDef(reg *builtin.Registry, t ir.Type) *ir.TypeDef {
	switch t := t.(type) {
	case *ir.Builtin:
		if d, ok := reg.Lookup(t.Name); ok {
			return d
		}
	case *ir.Named:
		return t.Def
	case *ir.App:
		return t.Def
	}
	return nil
}

// resolveDecl fills in def from the declaration: its generic parameters (whose
// names are in scope for the bounds, body, and methods), the body type, the
// associated constants, the refinement predicate, and the method signatures.
// env folds the associated-constant initializers (it is nil in callers that do
// not evaluate).
func resolveDecl(r *infer.TypeResolver, reg *builtin.Registry, td *ast.TypeDecl, def *ir.TypeDef, at func(ast.Node) span, diags *diagnostic.List, res *callResolutions, fns bodyFuncs) {
	// Resolve every parameter bound through the two-pass settle, so a bound may
	// name a later parameter or project off one (T: Box<U.x>); the resolved bounds
	// are back-filled, so the body, associated constants, and methods that name a
	// parameter as a bounded constructor's argument (type Index<K: comparable> =
	// map<K, nint>) resolve it to a TypeVar carrying the bound — the declaration-
	// site bound check then passes for a sufficiently-bounded parameter and fires
	// for an unbounded one.
	scope := make(infer.TypeScope, len(td.Params))
	for _, p := range td.Params {
		scope[p.Name] = nil
	}
	bounds := infer.SettleBounds(r, td.Params, scope)
	for i, p := range td.Params {
		def.Params = append(def.Params, &ir.TypeParam{Name: p.Name, Bound: bounds[i]})
	}
	// A `= builtin` body marks a primitive: its type is itself, and its native
	// semantics come from the registry rather than from a defining type.
	if _, ok := td.Body.(*ast.BuiltinType); ok {
		def.Builtin = true
		def.Body = &ir.Builtin{Name: td.Name}
	} else {
		def.Body = r.ResolveType(td.Body, scope)
		// A storage slot may not hold a type value: a record field, a function
		// type's parameter or result, or the alias itself resolving to the metatype
		// (type Schema = { type: type }, type Remap = fn(type): type) is
		// type_in_value_position. The projected-type alias (type X = Character.level)
		// resolves to the field's declared type, not the metatype, so it is allowed.
		reportMetatypeSlot(at, diags, td.Body, def.Body)
	}
	// The associated constants are resolved before the where-clause, so a
	// self-referential predicate (`where self <= Percent.Max`) can read them.
	resolveAssocConsts(r, reg, td, def, scope, at, diags)
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
		// A method parameter or result may not be a type value (fn f(t: type) — the
		// "no type-value functions" half of the storage rule).
		reportMetatypeSlot(at, diags, m, sigType(rm.Params, rm.Result))
		key := rm.Name + signatureKey(def, rm)
		if m.Name != "" && seen[key] {
			reportDuplicateMethod(rm, def, m, at, diags)
			continue
		}
		seen[key] = true
		def.AttachMethods(rm)
	}
	// The accessor/static declaration checks run after the methods, fields, and
	// associated constants are all on the definition, so the collision checks see
	// the whole type.
	checkMemberDecls(def, at, diags)
	// The where-clause is resolved last, after the methods, so a predicate that
	// calls a method of the type (`where self.isValid()`) can resolve it — self
	// is the nominal type, and its impl methods are now on the definition.
	resolveWhere(r, reg, td, def, at, diags, res)
}

// resolveMasterDecl resolves a master declaration into its definition. The row
// is an ordinary record body, so its field types resolve through the same
// resolver a type's body does; the resolved fields are kept on def.Master rather
// than def.Body, which leaves the master a leaf the type algebra does not look
// through (opaque to its row record). The impl methods and
// associated constants resolve exactly as a type's do, with self the master
// nominal, so a row method (label(): string { return self.name }) resolves its
// field reads through recordOf, which knows the master case. Finally the
// primary-key columns are recorded and any that names no field is reported. A
// master has no generic parameters; the row predicate (a where over a row) is a
// later concern, so — with Body nil — resolveWhere is not run (it would skip a
// body-less definition anyway). It takes no callResolutions stream because the
// only checked body it produces, a row method, is checked by checkMethodBodies
// like every other method, not here.
func resolveMasterDecl(r *infer.TypeResolver, reg *builtin.Registry, md *ast.MasterDecl, def *ir.TypeDef, at func(ast.Node) span, diags *diagnostic.List, fns bodyFuncs) {
	// The row type is kept as written (inline record, named record alias, or a
	// generic application). underlyingRecord unwraps a nominal alias to the record
	// for the key/field checks; an absent or non-record row leaves it nil (reported
	// by checkMaster), while a generic record alias (record Row<int>) resolves to
	// an application this slice does not expand — a real record row, validated
	// later, so it is neither reported missing nor read here.
	rowType := r.ResolveType(md.Record, nil)
	def.Master.Row = rowType
	// A row column may not store a type value, the master twin of a record field's
	// storage rule (the row is the master's record body).
	reportMetatypeSlot(at, diags, md.Record, rowType)
	row := underlyingRecord(rowType)
	// The primary key is stored de-duplicated, so the IR never carries a malformed
	// doubled key tuple even when the duplicate is also reported below.
	def.Master.Primary = dedupeStrings(md.Primary)
	def.Consts = resolveAssocConstList(r, reg, def, md.Consts, nil, at, diags)
	// Same-name methods are overloads unless a signature repeats; the first wins
	// and the repeat is reported, exactly as resolveDecl drops a type's.
	seen := make(map[string]bool, len(md.Methods))
	for _, m := range md.Methods {
		rm := resolveMethod(r, reg, &ir.Named{Def: def}, m, nil, fns)
		// A master method's parameter or result may not be a type value, the same
		// storage rule a nominal type's method obeys.
		reportMetatypeSlot(at, diags, m, sigType(rm.Params, rm.Result))
		key := rm.Name + signatureKey(def, rm)
		if m.Name != "" && seen[key] {
			reportDuplicateMethod(rm, def, m, at, diags)
			continue
		}
		seen[key] = true
		def.AttachMethods(rm)
	}
	resolveMasterValidations(r, reg, def, md, fns)
	checkMemberDecls(def, at, diags)
	checkMaster(md, row, isGenericRecordAlias(rowType), at, diags)
}

// resolveMasterValidations lowers a master's per-row validate checks onto its
// definition: each each-clause assert becomes a resolved ir.AssertStmt whose
// condition is a value graph over self (the row), the same lowering a row method
// body gets, so self and the row's fields resolve and the data layer can fold it
// against every loaded row. A per-table check (validate all) folds once over the
// whole table, not per row, so it is left for a later step. The conditions are
// lowered untyped here and typed by the write-back (writeBackResolutions), whose
// facts the checking walk (checkMasterValidations) streams — exactly as a method
// body and the refinement predicate are.
func resolveMasterValidations(r *infer.TypeResolver, reg *builtin.Registry, def *ir.TypeDef, md *ast.MasterDecl, fns bodyFuncs) {
	self := ir.Type(&ir.Named{Def: def})
	for _, clause := range md.Validations {
		if !clause.PerRow {
			continue // a per-table check (validate all) is a later concern
		}
		binder := bodyBinder{r: r, reg: reg, selfType: self, funcs: fns, self: true}
		for _, s := range lower.Body(clause.Body, binder) {
			if a, ok := s.(*ir.AssertStmt); ok {
				def.Master.RowChecks = append(def.Master.RowChecks, a)
			}
		}
	}
}

// checkMaster reports a master's well-formedness problems: an absent or
// non-record row (master_missing_row — there is nothing to key), an absent
// primary key (master_missing_primary — a master with no key cannot identify a
// row), a primary column repeated (master_duplicate_primary_key — a key tuple
// must not name a column twice), and each named key that is not a field of the
// row (master_primary_unknown_field). row is the resolved row record, or nil
// when it is absent or not a record. deferredRow is true when the row is a
// generic record alias this slice does not expand: it is a real record, so it is
// neither reported as missing nor its keys checked (the fields are unknown). It
// runs only in the reporting pass (at/diags non-nil); the silent memoized pass
// builds the same definition without it, so the definitions and the diagnostics
// never disagree. Each diagnostic is anchored at the whole master declaration —
// the AST keeps the primary key as a bare name with no node of its own, so the
// declaration is the finest anchor available for now.
func checkMaster(md *ast.MasterDecl, row *ir.Record, deferredRow bool, at func(ast.Node) span, diags *diagnostic.List) {
	if at == nil || diags == nil {
		return
	}
	s := at(md)
	// A row predicate (where) is parsed but not yet given meaning — row validation
	// is later work — so it is rejected as unsupported rather than silently
	// dropped, which would lose a misspelled or intended constraint without a word.
	if md.Where != nil {
		diags.Add(newMasterWhereUnsupportedDiagnostic(s.offset, s.width, md.Name))
	}
	if row == nil && !deferredRow {
		diags.Add(newMasterMissingRowDiagnostic(s.offset, s.width, md.Name))
	}
	if len(md.Primary) == 0 {
		diags.Add(newMasterMissingPrimaryDiagnostic(s.offset, s.width, md.Name))
		return
	}
	// A repeated column is malformed regardless of the row, so the duplicate check
	// runs even when there is no field list to read (a deferred generic-alias row);
	// only the unknown-column check needs the row's fields, so it is skipped then —
	// the missing-row diagnostic above already covers a row that is truly absent.
	fields := map[string]bool{}
	if row != nil {
		fields = make(map[string]bool, len(row.Fields))
		for _, f := range row.Fields {
			fields[f.Name] = true
		}
	}
	seen := make(map[string]bool, len(md.Primary))
	for _, key := range md.Primary {
		switch {
		case seen[key]:
			diags.Add(newMasterDuplicatePrimaryKeyDiagnostic(s.offset, s.width, key, md.Name))
		case row != nil && !fields[key]:
			diags.Add(newMasterPrimaryUnknownFieldDiagnostic(s.offset, s.width, key, md.Name))
		}
		seen[key] = true
	}
}

// resolveAssocConsts resolves a type's associated constants — the impl block's
// `const` items, read as TypeName.Name — into ir.AssocConsts, in source order.
// Each carries its resolved type (the annotation when present, else the kind of
// its folded value) and its folded value (env folds the initializer; nil when
// no evaluator is supplied). A `= builtin` constant takes its value from the
// registry's value range — sbyte.Max/Min — and is rejected (no_bound) on a type
// with no bound on that side (the arbitrary-precision nint). A duplicate name
// keeps the first and is reported. tscope holds the type's generic-parameter
// names, so an annotation may mention them.
func resolveAssocConsts(r *infer.TypeResolver, reg *builtin.Registry, td *ast.TypeDecl, def *ir.TypeDef, tscope infer.TypeScope, at func(ast.Node) span, diags *diagnostic.List) {
	def.Consts = resolveAssocConstList(r, reg, def, td.Consts, tscope, at, diags)
}

// resolveAssocConstList is the shared resolution of a list of associated
// constants — used for both a type declaration's and an enum's impl block.
func resolveAssocConstList(r *infer.TypeResolver, reg *builtin.Registry, def *ir.TypeDef, decls []*ast.ConstDecl, tscope infer.TypeScope, at func(ast.Node) span, diags *diagnostic.List) []*ir.AssocConst {
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
			// constant (the arbitrary-precision nint): it resolves to nothing.
			// The declaration site needs no diagnostic here — a user file may
			// not write `= builtin` at all (the builtin-surface check reports
			// it), and the prelude, where the spelling is legal, is pinned
			// bound-for-bound by its own tests.
			value, ok := builtinBound(reg, def.Name, c.Name)
			if !ok {
				ac.Type = ir.Invalid
				out = append(out, ac)
				continue
			}
			ac.Value = value
			// A builtin bound is typed as the arbitrary-precision nint (the type of
			// an integer literal), not the concrete sized type: sbyte.Max is the
			// value 127, and like the literal 127 it adapts to whatever sized
			// integer it is compared or assigned to (self <= short.Max in an int
			// refinement). An explicit annotation overrides this.
			if annType != nil {
				ac.Type = annType
			} else {
				ac.Type = &ir.Builtin{Name: builtin.NameNint}
			}
			out = append(out, ac)
			continue
		}

		// An ordinary associated constant records its resolved annotation type
		// here; its initializer folds in resolveTypes' fourth pass, once every
		// type and enum of the file has resolved, so a cross-type reference
		// folds (foldAssocConsts — which also settles the type of an
		// unannotated constant from its folded value). The annotation is the
		// fold's expectation channel: a bare member resolves through its enum
		// (const Fav: Rarity = Legend), the assoc-const twin of a top-level
		// const's rule.
		ac.Type = annType
		out = append(out, ac)
	}
	return out
}

// builtinBound returns the value of a builtin associated constant — the named
// bound (Max or Min) of the named primitive's value range — and whether the
// primitive has that bound. A non-integer primitive, an unknown bound name, or
// an unbounded side (the arbitrary-precision nint, or nuint's missing upper
// bound) yields ok == false.
func builtinBound(reg *builtin.Registry, typeName, bound string) (*ir.Constant, bool) {
	native, ok := reg.Native(typeName)
	if !ok || !native.IsInteger() {
		return nil, false
	}
	lo, hi := native.Bounds()
	switch bound {
	case "Max":
		if hi == nil {
			return nil, false
		}
		return ir.IntConstant(hi), true
	case "Min":
		if lo == nil {
			return nil, false
		}
		return ir.IntConstant(lo), true
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
		return &ir.Builtin{Name: builtin.NameNint}
	case ir.ConstBool:
		return &ir.Builtin{Name: builtin.NameBool}
	case ir.ConstString:
		return &ir.Builtin{Name: builtin.NameString}
	case ir.ConstDatetime:
		return &ir.Builtin{Name: "datetime"}
	case ir.ConstDuration:
		return &ir.Builtin{Name: "duration"}
	case ir.ConstEnum:
		if v.EnumDef != nil {
			return &ir.Named{Def: v.EnumDef}
		}
		return ir.Invalid
	case ir.ConstType:
		// A type value's own type is the metatype `type` — so an associated
		// constant inferred from one (const C = sbyte) settles to it and the
		// storage-rule check then rejects the slot.
		return &ir.Builtin{Name: builtin.NameType}
	default:
		return ir.Invalid
	}
}

// resolveEnumDecl fills in an enum definition from its declaration: the base
// type (the integer family or string; the default nint when omitted), the member
// values determined by the enum member-value rules, and the operator methods (the six
// comparisons every enum has, plus the impl block's). Diagnostics — an invalid
// base type, an out-of-range or duplicate value, a duplicate member name — are
// reported through diags (nil in the silent memoized pass). env folds the
// member initializers (a constant expression may reference a top-level const);
// it is nil in callers that do not evaluate.
func resolveEnumDecl(folder exprFolder, defs map[string]*ir.TypeDef, r *infer.TypeResolver, reg *builtin.Registry, ed *ast.EnumDecl, def *ir.TypeDef, at func(ast.Node) span, diags *diagnostic.List, fns bodyFuncs) {
	// The base type: the annotation when present, else the default nint. It must
	// resolve to an integer-family or string primitive — anything else (bool, a
	// user type, a composite) is rejected, and the enum falls back to nint so the
	// rest of resolution stays well-formed.
	base := builtin.NameNint
	baseType := ir.Type(&ir.Builtin{Name: builtin.NameNint})
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

	resolveEnumMembers(folder, defs, reg, ed, def, base, baseType, isString, at, diags)

	// Duplicate values are forbidden outright: two members whose settled values
	// coincide — explicit or defaulted — are an error, reported at the second.
	checkDuplicateEnumValues(def, ed, at, diags)

	// The impl block's associated constants (read as EnumName.Name), the same
	// mechanism a type declaration's impl carries.
	def.Consts = resolveAssocConstList(r, reg, def, ed.Consts, nil, at, diags)

	resolveEnumMethods(r, reg, ed, def, at, diags, fns)
	// The accessor/static declaration checks, the same as a nominal type's: a
	// static fn collides with an enum member of the same name (both read
	// EnumName.Name), and an accessor's signature and field/method collisions are
	// checked too.
	checkMemberDecls(def, at, diags)
}

// resolveEnumMembers settles the enum's member values in declaration order:
// an explicit initializer folds against the base type; an omitted one
// takes the previous integer value plus one (zero for the first), or, for a
// string base, the member's own name. The values are settled for the whole enum
// before duplicate detection, so a diagnostic never leaves the value table in a
// half-built state. It reports a duplicate member name, an out-of-range value,
// and an initializer that does not type as the base.
func resolveEnumMembers(folder exprFolder, defs map[string]*ir.TypeDef, reg *builtin.Registry, ed *ast.EnumDecl, def *ir.TypeDef, base string, baseType ir.Type, isString bool, at func(ast.Node) span, diags *diagnostic.List) {
	def.Enum.Members = make([]ir.EnumMember, len(ed.Members))
	memberSeen := map[string]bool{}
	var prevInt *big.Int
	for i, m := range ed.Members {
		def.Enum.Members[i] = ir.EnumMember{Name: m.Name}

		// A duplicate member name is reported once, at the repeat.
		if m.Name != "" {
			if memberSeen[m.Name] && at != nil && diags != nil {
				s := at(m)
				diags.Add(newDuplicateEnumMemberDiagnostic(s.offset, s.width, m.Name))
			}
			memberSeen[m.Name] = true
		}

		value, graph, nextInt := enumMemberValue(folder, defs, m, isString, baseType, prevInt)
		prevInt = nextInt
		def.Enum.Members[i].Value = value
		def.Enum.Members[i].ValueGraph = graph
		reportEnumMemberValueErrors(reg, m, value, base, baseType, isString, at, diags)
	}
}

// reportEnumMemberValueErrors reports a member's value diagnostics: an integer
// base with a fixed width rejects an out-of-range value (constant_overflow,
// reusing the over-large const diagnostic), and a written initializer that does
// not type as the base (an int base given a string, say) is a type mismatch.
func reportEnumMemberValueErrors(reg *builtin.Registry, m *ast.EnumMember, value *ir.Constant, base string, baseType ir.Type, isString bool, at func(ast.Node) span, diags *diagnostic.List) {
	if at == nil || diags == nil {
		return
	}
	if value != nil && value.Kind == ir.ConstInt && !types.Fits(reg, baseType, value.Int) {
		s := enumValueSpan(at, m)
		diags.Add(newConstantOverflowDiagnostic(s.offset, s.width, value.String(), base))
	}
	if m.Value != nil && value != nil && !valueFitsBaseKind(value, isString) {
		s := enumValueSpan(at, m)
		diags.Add(newTypeMismatchDiagnostic(s.offset, s.width, kindName(value.Kind), base))
	}
}

// resolveEnumMethods attaches the enum's operator methods — the six comparisons
// every enum carries, then the impl block's own methods (which may shadow a
// comparison or add new ones) — reporting a duplicate same-name, same-kind,
// same-signature declaration.
func resolveEnumMethods(r *infer.TypeResolver, reg *builtin.Registry, ed *ast.EnumDecl, def *ir.TypeDef, at func(ast.Node) span, diags *diagnostic.List, fns bodyFuncs) {
	def.AttachMethods(builtin.EnumComparisonMethods()...)
	scope := infer.TypeScope{}
	seen := make(map[string]bool, len(ed.Methods))
	for _, m := range ed.Methods {
		rm := resolveMethod(r, reg, &ir.Named{Def: def}, m, scope, fns)
		// An enum method's parameter or result may not be a type value, the same
		// storage rule a nominal type's method obeys.
		reportMetatypeSlot(at, diags, m, sigType(rm.Params, rm.Result))
		key := rm.Name + signatureKey(def, rm)
		if m.Name != "" && seen[key] {
			reportDuplicateMethod(rm, def, m, at, diags)
			continue
		}
		seen[key] = true
		def.AttachMethods(rm)
	}
}

// reportDuplicateMethod reports a method that repeats an earlier same-name,
// same-kind, same-signature declaration. The diagnostic depends on the kind: an
// ordinary method is a dropped overload (duplicate_overload), a getter/setter is
// an accessor collision (a property name belongs to one accessor of each kind),
// and a static fn is a dropped function-style overload (duplicate_func_overload,
// reading Type.name). It is a no-op when there is nowhere to report.
func reportDuplicateMethod(rm *ir.Method, def *ir.TypeDef, m *ast.MethodDecl, at func(ast.Node) span, diags *diagnostic.List) {
	if at == nil || diags == nil {
		return
	}
	s := at(m)
	switch rm.Kind {
	case ir.MethodGetter, ir.MethodSetter:
		diags.Add(newAccessorCollisionDiagnostic(s.offset, s.width, rm.Name, def.Name))
	case ir.MethodStatic:
		diags.Add(newDuplicateFuncOverloadDiagnostic(s.offset, s.width, def.Name+"."+rm.Name, paramTypes(rm)))
	default:
		diags.Add(newDuplicateOverloadDiagnostic(s.offset, s.width, rm.Name, paramTypes(rm)))
	}
}

// enumMemberValue determines one member's value and the running integer counter
// for the next member. An explicit initializer folds through env (nil when no
// evaluator is supplied); an omitted one is the member's own name for a string
// base, or the previous integer plus one (zero for the first) for an integer
// base. nextInt is the counter the following member continues from — the folded
// value when it is an integer, else prev+1 so auto-numbering survives an
// unevaluable explicit value.
func enumMemberValue(folder exprFolder, defs map[string]*ir.TypeDef, m *ast.EnumMember, isString bool, baseType ir.Type, prevInt *big.Int) (value *ir.Constant, graph ir.Value, nextInt *big.Int) {
	if m.Value != nil {
		if folder.q != nil {
			binder := folder.binder(nil)
			binder.universe = defs
			// Keep the resolved graph for reachability: a member's initializer may
			// read a top-level const the folded value drops.
			graph = lower.Value(m.Value, binder)
			value = eval.GraphExpecting(graph, baseType, assocGraphEnv{q: folder.q, defs: defs})
		}
		if value != nil && value.Kind == ir.ConstInt {
			return value, graph, new(big.Int).Add(value.Int, big.NewInt(1))
		}
		return value, graph, nextIntCounter(prevInt)
	}
	if isString {
		// A string base defaults a member to its own name.
		if m.Name == "" {
			return nil, nil, prevInt
		}
		return ir.StringConstant(m.Name), nil, prevInt
	}
	// An integer base auto-numbers: zero for the first member, the previous
	// value plus one thereafter.
	n := nextIntCounter(prevInt)
	return ir.IntConstant(n), nil, new(big.Int).Add(n, big.NewInt(1))
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
// settled value coincides with an earlier member's — the no-alias rule, which
// keeps Name and Value in bijection. A member with no settled value (an
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
		return builtin.NameString
	case ir.ConstBool:
		return builtin.NameBool
	case ir.ConstInt:
		return builtin.NameNint
	default:
		return "value" //nolint:goconst // the other occurrences are generated diagnostic plumbing, not this vocabulary
	}
}

// methodKind maps the AST method kind onto the IR method kind. The two enums
// mirror each other; the mapping is explicit so a future divergence is a compile
// error rather than a silent miscarry.
func methodKind(k ast.MethodKind) ir.MethodKind {
	switch k {
	case ast.MethodGetter:
		return ir.MethodGetter
	case ast.MethodSetter:
		return ir.MethodSetter
	case ast.MethodStatic:
		return ir.MethodStatic
	default:
		return ir.MethodNormal
	}
}

// resolveMethod resolves a method's signature (parameter types and result type)
// and lowers its body to IR; fns is the file's function shells by name, so a
// body may call a top-level function. reg and self (the receiver type) let the
// body binder infer an inferred let's value type. The body is not yet
// type-checked.
func resolveMethod(r *infer.TypeResolver, reg *builtin.Registry, self ir.Type, m *ast.MethodDecl, scope infer.TypeScope, fns bodyFuncs) *ir.Method {
	method := &ir.Method{Name: m.Name, Public: m.Public, Extern: m.Extern, Kind: methodKind(m.Kind), Effects: m.Effects, Doc: m.Doc, Syntax: m}

	// Method-introduced type variables: the explicit ones (the A in fold<A>) and
	// the free type names appearing in a parameter type that the enclosing type
	// does not bind and that name no known type — the R in map(func: fn(T): R):
	// list<R>. They join the scope for this method's signature so they resolve to
	// ir.TypeVar instead of being reported unknown. Free names are scanned only in
	// parameter positions: a variable must be inferable from an argument, so an
	// unknown name in the result alone (a typo like `Nope`) stays an unknown-type
	// error rather than becoming a silent, unsolvable variable.
	mscope := scope
	paramTypes := make([]ast.TypeExpr, 0, len(m.Params))
	for _, p := range m.Params {
		paramTypes = append(paramTypes, p.Type)
	}
	free := r.FreeTypeVars(scope, paramTypes...)
	if len(m.TypeParams) > 0 || len(free) > 0 {
		mscope = make(infer.TypeScope, len(scope)+len(m.TypeParams)+len(free))
		for k, v := range scope {
			mscope[k] = v
		}
		for _, tp := range m.TypeParams {
			mscope[tp.Name] = nil
		}
		for _, v := range free {
			mscope[v] = nil
		}
		// Resolve each explicit method type parameter's bound in the full method
		// scope and back-fill it, so a parameter naming it carries the bound. The
		// two-pass settle lets a bound project off another parameter (T: Box<U.x>).
		infer.SettleBounds(r, m.TypeParams, mscope)
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
	// A static fn has no receiver: its body lowers with self unbound, exactly as the
	// checker types it (Self ir.Invalid), so a bare name there reads a top-level
	// constant or a type — never an implicit self.field — and the lowered IR matches
	// the checked expression. An instance method, getter, or setter binds self.
	selfType, hasSelf := self, true
	if m.Kind == ast.MethodStatic {
		selfType, hasSelf = ir.Invalid, false
	}
	method.Body = lower.Body(m.Body, bodyBinder{r: r, reg: reg, params: params, paramTypes: resolvedParams, selfType: selfType, tscope: mscope, funcs: fns, self: hasSelf})
	return method
}

// checkMemberDecls runs the declaration-site checks for accessors and static
// fns over a resolved type: each accessor/static method's signature is verified
// by kind (a getter takes no parameters, a setter takes one and returns self, a
// static fn is not generic in the MVP), and the three member name spaces are
// checked for collisions — an accessor against a record field, an ordinary
// method, or a same-kind duplicate of the same name; a static fn against an
// associated constant or enum member of the same name. The checks are gathered
// here so every kind's declaration rule lives in one place, run once per type
// after its methods, fields, constants, and enum members are resolved. It is a
// no-op when there is nowhere to report (the prelude, a memoized resolution).
func checkMemberDecls(def *ir.TypeDef, at func(ast.Node) span, diags *diagnostic.List) {
	if at == nil || diags == nil {
		return
	}
	fields := memberFields(def)
	consts := make(map[string]bool, len(def.Consts))
	for _, c := range def.Consts {
		consts[c.Name] = true
	}
	members := map[string]bool{}
	if def.Enum != nil {
		for _, em := range def.Enum.Members {
			members[em.Name] = true
		}
	}
	// An accessor pairs a getter with a setter of the same name (that is a
	// property), so a get and a set of one name do not collide with each other.
	// Every other same-name member across the value/method/type name spaces does.
	accessorSeen := map[string]ir.MethodKind{}
	for _, m := range def.Methods {
		if m.Syntax == nil || m.Name == "" {
			continue // a bootstrap/synthesized method carries no declaration to anchor
		}
		s := at(m.Syntax)
		switch m.Kind {
		case ir.MethodGetter, ir.MethodSetter:
			checkAccessorDecl(def, m, s, fields, accessorSeen, diags)
		case ir.MethodStatic:
			checkStaticDecl(def, m, s, consts, members, diags)
		default:
			// An ordinary method (MethodNormal) has no accessor- or
			// static-specific collision rule to check here: move to the next.
		}
	}
}

// checkAccessorDecl checks one getter/setter declaration: its signature (a
// getter takes no parameters, a setter one parameter and a self result) and its
// collisions — a record field, an ordinary method of the same name, or a second
// accessor of the same kind. A getter+setter pair of one name is the property
// and is allowed, so the seen map records the kind.
func checkAccessorDecl(def *ir.TypeDef, m *ir.Method, s span, fields map[string]bool, accessorSeen map[string]ir.MethodKind, diags *diagnostic.List) {
	if m.Kind == ir.MethodGetter && len(m.Params) != 0 {
		diags.Add(newInvalidGetterSignatureDiagnostic(s.offset, s.width, m.Name))
	}
	if m.Kind == ir.MethodSetter && (len(m.Params) != 1 || !isSelfResult(m.Result)) {
		diags.Add(newInvalidSetterSignatureDiagnostic(s.offset, s.width, m.Name))
	}
	if fields[m.Name] || hasNormalMethod(def, m.Name) || accessorSeen[m.Name] == m.Kind {
		diags.Add(newAccessorCollisionDiagnostic(s.offset, s.width, m.Name, def.Name))
	}
	accessorSeen[m.Name] = m.Kind
}

// checkStaticDecl checks one static fn declaration: a static fn may not be
// generic, and it collides with an associated constant or enum member of the
// same name (both read EnumName.Name).
func checkStaticDecl(def *ir.TypeDef, m *ir.Method, s span, consts, members map[string]bool, diags *diagnostic.List) {
	if m.Syntax != nil && len(m.Syntax.TypeParams) > 0 {
		diags.Add(newGenericStaticDiagnostic(s.offset, s.width, m.Name))
	}
	if consts[m.Name] || members[m.Name] {
		diags.Add(newStaticCollisionDiagnostic(s.offset, s.width, m.Name, def.Name))
	}
}

// recordFieldNames returns the set of field names a record body declares, or an
// empty set for a non-record body (a nominal type over a primitive, an enum).
func recordFieldNames(body ir.Type) map[string]bool {
	rec, ok := body.(*ir.Record)
	if !ok {
		return map[string]bool{}
	}
	names := make(map[string]bool, len(rec.Fields))
	for _, f := range rec.Fields {
		names[f.Name] = true
	}
	return names
}

// underlyingRecord returns the record a type denotes — the record itself, or the
// one a named record type aliases (record Row, type Row = { ... }) — or nil when
// it is neither. It looks through a nominal alias one level at a time, guarding a
// self-referential definition, so a master's row resolves whether it is written
// inline or by name.
func underlyingRecord(t ir.Type) *ir.Record {
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

// isGenericRecordAlias reports whether t is a generic record alias applied to
// type arguments (record Row<int>, type Row<T> = { ... }) — an application this
// slice does not expand into row fields. It tells a master row written with a
// generic alias (a real record, deferred) apart from one that is genuinely not a
// record, so the former is not reported as a missing row.
func isGenericRecordAlias(t ir.Type) bool {
	app, ok := t.(*ir.App)
	return ok && app.Def != nil && underlyingRecord(app.Def.Body) != nil
}

// memberFields returns the field names the member-collision checks compare an
// accessor or static against. A type's or enum's fields are its record body's; a
// master keeps Body nil and stores its row as a type on the descriptor, so its
// fields are read from the row record — without this a master getter/setter could
// shadow a row field uncaught.
func memberFields(def *ir.TypeDef) map[string]bool {
	if def.Master != nil {
		return recordFieldNames(underlyingRecordOf(def.Master.Row))
	}
	return recordFieldNames(def.Body)
}

// underlyingRecordOf returns a master row type's record as an ir.Type for the
// field-name helper (nil when the row is absent or a form this slice does not
// expand), so recordFieldNames reads its fields the same way it reads a body's.
func underlyingRecordOf(row ir.Type) ir.Type {
	if rec := underlyingRecord(row); rec != nil {
		return rec
	}
	return nil
}

// dedupeStrings returns names with later duplicates dropped, preserving the
// first occurrence's order. It keeps a primary key tuple free of repeated
// columns in the IR even when the repeat is reported as a diagnostic.
func dedupeStrings(names []string) []string {
	if len(names) == 0 {
		return names
	}
	seen := make(map[string]bool, len(names))
	out := make([]string, 0, len(names))
	for _, n := range names {
		if seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out
}

// hasNormalMethod reports whether def declares an ordinary instance method of
// the given name — an accessor of the same name collides with it (the read
// `value.name` would be ambiguous with the method value `value.name`).
func hasNormalMethod(def *ir.TypeDef, name string) bool {
	for _, m := range def.Methods {
		if m.Kind == ir.MethodNormal && m.Name == name {
			return true
		}
	}
	return false
}

// isSelfResult reports whether a method's result type is self — the shape a
// setter must return (it computes the next value of its own type).
func isSelfResult(t ir.Type) bool {
	_, ok := t.(*ir.SelfType)
	return ok
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
func resolveFuncs(file *ast.File, at func(ast.Node) span, diags *diagnostic.List, reg *builtin.Registry, universe map[string]*ir.TypeDef, qualified func(namespace, name string) *ir.TypeDef, fns bodyFuncs) []*ir.Function {
	if len(file.Funcs) == 0 {
		return nil
	}
	r := &infer.TypeResolver{
		Defs:           universe,
		Qualified:      qualified,
		Report:         unknownTypeReporter(at, diags),
		Registry:       reg,
		BoundViolation: boundViolationReporter(at, diags),
		ArityMismatch:  arityMismatchReporter(at, diags),
	}
	shells := fns.shells
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
