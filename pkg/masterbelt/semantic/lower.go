package semantic

import (
	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/lower"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/types"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/types/infer"
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
}

func (b constBinder) Leaf(e ast.Expr, sub func(ast.Expr) ir.Value) ir.Value {
	switch e := e.(type) {
	case *ast.NullLit:
		// The null literal is a value like any other (const Absent:
		// optional<nint> = null), so the graph carries it; whether null fits
		// the declared type is the checker's question.
		return &ir.NullValue{Syntax: e}
	case *ast.Identifier:
		if target := b.q.resolve(b.file, e); target != nil {
			return &ir.Reference{Target: b.irOf[target], Syntax: e}
		}
		// A bare member resolves through the expected enum (const Top: Rarity =
		// Legend). The expectation is the const's own annotation, so it only
		// reaches a bare name that is the whole initializer.
		if idx := enumIndex(b.expected, e.Name); idx >= 0 {
			return &ir.EnumMemberValue{Def: b.expected, Index: idx, Syntax: e}
		}
	case *ast.MemberExpr:
		if target := b.q.resolveMember(b.file, e); target != nil {
			return &ir.Reference{Target: b.irOf[target], Syntax: e}
		}
		// A member access whose receiver names an enum type (Rarity.Common).
		if def, idx := enumMemberAccess(b.q.universe(b.file), e); idx >= 0 {
			return &ir.EnumMemberValue{Def: def, Index: idx, Syntax: e}
		}
		// A member access whose receiver names a type and whose member names one
		// of its associated constants (int8.Max, Level.Max).
		if def, idx := assocConstAccess(b.q.universe(b.file), e); idx >= 0 {
			return &ir.AssocConstValue{Def: def, Index: idx, Syntax: e}
		}
		// Otherwise the receiver is a value: a field access on a record-typed
		// constant (Hero.lv), reading the field — the same value form a method body
		// lowers, so a const initializer reading a record field has a value graph.
		return &ir.FieldAccess{Receiver: sub(e.Receiver), Field: e.Member.Name, Syntax: e}
	case *ast.CallExpr:
		switch callee := e.Callee.(type) {
		case *ast.Identifier:
			// A call whose callee names a type is a conversion T(x) — the type
			// wins over a same-named function, exactly as in a body.
			if def, ok := b.q.universe(b.file)[callee.Name]; ok {
				t := ir.Type(&ir.Named{Def: def})
				if def.Builtin {
					t = &ir.Builtin{Name: def.Name}
				}
				return &ir.Conversion{Type: t, Args: convArgs(e.Arguments, sub), Syntax: e}
			}
			if cands := b.q.resolveFunc(b.file, callee); len(cands) > 0 {
				return funcCall(b.fnOf[pickOverload(cands, len(e.Arguments))], e, sub)
			}
		case *ast.MemberExpr:
			if cands := b.q.resolveFuncMember(b.file, callee); len(cands) > 0 {
				return funcCall(b.fnOf[pickOverload(cands, len(e.Arguments))], e, sub)
			}
			// A call whose callee is a member access on a type name is a static fn
			// call (Celsius.freezing()) — the Type.Name path, after the namespace
			// function claim. A constant initializer has no locals/params, so no name
			// shadows the type.
			if def := staticFnDef(b.q.universe(b.file), callee, nil); def != nil {
				return staticCall(def, callee.Member.Name, e, sub)
			}
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

// enumMemberAccess resolves a member access whose receiver names an enum type
// (Rarity.Common): the enum definition and the member's index, or (nil, -1)
// when the receiver is not an enum or the member is unknown.
func enumMemberAccess(universe map[string]*ir.TypeDef, m *ast.MemberExpr) (*ir.TypeDef, int) {
	recv, ok := m.Receiver.(*ast.Identifier)
	if !ok {
		return nil, -1
	}
	def, ok := universe[recv.Name]
	if !ok || def.Enum == nil {
		return nil, -1
	}
	return def, enumIndex(def, m.Member.Name)
}

// assocConstAccess resolves a member access whose receiver names a type and
// whose member names one of its associated constants (int8.Max, Level.Max): the
// owning definition and the constant's index, or (nil, -1) when the receiver
// names no type or has no such constant.
func assocConstAccess(universe map[string]*ir.TypeDef, m *ast.MemberExpr) (*ir.TypeDef, int) {
	recv, ok := m.Receiver.(*ast.Identifier)
	if !ok {
		return nil, -1
	}
	def, ok := universe[recv.Name]
	if !ok {
		return nil, -1
	}
	return def, assocConstIndex(def, m.Member.Name)
}

// assocConstIndex returns the index of the named associated constant of a type
// definition, or -1 when it has no such constant.
func assocConstIndex(def *ir.TypeDef, name string) int {
	if def == nil {
		return -1
	}
	for i, c := range def.Consts {
		if c.Name == name {
			return i
		}
	}
	return -1
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
	def, ok := universe[recv.Name]
	if !ok || !hasStaticFn(def, callee.Member.Name) {
		return nil
	}
	return def
}

// hasStaticFn reports whether a type definition declares a static fn of the given
// name. A static fn is not derived from the underlying type (it is scoped to the
// declaring type, like an associated constant), so only the definition's own
// methods are consulted.
func hasStaticFn(def *ir.TypeDef, name string) bool {
	if def == nil {
		return false
	}
	for _, m := range def.Methods {
		if m.Kind == ir.MethodStatic && m.Name == name {
			return true
		}
	}
	return false
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
	// locals maps each let-bound block-local in scope to its settled type. A
	// reference to one lowers to an ir.LocalRef (shadowing a same-named parameter
	// or type), and its type is read here when inferring a later let's value. It
	// grows as a block's lets are lowered (LetLocal) and is the body counterpart
	// of paramTypes for mutable locals.
	locals map[string]ir.Type
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
	}
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

func (b bodyBinder) Leaf(e ast.Expr, sub func(ast.Expr) ir.Value) ir.Value {
	switch e := e.(type) {
	case *ast.SelfExpr:
		if !b.self {
			return nil // a function body has no receiver
		}
		return &ir.SelfValue{Syntax: e}
	case *ast.NullLit:
		return &ir.NullValue{Syntax: e}
	case *ast.Identifier:
		// A let-bound local shadows a same-named parameter or type, so it is
		// resolved first.
		if _, ok := b.locals[e.Name]; ok {
			return &ir.LocalRef{Name: e.Name, Syntax: e}
		}
		if b.params[e.Name] {
			return &ir.ParamRef{Name: e.Name, Syntax: e}
		}
		return nil
	case *ast.MemberExpr:
		// A member access whose receiver names a type — an enum member
		// (Element.Fire) or an associated constant (int8.Max) — is that type's
		// value; a parameter shadowing the type name takes the record-field
		// reading instead.
		if recv, ok := e.Receiver.(*ast.Identifier); ok && !b.shadows(recv.Name) {
			if def := b.r.Defs[recv.Name]; def != nil {
				if def.Enum != nil {
					if idx := enumIndex(def, e.Member.Name); idx >= 0 {
						return &ir.EnumMemberValue{Def: def, Index: idx, Syntax: e}
					}
				}
				if idx := assocConstIndex(def, e.Member.Name); idx >= 0 {
					return &ir.AssocConstValue{Def: def, Index: idx, Syntax: e}
				}
			}
		}
		// A member access used as a value is a record field access.
		return &ir.FieldAccess{Receiver: sub(e.Receiver), Field: e.Member.Name, Syntax: e}
	case *ast.CallExpr:
		// A call whose callee names a type is a conversion T(x); one that
		// names a top-level function — by name, or through a namespace import
		// (geo.area(...)) — is a function call.
		switch callee := e.Callee.(type) {
		case *ast.Identifier:
			if b.shadows(callee.Name) {
				return nil
			}
			if t := b.r.ResolveName(callee.Name, b.tscope); t != ir.Invalid {
				return &ir.Conversion{Type: t, Args: convArgs(e.Arguments, sub), Syntax: e}
			}
			if cands := b.funcs.local[callee.Name]; len(cands) > 0 {
				return funcCall(pickShellOverload(cands, len(e.Arguments)), e, sub)
			}
			// A bare call inside a method body whose name is a method of self is
			// an implicit self-call (self omitted) — the form an interface's
			// provided method uses to call the required fold. It lowers to the
			// same ir.Call a written self.fold(...) would.
			if b.self && b.selfHasMethod(callee.Name) {
				args := make([]ir.Value, len(e.Arguments))
				for i, a := range e.Arguments {
					args[i] = sub(a)
				}
				return &ir.Call{Receiver: &ir.SelfValue{}, Method: callee.Name, Args: args, Syntax: e}
			}
		case *ast.MemberExpr:
			recv, ok := callee.Receiver.(*ast.Identifier)
			if !ok || b.shadows(recv.Name) {
				return nil
			}
			if b.funcs.qualified != nil {
				if cands := b.funcs.qualified(recv.Name, callee.Member.Name); len(cands) > 0 {
					return funcCall(b.funcs.shells[pickOverload(cands, len(e.Arguments))], e, sub)
				}
			}
			// A call whose callee is a member access on a type name is a static fn
			// call (Celsius.freezing()) — the Type.Name path, after the namespace
			// function claim, with a local or parameter of that name shadowing the
			// type (checked above through shadows).
			if def := staticFnDef(b.r.Defs, callee, b.shadows); def != nil {
				return staticCall(def, callee.Member.Name, e, sub)
			}
		}
		return nil
	default:
		return nil
	}
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
