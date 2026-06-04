// Package infer is the syntax-driven half of masterbelt's type system: it
// derives the type of an expression or declaration by walking the AST, and
// checks an expression for operator-method type errors. Where package types is
// the pure algebra over a type value (no syntax), infer applies that algebra to
// the tree.
//
// One walk (exprType) types every expression. The forms shared by every context
// — int, string, and boolean literals, collection literals, and method calls —
// are typed here uniformly; the forms whose meaning depends on context — a value
// name, the receiver self, a record field access, a conversion, the null literal
// — are delegated to a scope. A constant initializer (Expr/Decl) and a method
// body (Body) are the same walk over two scopes, so the collection and method
// rules are written once.
//
// Inference reads name resolution and declaration types through an Env, so it
// has no dependency on the semantic query engine — the engine supplies a
// memoizing Env, but the rules here are a pure function of the AST and that
// environment, which is what makes them testable in isolation.
package infer

import (
	"strings"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/types"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// Env is what inference and checking need from their driver: name resolution,
// the type of a referenced declaration, the type universe (to resolve a type
// annotation), and the builtin registry (to type operator-method calls). Keeping
// this an interface lets the semantic engine supply a memoizing implementation
// (so an identifier's type is computed once and dependencies are tracked) while
// this package stays a pure set of rules.
type Env interface {
	// Resolve returns the declaration a value-position identifier refers to, or
	// nil if no declaration has that name.
	Resolve(id *ast.Identifier) *ast.ConstDecl
	// ResolveMember returns the declaration a namespace member access
	// (geo.Origin) refers to, or nil when the receiver names no namespace or
	// the member is not among the namespace's exported values.
	ResolveMember(m *ast.MemberExpr) *ast.ConstDecl
	// TypeOf returns a declaration's type (ir.Invalid when undeterminable).
	TypeOf(decl *ast.ConstDecl) ir.Type
	// Universe returns the named type definitions annotations resolve in: the
	// file's own type declarations shadowing its imported ones.
	Universe() map[string]*ir.TypeDef
	// QualifiedType resolves a namespace-qualified type name (geo.Point) to
	// the definition the namespace's target exports, or nil.
	QualifiedType(namespace, name string) *ir.TypeDef
	// Registry returns the builtin registry the program types against.
	Registry() *builtin.Registry
}

// scope is the typing context an expression is walked in. It owns the registry
// (so method and collection rules can reach the builtin types) and types the
// context-specific leaf forms, recursing into sub-expressions through the same
// walk (exprType) so it sees the scope's own rules.
type scope interface {
	registry() *builtin.Registry
	// universe is the named type definitions annotations resolve in within
	// this scope.
	universe() map[string]*ir.TypeDef
	// qualified is the namespace-qualified type lookup in effect within this
	// scope (nil when no namespaces are in scope).
	qualified() func(namespace, name string) *ir.TypeDef
	// leaf types an expression form whose meaning is context-specific — a value
	// name, self, a field access, a conversion, the null literal — returning
	// ir.Invalid when the form is not meaningful in this scope.
	leaf(e ast.Expr) ir.Type
}

// Decl is the type rule for a declaration: an annotation gives a concrete type,
// otherwise the type is inferred from the initializer expression. It reads other
// declarations' types through env so a memoizing engine can track the
// dependencies. The annotation resolves in env's universe — the file's own type
// declarations and its imports, over the registry — silently; the diagnostic
// pass resolves it again with reporting enabled.
func Decl(decl *ast.ConstDecl, env Env) ir.Type {
	if decl.Type != nil {
		r := &TypeResolver{Reg: env.Registry(), Defs: env.Universe(), Qualified: env.QualifiedType}
		return r.ResolveType(decl.Type, nil)
	}
	if decl.Value == nil {
		return ir.Invalid
	}
	return Expr(decl.Value, env)
}

// Expr infers the type of a constant initializer: an integer literal is int, a
// string literal string, a boolean literal bool, a value reference inherits its
// referent's type, and a method call's type comes from the builtin method
// rules driven bidirectionally (callType), so a function-literal argument is
// typed against the parameter it is passed to.
func Expr(e ast.Expr, env Env) ir.Type {
	return exprType(e, constScope{env})
}

// exprType is the one inference walk. The shared forms are typed here; the
// context-specific leaves go through scope.leaf.
func exprType(e ast.Expr, s scope) ir.Type {
	switch e := e.(type) {
	case *ast.IntLit:
		return &ir.Builtin{Name: "int"}
	case *ast.StringLit:
		return &ir.Builtin{Name: "string"}
	case *ast.BoolLit:
		return &ir.Builtin{Name: "bool"}
	case *ast.CollectionLit:
		return collectionType(e, s)
	case *ast.FuncLit:
		return funcLitType(e, s)
	case *ast.CallExpr:
		// A call through a member access is a method call; any other callee is a
		// context-specific form (a conversion in a method body, otherwise nothing).
		return callType(e, s, nil)
	default:
		return s.leaf(e)
	}
}

// funcLitType is the type of a function-literal expression: the Func type built
// from its declared parameter types and its declared — or, when the annotation
// is omitted, inferred — result type. The annotations resolve in the scope's
// universe — the same one a constant annotation resolves in — so file-local
// and imported type names work in a literal's signature exactly as they do on
// a declaration. An omitted result type is synthesized from the body's return
// values, typed in a funcScope over s; an omitted parameter type is ir.Invalid
// here (only a checking context can supply it).
func funcLitType(e *ast.FuncLit, s scope) ir.Type {
	r := &TypeResolver{Reg: s.registry(), Defs: s.universe(), Qualified: s.qualified()}
	params := make([]ir.Type, len(e.Params))
	names := make(map[string]ir.Type, len(e.Params))
	for i, p := range e.Params {
		params[i] = r.ResolveType(p.Type, nil)
		names[p.Name] = params[i]
	}
	result := r.ResolveType(e.Result, nil)
	if e.Result == nil {
		result = returnedType(e.Body, funcScope{outer: s, params: names})
	}
	return &ir.Func{Params: params, Result: result}
}

// returnedType synthesizes a function body's result type: the unified type of
// every returned value. A body that returns nothing — or whose returns do not
// unify — has no synthesizable result and is ir.Invalid.
func returnedType(body []ast.Stmt, s scope) ir.Type {
	var result ir.Type
	for _, stmt := range body {
		ret, ok := stmt.(*ast.ReturnStmt)
		if !ok || ret.Value == nil {
			continue
		}
		t := exprType(ret.Value, s)
		if result == nil {
			result = t
		} else {
			result = types.Unify(s.registry(), result, t)
		}
	}
	if result == nil {
		return ir.Invalid
	}
	return result
}

// funcScope types a function literal's body: the literal's own parameters
// resolve to their (declared or pushed-down) types, and every other leaf is
// delegated to the enclosing scope — mirroring how funcBinder chains the
// lowering scopes for the same body. A nested literal just wraps another
// funcScope around this one.
type funcScope struct {
	outer  scope
	params map[string]ir.Type
}

func (s funcScope) registry() *builtin.Registry { return s.outer.registry() }

func (s funcScope) universe() map[string]*ir.TypeDef { return s.outer.universe() }

func (s funcScope) qualified() func(namespace, name string) *ir.TypeDef { return s.outer.qualified() }

func (s funcScope) leaf(e ast.Expr) ir.Type {
	if id, ok := e.(*ast.Identifier); ok {
		if t, ok := s.params[id.Name]; ok {
			return t
		}
	}
	return s.outer.leaf(e)
}

// collectionType infers a collection literal's type: list<E> from the unified
// element type, or map<K, V> from the unified key and value types. An empty
// literal has no entries to infer from, so its type comes from the annotation,
// not from here — it returns ir.Invalid. A literal whose entries do not unify
// (mismatched element types) is ir.Invalid too.
func collectionType(e *ast.CollectionLit, s scope) ir.Type {
	if len(e.Entries) == 0 {
		return ir.Invalid
	}
	reg := s.registry()
	if e.IsMap() {
		def, ok := reg.Lookup("map")
		if !ok {
			return ir.Invalid
		}
		keyT, valT := ir.Type(nil), ir.Type(nil)
		for i, entry := range e.Entries {
			k, v := exprType(entry.Key, s), exprType(entry.Value, s)
			if i == 0 {
				keyT, valT = k, v
			} else {
				keyT, valT = types.Unify(reg, keyT, k), types.Unify(reg, valT, v)
			}
		}
		if keyT == ir.Invalid || valT == ir.Invalid {
			return ir.Invalid
		}
		return &ir.App{Def: def, Args: []ir.Type{keyT, valT}}
	}
	def, ok := reg.Lookup("list")
	if !ok {
		return ir.Invalid
	}
	var elemT ir.Type
	for i, entry := range e.Entries {
		t := exprType(entry.Value, s)
		if i == 0 {
			elemT = t
		} else {
			elemT = types.Unify(reg, elemT, t)
		}
	}
	if elemT == ir.Invalid {
		return ir.Invalid
	}
	return &ir.App{Def: def, Args: []ir.Type{elemT}}
}

// constScope types a constant initializer: the only context-specific form is a
// value reference, whose type is its referent's. Self, field access, a
// conversion, and the null literal are not meaningful in a constant, so they are
// ir.Invalid.
type constScope struct{ env Env }

func (s constScope) registry() *builtin.Registry { return s.env.Registry() }

func (s constScope) universe() map[string]*ir.TypeDef { return s.env.Universe() }

func (s constScope) qualified() func(namespace, name string) *ir.TypeDef { return s.env.QualifiedType }

func (s constScope) leaf(e ast.Expr) ir.Type {
	switch e := e.(type) {
	case *ast.Identifier:
		if target := s.env.Resolve(e); target != nil {
			return s.env.TypeOf(target)
		}
	case *ast.MemberExpr:
		// A member access on a namespace import (geo.Origin) inherits the
		// referenced declaration's type.
		if target := s.env.ResolveMember(e); target != nil {
			return s.env.TypeOf(target)
		}
	}
	return ir.Invalid
}

// BodyScope types a method body: the receiver type (Self), the parameter types
// (Params), and the type universe (Universe) a conversion resolves against,
// alongside the registry.
type BodyScope struct {
	Reg      *builtin.Registry
	Universe map[string]*ir.TypeDef
	// Qualified is the namespace-qualified type lookup the body's annotations
	// and conversions resolve through, or nil when no namespaces are in scope.
	Qualified func(namespace, name string) *ir.TypeDef
	Self      ir.Type
	Params    map[string]ir.Type
}

// Body infers the type of a method-body expression: self, a parameter, a
// literal, a record field access, a type conversion (T(x)), or a method call
// (the form operators desugar to). An unresolvable expression is ir.Invalid.
func Body(e ast.Expr, s BodyScope) ir.Type { return exprType(e, s) }

func (s BodyScope) registry() *builtin.Registry { return s.Reg }

func (s BodyScope) universe() map[string]*ir.TypeDef { return s.Universe }

func (s BodyScope) qualified() func(namespace, name string) *ir.TypeDef { return s.Qualified }

func (s BodyScope) leaf(e ast.Expr) ir.Type {
	switch e := e.(type) {
	case *ast.SelfExpr:
		return s.Self
	case *ast.NullLit:
		return &ir.Builtin{Name: "null"}
	case *ast.Identifier:
		if t, ok := s.Params[e.Name]; ok {
			return t
		}
		return ir.Invalid
	case *ast.MemberExpr:
		// A member access used as a value is a record field access.
		return fieldType(exprType(e.Receiver, s), e.Member.Name)
	case *ast.CallExpr:
		// A non-method call whose callee names a type is a conversion T(x).
		if id, ok := e.Callee.(*ast.Identifier); ok {
			if _, isParam := s.Params[id.Name]; !isParam {
				if t := s.lookupType(id.Name); t != ir.Invalid {
					return t
				}
			}
		}
		return ir.Invalid
	default:
		return ir.Invalid
	}
}

// lookupType resolves a type name (a conversion callee) to its type, against the
// body's universe of declared types and then the builtin registry.
func (s BodyScope) lookupType(name string) ir.Type {
	if d, ok := s.Universe[name]; ok {
		if d.Builtin {
			return &ir.Builtin{Name: name}
		}
		return &ir.Named{Def: d}
	}
	if _, ok := s.Reg.Lookup(name); ok {
		return &ir.Builtin{Name: name}
	}
	return ir.Invalid
}

// fieldType returns the type of a record's field, following named types to their
// underlying record.
func fieldType(recv ir.Type, name string) ir.Type {
	rec := recordOf(recv)
	if rec == nil {
		return ir.Invalid
	}
	for _, f := range rec.Fields {
		if f.Name == name {
			return f.Type
		}
	}
	return ir.Invalid
}

func recordOf(t ir.Type) *ir.Record {
	switch t := t.(type) {
	case *ir.Record:
		return t
	case *ir.Named:
		if t.Def != nil {
			return recordOf(t.Def.Body)
		}
	}
	return nil
}

// Sink receives the checking walk's findings. Every field is optional and a
// nil Sink checks silently, so the same walk serves pure typing and the
// diagnostic pass; the semantic layer wires each callback to its diagnostic.
type Sink struct {
	// InvalidOp fires at the innermost method call whose operand types it is
	// not defined on, with the operand types rendered as "recv, arg, ...".
	InvalidOp func(node ast.Node, method, operands string)
	// Mismatch fires where got cannot be used as the expected type want. The
	// node is the offending expression — or, for a function-literal parameter
	// whose annotation conflicts with the pushed-down type, the parameter.
	Mismatch func(node ast.Node, got, want ir.Type)
	// Checked fires for every (expression, expected-type) pair the checking
	// walk visits, so the semantic layer can hook value-range (Fits) checks
	// without this package depending on eval.
	Checked func(e ast.Expr, want ir.Type)
	// ArityMismatch fires at a function literal whose parameter count differs
	// from the expected function type's.
	ArityMismatch func(lit *ast.FuncLit, got, want int)
	// UninferableParam fires at a function-literal parameter that neither has
	// an annotation nor receives a concrete type from the checking context.
	UninferableParam func(p *ast.ParamDef)
	// UninferableResult fires at a function literal that neither annotates its
	// result type nor returns a value to infer it from.
	UninferableResult func(lit *ast.FuncLit)
	// SolvedFuncLit fires for every function literal the walk types, with its
	// settled signature — annotations, pushed-down expectations, and inferred
	// parts combined. It is informational (the editor's hover and inlay hints
	// read it), never a finding.
	SolvedFuncLit func(lit *ast.FuncLit, t *ir.Func)
}

func (s *Sink) invalidOp(node ast.Node, method, operands string) {
	if s != nil && s.InvalidOp != nil {
		s.InvalidOp(node, method, operands)
	}
}

func (s *Sink) mismatch(node ast.Node, got, want ir.Type) {
	if s != nil && s.Mismatch != nil {
		s.Mismatch(node, got, want)
	}
}

func (s *Sink) checked(e ast.Expr, want ir.Type) {
	if s != nil && s.Checked != nil {
		s.Checked(e, want)
	}
}

func (s *Sink) arityMismatch(lit *ast.FuncLit, got, want int) {
	if s != nil && s.ArityMismatch != nil {
		s.ArityMismatch(lit, got, want)
	}
}

func (s *Sink) uninferableParam(p *ast.ParamDef) {
	if s != nil && s.UninferableParam != nil {
		s.UninferableParam(p)
	}
}

func (s *Sink) uninferableResult(lit *ast.FuncLit) {
	if s != nil && s.UninferableResult != nil {
		s.UninferableResult(lit)
	}
}

func (s *Sink) solvedFuncLit(lit *ast.FuncLit, t *ir.Func) {
	if s != nil && s.SolvedFuncLit != nil {
		s.SolvedFuncLit(lit, t)
	}
}

// Check type-checks an expression, reporting through sink the innermost method
// call whose operand types it is not defined on, and — inside a function
// literal's body — each return value that does not satisfy the literal's
// declared (or, across several returns, unified) result type. It returns the
// expression's type so recursion can propagate an existing error — an operand
// that is itself Invalid, or an undefined reference reported elsewhere —
// without re-reporting it.
func Check(e ast.Expr, env Env, sink *Sink) ir.Type {
	return check(e, constScope{env}, sink)
}

// CheckAgainst checks expression e against the expected type want, pushing
// want into the forms that benefit (function and collection literals) and
// falling back to synthesis plus subsumption for everything else. It returns
// the expression's type — concrete where want filled in what the expression
// omitted. A nil sink checks silently (pure typing); diagnostic callers pass
// callbacks.
func CheckAgainst(e ast.Expr, want ir.Type, env Env, sink *Sink) ir.Type {
	return checkType(e, want, constScope{env}, map[string]ir.Type{}, sink)
}

// CheckBody is CheckAgainst over a method-body scope, so a returned function
// or collection literal receives the method's declared result type.
func CheckBody(e ast.Expr, want ir.Type, s BodyScope, sink *Sink) ir.Type {
	return checkType(e, want, s, map[string]ir.Type{}, sink)
}

// checkType is the checking walk: the bidirectional half of the type rules.
// want may contain still-unbound method type variables — a call site passes
// its bindings in subst and gains the ones the walk solves; the declaration
// paths pass a fresh map.
func checkType(e ast.Expr, want ir.Type, s scope, subst map[string]ir.Type, sink *Sink) ir.Type {
	if e == nil {
		return ir.Invalid
	}
	if want == ir.Invalid {
		// No usable expectation (an annotation that failed to resolve is
		// reported at its own node): synthesize, keeping the body diagnostics.
		return check(e, s, sink)
	}
	want = types.Substitute(want, subst) // pin what the context already solved
	sink.checked(e, want)
	switch e := e.(type) {
	case *ast.FuncLit:
		fw, ok := want.(*ir.Func)
		if !ok {
			got := check(e, s, sink)
			sink.mismatch(e, got, want)
			return got
		}
		return checkFuncLitAgainst(e, fw, s, subst, sink)
	case *ast.CollectionLit:
		return checkCollectionAgainst(e, want, s, subst, sink)
	default:
		// Synthesis plus subsumption: any other form's type is its own; it
		// must merely satisfy want (binding any type variable want still has).
		got := check(e, s, sink)
		if got != ir.Invalid && !types.Match(s.registry(), want, got, subst) {
			sink.mismatch(e, got, want)
		}
		return got
	}
}

// checkCollectionAgainst checks a collection literal against an expected list
// or map type: the shape must agree, and every entry is checked against the
// element type — which is how an annotation reaches into the literal, and how
// an empty literal gets a type at all (it is exactly its expectation).
func checkCollectionAgainst(lit *ast.CollectionLit, want ir.Type, s scope, subst map[string]ir.Type, sink *Sink) ir.Type {
	app, ok := collectionApp(want)
	if !ok || (len(lit.Entries) > 0 && lit.IsMap() != (len(app.Args) == 2)) {
		// Not a collection expectation, or a map literal under a list type
		// (or vice versa): report with the synthesized type, as the const
		// annotation check always has.
		got := check(lit, s, sink)
		sink.mismatch(lit, got, want)
		return got
	}
	switch len(app.Args) {
	case 1:
		for _, entry := range lit.Entries {
			checkType(entry.Value, app.Args[0], s, subst, sink)
		}
	case 2:
		for _, entry := range lit.Entries {
			if entry.Key != nil {
				checkType(entry.Key, app.Args[0], s, subst, sink)
			}
			checkType(entry.Value, app.Args[1], s, subst, sink)
		}
	}
	return want
}

// collectionApp returns t as a list or map application, or false if t is not a
// builtin collection type.
func collectionApp(t ir.Type) (*ir.App, bool) {
	app, ok := t.(*ir.App)
	if !ok || app.Def == nil {
		return nil, false
	}
	if app.Def.Name == "list" || app.Def.Name == "map" {
		return app, true
	}
	return nil, false
}

// checkFuncLitAgainst checks a function literal against an expected function
// type — the heart of the bidirectional rules. The expectation supplies what
// the literal omits: an unannotated parameter takes the expected parameter
// type, and an unannotated result is either checked against the expected
// result or, when that still contains an unbound method type variable (map's
// R), synthesized from the body and matched to bind the variable in subst.
// A written annotation wins for the body scope but must agree with the
// expectation (default-int adaption only).
func checkFuncLitAgainst(lit *ast.FuncLit, want *ir.Func, s scope, subst map[string]ir.Type, sink *Sink) ir.Type {
	reg := s.registry()
	if len(lit.Params) != len(want.Params) {
		sink.arityMismatch(lit, len(lit.Params), len(want.Params))
		return ir.Invalid
	}
	r := &TypeResolver{Reg: reg, Defs: s.universe(), Qualified: s.qualified()}
	params := make([]ir.Type, len(lit.Params))
	names := make(map[string]ir.Type, len(lit.Params))
	for i, p := range lit.Params {
		wp := types.Substitute(want.Params[i], subst)
		switch {
		case p.Type != nil:
			ap := r.ResolveType(p.Type, nil)
			if conflicts(reg, ap, wp, subst) {
				sink.mismatch(p, ap, wp)
			}
			params[i] = ap
		case !hasTypeVar(wp):
			params[i] = wp
		default:
			// The context has not pinned the variable this parameter needs
			// (and pass-2 ordering means it never will).
			sink.uninferableParam(p)
			params[i] = ir.Invalid
		}
		names[p.Name] = params[i]
	}
	body := funcScope{outer: s, params: names}

	wr := types.Substitute(want.Result, subst)
	var result ir.Type
	switch {
	case lit.Result != nil:
		// The annotation wins for the returns, after agreeing with the
		// expectation the same way a parameter annotation must.
		result = r.ResolveType(lit.Result, nil)
		if conflicts(reg, result, wr, subst) {
			sink.mismatch(lit.Result, result, wr)
		}
		checkReturns(lit, result, body, subst, sink)
	case !hasTypeVar(wr):
		// The expectation determines the result; every return is checked
		// against it (no return at all still leaves the signature complete).
		result = wr
		checkReturns(lit, wr, body, subst, sink)
	default:
		// The expected result still has an unbound method type variable (the
		// R of map): synthesize the body's result and bind the variable.
		unified, sawReturn := synthesizeReturns(lit, body, sink)
		switch {
		case !sawReturn:
			sink.uninferableResult(lit)
			result = ir.Invalid
		case unified == ir.Invalid:
			result = ir.Invalid
		case types.Match(reg, wr, unified, subst):
			result = types.Substitute(wr, subst)
		default:
			sink.mismatch(lit, unified, wr)
			result = ir.Invalid
		}
	}
	t := &ir.Func{Params: params, Result: result}
	sink.solvedFuncLit(lit, t)
	return t
}

// conflicts reports whether a written annotation disagrees with the expected
// type at the same position. A concrete expectation must unify with the
// annotation (default-int adaption only); one still carrying a method type
// variable is instead bound by the annotation through Match — which is how a
// fully annotated literal solves the R of list<T>.map — and conflicts only if
// the variable was already bound incompatibly. An unresolved annotation was
// reported at its own node.
func conflicts(reg *builtin.Registry, annotation, want ir.Type, subst map[string]ir.Type) bool {
	if annotation == ir.Invalid {
		return false
	}
	if hasTypeVar(want) {
		return !types.Match(reg, want, annotation, subst)
	}
	return types.Unify(reg, annotation, want) == ir.Invalid
}

// checkReturns walks a literal's body with a known result type: every return
// value is checked against it (pushing it into nested literals), and bare
// expression statements are checked for their own errors.
func checkReturns(lit *ast.FuncLit, want ir.Type, body funcScope, subst map[string]ir.Type, sink *Sink) {
	for _, stmt := range lit.Body {
		switch stmt := stmt.(type) {
		case *ast.ReturnStmt:
			if stmt.Value != nil {
				checkType(stmt.Value, want, body, subst, sink)
			}
		case *ast.ExprStmt:
			check(stmt.X, body, sink)
		}
	}
}

// synthesizeReturns walks a literal's body in synthesis mode, unifying the
// returned types the way checkFuncLit does: the unified type (Invalid when a
// return is Invalid or the returns conflict, reported at the later return)
// and whether any return carried a value.
func synthesizeReturns(lit *ast.FuncLit, body funcScope, sink *Sink) (unified ir.Type, sawReturn bool) {
	reg := body.registry()
	for _, stmt := range lit.Body {
		switch stmt := stmt.(type) {
		case *ast.ReturnStmt:
			if stmt.Value == nil {
				continue
			}
			sawReturn = true
			got := check(stmt.Value, body, sink)
			switch {
			case unified == nil:
				unified = got
			case unified == ir.Invalid || got == ir.Invalid:
				unified = ir.Invalid
			default:
				if u := types.Unify(reg, unified, got); u == ir.Invalid {
					sink.mismatch(stmt.Value, got, unified)
					unified = ir.Invalid
				} else {
					unified = u
				}
			}
		case *ast.ExprStmt:
			check(stmt.X, body, sink)
		}
	}
	return unified, sawReturn
}

// hasTypeVar reports whether t still contains a type variable — i.e. the
// checking context has not pinned every generic part to a concrete type.
func hasTypeVar(t ir.Type) bool {
	switch t := t.(type) {
	case *ir.TypeVar:
		return true
	case *ir.App:
		for _, a := range t.Args {
			if hasTypeVar(a) {
				return true
			}
		}
	case *ir.Func:
		for _, p := range t.Params {
			if hasTypeVar(p) {
				return true
			}
		}
		return hasTypeVar(t.Result)
	case *ir.Union:
		for _, m := range t.Members {
			if hasTypeVar(m) {
				return true
			}
		}
	case *ir.Record:
		for _, f := range t.Fields {
			if hasTypeVar(f.Type) {
				return true
			}
		}
	}
	return false
}

// check is the checking walk behind Check, parameterized over the scope so a
// function literal's body is checked in its parameter scope.
func check(e ast.Expr, s scope, sink *Sink) ir.Type {
	switch e := e.(type) {
	case *ast.IntLit:
		return &ir.Builtin{Name: "int"}
	case *ast.StringLit:
		return &ir.Builtin{Name: "string"}
	case *ast.BoolLit:
		return &ir.Builtin{Name: "bool"}
	case *ast.CollectionLit:
		// Surface any operator error inside an entry; the element-type and range
		// checks against the (possibly annotated) element type are the caller's.
		for _, entry := range e.Entries {
			if entry.Key != nil {
				check(entry.Key, s, sink)
			}
			if entry.Value != nil {
				check(entry.Value, s, sink)
			}
		}
		return collectionType(e, s)
	case *ast.FuncLit:
		return checkFuncLit(e, s, sink)
	case *ast.CallExpr:
		return callType(e, s, sink)
	default:
		return s.leaf(e)
	}
}

// callType is the type rule for a method call, bidirectionally: the receiver
// and the non-literal arguments are synthesized first (left to right), solving
// the method's type variables they pin down; the function-literal arguments
// are then checked against their parameter patterns, so the call's expectation
// reaches into each literal — and the literal bodies solve what remains (the R
// of list<T>.map). A nil sink types silently; with one, the innermost call
// whose operands do not fit is reported, except when the failure is inside a
// literal — the checking walk reported its precise cause already, and the
// Invalid propagation suppresses the pile-on (the bad flag).
func callType(e *ast.CallExpr, s scope, sink *Sink) ir.Type {
	member, ok := e.Callee.(*ast.MemberExpr)
	if !ok {
		return s.leaf(e)
	}
	reg := s.registry()
	recv := check(member.Receiver, s, sink)
	bad := recv == ir.Invalid
	args := make([]ir.Type, len(e.Arguments))

	m, subst, found := types.BindReceiver(reg, recv, member.Member.Name)
	if !found || len(e.Arguments) != len(m.Params) {
		// No such method, or the wrong argument count: synthesize the
		// arguments for their own diagnostics, then report the call.
		for i, a := range e.Arguments {
			args[i] = check(a, s, sink)
			bad = bad || args[i] == ir.Invalid
		}
		if !bad {
			sink.invalidOp(e, member.Member.Name, typesList(recv, args))
		}
		return ir.Invalid
	}

	// Pass 1 — the non-literal arguments, left to right. Self-typed operands
	// unify with the receiver (the default int adapts); pattern-typed ones
	// match, binding the method's type variables. Going literal-last maximizes
	// what pass 2 can push into each literal.
	operand := recv // the unified type of the receiver and the self-typed args
	fail := false   // the operands do not fit (and no precise report fired)
	for i, a := range e.Arguments {
		if _, isLit := a.(*ast.FuncLit); isLit {
			continue
		}
		args[i] = check(a, s, sink)
		if args[i] == ir.Invalid {
			bad = true
			continue
		}
		pt := types.Substitute(m.Params[i].Type, subst)
		if _, isSelf := pt.(*ir.SelfType); isSelf {
			if operand = types.Unify(reg, operand, args[i]); operand == ir.Invalid {
				fail = true
			}
		} else if !types.Match(reg, pt, args[i], subst) {
			fail = true
		}
	}

	// Pass 2 — the function literals, each checked against its parameter
	// pattern. A finding inside the literal (a mismatch, an uninferable part)
	// fails the call without the generic report; so does an Invalid left in
	// the literal's type by a cause reported elsewhere.
	for i, a := range e.Arguments {
		lit, isLit := a.(*ast.FuncLit)
		if !isLit {
			continue
		}
		pt := types.Substitute(m.Params[i].Type, subst)
		litFailed := false
		args[i] = checkType(lit, pt, s, subst, observe(sink, &litFailed))
		if litFailed || ir.HasInvalid(args[i]) {
			bad = true
		}
	}

	if fail || bad {
		if fail && !bad {
			sink.invalidOp(e, member.Member.Name, typesList(recv, args))
		}
		return ir.Invalid
	}
	if _, isSelf := m.Result.(*ir.SelfType); isSelf {
		return operand
	}
	result := types.Substitute(m.Result, subst)
	if hasTypeVar(result) {
		// A variable no argument could solve survived to the result.
		sink.invalidOp(e, member.Member.Name, typesList(recv, args))
		return ir.Invalid
	}
	return result
}

// observe wraps sink so the caller learns whether the wrapped walk reported a
// finding (Checked is a stream, not a finding). The wrapper stays valid for a
// nil sink, which is what lets the silent walk share the call rule.
func observe(sink *Sink, fired *bool) *Sink {
	return &Sink{
		InvalidOp: func(node ast.Node, method, operands string) {
			*fired = true
			sink.invalidOp(node, method, operands)
		},
		Mismatch: func(node ast.Node, got, want ir.Type) {
			*fired = true
			sink.mismatch(node, got, want)
		},
		Checked: func(e ast.Expr, want ir.Type) {
			sink.checked(e, want)
		},
		ArityMismatch: func(lit *ast.FuncLit, got, want int) {
			*fired = true
			sink.arityMismatch(lit, got, want)
		},
		UninferableParam: func(p *ast.ParamDef) {
			*fired = true
			sink.uninferableParam(p)
		},
		UninferableResult: func(lit *ast.FuncLit) {
			*fired = true
			sink.uninferableResult(lit)
		},
		SolvedFuncLit: func(lit *ast.FuncLit, t *ir.Func) {
			sink.solvedFuncLit(lit, t)
		},
	}
}

// checkFuncLit checks a function literal with no expected type: its body is
// walked in the literal's parameter scope, each return value is checked
// against the declared result type (or unified with the other returns when
// the annotation is omitted), and a literal with neither a result annotation
// nor a return to infer one from is reported as uninferable. The signature it
// returns is built from the same walk, so it agrees with funcLitType's (the
// silent twin over exprType) without typing the body a second time.
func checkFuncLit(lit *ast.FuncLit, s scope, sink *Sink) ir.Type {
	r := &TypeResolver{Reg: s.registry(), Defs: s.universe(), Qualified: s.qualified()}
	params := make([]ir.Type, len(lit.Params))
	names := make(map[string]ir.Type, len(lit.Params))
	for i, p := range lit.Params {
		if p.Type == nil {
			// With no expected type there is no context to infer an
			// unannotated parameter from.
			sink.uninferableParam(p)
		}
		params[i] = r.ResolveType(p.Type, nil)
		names[p.Name] = params[i]
	}
	body := funcScope{outer: s, params: names}

	var result ir.Type
	if lit.Result != nil {
		result = r.ResolveType(lit.Result, nil)
		checkReturns(lit, result, body, map[string]ir.Type{}, sink)
	} else {
		unified, sawReturn := synthesizeReturns(lit, body, sink)
		if !sawReturn {
			sink.uninferableResult(lit)
		}
		result = unified
		if result == nil {
			result = ir.Invalid
		}
	}
	t := &ir.Func{Params: params, Result: result}
	sink.solvedFuncLit(lit, t)
	return t
}

// typesList renders the receiver and argument types as "recv, arg, ..." for the
// invalid-operation diagnostic.
func typesList(recv ir.Type, args []ir.Type) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, recv.String())
	for _, a := range args {
		parts = append(parts, a.String())
	}
	return strings.Join(parts, ", ")
}
