// Package infer is the syntax-driven half of masterbelt's type system: it
// derives the type of an expression or declaration by walking the AST, and
// checks an expression for operator-method type errors. Where package types is
// the pure algebra over a type value (no syntax), infer applies that algebra to
// the tree.
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
	// ResolveType resolves a type annotation (a full type expression, e.g.
	// list<int>) to its type, or ir.Invalid when it does not resolve.
	ResolveType(t ast.TypeExpr) ir.Type
	// Registry returns the builtin registry the program types against.
	Registry() *builtin.Registry
}

// Decl is the type rule for a declaration: an annotation gives a concrete type,
// otherwise the type is inferred from the initializer expression. It reads other
// declarations' types through env so a memoizing engine can track the
// dependencies.
func Decl(decl *ast.ConstDecl, env Env) ir.Type {
	if decl.Type != nil {
		return env.ResolveType(decl.Type)
	}
	if decl.Value == nil {
		return ir.Invalid
	}
	return Expr(decl.Value, env)
}

// Expr infers the type of an expression: an integer literal is int and a
// boolean literal is bool, a value reference inherits its referent's type, and a
// method call's type comes from the builtin method rules (types.MethodResult).
func Expr(e ast.Expr, env Env) ir.Type {
	switch e := e.(type) {
	case *ast.IntLit:
		return &ir.Builtin{Name: "int"}
	case *ast.StringLit:
		return &ir.Builtin{Name: "string"}
	case *ast.BoolLit:
		return &ir.Builtin{Name: "bool"}
	case *ast.CollectionLit:
		return collectionType(e, env)
	case *ast.Identifier:
		if target := env.Resolve(e); target != nil {
			return env.TypeOf(target)
		}
		return ir.Invalid
	case *ast.CallExpr:
		member, ok := e.Callee.(*ast.MemberExpr)
		if !ok {
			return ir.Invalid
		}
		recv := Expr(member.Receiver, env)
		args := make([]ir.Type, len(e.Arguments))
		for i, a := range e.Arguments {
			args[i] = Expr(a, env)
		}
		return types.MethodResult(env.Registry(), recv, member.Member.Name, args)
	default:
		return ir.Invalid
	}
}

// collectionType infers a collection literal's type: list<E> from the unified
// element type, or map<K, V> from the unified key and value types. An empty
// literal has no entries to infer from, so its type comes from the annotation,
// not from here — it returns ir.Invalid. A literal whose entries do not unify
// (mismatched element types) is ir.Invalid too.
func collectionType(e *ast.CollectionLit, env Env) ir.Type {
	if len(e.Entries) == 0 {
		return ir.Invalid
	}
	reg := env.Registry()
	if e.IsMap() {
		def, ok := reg.Lookup("map")
		if !ok {
			return ir.Invalid
		}
		keyT, valT := ir.Type(nil), ir.Type(nil)
		for i, entry := range e.Entries {
			k, v := Expr(entry.Key, env), Expr(entry.Value, env)
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
		t := Expr(entry.Value, env)
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
		return collectionType(e, env)
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
