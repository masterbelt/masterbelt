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
		return funcLitType(e, s.registry())
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
// from its declared parameter and result types. The annotations resolve against
// the registry alone — the same universe a constant annotation resolves against
// — so a literal whose annotations name only primitives types precisely, while
// one naming a file-local type leaves that part invalid (as a const annotation
// would).
func funcLitType(e *ast.FuncLit, reg *builtin.Registry) ir.Type {
	r := &TypeResolver{Reg: reg}
	params := make([]ir.Type, len(e.Params))
	for i, p := range e.Params {
		params[i] = r.ResolveType(p.Type, nil)
	}
	return &ir.Func{Params: params, Result: r.ResolveType(e.Result, nil)}
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

// Check type-checks an expression, reporting the innermost method call whose
// operand types it is not defined on. It returns the expression's type so
// recursion can propagate an existing error — an operand that is itself Invalid,
// or an undefined reference reported elsewhere — without re-reporting it. The
// report callback receives the offending call node, the method name, and the
// operand types rendered as "recv, arg, ...".
func Check(e ast.Expr, env Env, report func(node ast.Node, method, operands string)) ir.Type {
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
				Check(entry.Key, env, report)
			}
			if entry.Value != nil {
				Check(entry.Value, env, report)
			}
		}
		return collectionType(e, constScope{env})
	case *ast.FuncLit:
		// A function literal's type is its signature; its body introduces a
		// parameter scope this const-context walk does not enter, so the body's
		// own operator errors are not reported here.
		return funcLitType(e, env.Registry())
	case *ast.Identifier:
		if t := env.Resolve(e); t != nil {
			return env.TypeOf(t)
		}
		return ir.Invalid
	case *ast.CallExpr:
		member, ok := e.Callee.(*ast.MemberExpr)
		if !ok {
			return ir.Invalid
		}
		recv := Check(member.Receiver, env, report)
		bad := recv == ir.Invalid
		args := make([]ir.Type, len(e.Arguments))
		for i, a := range e.Arguments {
			args[i] = Check(a, env, report)
			bad = bad || args[i] == ir.Invalid
		}
		res := types.MethodResult(env.Registry(), recv, member.Member.Name, args)
		if res == ir.Invalid && !bad {
			report(e, member.Member.Name, typesList(recv, args))
		}
		return res
	default:
		return ir.Invalid
	}
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
