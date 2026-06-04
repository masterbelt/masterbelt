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
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/ast"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/ir"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/types"
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
	// TypeOf returns a declaration's type (ir.Invalid when undeterminable).
	TypeOf(decl *ast.ConstDecl) ir.Type
	// Registry returns the builtin registry the program types against.
	Registry() *builtin.Registry
}

// scope is the typing context an expression is walked in. It owns the registry
// (so method and collection rules can reach the builtin types) and types the
// context-specific leaf forms, recursing into sub-expressions through the same
// walk (exprType) so it sees the scope's own rules.
type scope interface {
	registry() *builtin.Registry
	// leaf types an expression form whose meaning is context-specific — a value
	// name, self, a field access, a conversion, the null literal — returning
	// ir.Invalid when the form is not meaningful in this scope.
	leaf(e ast.Expr) ir.Type
}

// Decl is the type rule for a declaration: an annotation gives a concrete type,
// otherwise the type is inferred from the initializer expression. It reads other
// declarations' types through env so a memoizing engine can track the
// dependencies. A file's own type declarations are not visible to a constant
// annotation, so the annotation resolves against the registry alone; the
// diagnostic pass resolves it again with reporting enabled.
func Decl(decl *ast.ConstDecl, env Env) ir.Type {
	if decl.Type != nil {
		r := &TypeResolver{Reg: env.Registry()}
		return r.ResolveType(decl.Type, nil)
	}
	if decl.Value == nil {
		return ir.Invalid
	}
	return Expr(decl.Value, env)
}

// Expr infers the type of a constant initializer: an integer literal is int, a
// string literal string, a boolean literal bool, a value reference inherits its
// referent's type, and a method call's type comes from the builtin method rules
// (types.MethodResult).
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
		if member, ok := e.Callee.(*ast.MemberExpr); ok {
			recv := exprType(member.Receiver, s)
			args := make([]ir.Type, len(e.Arguments))
			for i, a := range e.Arguments {
				args[i] = exprType(a, s)
			}
			return types.MethodResult(s.registry(), recv, member.Member.Name, args)
		}
		return s.leaf(e)
	default:
		return s.leaf(e)
	}
}

// funcLitType is the type of a function-literal expression: the Func type built
// from its declared parameter types and its declared — or, when the annotation
// is omitted, inferred — result type. The annotations resolve against the
// registry alone — the same universe a constant annotation resolves against —
// so a literal whose annotations name only primitives types precisely, while
// one naming a file-local type leaves that part invalid (as a const annotation
// would). An omitted result type is synthesized from the body's return values,
// typed in a funcScope over s; an omitted parameter type is ir.Invalid here
// (only a checking context can supply it).
func funcLitType(e *ast.FuncLit, s scope) ir.Type {
	r := &TypeResolver{Reg: s.registry()}
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

func (s constScope) leaf(e ast.Expr) ir.Type {
	if id, ok := e.(*ast.Identifier); ok {
		if target := s.env.Resolve(id); target != nil {
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
	Self     ir.Type
	Params   map[string]ir.Type
}

// Body infers the type of a method-body expression: self, a parameter, a
// literal, a record field access, a type conversion (T(x)), or a method call
// (the form operators desugar to). An unresolvable expression is ir.Invalid.
func Body(e ast.Expr, s BodyScope) ir.Type { return exprType(e, s) }

func (s BodyScope) registry() *builtin.Registry { return s.Reg }

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
	// Mismatch fires where got cannot be used as the expected type want.
	Mismatch func(e ast.Expr, got, want ir.Type)
	// UninferableResult fires at a function literal that neither annotates its
	// result type nor returns a value to infer it from.
	UninferableResult func(lit *ast.FuncLit)
}

func (s *Sink) invalidOp(node ast.Node, method, operands string) {
	if s != nil && s.InvalidOp != nil {
		s.InvalidOp(node, method, operands)
	}
}

func (s *Sink) mismatch(e ast.Expr, got, want ir.Type) {
	if s != nil && s.Mismatch != nil {
		s.Mismatch(e, got, want)
	}
}

func (s *Sink) uninferableResult(lit *ast.FuncLit) {
	if s != nil && s.UninferableResult != nil {
		s.UninferableResult(lit)
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
		member, ok := e.Callee.(*ast.MemberExpr)
		if !ok {
			return s.leaf(e)
		}
		recv := check(member.Receiver, s, sink)
		bad := recv == ir.Invalid
		args := make([]ir.Type, len(e.Arguments))
		for i, a := range e.Arguments {
			args[i] = check(a, s, sink)
			bad = bad || args[i] == ir.Invalid
		}
		res := types.MethodResult(s.registry(), recv, member.Member.Name, args)
		if res == ir.Invalid && !bad {
			sink.invalidOp(e, member.Member.Name, typesList(recv, args))
		}
		return res
	default:
		return s.leaf(e)
	}
}

// checkFuncLit checks a function literal: its body is walked in the literal's
// parameter scope, each return value is checked against the declared result
// type (or unified with the other returns when the annotation is omitted), and
// a literal with neither a result annotation nor a return to infer one from is
// reported as uninferable. The signature it returns is built from the same
// walk, so it agrees with funcLitType's (the silent twin over exprType)
// without typing the body a second time.
func checkFuncLit(lit *ast.FuncLit, s scope, sink *Sink) ir.Type {
	reg := s.registry()
	r := &TypeResolver{Reg: reg}
	params := make([]ir.Type, len(lit.Params))
	names := make(map[string]ir.Type, len(lit.Params))
	for i, p := range lit.Params {
		params[i] = r.ResolveType(p.Type, nil)
		names[p.Name] = params[i]
	}
	body := funcScope{outer: s, params: names}

	var want ir.Type // the declared result type, nil when omitted
	if lit.Result != nil {
		want = r.ResolveType(lit.Result, nil)
	}
	var unified ir.Type // the returns unified so far, when no annotation
	sawReturn := false
	for _, stmt := range lit.Body {
		switch stmt := stmt.(type) {
		case *ast.ReturnStmt:
			if stmt.Value == nil {
				continue
			}
			sawReturn = true
			got := check(stmt.Value, body, sink)
			switch {
			case want != nil:
				// An Invalid return is already reported (or reported
				// elsewhere); do not pile on.
				if got != ir.Invalid && want != ir.Invalid && !types.Assignable(reg, got, want) {
					sink.mismatch(stmt.Value, got, want)
				}
			case unified == nil:
				unified = got
			case unified == ir.Invalid || got == ir.Invalid:
				// An Invalid return poisons the synthesis — the same outcome
				// returnedType reaches through Unify — without a report.
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
	if lit.Result == nil && !sawReturn {
		sink.uninferableResult(lit)
	}
	result := want
	if lit.Result == nil {
		result = unified
		if result == nil {
			result = ir.Invalid
		}
	}
	return &ir.Func{Params: params, Result: result}
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
