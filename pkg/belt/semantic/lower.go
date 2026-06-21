package semantic

import (
	"github.com/masterbelt/masterbelt/pkg/belt/builtin"
	"github.com/masterbelt/masterbelt/pkg/belt/lower"
	"github.com/masterbelt/masterbelt/pkg/belt/types"
	"github.com/masterbelt/masterbelt/pkg/belt/types/infer"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// constBinder lowers the leaves of a constant initializer: a value-position
// identifier — or a namespace member access (geo.Origin) — binds to its
// declaration's *Const, and a call whose callee names a top-level function
// binds to its *Function (both through the resolution queries and the
// program-wide shell tables); no other leaf form lowers in a constant. The
// file is the one the initializer sits in, scoping its resolution.
type constBinder struct {
	q    queries
	file FileID
	irOf map[*ast.ConstDecl]*ir.Const
	fnOf map[*ast.FuncDecl]*ir.Function
	// expected is the enum a bare member lowers through (the const's
	// annotation), or nil when there is none. It reaches only the initializer's
	// top leaf — a bare member is meaningful only as the const's whole value.
	expected *ir.TypeDef
	// universe, when non-nil, overrides the file's universe query for type-name
	// resolution — the in-flight defs map the type-resolution pass supplies, so
	// an eager fold inside the memoized typeDefs computation (an enum member's
	// or associated constant's initializer) resolves the file's own types
	// without re-entering its own query.
	universe map[string]*ir.TypeDef
}

// uni is the type-name surface the binder resolves against: the override when
// the caller supplied one, else the file's universe query.
func (b constBinder) uni() map[string]*ir.TypeDef {
	if b.universe != nil {
		return b.universe
	}
	return b.q.universe(b.file)
}

func (b constBinder) Leaf(e ast.Expr, sub func(ast.Expr) ir.Value) ir.Value {
	switch e := e.(type) {
	case *ast.NullLit:
		// The null literal is a value like any other (const Absent:
		// optional<nint> = null), so the graph carries it; whether null fits
		// the declared type is the checker's question.
		return &ir.NullValue{Syntax: e}
	case *ast.Identifier:
		return b.leafConstIdent(e)
	case *ast.MemberExpr:
		return b.leafConstMember(e, sub)
	case *ast.CallExpr:
		return b.leafConstCall(e, sub)
	}
	return nil
}

// leafConstIdent lowers a bare name in a constant initializer: a constant
// reference, a bare member of the expected enum (const Top: Rarity = Legend), or
// — last, a value of the name winning — a bare type name reified to a type value
// (const x = sbyte; const t = type).
func (b constBinder) leafConstIdent(e *ast.Identifier) ir.Value {
	if target := b.q.resolve(b.file, e); target != nil {
		return &ir.Reference{Target: b.irOf[target], Syntax: e}
	}
	if idx := enumIndex(b.expected, e.Name); idx >= 0 {
		return &ir.EnumMemberValue{Def: b.expected, Index: idx, Syntax: e}
	}
	if def, ok := b.uni()[e.Name]; ok {
		return &ir.TypeValue{Reified: reifyType(def), Syntax: e}
	}
	return nil
}

// leafConstMember lowers a member access in a constant initializer: a namespace
// import reference (geo.Origin), an enum member (Rarity.Common) or associated
// constant (sbyte.Max, Level.Max) on a type name, or — otherwise — a field access
// on a record-typed constant (Hero.lv).
func (b constBinder) leafConstMember(e *ast.MemberExpr, sub func(ast.Expr) ir.Value) ir.Value {
	if target := b.q.resolveMember(b.file, e); target != nil {
		return &ir.Reference{Target: b.irOf[target], Syntax: e}
	}
	shadowed := func(id *ast.Identifier) bool { return b.q.resolve(b.file, id) != nil }
	qualified := qualifiedFrom(b.q, b.q.importsOf(b.file))
	if def := infer.MemberReceiverDef(b.uni(), qualified, shadowed, e.Receiver); def != nil {
		if v := typeMemberValue(b.q.registry(), def, e); v != nil {
			return v
		}
	}
	// A namespace-qualified type name used as a value (geo.Item, no trailing
	// projection) reifies to a type value, the qualified twin of a bare local type
	// name; a value shadowing the namespace name defers to the field access below.
	if v := qualifiedTypeValue(qualified, shadowed, e); v != nil {
		return v
	}
	return &ir.FieldAccess{Receiver: sub(e.Receiver), Field: e.Member.Name, Syntax: e}
}

// qualifiedTypeValue lowers a namespace-qualified type name used as a value
// (geo.Item) to its reified type value — the qualified twin of a bare local type
// name (leafConstIdent's Item). The resolution decision (a namespace export that
// is a type, not shadowed by a value) is the shared infer.QualifiedTypeDef the
// checker uses; this is the value-position wrapper that reifies the def to a type
// value, so a body and a const agree on a bare qualified type value.
func qualifiedTypeValue(qualified func(namespace, name string) *ir.TypeDef, valueShadows func(*ast.Identifier) bool, e *ast.MemberExpr) ir.Value {
	if def := infer.QualifiedTypeDef(qualified, valueShadows, e); def != nil {
		return &ir.TypeValue{Reified: reifyType(def), Syntax: e}
	}
	return nil
}

// leafConstCall lowers a call in a constant initializer: a conversion T(x) when
// the callee names a type, a top-level (or namespace-qualified) function call, or
// a static fn call on a type name (Celsius.freezing()). A constant initializer
// has no locals or params, so no name shadows the type.
func (b constBinder) leafConstCall(e *ast.CallExpr, sub func(ast.Expr) ir.Value) ir.Value {
	switch callee := e.Callee.(type) {
	case *ast.Identifier:
		if def, ok := b.uni()[callee.Name]; ok {
			return &ir.Conversion{Type: reifyType(def), Args: convArgs(e.Arguments, sub), Syntax: e}
		}
		if cands := b.q.resolveFunc(b.file, callee); len(cands) > 0 {
			return funcCall(b.fnOf[pickOverload(cands, len(e.Arguments))], e, sub)
		}
	case *ast.MemberExpr:
		if cands := b.q.resolveFuncMember(b.file, callee); len(cands) > 0 {
			return funcCall(b.fnOf[pickOverload(cands, len(e.Arguments))], e, sub)
		}
		if def := staticFnDef(b.uni(), callee, nil); def != nil {
			return staticCall(def, callee.Member.Name, e, sub)
		}
	}
	return nil
}

// pickOverload narrows an overload set to the call's target for the untyped
// value graph: the sole candidate whose arity matches, or — when the argument
// types are needed to decide — the set's first declaration as the
// representative (the set shares the name; a typed consumer re-selects).
func pickOverload(cands []*ast.FuncDecl, arity int) *ast.FuncDecl {
	var match *ast.FuncDecl
	n := 0
	for _, fd := range cands {
		if len(fd.Params) == arity {
			match = fd
			n++
		}
	}
	if n == 1 {
		return match
	}
	return cands[0]
}

func (b constBinder) EnterFunc(params []*ast.ParamDef) lower.Binder { return enterFunc(b, params) }

// reifyType builds the type a universe definition denotes — a primitive's
// Builtin, a declared type's Named — the form a reified type value carries. It
// is the value-graph twin of the type resolver's named-type rule (a `= builtin`
// def is its own primitive; any other def is named by its definition).
func reifyType(def *ir.TypeDef) ir.Type {
	if def.Builtin {
		return &ir.Builtin{Name: def.Name}
	}
	return &ir.Named{Def: def}
}

// enumIndex returns the index of the named member of an enum definition, or -1
// when def is not an enum or has no such member.
func enumIndex(def *ir.TypeDef, name string) int {
	if def == nil || def.Enum == nil {
		return -1
	}
	for i, m := range def.Enum.Members {
		if m.Name == name {
			return i
		}
	}
	return -1
}

// typeMemberValue lowers a member access whose receiver names the type def to its
// value node — an enum member or an associated constant — through the single
// member resolver (types.ResolveMember). It returns nil for a static fn (read
// without a call) or no match, which the caller takes as a record-field access.
// It is the one place T.member becomes a value, shared by the const and the
// method-body value lowering, so there is no second member resolution.
func typeMemberValue(reg *builtin.Registry, def *ir.TypeDef, m *ast.MemberExpr) ir.Value {
	if def == nil {
		return nil // the receiver names no type; the caller reads a record-field access
	}
	switch r := types.ResolveMember(def, m.Member.Name); r.Kind {
	case types.MemberEnum:
		return &ir.EnumMemberValue{Def: def, Index: r.Index, Syntax: m}
	case types.MemberConst:
		return &ir.AssocConstValue{Def: def, Index: r.Index, Syntax: m}
	case types.MemberNone, types.MemberStatic:
		// A readable member of a type — a declared field of a record (or master),
		// or a getter — projected in value position (Character.level), is a type
		// value of that member's type: the comptime projection a consuming
		// expression (assert Character.id == long) reads. The type is read from the
		// settled body, so nominal identity is preserved. A static fn read without a
		// call, or any other non-readable member, returns nil, which the caller
		// takes as an instance field access (item.level — the runtime read).
		if t, ok := types.ReadableMemberType(reg, reifyType(def), m.Member.Name); ok {
			return &ir.TypeValue{Reified: t, Syntax: m}
		}
	}
	return nil
}

// staticFnDef resolves a call whose callee is a member access on a type name to
// the owning definition, when the type declares a static fn of that name:
// Celsius.freezing() yields Celsius's def. It returns nil when the receiver names
// no known type or the type has no static fn of that name — the same fall-through
// the enum-member and associated-constant claims give, so the static call shares
// the Type.Name path. shadow reports whether the receiver name is shadowed by a
// local or parameter (a value of that name, not the type); a shadowed name is not
// a static call.
func staticFnDef(universe map[string]*ir.TypeDef, callee *ast.MemberExpr, shadow func(string) bool) *ir.TypeDef {
	recv, ok := callee.Receiver.(*ast.Identifier)
	if !ok || (shadow != nil && shadow(recv.Name)) {
		return nil
	}
	// A metatype method (eql/neq) on a type name is type-value equality, never a
	// static call — even when the type also declares a static of that name — so it
	// lowers to a method call on the reified type value, mirroring the type rule.
	if types.IsMetatypeMethod(universe[builtin.NameType], callee.Member.Name) {
		return nil
	}
	def := universe[recv.Name]
	// An interface's static entries are requirements, not implementations, so a
	// direct I.make() has nothing to lower (and the checker rejects it); only a
	// concrete type's static, or a bounded parameter's (boundStaticDef), is a call.
	if def != nil && def.Interface != nil {
		return nil
	}
	if types.ResolveMember(def, callee.Member.Name).Kind != types.MemberStatic {
		return nil
	}
	return def
}

// staticCall lowers a static-fn call to its IR value: the owning definition, the
// static fn name, and the lowered arguments. The call expression rides along as
// the node's Syntax, the key the checker's overload selection is written back
// through.
func staticCall(def *ir.TypeDef, name string, e *ast.CallExpr, sub func(ast.Expr) ir.Value) ir.Value {
	return &ir.StaticCall{Def: def, Name: name, Args: convArgs(e.Arguments, sub), Syntax: e}
}

// bodyFuncs is what a body binder needs to lower function calls: the file's
// own shells by name, the namespace-qualified declaration lookup (nil when no
// namespaces are in scope), and the program-wide shell table mapping a
// resolved declaration to its shell.
type bodyFuncs struct {
	local     map[string][]*ir.Function
	qualified func(namespace, name string) []*ast.FuncDecl
	shells    map[*ast.FuncDecl]*ir.Function
	// constRef resolves a value-position identifier to the shell of the
	// top-level constant it names (nil when it names none), and nsConstRef a
	// namespace member access (geo.Origin) likewise — the channels a body's
	// reference to a constant lowers to an ir.Reference through, the body twin
	// of the const binder's resolution. Both are nil in contexts with no
	// constant surface (a refinement predicate, whose type rules forbid one).
	constRef   func(*ast.Identifier) *ir.Const
	nsConstRef func(*ast.MemberExpr) *ir.Const
}

// constRefFrom builds the constRef channel over the queries: resolution to the
// declaration, then to its program-wide shell.
func constRefFrom(q queries, file FileID) func(*ast.Identifier) *ir.Const {
	return func(id *ast.Identifier) *ir.Const {
		if decl := q.resolve(file, id); decl != nil {
			return q.constShellTable()[decl]
		}
		return nil
	}
}

// constShadowsFrom builds a body's const-shadowing predicate: whether a name
// resolves to a top-level declaration — a const (or function) that shadows a
// same-named namespace import in value position, so a qualified member off it
// (geo.Item, geo.Item.id) is read as a value rather than reified as the imported
// type. It is the body twin of the const initializer's resolve check, shared by
// the type-check, effect, and pure-context body walks so they agree on shadowing.
func constShadowsFrom(q queries, file FileID) func(*ast.Identifier) bool {
	return func(id *ast.Identifier) bool { return q.resolve(file, id) != nil }
}

// namespaceShadowsFrom builds the namespace-import predicate the value-position
// type-parameter check consults: a name that is a namespace import (and no value)
// shadows a same-named type parameter in value position, so a member read off it
// (T.x reading the import's exported member) is not a value use of the parameter.
func namespaceShadowsFrom(q queries, file FileID) func(*ast.Identifier) bool {
	return func(id *ast.Identifier) bool { return isNamespace(file, id, q) }
}

// nsConstRefFrom builds the nsConstRef channel over the queries, mirroring
// constRefFrom for a namespace member access.
func nsConstRefFrom(q queries, file FileID) func(*ast.MemberExpr) *ir.Const {
	return func(m *ast.MemberExpr) *ir.Const {
		if decl := q.resolveMember(file, m); decl != nil {
			return q.constShellTable()[decl]
		}
		return nil
	}
}

// astByName projects the local shells back to their declarations by name, the
// form a typing scope wants (BodyScope.Funcs) — so inferring an inferred let's
// value (let x = f(1)) sees the file's functions exactly as the checking walk
// does. A shell with no syntax (only the prelude has none) is skipped.
func (f bodyFuncs) astByName() map[string][]*ast.FuncDecl {
	if len(f.local) == 0 {
		return nil
	}
	out := make(map[string][]*ast.FuncDecl, len(f.local))
	for name, shells := range f.local {
		decls := make([]*ast.FuncDecl, 0, len(shells))
		for _, s := range shells {
			if s.Syntax != nil {
				decls = append(decls, s.Syntax)
			}
		}
		if len(decls) > 0 {
			out[name] = decls
		}
	}
	return out
}

// bodyBinder lowers the leaves of a method or function body: self (methods
// only), a parameter reference, a record field access (recv.field), a type
// conversion (T(x), when the callee names a type), a call of a top-level
// function (by name or through a namespace import), or nothing. The type-name
// resolution for a conversion is the resolver's; params and tscope are the
// parameter and generic-parameter names in scope, and funcs the function
// surface callable from the body.
type bodyBinder struct {
	r      *infer.TypeResolver
	reg    *builtin.Registry
	params map[string]bool
	// paramTypes maps each parameter to its resolved type, so a switch can read
	// the scrutinee's enum (the expected type its bare-member arms resolve
	// through) without consulting the type query. selfType is the receiver's
	// type (a method body), used the same way for a "switch self".
	paramTypes map[string]ir.Type
	selfType   ir.Type
	tscope     infer.TypeScope
	funcs      bodyFuncs
	self       bool // whether self has a value here (a method body; never a function's)
	// relation is set in a validate all clause, where the subject is the master's
	// relation rather than a row: a bare count there reads the relation's row count
	// (an aggregate over the table), the per-table counterpart of self omission.
	relation bool
	// relationSelf is the master a static fn's self denotes, or nil. A master's
	// static fn reads self as the master's relation (the rows the query methods
	// operate on), so self there lowers to the same MasterRelation a bare master name
	// does — the query chain over self lowers like one over the name. It is nil for
	// every other body (an instance method's self is a row, a function has none).
	relationSelf *ir.TypeDef
	// locals maps each let-bound block-local in scope to its settled type. A
	// reference to one lowers to an ir.LocalRef (shadowing a same-named parameter
	// or type), and its type is read here when inferring a later let's value. It
	// grows as a block's lets are lowered (LetLocal) and is the body counterpart
	// of paramTypes for mutable locals.
	locals map[string]ir.Type
	// columnsMaster is the master a bare-column query argument reads its columns off
	// — set only on the derived binder that lowers a relation method's bare argument
	// (where(cost > 100), sum(cost)), nil everywhere else. A bare name that is a column
	// of it lowers to the same field access the lambda form's c.name does, off the
	// synthesized columnsParam binding, so the columns binding is omitted the way self
	// is. It is last resort, so a local, parameter, or constant of the same name wins.
	columnsMaster *ir.TypeDef
	columnsParam  string
}

func (b bodyBinder) EnterFunc(params []*ast.ParamDef) lower.Binder { return enterFunc(b, params) }

// LetLocal records a let-bound local on top of this binder's scope and returns
// the extended binder and the binding's settled type: the annotation when one is
// written, otherwise the value's type inferred against the locals (and params)
// already in scope. The locals map is copied so the extension reaches only the
// statements after the let, leaving an outer block's binder untouched — which is
// what gives let block scoping and lets an inner let shadow an outer one.
func (b bodyBinder) LetLocal(name string, annotation ast.TypeExpr, value ast.Expr) (lower.Binder, ir.Type) {
	var typ ir.Type
	switch {
	case annotation != nil:
		typ = b.r.ResolveType(annotation, b.tscope)
	case value != nil:
		typ = infer.Body(value, b.bodyScope())
	default:
		typ = ir.Invalid
	}
	next := b
	next.locals = make(map[string]ir.Type, len(b.locals)+1)
	for k, v := range b.locals {
		next.locals[k] = v
	}
	next.locals[name] = typ
	return next, typ
}

// bodyScope builds the typing scope an inferred let's value is typed against:
// the binder's params and the locals already in scope, with self and the
// function surface, so the inference matches the checking walk. It carries no
// diagnostic sink — the checking pass reports the value's errors; this is only
// to settle the binding's type for the IR.
func (b bodyBinder) bodyScope() infer.BodyScope {
	self := ir.Invalid
	if b.self {
		self = b.selfType
	}
	return infer.BodyScope{
		Reg:            b.reg,
		Universe:       b.r.Defs,
		Qualified:      b.r.Qualified,
		Self:           self,
		Params:         b.paramTypes,
		Locals:         b.locals,
		Funcs:          b.funcs.astByName(),
		QualifiedFuncs: b.funcs.qualified,
		ConstShadows:   b.constShadows,
		// The generic type parameters in scope, so an inferred let's value that
		// names one (let y = T(v)) settles to the same type the checking walk gives
		// it — the conversion's parameter type — rather than an unknown type, keeping
		// the binding's IR type consistent with the lowered conversion.
		TScope: b.tscope,
	}
}

// constShadows reports whether a name is bound by a top-level constant, so an
// inferred let's value (let x = geo.Item) settles its qualified member against a
// const receiver named geo rather than the imported type — agreeing with the
// lowering's own valueShadows. It is nil-safe through constRef.
func (b bodyBinder) constShadows(id *ast.Identifier) bool {
	return b.funcs.constRef != nil && b.funcs.constRef(id) != nil
}

// ExpectedEnum returns the enum definition a switch scrutinee's static type
// names, so its bare-member arms (Common rather than Rarity.Common) lower to
// enum-member values. It reads the type syntactically from the binder's scope
// — a let-bound local (which shadows a same-named parameter), a parameter's
// resolved type, the receiver's type for self — without the type query, keeping
// value lowering independent of typing. A scrutinee whose enum cannot be read
// this way yields nil, and its bare members stay unresolved (the qualified form
// always works).
func (b bodyBinder) ExpectedEnum(scrutinee ast.Expr) *ir.TypeDef {
	switch e := scrutinee.(type) {
	case *ast.Identifier:
		if t, ok := b.locals[e.Name]; ok {
			return enumDefOf(t)
		}
		if t, ok := b.paramTypes[e.Name]; ok {
			return enumDefOf(t)
		}
	case *ast.SelfExpr:
		if b.self {
			return enumDefOf(b.selfType)
		}
	}
	return nil
}

// AnnotationEnum resolves a written type annotation to the enum it names — a
// let initializer's bare member (let r: Rarity = Legend) resolves through it,
// the body twin of a const's annotationEnum. It is the resolver's universe
// lookup, the same path a parameter or let annotation takes, never the type
// query — so the value lowering stays independent of typing. A union, named, or
// generic alias of an enum unwraps the same way the receiver channel does.
func (b bodyBinder) AnnotationEnum(t ast.TypeExpr) *ir.TypeDef {
	if t == nil {
		return nil
	}
	return enumDefOf(b.r.ResolveType(t, b.tscope))
}

// EnumMember resolves a bare member identifier against an enum definition to
// its enum-member value, or nil when def has no such member — the bare-member
// rule a switch arm shares with a const initializer. The identifier rides
// along as the node's Syntax anchor.
func (b bodyBinder) EnumMember(def *ir.TypeDef, id *ast.Identifier) ir.Value {
	if idx := enumIndex(def, id.Name); idx >= 0 {
		return &ir.EnumMemberValue{Def: def, Index: idx, Syntax: id}
	}
	return nil
}

// ArmType resolves a match arm's member type expression to its resolved type —
// the resolver's universe lookup, the same path an annotation takes, never the
// type query — so the value lowering stays independent of typing. It satisfies
// lower.MatchBinder.
func (b bodyBinder) ArmType(t ast.TypeExpr) ir.Type {
	return b.r.ResolveType(t, b.tscope)
}

// NarrowLocal records the arm's binding name at the narrowed arm type on top of
// this binder's scope and returns the extended binder, so a reference to the
// binding inside the arm body lowers to an ir.LocalRef. The locals map is copied
// so the narrowing reaches only that arm body, not a sibling arm or an enclosing
// block — the same block scoping LetLocal gives. It satisfies lower.MatchBinder.
func (b bodyBinder) NarrowLocal(name string, typ ir.Type) lower.Binder {
	next := b
	next.locals = make(map[string]ir.Type, len(b.locals)+1)
	for k, v := range b.locals {
		next.locals[k] = v
	}
	next.locals[name] = typ
	return next
}

// ForLocal resolves a for loop's variable type from the iterated expression —
// the foldable's value type for an of-loop, its key type for an in-loop — and
// binds the variable at that type for the loop body, returning the extended
// binder. The iter type is inferred against the binder's scope (no type query),
// and ForElement reads the element type from its foldable impl; an unfoldable
// iter binds ir.Invalid (the semantic layer reports not_iterable). It reuses
// NarrowLocal for the block-scoped binding, so the loop body sees the variable
// exactly as a match arm sees its narrowed binding. It satisfies lower.ForBinder.
func (b bodyBinder) ForLocal(name string, iter ast.Expr, of bool) (lower.Binder, ir.Type) {
	iterT := infer.Body(iter, b.bodyScope())
	elem, _ := types.ForElement(b.reg, iterT, of)
	return b.NarrowLocal(name, elem), elem
}

// selfHasMethod reports whether the receiver type binds a method of the given
// name — what an implicit self-call (a bare call inside a method body) needs to
// distinguish a self-method from an unresolved name. It uses the same overload
// lookup a written self.method() call would, so the two forms resolve
// identically.
func (b bodyBinder) selfHasMethod(name string) bool {
	if b.selfType == nil || b.selfType == ir.Invalid {
		return false
	}
	_, _, ok := types.Candidates(b.reg, b.selfType, name)
	return ok
}

// enumDefOf returns the enum definition a type names, or nil when it carries
// none. It is the semantic layer's name for types.EnumDef, the single channel a
// bare member resolves through: a nominal enum, a union carrying one (R | error),
// and — unwrapped through types.UnionType — a named or generic union alias of one
// (optional<Rarity>) all resolve, so the editor completion, the lowering, and the
// fold agree on the same member set wherever a syntactic enum is expected.
func enumDefOf(t ir.Type) *ir.TypeDef {
	return types.EnumDef(t)
}

// shadows reports whether name is bound by a let local or a parameter, either of
// which shadows a same-named type or top-level function in value position.
func (b bodyBinder) shadows(name string) bool {
	if _, ok := b.locals[name]; ok {
		return true
	}
	return b.params[name]
}

// valueShadows reports whether a namespace identifier is shadowed by a value in
// the body — a let-bound local, a parameter, or a top-level const named like the
// import — so a qualified type member (geo.Item, geo.Item.id) defers to that value
// receiver rather than reifying the imported type. It is the body twin of the
// const initializer's resolve check, which already catches a same-named const.
func (b bodyBinder) valueShadows(id *ast.Identifier) bool {
	if b.shadows(id.Name) {
		return true
	}
	return b.funcs.constRef != nil && b.funcs.constRef(id) != nil
}

func (b bodyBinder) Leaf(e ast.Expr, sub func(ast.Expr) ir.Value) ir.Value {
	switch e := e.(type) {
	case *ast.SelfExpr:
		if !b.self {
			return nil // a function body has no receiver
		}
		if b.relationSelf != nil {
			// A master's static fn reads self as the master's relation — the same value
			// a bare master name lowers to — so a query chain over self lowers like one
			// over the name and the master-layer driver interprets it identically.
			return &ir.MasterRelation{Master: b.relationSelf, Syntax: e}
		}
		return &ir.SelfValue{Syntax: e}
	case *ast.NullLit:
		return &ir.NullValue{Syntax: e}
	case *ast.Identifier:
		return b.leafIdentifier(e)
	case *ast.MemberExpr:
		return b.leafMember(e, sub)
	case *ast.CallExpr:
		return b.leafCall(e, sub)
	default:
		return nil
	}
}

// leafIdentifier lowers a bare name in value position. The ordinary scope is
// read first — a let local (which shadows a same-named parameter), a parameter, a
// top-level constant, a type name — and a readable member of self is the last
// resort: a bare name lowers to self.X only where it resolves no other way (self
// omission), so power means self.power exactly where the checker's body leaf reads
// it as one, in every walk.
func (b bodyBinder) leafIdentifier(e *ast.Identifier) ir.Value {
	if _, ok := b.locals[e.Name]; ok {
		return &ir.LocalRef{Name: e.Name, Syntax: e}
	}
	if b.params[e.Name] {
		return &ir.ParamRef{Name: e.Name, Syntax: e}
	}
	if b.funcs.constRef != nil {
		if c := b.funcs.constRef(e); c != nil {
			return &ir.Reference{Target: c, Syntax: e}
		}
	}
	// A bare master name is its relation — the value the query operations are
	// methods on; every other bare type name reifies to a type value (long == long,
	// the receiver of a metatype method call), the same reading the constant binder
	// gives it, so a body and a const agree and the metatype comparison folds.
	if def, ok := b.r.Defs[e.Name]; ok {
		if def.Master != nil {
			return &ir.MasterRelation{Master: def, Syntax: e}
		}
		return &ir.TypeValue{Reified: reifyType(def), Syntax: e}
	}
	// Last resort: a bare name that resolves no other way reads a readable member
	// of self (a field or getter), lowering to the same self.X access the explicit
	// form does so power and self.power desugar identically. The membership test is
	// the checker's exact field∪getter rule, so a name the body leaf typed as a self
	// member lowers to a member read in every walk.
	if b.self && infer.IsReadableMember(b.reg, b.selfType, e.Name) {
		return &ir.FieldAccess{Receiver: &ir.SelfValue{}, Field: e.Name, Syntax: e}
	}
	// In a validate all clause the subject is the relation, not a row: a bare count
	// that resolves no other way reads the relation's row count. The same last-resort
	// rule as self omission, so a local/parameter/type/constant named count shadows
	// it; only count means the aggregate, and only here.
	if b.relation && e.Name == infer.RelationCountName {
		return &ir.RelationCount{Type: infer.RelationCountType(), Syntax: e}
	}
	// In a relation method's columns context the bare name reads M's column — the
	// bare-column form of a query (where(cost > 100), sum(cost)), the columns binding
	// omitted the way self is. It lowers to the same field access the lambda form's
	// c.cost does, off the synthesized columns binding, so the SQL driver reads the
	// column identically. Last resort and membership-tested, so a local, parameter,
	// constant, type, or self member of the same name (claimed above) shadows it and a
	// name that is no column stays unresolved.
	if b.columnsMaster != nil && infer.MasterHasColumn(b.reg, b.columnsMaster, e.Name) {
		return &ir.FieldAccess{Receiver: &ir.ParamRef{Name: b.columnsParam}, Field: e.Name, Syntax: e}
	}
	return nil
}

// leafMember lowers a member access in value position. A receiver naming a
// namespace import (geo.Origin) is a reference to the imported constant — the
// namespace claim runs first, exactly as the const binder orders it — then one
// naming a type: an enum member (Element.Fire) or an associated constant
// (int8.Max); a parameter shadowing the name, or any other receiver, takes the
// record-field reading instead.
func (b bodyBinder) leafMember(e *ast.MemberExpr, sub func(ast.Expr) ir.Value) ir.Value {
	if ref := b.leafNamespaceOrTypeMember(e); ref != nil {
		return ref
	}
	// A member access used as a value is a record field access.
	return &ir.FieldAccess{Receiver: sub(e.Receiver), Field: e.Member.Name, Syntax: e}
}

// leafNamespaceOrTypeMember resolves a member access whose receiver names a
// namespace import or a type, returning the imported-constant reference, enum
// member, or associated constant — or nil when the receiver is shadowed or
// names neither (the caller then reads it as a record field access).
func (b bodyBinder) leafNamespaceOrTypeMember(e *ast.MemberExpr) ir.Value {
	// A local or parameter shadowing a same-named type or namespace takes the
	// record-field reading; only a bare-name receiver can be shadowed, never a
	// namespace-qualified one (geo.Item).
	if recv, ok := e.Receiver.(*ast.Identifier); ok && b.shadows(recv.Name) {
		return nil
	}
	if b.funcs.nsConstRef != nil {
		if c := b.funcs.nsConstRef(e); c != nil {
			return &ir.Reference{Target: c, Syntax: e}
		}
	}
	// The type-member reading, through the single resolver — the same enum-member,
	// associated-constant, or projected-field value the const initializer lowers,
	// off a local (Item.id) or namespace-qualified (geo.Item.id) type, so a body
	// and a const agree on T.member. A static fn, or no match, returns nil and the
	// caller reads a record-field access. A value shadowing the namespace name —
	// a local, a parameter, or a top-level const named like the import — defers the
	// qualified form to a value receiver, the body twin of the const initializer's
	// resolve check.
	shadowed := b.valueShadows
	if v := typeMemberValue(b.reg, infer.MemberReceiverDef(b.r.Defs, b.r.Qualified, shadowed, e.Receiver), e); v != nil {
		return v
	}
	// A namespace-qualified master name used as a value (geo.Cards) is its relation,
	// the qualified twin of the bare master name leafIdentifier lowers — the value
	// the query operations are methods on. Every other qualified type name reifies
	// to a type value, so a body folds geo.Item == geo.Item exactly as a const does.
	if def := infer.QualifiedTypeDef(b.r.Qualified, shadowed, e); def != nil && def.Master != nil {
		return &ir.MasterRelation{Master: def, Syntax: e}
	}
	return qualifiedTypeValue(b.r.Qualified, shadowed, e)
}

// leafCall lowers a call in value position. A call whose callee names a type is
// a conversion T(x); one that names a top-level function — by name, or through
// a namespace import (geo.area(...)) — is a function call.
func (b bodyBinder) leafCall(e *ast.CallExpr, sub func(ast.Expr) ir.Value) ir.Value {
	switch callee := e.Callee.(type) {
	case *ast.Identifier:
		return b.leafIdentCall(e, callee, sub)
	case *ast.MemberExpr:
		return b.leafMemberCall(e, callee, sub)
	}
	return nil
}

// leafIdentCall lowers a call whose callee is a bare name: a type name is a
// conversion, a top-level function name a function call, and a method of self
// an implicit self-call (self omitted) — the form an interface's provided
// method uses to call the required fold, lowering to the same ir.Call a written
// self.fold(...) would.
func (b bodyBinder) leafIdentCall(e *ast.CallExpr, callee *ast.Identifier, sub func(ast.Expr) ir.Value) ir.Value {
	if b.shadows(callee.Name) {
		return nil
	}
	if t := b.r.ResolveName(callee.Name, b.tscope); t != ir.Invalid {
		return &ir.Conversion{Type: t, Args: convArgs(e.Arguments, sub), Syntax: e}
	}
	if cands := b.funcs.local[callee.Name]; len(cands) > 0 {
		return funcCall(pickShellOverload(cands, len(e.Arguments)), e, sub)
	}
	if b.self && b.selfHasMethod(callee.Name) {
		// A master's static fn reads self as the master's relation, so an implicit
		// self-call there (count()) lowers to the same MasterRelation receiver the
		// explicit self.count() does — the data driver anchors it whether or not self
		// was written. Every other body's self is a row, lowering to a self value.
		recv := b.selfReceiver()
		args := make([]ir.Value, len(e.Arguments))
		for i, a := range e.Arguments {
			// A bare-column argument to a relation method called on self (where(cost >
			// 100) in a scope fn) lowers as the columns form, the columns binding
			// synthesized — the implicit-self twin of the explicit Cards.where(...) form.
			if _, isLit := a.(*ast.FuncLit); !isLit {
				if v := b.ColumnsArg(recv, callee.Name, a); v != nil {
					args[i] = v
					continue
				}
			}
			args[i] = sub(a)
		}
		return &ir.Call{Receiver: recv, Method: callee.Name, Args: args, Syntax: e}
	}
	return nil
}

// columnsParamName is the synthesized binding a bare-column query argument reads its
// columns off — where(cost > 100) lowers as fn($columns) -> $columns.cost > 100. The
// name carries a sigil no source identifier can spell, so the synthesized lambda's
// binding never collides with a user parameter and the columns stay private to it.
const columnsParamName = "$columns"

// ColumnsArg lowers a relation method's non-lambda argument as a bare-column query
// argument — where(cost > 100) as fn($columns) -> $columns.cost > 100, the columns
// binding synthesized so the bare names read M's columns. It returns nil when the
// receiver is not a relation or the method takes no columns argument, so the argument
// lowers the ordinary way. The synthesized lambda is the shape the query driver
// already reads (the lambda form's twin), so the bare and bound forms lower alike. Its
// body lowers through the placeholdering return path, so a bare enum member a column
// comparison resolves (rarity == legend) becomes a placeholder the checker's write-back
// fills, exactly as the lambda form's return does.
func (b bodyBinder) ColumnsArg(receiver ir.Value, method string, arg ast.Expr) ir.Value {
	master := b.relationMaster(receiver)
	if master == nil || !infer.ColumnsContextMethods[method] {
		return nil
	}
	// Only a bare-column expression is rewritten: a value argument (a function bound to a
	// parameter, constant, or field, matching the lambda overload) is the predicate or
	// selector already and lowers the ordinary way, so it must not be wrapped in a
	// synthesized columns lambda. The classification runs with no columns master, so a
	// bare column reads as unresolved: an enclosing query's columns master (this is a
	// nested query) must not make the inner relation's column look already resolved.
	probe := b
	probe.columnsMaster = nil
	probe.relation = false
	if !probe.bareColumnArg(arg) {
		return nil
	}
	cb := b
	cb.columnsMaster = master
	cb.columnsParam = columnsParamName
	// Inside a columns argument the validate-all bare-count aggregate is out of scope (the
	// subject is a row, not the relation), so a bare count there reads the column of that
	// name rather than the row count — disabling the aggregate the way the checker does.
	cb.relation = false
	return &ir.FuncLiteral{
		Params: []string{columnsParamName},
		Body:   lower.Body([]ast.Stmt{&ast.ReturnStmt{Value: arg}}, cb),
	}
}

// bareColumnArg reports whether arg is a bare-column expression to rewrite as a columns
// lambda — a comparison, selector, or conditional built from bare column names — rather
// than a value the lambda overload takes (a function bound to a parameter/constant/field,
// a predicate returned by a call). A bare name the binder does not otherwise resolve is a
// column; an operator or selector call (cost > 100, cost.desc()) is one when its own
// receiver bottoms out in a column; a conditional (cond ? a : b) is one when a branch is;
// everything else — a resolvable name, a field access, a function call — is a value.
func (b bodyBinder) bareColumnArg(arg ast.Expr) bool {
	switch a := arg.(type) {
	case *ast.Identifier:
		return b.leafIdentifier(a) == nil
	case *ast.CallExpr:
		// An operator or selector call is a bare-column expression when its receiver
		// bottoms out in a column (cost > 100, cost.desc(), (cost > 0) && (power < 9)).
		// The column is always the operator's receiver: a column comparison yields a
		// predicate, so a captured value on the left (value && column-predicate) does not
		// type, and the argument side need not be scanned.
		member, ok := a.Callee.(*ast.MemberExpr)
		return ok && b.bareColumnArg(member.Receiver)
	case *ast.TernaryExpr:
		// The condition is a bool, never a column comparison (a column comparison is a
		// predicate, which the checker rejects as a ternary condition), so only the
		// branches can bottom out in a column.
		return b.bareColumnArg(a.Then) || b.bareColumnArg(a.Else)
	case *ast.AwaitExpr:
		return b.bareColumnArg(a.Value)
	default:
		return false
	}
}

// relationMaster returns the master a lowered relation value is over — a master used
// whole (MasterRelation), a relation-valued parameter, local, self, or function result
// (read through its resolved type), or a relation narrowed by a chain of relation-
// returning query methods (an ir.Call over one of these) — or nil when the value is not
// a relation. It is how a bare-column argument finds the columns it reads. A chain is
// followed only through methods that return a relation (where/order/limit/offset), so a
// query written after an aggregate (Cards.count().where(...)) does not resolve to the
// master a broken chain names.
func (b bodyBinder) relationMaster(v ir.Value) *ir.TypeDef {
	for {
		switch n := v.(type) {
		case *ir.MasterRelation:
			return n.Master
		case *ir.Call:
			if !infer.RelationReturningMethods[n.Method] {
				return nil
			}
			v = n.Receiver
		case *ir.ParamRef:
			return relationTypeMaster(b.reg, b.paramTypes[n.Name])
		case *ir.LocalRef:
			return relationTypeMaster(b.reg, b.locals[n.Name])
		case *ir.SelfValue:
			return relationTypeMaster(b.reg, b.selfType)
		default:
			return nil
		}
	}
}

// relationTypeMaster returns the master a relation<M> type is over — the def of its M —
// or nil when t is not a relation type. It reads the master off a relation-valued
// receiver's type, so a query on a relation parameter or local finds the columns it
// reads the way a query on a named master does.
func relationTypeMaster(reg *builtin.Registry, t ir.Type) *ir.TypeDef {
	app, ok := t.(*ir.App)
	if !ok || app.Def == nil || len(app.Args) != 1 {
		return nil
	}
	if def, ok := reg.Lookup(builtin.NameRelation); !ok || app.Def != def {
		return nil
	}
	named, ok := app.Args[0].(*ir.Named)
	if !ok || named.Def == nil || named.Def.Master == nil {
		return nil
	}
	return named.Def
}

// selfReceiver is the value an implicit self-call's receiver denotes in this body: a
// master static fn's relation (the same MasterRelation a bare master name lowers
// to), or a row self everywhere else.
func (b bodyBinder) selfReceiver() ir.Value {
	if b.relationSelf != nil {
		return &ir.MasterRelation{Master: b.relationSelf}
	}
	return &ir.SelfValue{}
}

// leafMemberCall lowers a call whose callee is a member access: a namespace
// function call (geo.area(...)), or a static fn call on a type name
// (Celsius.freezing()) — the Type.Name path, after the namespace function
// claim, with a local or parameter of that name shadowing the type.
func (b bodyBinder) leafMemberCall(e *ast.CallExpr, callee *ast.MemberExpr, sub func(ast.Expr) ir.Value) ir.Value {
	recv, ok := callee.Receiver.(*ast.Identifier)
	if !ok || b.shadows(recv.Name) {
		return nil
	}
	if b.funcs.qualified != nil {
		if cands := b.funcs.qualified(recv.Name, callee.Member.Name); len(cands) > 0 {
			return funcCall(b.funcs.shells[pickOverload(cands, len(e.Arguments))], e, sub)
		}
	}
	// A bounded type parameter wins over a same-named declared type here, the same
	// precedence the type checker applies (it resolves the receiver through the type
	// scope first), so the lowered callee matches the one that was type-checked.
	if def := b.boundStaticDef(recv.Name, callee.Member.Name); def != nil {
		return staticCall(def, callee.Member.Name, e, sub)
	}
	if def := staticFnDef(b.r.Defs, callee, b.shadows); def != nil {
		return staticCall(def, callee.Member.Name, e, sub)
	}
	return nil
}

// boundStaticDef returns the interface definition that requires a static fn named
// member of the bound of the type parameter named recv — so a call T.member() on a
// bounded parameter lowers to a static call the way Type.member() does, the static
// twin of an instance method resolved through the bound. It returns nil when recv
// is not a bounded parameter or its bound requires no such static, leaving the call
// to the type checker's value-position reading (the checker and the lowering agree).
func (b bodyBinder) boundStaticDef(recv, member string) *ir.TypeDef {
	if b.shadows(recv) {
		return nil
	}
	bound, ok := b.tscope[recv]
	if !ok || bound == nil {
		return nil
	}
	ms, _, ok := types.StaticCandidates(b.reg, &ir.TypeVar{Name: recv, Bound: bound}, member)
	if !ok || len(ms) == 0 {
		return nil
	}
	return ms[0].Owner
}

// pickShellOverload is pickOverload over function shells: the arity reads off
// the declaration syntax, since a shell's signature may not be resolved yet
// when a method body lowers.
func pickShellOverload(cands []*ir.Function, arity int) *ir.Function {
	var match *ir.Function
	n := 0
	for _, f := range cands {
		if f.Syntax != nil && len(f.Syntax.Params) == arity {
			match = f
			n++
		}
	}
	if n == 1 {
		return match
	}
	return cands[0]
}

// funcCall lowers a resolved function call: the target and its lowered
// arguments. The call expression rides along as the node's Syntax, the key the
// checker's overload selection is written back through.
func funcCall(target *ir.Function, e *ast.CallExpr, sub func(ast.Expr) ir.Value) ir.Value {
	return &ir.FuncCall{Target: target, Args: convArgs(e.Arguments, sub), Syntax: e}
}

// convArgs lowers a conversion or constructor's argument expressions to their IR
// values, preserving their order — one for the common conversion form (Level(5),
// error("msg")), two for a constructor (range(start, end)). An empty argument
// list (a recovered conversion) lowers to no arguments.
func convArgs(args []ast.Expr, sub func(ast.Expr) ir.Value) []ir.Value {
	if len(args) == 0 {
		return nil
	}
	out := make([]ir.Value, len(args))
	for i, a := range args {
		out[i] = sub(a)
	}
	return out
}

// funcBinder lowers the body of a function literal. Its own parameters lower to
// ir.ParamRef and a let it introduces to an ir.LocalRef; any other leaf is
// delegated to the enclosing binder — so a reference to an outer constant, a
// conversion, or self still lowers as it would outside the lambda. Nesting a
// literal wraps another funcBinder around this one, chaining the parameter and
// local scopes.
//
// scope is a bodyBinder that carries the lambda's own params and lets plus the
// type-resolution context inherited from the enclosing body (or, in a constant
// initializer, a context that resolves only annotations). It backs the
// statement capabilities a block body needs — LocalBinder.LetLocal, and the
// switch's EnumExpecter/EnumMemberResolver — so a let/assign/if/switch in a
// lambda body lowers exactly as it does in a method body, rather than being
// dropped. The lambda's params are recorded in scope so an inferred let that
// reads one settles at the right type and a switch on one reaches its enum.
type funcBinder struct {
	outer lower.Binder
	scope bodyBinder
}

// enterFunc builds the binder for a function literal's body from the enclosing
// binder and the literal's parameters. The lambda's scope extends the enclosing
// body's type context (funcTypeContext) with the literal's parameters on top of
// the names already captured — so a let or switch in the body settles and
// resolves against the same surface a method body does, while the lambda's own
// params (and the lets it goes on to introduce) shadow same-named outer names. A
// param with no annotation resolves to ir.Invalid: only the type query could
// pin it bidirectionally, and the value graph must not depend on it.
func enterFunc(outer lower.Binder, params []*ast.ParamDef) funcBinder {
	scope := funcTypeContext(outer)
	scope.params = copyBoolSet(scope.params, len(params))
	scope.paramTypes = copyTypeMap(scope.paramTypes, len(params))
	for _, p := range params {
		scope.params[p.Name] = true
		scope.paramTypes[p.Name] = scope.r.ResolveType(p.Type, scope.tscope)
	}
	return funcBinder{outer: outer, scope: scope}
}

// ColumnsArg forwards the bare-column query lowering to the lambda's own body scope,
// which carries the lambda's parameters — so a bare-column query inside a function
// literal body (a lambda-wrapped assert, a helper closure) lowers its columns the same
// way a method body does, rather than losing the columns context at the lambda
// boundary and leaving the column an unresolved name.
func (b funcBinder) ColumnsArg(receiver ir.Value, method string, arg ast.Expr) ir.Value {
	return b.scope.ColumnsArg(receiver, method, arg)
}

// funcTypeContext returns the type-resolution context a lambda body settles its
// lets and resolves its switch scrutinees against, drawn from the enclosing
// binder. A method or function body supplies its own resolver, registry,
// function surface, and the params, locals, and self captured at the lambda's
// definition site (so an inferred let or a switch in the lambda reads them); a
// lambda nested in another lambda reuses that lambda's scope the same way; a
// constant initializer supplies a context that resolves annotations through the
// file's universe — never the type query, so the value graph stays independent
// of typeOf.
func funcTypeContext(outer lower.Binder) bodyBinder {
	switch b := outer.(type) {
	case bodyBinder:
		return b
	case funcBinder:
		return b.scope
	case constBinder:
		return bodyBinder{
			r:     &infer.TypeResolver{Defs: b.q.universe(b.file), Qualified: qualifiedFrom(b.q, b.q.importsOf(b.file))},
			reg:   b.q.registry(),
			funcs: bodyFuncs{qualified: qualifiedFuncsFrom(b.q, b.q.importsOf(b.file))},
		}
	default:
		return bodyBinder{}
	}
}

// copyBoolSet returns a copy of m with room for extra more entries (a nil m
// yields a fresh map), so a lambda's params extend the captured set without
// mutating it.
func copyBoolSet(m map[string]bool, extra int) map[string]bool {
	out := make(map[string]bool, len(m)+extra)
	for k, v := range m {
		out[k] = v
	}
	return out
}

// copyTypeMap returns a copy of m with room for extra more entries (a nil m
// yields a fresh map).
func copyTypeMap(m map[string]ir.Type, extra int) map[string]ir.Type {
	out := make(map[string]ir.Type, len(m)+extra)
	for k, v := range m {
		out[k] = v
	}
	return out
}

func (b funcBinder) Leaf(e ast.Expr, sub func(ast.Expr) ir.Value) ir.Value {
	switch e := e.(type) {
	case *ast.Identifier:
		// A let-bound local shadows a same-named parameter, so it is read first.
		if _, ok := b.scope.locals[e.Name]; ok {
			return &ir.LocalRef{Name: e.Name, Syntax: e}
		}
		if b.scope.params[e.Name] {
			return &ir.ParamRef{Name: e.Name, Syntax: e}
		}
	case *ast.MemberExpr:
		// A member access whose receiver is this lambda's parameter or local is a
		// field access on that binding, not a type-member read on a same-named type:
		// the binding shadows the type, so the receiver lowers as a value.
		if recv, ok := e.Receiver.(*ast.Identifier); ok && b.scope.shadows(recv.Name) {
			return &ir.FieldAccess{Receiver: sub(e.Receiver), Field: e.Member.Name, Syntax: e}
		}
	case *ast.CallExpr:
		if id, ok := e.Callee.(*ast.Identifier); ok && b.scope.shadows(id.Name) {
			return nil // a call of a parameter or local: the literal's binding shadows a function
		}
	}
	return b.outer.Leaf(e, sub)
}

func (b funcBinder) EnterFunc(params []*ast.ParamDef) lower.Binder { return enterFunc(b, params) }

// LetLocal records a let-bound local on the lambda's scope and returns the
// extended binder and the binding's settled type — the same rule a method body
// uses (bodyBinder.LetLocal), so a later reference lowers to an ir.LocalRef and
// a later switch on the local reaches its enum. The extension is copied so it
// reaches only the statements after the let within this block.
func (b funcBinder) LetLocal(name string, annotation ast.TypeExpr, value ast.Expr) (lower.Binder, ir.Type) {
	next, typ := b.scope.LetLocal(name, annotation, value)
	b.scope = next.(bodyBinder)
	return b, typ
}

// ExpectedEnum reports the enum a switch scrutinee's static type names within
// the lambda body, so its bare-member arms lower to enum-member values exactly
// as a method body's switch does. A let-bound local is read first (it shadows a
// same-named parameter), then the parameter and self readings the lambda's scope
// already provides.
func (b funcBinder) ExpectedEnum(scrutinee ast.Expr) *ir.TypeDef {
	if id, ok := scrutinee.(*ast.Identifier); ok {
		if t, ok := b.scope.locals[id.Name]; ok {
			return enumDefOf(t)
		}
	}
	return b.scope.ExpectedEnum(scrutinee)
}

// AnnotationEnum resolves a let initializer's annotation enum within the lambda
// body, delegated to the lambda's scope (the resolver's universe lookup, never
// the type query) — so a let in a lambda body resolves a bare member exactly as
// one in a method body does.
func (b funcBinder) AnnotationEnum(t ast.TypeExpr) *ir.TypeDef {
	return b.scope.AnnotationEnum(t)
}

// EnumMember resolves a bare member identifier against an enum definition to
// its enum-member value — the bare-member rule a switch arm shares with a
// const initializer, delegated to the lambda's scope.
func (b funcBinder) EnumMember(def *ir.TypeDef, id *ast.Identifier) ir.Value {
	return b.scope.EnumMember(def, id)
}

// ArmType resolves a match arm's member type within the lambda body, delegated
// to the lambda's scope (the resolver's universe lookup, never the type query).
func (b funcBinder) ArmType(t ast.TypeExpr) ir.Type {
	return b.scope.ArmType(t)
}

// NarrowLocal binds a match arm's binding name at the narrowed arm type on the
// lambda's scope and returns the extended binder, so a reference to the binding
// inside the arm body lowers to an ir.LocalRef — the lambda twin of
// bodyBinder.NarrowLocal.
func (b funcBinder) NarrowLocal(name string, typ ir.Type) lower.Binder {
	b.scope = b.scope.NarrowLocal(name, typ).(bodyBinder)
	return b
}

// ForLocal binds a for loop's variable at its settled element type on the
// lambda's scope and returns the extended binder, so a reference to it inside the
// loop body lowers to an ir.LocalRef — the lambda twin of bodyBinder.ForLocal.
func (b funcBinder) ForLocal(name string, iter ast.Expr, of bool) (lower.Binder, ir.Type) {
	next, typ := b.scope.ForLocal(name, iter, of)
	b.scope = next.(bodyBinder)
	return b, typ
}
