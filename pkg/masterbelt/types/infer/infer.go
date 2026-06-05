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
	// ResolveFunc returns the overload set a call's callee name refers to —
	// every same-name function declaration, in source order — or nil if no
	// function has that name.
	ResolveFunc(id *ast.Identifier) []*ast.FuncDecl
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
	// fn resolves a call's callee name to the overload set of the top-level
	// function it names, or nil when nothing of that name is callable here —
	// a parameter shadows a same-named function, and in a method body a type
	// name (a conversion) wins over one.
	fn(id *ast.Identifier) []*ast.FuncDecl
}

// Decl is the type rule for a declaration: an annotation gives a concrete type,
// otherwise the type is inferred from the initializer expression. It reads other
// declarations' types through env so a memoizing engine can track the
// dependencies. The annotation resolves in env's universe — the file's own type
// declarations and its imports, over the registry — silently; the diagnostic
// pass resolves it again with reporting enabled.
func Decl(decl *ast.ConstDecl, env Env) ir.Type {
	if decl.Type != nil {
		r := &TypeResolver{Defs: env.Universe(), Qualified: env.QualifiedType}
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
	case *ast.DatetimeLit:
		return &ir.Builtin{Name: "datetime"}
	case *ast.DurationLit:
		return &ir.Builtin{Name: "duration"}
	case *ast.CollectionLit:
		return collectionType(e, s)
	case *ast.RecordLit:
		return recordLitType(e, s)
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

// recordLitType is the silent type of a record literal: the named record type
// of the typed form (Point{...}). The inferred form ({...}) has no type of its
// own — only a checking context can supply one — and a name that is unknown or
// not a record carries none either, so all three are ir.Invalid here; the
// checking twin (checkRecordLit) reports each cause.
func recordLitType(e *ast.RecordLit, s scope) ir.Type {
	if e.TypeName == "" {
		return ir.Invalid
	}
	typ, rec := namedRecord(e.TypeName, s)
	if rec == nil {
		return ir.Invalid
	}
	return typ
}

// namedRecord resolves a record literal's type name in the scope's universe:
// the type it names (nil when the name is unknown) and its underlying record
// (nil when the type is not a record).
func namedRecord(name string, s scope) (ir.Type, *ir.Record) {
	def, ok := s.universe()[name]
	if !ok {
		return nil, nil
	}
	var t ir.Type
	if def.Builtin {
		t = &ir.Builtin{Name: def.Name}
	} else {
		t = &ir.Named{Def: def}
	}
	return t, recordOf(t)
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
	r := &TypeResolver{Defs: s.universe(), Qualified: s.qualified()}
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

func (s funcScope) fn(id *ast.Identifier) []*ast.FuncDecl {
	if _, ok := s.params[id.Name]; ok {
		return nil // a parameter shadows a same-named function
	}
	return s.outer.fn(id)
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

func (s constScope) fn(id *ast.Identifier) []*ast.FuncDecl { return s.env.ResolveFunc(id) }

// BodyScope types a method or function body: the receiver type (Self —
// ir.Invalid in a function, which has none), the parameter types (Params),
// the type universe (Universe) a conversion resolves against, and the
// top-level functions callable from the body (Funcs), alongside the registry.
type BodyScope struct {
	Reg      *builtin.Registry
	Universe map[string]*ir.TypeDef
	// Qualified is the namespace-qualified type lookup the body's annotations
	// and conversions resolve through, or nil when no namespaces are in scope.
	Qualified func(namespace, name string) *ir.TypeDef
	Self      ir.Type
	Params    map[string]ir.Type
	// Funcs is the file's top-level functions by name — each name carrying
	// its overload set in source order — or nil when none are in scope (a
	// refinement predicate).
	Funcs map[string][]*ast.FuncDecl
}

// Body infers the type of a method-body expression: self, a parameter, a
// literal, a record field access, a type conversion (T(x)), or a method call
// (the form operators desugar to). An unresolvable expression is ir.Invalid.
func Body(e ast.Expr, s BodyScope) ir.Type { return exprType(e, s) }

func (s BodyScope) registry() *builtin.Registry { return s.Reg }

func (s BodyScope) universe() map[string]*ir.TypeDef { return s.Universe }

func (s BodyScope) qualified() func(namespace, name string) *ir.TypeDef { return s.Qualified }

func (s BodyScope) fn(id *ast.Identifier) []*ast.FuncDecl {
	if _, isParam := s.Params[id.Name]; isParam {
		return nil // a parameter shadows a same-named function
	}
	if _, isType := s.Universe[id.Name]; isType {
		return nil // a type name is a conversion, which wins in a body
	}
	return s.Funcs[id.Name]
}

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

// lookupType resolves a type name (a conversion callee) to its type against
// the body's universe — which carries the prelude beneath the declared and
// imported types, so there is no second source to consult.
func (s BodyScope) lookupType(name string) ir.Type {
	if d, ok := s.Universe[name]; ok {
		if d.Builtin {
			return &ir.Builtin{Name: name}
		}
		return &ir.Named{Def: d}
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
	// NoMatchingOverload fires at a call whose receiver declares the method
	// under several signatures, none of which the operand types fit. (A
	// single-signature method that does not fit stays InvalidOp.)
	NoMatchingOverload func(node ast.Node, method, operands string)
	// AmbiguousOverload fires at a call whose operand types fit two or more
	// signatures of the method — resolved by annotating an operand, never by
	// an implicit priority.
	AmbiguousOverload func(node ast.Node, method, operands string)
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
	// CallArityMismatch fires at a call of a top-level function (with a
	// single signature) with the wrong number of arguments.
	CallArityMismatch func(call *ast.CallExpr, name string, got, want int)
	// NoMatchingFuncOverload fires at a call of an overloaded function none
	// of whose signatures the argument types fit.
	NoMatchingFuncOverload func(call *ast.CallExpr, name, types string)
	// AmbiguousFuncOverload fires at a call whose argument types fit two or
	// more signatures of the function — resolved by annotating an argument,
	// never by an implicit priority.
	AmbiguousFuncOverload func(call *ast.CallExpr, name, types string)
	// UninferableParam fires at a function-literal parameter that neither has
	// an annotation nor receives a concrete type from the checking context.
	UninferableParam func(p *ast.ParamDef)
	// UninferableResult fires at a function literal that neither annotates its
	// result type nor returns a value to infer it from.
	UninferableResult func(lit *ast.FuncLit)
	// MissingField fires at a record literal that leaves a field of its record
	// type typ uninitialized.
	MissingField func(lit *ast.RecordLit, field string, typ ir.Type)
	// UnknownField fires at a field initializer whose name the record type typ
	// does not declare.
	UnknownField func(field *ast.FieldInit, name string, typ ir.Type)
	// UninferableRecord fires at an inferred-form record literal ({...}) that
	// no checking context supplies a type to.
	UninferableRecord func(lit *ast.RecordLit)
	// UnknownRecordType fires at a typed record literal whose type name
	// resolves to no definition.
	UnknownRecordType func(lit *ast.RecordLit, name string)
	// NotARecord fires at a typed record literal whose type name resolves to a
	// type that is not a record.
	NotARecord func(lit *ast.RecordLit, typ ir.Type)
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

func (s *Sink) noMatchingOverload(node ast.Node, method, operands string) {
	if s != nil && s.NoMatchingOverload != nil {
		s.NoMatchingOverload(node, method, operands)
	}
}

func (s *Sink) ambiguousOverload(node ast.Node, method, operands string) {
	if s != nil && s.AmbiguousOverload != nil {
		s.AmbiguousOverload(node, method, operands)
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

func (s *Sink) arityMismatchLit(lit *ast.FuncLit, got, want int) {
	if s != nil && s.ArityMismatch != nil {
		s.ArityMismatch(lit, got, want)
	}
}

func (s *Sink) arityMismatch(call *ast.CallExpr, name string, got, want int) {
	if s != nil && s.CallArityMismatch != nil {
		s.CallArityMismatch(call, name, got, want)
	}
}

func (s *Sink) noMatchingFuncOverload(call *ast.CallExpr, name, types string) {
	if s != nil && s.NoMatchingFuncOverload != nil {
		s.NoMatchingFuncOverload(call, name, types)
	}
}

func (s *Sink) ambiguousFuncOverload(call *ast.CallExpr, name, types string) {
	if s != nil && s.AmbiguousFuncOverload != nil {
		s.AmbiguousFuncOverload(call, name, types)
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

func (s *Sink) missingField(lit *ast.RecordLit, field string, typ ir.Type) {
	if s != nil && s.MissingField != nil {
		s.MissingField(lit, field, typ)
	}
}

func (s *Sink) unknownField(field *ast.FieldInit, name string, typ ir.Type) {
	if s != nil && s.UnknownField != nil {
		s.UnknownField(field, name, typ)
	}
}

func (s *Sink) uninferableRecord(lit *ast.RecordLit) {
	if s != nil && s.UninferableRecord != nil {
		s.UninferableRecord(lit)
	}
}

func (s *Sink) unknownRecordType(lit *ast.RecordLit, name string) {
	if s != nil && s.UnknownRecordType != nil {
		s.UnknownRecordType(lit, name)
	}
}

func (s *Sink) notARecord(lit *ast.RecordLit, typ ir.Type) {
	if s != nil && s.NotARecord != nil {
		s.NotARecord(lit, typ)
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

// CheckPredicate is Check over a body scope: it synthesizes the expression's
// type with self bound to s.Self, reporting operator errors through sink. A
// refinement predicate types this way — whether the result must be a bool is
// the caller's rule, reported as its own diagnostic rather than a mismatch.
func CheckPredicate(e ast.Expr, s BodyScope, sink *Sink) ir.Type {
	return check(e, s, sink)
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
	case *ast.RecordLit:
		return checkRecordAgainst(e, want, s, subst, sink)
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

// checkRecordAgainst checks a record literal against an expected type. The
// inferred form takes the expectation outright: want must be a record (or a
// named record), whose declared fields reach into the field values — which is
// how an empty {} gets a type at all, exactly as an empty collection does. The
// typed form carries its own type: its fields check against its own record,
// and the type must then satisfy want like any synthesized form.
func checkRecordAgainst(e *ast.RecordLit, want ir.Type, s scope, subst map[string]ir.Type, sink *Sink) ir.Type {
	if e.TypeName != "" {
		got := checkRecordLit(e, s, sink)
		if got != ir.Invalid && !types.Match(s.registry(), want, got, subst) {
			sink.mismatch(e, got, want)
		}
		return got
	}
	rec := recordOf(want)
	if rec == nil {
		// Not a record expectation: the field values are checked bare for
		// their own errors, and the literal reports with its structural shape.
		checkRecordFieldsBare(e, s, sink)
		sink.mismatch(e, structuralRecord(e, s), want)
		return ir.Invalid
	}
	checkRecordFields(e, rec, want, s, subst, sink)
	return want
}

// checkRecordLit checks a record literal with no expected type. The typed form
// carries its own type — the named record — and its fields are checked against
// that record's field types. The inferred form has nothing to check against:
// it is reported as uninferable, after its field values are checked bare for
// their own errors.
func checkRecordLit(e *ast.RecordLit, s scope, sink *Sink) ir.Type {
	if e.TypeName == "" {
		checkRecordFieldsBare(e, s, sink)
		sink.uninferableRecord(e)
		return ir.Invalid
	}
	typ, rec := namedRecord(e.TypeName, s)
	if typ == nil {
		checkRecordFieldsBare(e, s, sink)
		sink.unknownRecordType(e, e.TypeName)
		return ir.Invalid
	}
	if rec == nil {
		checkRecordFieldsBare(e, s, sink)
		sink.notARecord(e, typ)
		return ir.Invalid
	}
	checkRecordFields(e, rec, typ, s, map[string]ir.Type{}, sink)
	return typ
}

// checkRecordFields checks a record literal's fields against the record rec
// (displayed as typ in diagnostics): every initializer must name a declared
// field — its value checked against the field's type, so the expectation
// reaches into nested literals — every declared field must be initialized
// (missing_field), and an undeclared one is rejected (unknown_field).
func checkRecordFields(lit *ast.RecordLit, rec *ir.Record, typ ir.Type, s scope, subst map[string]ir.Type, sink *Sink) {
	declared := make(map[string]ir.Type, len(rec.Fields))
	for _, f := range rec.Fields {
		declared[f.Name] = f.Type
	}
	seen := make(map[string]bool, len(lit.Fields))
	for _, f := range lit.Fields {
		if f.Name == "" {
			continue // recovered away; already a parse diagnostic
		}
		ft, ok := declared[f.Name]
		if !ok {
			sink.unknownField(f, f.Name, typ)
			if f.Value != nil {
				check(f.Value, s, sink) // its own errors still surface
			}
			continue
		}
		seen[f.Name] = true
		if f.Value != nil {
			checkType(f.Value, ft, s, subst, sink)
		}
	}
	for _, f := range rec.Fields {
		if !seen[f.Name] {
			sink.missingField(lit, f.Name, typ)
		}
	}
}

// checkRecordFieldsBare checks each field value for its own errors when there
// is no field type to check against — the literal's own problem (an unknown
// type, no expectation) is reported by the caller.
func checkRecordFieldsBare(e *ast.RecordLit, s scope, sink *Sink) {
	for _, f := range e.Fields {
		if f.Value != nil {
			check(f.Value, s, sink)
		}
	}
}

// structuralRecord renders an inferred record literal's shape for a mismatch
// message: the structural record of its field value types.
func structuralRecord(e *ast.RecordLit, s scope) ir.Type {
	fields := make([]ir.Field, 0, len(e.Fields))
	for _, f := range e.Fields {
		if f.Name == "" {
			continue
		}
		t := ir.Invalid
		if f.Value != nil {
			t = exprType(f.Value, s)
		}
		fields = append(fields, ir.Field{Name: f.Name, Type: t})
	}
	return &ir.Record{Fields: fields}
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
		sink.arityMismatchLit(lit, len(lit.Params), len(want.Params))
		return ir.Invalid
	}
	r := &TypeResolver{Defs: s.universe(), Qualified: s.qualified()}
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
	case *ast.DatetimeLit:
		return &ir.Builtin{Name: "datetime"}
	case *ast.DurationLit:
		return &ir.Builtin{Name: "duration"}
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
	case *ast.RecordLit:
		return checkRecordLit(e, s, sink)
	case *ast.FuncLit:
		return checkFuncLit(e, s, sink)
	case *ast.CallExpr:
		return callType(e, s, sink)
	default:
		return s.leaf(e)
	}
}

// callType is the type rule for a method call, bidirectionally: the receiver
// and the non-literal arguments are synthesized first (left to right), the
// overload the argument types fit is selected (types.SelectOverload — solving
// the type variables the synthesized arguments pin down), and the
// function-literal arguments are then checked against the selected
// signature's parameter patterns, so the call's expectation reaches into each
// literal — and the literal bodies solve what remains (the R of list<T>.map).
// A nil sink types silently; with one, the innermost call whose operands fit
// no signature is reported — InvalidOp for a single-signature method,
// NoMatchingOverload/AmbiguousOverload across an overload set — except when
// the failure is inside a literal: the checking walk reported its precise
// cause already, and the Invalid propagation suppresses the pile-on (the bad
// flag).
func callType(e *ast.CallExpr, s scope, sink *Sink) ir.Type {
	member, ok := e.Callee.(*ast.MemberExpr)
	if !ok {
		// A call whose callee names a top-level function is a function call;
		// any other callee is a context-specific form (a conversion in a
		// method body, otherwise nothing).
		if id, isIdent := e.Callee.(*ast.Identifier); isIdent {
			if cands := s.fn(id); len(cands) > 0 {
				return funcCallType(e, id.Name, cands, s, sink)
			}
		}
		return s.leaf(e)
	}
	reg := s.registry()
	recv := check(member.Receiver, s, sink)
	bad := recv == ir.Invalid
	args := make([]ir.Type, len(e.Arguments))

	candidates, _, found := types.Candidates(reg, recv, member.Member.Name)
	if !found {
		// No such method: synthesize the arguments for their own diagnostics,
		// then report the call.
		for i, a := range e.Arguments {
			args[i] = check(a, s, sink)
			bad = bad || args[i] == ir.Invalid
		}
		if !bad {
			sink.invalidOp(e, member.Member.Name, typesList(recv, args))
		}
		return ir.Invalid
	}

	// Pass 1 — synthesize the non-literal arguments, left to right. The
	// function literals stay nil — they fit any parameter during selection —
	// so the overload settles before any literal is checked, and pass 2 can
	// push the winner's parameter patterns into each one. An Invalid argument
	// (its cause reported at its own node) also selects as fits-anything, so
	// the suppression style survives overloading.
	known := make([]ir.Type, len(e.Arguments))
	for i, a := range e.Arguments {
		if _, isLit := a.(*ast.FuncLit); isLit {
			continue
		}
		args[i] = check(a, s, sink)
		if args[i] == ir.Invalid {
			bad = true
			continue
		}
		known[i] = args[i]
	}

	matches, _ := types.SelectOverload(reg, recv, member.Member.Name, known)
	if len(matches) != 1 {
		// No fitting signature, or several: check the literals bare for their
		// own diagnostics, then report the call — unless an operand already
		// carried its own report.
		for i, a := range e.Arguments {
			if lit, isLit := a.(*ast.FuncLit); isLit {
				args[i] = check(lit, s, sink)
			}
		}
		if !bad {
			switch {
			case len(matches) > 1:
				sink.ambiguousOverload(e, member.Member.Name, typesList(recv, args))
			case len(candidates) > 1:
				sink.noMatchingOverload(e, member.Member.Name, typesList(recv, args))
			default:
				sink.invalidOp(e, member.Member.Name, typesList(recv, args))
			}
		}
		return ir.Invalid
	}
	m, subst, operand := matches[0].Method, matches[0].Subst, matches[0].Operand

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

	if bad {
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

// funcSig is one resolved candidate of a function call: its declaration and
// its parameter/result types.
type funcSig struct {
	fd     *ast.FuncDecl
	params []ir.Type
	result ir.Type
}

// funcCallType is the type rule for a call of a top-level function. A single
// signature checks each argument against the parameter's annotated type — so
// the expectation reaches into literal arguments, exactly as a method call's
// parameter patterns do — and reports a wrong argument count as
// arity_mismatch. An overloaded name selects the one signature the argument
// types fit, mirroring the method rules: the non-deferred arguments are
// synthesized first, the overload settles, and the deferred arguments (a
// function literal, an inferred record literal — the forms whose meaning needs
// an expectation) are then checked against the winner's parameter types. The
// signatures resolve in the scope's universe, the same one the declaration's
// own reporting pass resolves them in; an unresolved annotation was reported
// there, so an Invalid parameter or result type stays silent here.
func funcCallType(e *ast.CallExpr, name string, cands []*ast.FuncDecl, s scope, sink *Sink) ir.Type {
	r := &TypeResolver{Defs: s.universe(), Qualified: s.qualified()}

	// Resolve every candidate's signature, dropping a later one that repeats
	// an earlier signature — the declaration pass reports the duplicate, and
	// dropping it here keeps the first one callable instead of permanently
	// ambiguous (mirroring how a duplicate method overload is dropped).
	sigs := make([]funcSig, 0, len(cands))
	seen := make(map[string]bool, len(cands))
	for _, fd := range cands {
		params := make([]ir.Type, len(fd.Params))
		key := ""
		for i, p := range fd.Params {
			params[i] = r.ResolveType(p.Type, nil)
			key += typeKey(params[i]) + ","
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		sigs = append(sigs, funcSig{fd: fd, params: params, result: r.ResolveType(fd.Result, nil)})
	}

	if len(sigs) == 1 {
		sg := sigs[0]
		if len(e.Arguments) != len(sg.params) {
			// The arguments still check bare for their own diagnostics.
			for _, a := range e.Arguments {
				check(a, s, sink)
			}
			sink.arityMismatch(e, name, len(e.Arguments), len(sg.params))
			return ir.Invalid
		}
		subst := map[string]ir.Type{}
		for i, a := range e.Arguments {
			checkType(a, sg.params[i], s, subst, sink)
		}
		return sg.result
	}

	// Pass 1 — synthesize the non-deferred arguments, left to right. The
	// deferred forms stay nil — they fit any parameter during selection — so
	// the overload settles before any of them is checked. An Invalid argument
	// (its cause reported at its own node) also selects as fits-anything.
	args := make([]ir.Type, len(e.Arguments))
	known := make([]ir.Type, len(e.Arguments))
	bad := false
	for i, a := range e.Arguments {
		if deferredArg(a) {
			continue
		}
		args[i] = check(a, s, sink)
		if args[i] == ir.Invalid {
			bad = true
			continue
		}
		known[i] = args[i]
	}

	var matches []funcSig
	for _, sg := range sigs {
		if len(sg.params) != len(e.Arguments) {
			continue
		}
		fits := true
		for i, kt := range known {
			if kt == nil {
				continue
			}
			if !types.Match(s.registry(), sg.params[i], kt, map[string]ir.Type{}) {
				fits = false
				break
			}
		}
		if fits {
			matches = append(matches, sg)
		}
	}

	if len(matches) != 1 {
		// No fitting signature, or several: check the deferred arguments bare
		// for their own diagnostics, then report the call — unless an operand
		// already carried its own report.
		for i, a := range e.Arguments {
			if deferredArg(a) {
				args[i] = check(a, s, sink)
			}
		}
		if !bad {
			if len(matches) > 1 {
				sink.ambiguousFuncOverload(e, name, argTypesList(args))
			} else {
				sink.noMatchingFuncOverload(e, name, argTypesList(args))
			}
		}
		return ir.Invalid
	}

	// Pass 2 — the deferred arguments, each checked against the winner's
	// parameter type. A finding inside one fails the call without a generic
	// report, exactly as a method call's literal arguments do.
	win := matches[0]
	subst := map[string]ir.Type{}
	for i, a := range e.Arguments {
		if !deferredArg(a) {
			continue
		}
		argFailed := false
		args[i] = checkType(a, win.params[i], s, subst, observe(sink, &argFailed))
		if argFailed || ir.HasInvalid(args[i]) {
			bad = true
		}
	}
	if bad {
		return ir.Invalid
	}
	return win.result
}

// deferredArg reports whether an argument's typing needs the parameter's
// expectation — a function literal, or an inferred-form record literal — so
// overload selection must not synthesize it.
func deferredArg(a ast.Expr) bool {
	switch a := a.(type) {
	case *ast.FuncLit:
		return true
	case *ast.RecordLit:
		return a.TypeName == ""
	default:
		return false
	}
}

// typeKey renders a parameter type for the duplicate-signature key; nil-safe
// for a recovered annotation.
func typeKey(t ir.Type) string {
	if t == nil {
		return "<nil>"
	}
	return t.String()
}

// argTypesList renders the argument types as "a, b" for the overload
// diagnostics.
func argTypesList(args []ir.Type) string {
	parts := make([]string, len(args))
	for i, a := range args {
		parts[i] = typeKey(a)
	}
	return strings.Join(parts, ", ")
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
		NoMatchingOverload: func(node ast.Node, method, operands string) {
			*fired = true
			sink.noMatchingOverload(node, method, operands)
		},
		AmbiguousOverload: func(node ast.Node, method, operands string) {
			*fired = true
			sink.ambiguousOverload(node, method, operands)
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
			sink.arityMismatchLit(lit, got, want)
		},
		CallArityMismatch: func(call *ast.CallExpr, name string, got, want int) {
			*fired = true
			sink.arityMismatch(call, name, got, want)
		},
		NoMatchingFuncOverload: func(call *ast.CallExpr, name, types string) {
			*fired = true
			sink.noMatchingFuncOverload(call, name, types)
		},
		AmbiguousFuncOverload: func(call *ast.CallExpr, name, types string) {
			*fired = true
			sink.ambiguousFuncOverload(call, name, types)
		},
		UninferableParam: func(p *ast.ParamDef) {
			*fired = true
			sink.uninferableParam(p)
		},
		UninferableResult: func(lit *ast.FuncLit) {
			*fired = true
			sink.uninferableResult(lit)
		},
		MissingField: func(lit *ast.RecordLit, field string, typ ir.Type) {
			*fired = true
			sink.missingField(lit, field, typ)
		},
		UnknownField: func(field *ast.FieldInit, name string, typ ir.Type) {
			*fired = true
			sink.unknownField(field, name, typ)
		},
		UninferableRecord: func(lit *ast.RecordLit) {
			*fired = true
			sink.uninferableRecord(lit)
		},
		UnknownRecordType: func(lit *ast.RecordLit, name string) {
			*fired = true
			sink.unknownRecordType(lit, name)
		},
		NotARecord: func(lit *ast.RecordLit, typ ir.Type) {
			*fired = true
			sink.notARecord(lit, typ)
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
	r := &TypeResolver{Defs: s.universe(), Qualified: s.qualified()}
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
