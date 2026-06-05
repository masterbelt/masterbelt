// Package eval is the value half of masterbelt's constant analysis: it folds a
// constant expression to its value (ir.Constant). It is the evaluation mirror of
// package types/infer — where infer derives an expression's type, eval derives
// its value — over the same desugared shape: a literal, a value reference, or a
// method call, whose value comes from the receiver type's native intrinsic in
// the builtin registry.
//
// Evaluation reads name resolution and referenced values through an Env, so it
// has no dependency on the semantic query engine: the engine supplies a
// memoizing Env (which also tracks dependencies and guards cycles), but the
// rules here are a pure function of the AST and that environment.
package eval

import (
	"maps"
	"math/big"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// Env is what evaluation needs from its driver: name resolution, the value of a
// referenced declaration, and the builtin registry (for the native operator
// implementations). Keeping it an interface lets the semantic engine supply a
// memoizing implementation that tracks dependencies and breaks cycles, while
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
	// ValueOf returns a declaration's evaluated value, or nil when it cannot be
	// evaluated.
	ValueOf(decl *ast.ConstDecl) *ir.Constant
	// Registry returns the builtin registry the program evaluates against.
	Registry() *builtin.Registry
}

// Decl folds a declaration's value, or nil when it has no initializer. Overflow
// is intentionally not checked here — an integer literal is the arbitrary-
// precision int; the range check happens where the constant's concrete type is
// known.
func Decl(decl *ast.ConstDecl, env Env) *ir.Constant {
	if decl.Value == nil {
		return nil
	}
	return Expr(decl.Value, env)
}

// Expr folds an expression to its constant value, or nil when it cannot be
// evaluated. Reading references through env lets the engine track dependencies
// and reuse its cycle guard.
func Expr(e ast.Expr, env Env) *ir.Constant {
	return evalExpr(e, evalCtx{env: env})
}

// Predicate folds a refinement predicate with the self keyword bound to self.
// The semantic layer uses it to check that a constant's value satisfies its
// type's where-clause. It returns nil when the predicate cannot be folded.
func Predicate(pred ast.Expr, self *ir.Constant, env Env) *ir.Constant {
	return evalExpr(pred, evalCtx{env: env, self: self})
}

// evalCtx carries the evaluation context through the recursive fold: the
// driver's environment, the local bindings of the enclosing function literals
// or applied function (nil at the top level), the value the self keyword folds
// to (refinement predicates; nil where self has no value), and the
// function-application depth the recursion guard counts.
type evalCtx struct {
	env    Env
	locals map[string]*ir.Constant
	self   *ir.Constant
	depth  int
}

// maxApplyDepth caps function-application recursion: a recursive fold that has
// not bottomed out by this depth is treated as unevaluable (nil) — the same
// verdict an engine-level value cycle gets — instead of overflowing the stack.
const maxApplyDepth = 256

// evalExpr folds an expression, resolving an identifier first against the
// context's locals and then against the environment's declarations.
func evalExpr(e ast.Expr, ctx evalCtx) *ir.Constant {
	switch e := e.(type) {
	case *ast.IntLit:
		n, ok := new(big.Int).SetString(e.Text, 10)
		if !ok {
			return nil
		}
		return ir.IntConstant(n)
	case *ast.StringLit:
		return ir.StringConstant(e.Value)
	case *ast.BoolLit:
		return ir.BoolConstant(e.Value)
	case *ast.DatetimeLit:
		// The literal normalizes to a UTC instant here; a malformed one (the
		// lexer diagnosed it) folds to nothing.
		if ms, ok := DatetimeMillis(e.Text); ok {
			return ir.DatetimeConstant(ms)
		}
		return nil
	case *ast.DurationLit:
		// The groups total into milliseconds here; a malformed or overflowing
		// literal folds to nothing.
		if ms, ok := DurationMillis(e.Text); ok {
			return ir.DurationConstant(ms)
		}
		return nil
	case *ast.SelfExpr:
		// The bound self value, or nil outside a self-binding context (a method
		// body is not folded here yet).
		return ctx.self
	case *ast.CollectionLit:
		return collection(e, ctx)
	case *ast.RecordLit:
		return record(e, ctx)
	case *ast.FuncLit:
		// A function literal folds to a closure over the bindings in scope, so it
		// can be applied later (by list.map) or stored in a constant.
		return ir.FuncConstant(e, ctx.locals)
	case *ast.Identifier:
		if v, ok := ctx.locals[e.Name]; ok {
			return v
		}
		if target := ctx.env.Resolve(e); target != nil {
			return ctx.env.ValueOf(target)
		}
		return nil
	case *ast.MemberExpr:
		// A member access on a namespace import (geo.Origin) folds to the
		// referenced declaration's value.
		if target := ctx.env.ResolveMember(e); target != nil {
			return ctx.env.ValueOf(target)
		}
		return nil
	case *ast.CallExpr:
		member, ok := e.Callee.(*ast.MemberExpr)
		if !ok {
			// A call whose callee names a top-level function applies its body;
			// a local binding shadows a same-named function (and a call of a
			// local is not foldable here).
			if id, isIdent := e.Callee.(*ast.Identifier); isIdent {
				if _, isLocal := ctx.locals[id.Name]; !isLocal {
					if cands := ctx.env.ResolveFunc(id); len(cands) > 0 {
						return applyFunc(cands, e.Arguments, ctx)
					}
				}
			}
			return nil
		}
		recv := evalExpr(member.Receiver, ctx)
		args := make([]*ir.Constant, len(e.Arguments))
		for i, a := range e.Arguments {
			args[i] = evalExpr(a, ctx)
		}
		return call(ctx, recv, member.Member.Name, args)
	default:
		return nil
	}
}

// applyFunc folds a call of a top-level function: the arguments fold in the
// caller's context, the overload whose parameters accept their value kinds is
// selected, and its body's return folds with only the parameter bindings in
// scope (a function body sees its parameters and the other declarations
// through env, never the caller's locals). Evaluation is type-blind, so the
// selection is by value kind and conservative: when more than one candidate
// could plausibly accept the arguments — same-kind overloads like int8/int32,
// or a parameter type it cannot decide — the call simply does not fold, so a
// wrong overload's body is never applied. The depth guard turns runaway
// recursion into an unevaluated value.
func applyFunc(cands []*ast.FuncDecl, args []ast.Expr, ctx evalCtx) *ir.Constant {
	if ctx.depth >= maxApplyDepth {
		return nil
	}
	vals := make([]*ir.Constant, len(args))
	for i, a := range args {
		if vals[i] = evalExpr(a, ctx); vals[i] == nil {
			return nil
		}
	}

	var fd *ast.FuncDecl
	n := 0
	for _, cand := range cands {
		if len(cand.Params) != len(args) {
			continue
		}
		fits := true
		for i, p := range cand.Params {
			if !kindAccepts(p.Type, vals[i].Kind) {
				fits = false
				break
			}
		}
		if fits {
			fd = cand
			n++
		}
	}
	if n != 1 {
		return nil
	}

	locals := make(map[string]*ir.Constant, len(fd.Params))
	for i, p := range fd.Params {
		locals[p.Name] = vals[i]
	}
	for _, stmt := range fd.Body {
		if ret, ok := stmt.(*ast.ReturnStmt); ok {
			if ret.Value == nil {
				return nil
			}
			return evalExpr(ret.Value, evalCtx{env: ctx.env, locals: locals, depth: ctx.depth + 1})
		}
	}
	return nil
}

// kindAccepts reports whether a parameter's written type can hold a constant
// of the given kind. It decides by spelling for the prelude's primitive names
// and the structural type forms, and answers true for anything it cannot
// decide (a named alias, a union, a qualified name) — so a wrong overload is
// never ruled in, only an undecidable set kept from folding.
func kindAccepts(t ast.TypeExpr, k ir.ConstKind) bool {
	switch t := t.(type) {
	case *ast.NamedType:
		if t.Namespace != "" {
			return true // a qualified name resolves elsewhere; undecidable here
		}
		if len(t.Args) > 0 {
			if t.Name == "list" || t.Name == "map" {
				return k == ir.ConstCollection
			}
			return true
		}
		switch t.Name {
		case "int", "int8", "int16", "int32", "int64",
			"uint8", "uint16", "uint32", "uint64":
			return k == ir.ConstInt
		case "bool":
			return k == ir.ConstBool
		case "string":
			return k == ir.ConstString
		case "datetime":
			return k == ir.ConstDatetime
		case "duration":
			return k == ir.ConstDuration
		}
		return true
	case *ast.RecordType:
		return k == ir.ConstRecord
	case *ast.FuncType:
		return k == ir.ConstFunc
	default:
		return true
	}
}

// collection folds a collection literal: each entry's value (and key, for a map)
// is folded, in order. It returns nil if any element is unevaluated, so a
// collection with an unfoldable element does not fold to a partial value.
func collection(e *ast.CollectionLit, ctx evalCtx) *ir.Constant {
	entries := make([]ir.ConstEntry, 0, len(e.Entries))
	for _, entry := range e.Entries {
		var key *ir.Constant
		if entry.Key != nil {
			if key = evalExpr(entry.Key, ctx); key == nil {
				return nil
			}
		}
		val := evalExpr(entry.Value, ctx)
		if val == nil {
			return nil
		}
		entries = append(entries, ir.ConstEntry{Key: key, Value: val})
	}
	return ir.CollectionConstant(entries)
}

// record folds a record literal: each field's value is folded in source order,
// and the constant normalizes the fields to their canonical (name) order. It
// returns nil if any field is malformed or unevaluated, so a record with an
// unfoldable field does not fold to a partial value.
func record(e *ast.RecordLit, ctx evalCtx) *ir.Constant {
	fields := make([]ir.ConstField, 0, len(e.Fields))
	for _, f := range e.Fields {
		if f.Name == "" {
			return nil // recovered away; already a parse diagnostic
		}
		v := evalExpr(f.Value, ctx)
		if v == nil {
			return nil
		}
		fields = append(fields, ir.ConstField{Name: f.Name, Value: v})
	}
	return ir.RecordConstant(fields)
}

// call evaluates a method call: a collection receiver is handled here (the only
// foldable collection method is list.map), and a primitive receiver dispatches
// to its native intrinsic in the builtin registry, keyed on the receiver's value
// kind (every integer type shares one set of intrinsics, every boolean another)
// and the arguments' kinds — which is how an overloaded method (a name with
// several signatures) evaluates through the same implementation the type rules
// selected. It returns nil when an operand is unevaluated, the method has no
// value for the receiver (only reachable for a type-incorrect program), or the
// intrinsic itself has no value (a division by zero). The context threads the
// application depth through, so recursion through list.map is guarded too.
func call(ctx evalCtx, recv *ir.Constant, name string, args []*ir.Constant) *ir.Constant {
	if recv == nil {
		return nil
	}
	kinds := make([]ir.ConstKind, len(args))
	for i, a := range args {
		if a == nil {
			return nil
		}
		kinds[i] = a.Kind
	}
	if recv.Kind == ir.ConstCollection {
		return collectionMethod(ctx, recv, name, args)
	}
	var typeName string
	switch recv.Kind {
	case ir.ConstInt:
		typeName = "int"
	case ir.ConstBool:
		typeName = "bool"
	case ir.ConstString:
		typeName = "string"
	case ir.ConstDatetime:
		typeName = "datetime"
	case ir.ConstDuration:
		typeName = "duration"
	default:
		return nil
	}
	fn, ok := ctx.env.Registry().Intrinsic(typeName, name, kinds)
	if !ok {
		return nil
	}
	return fn(recv, args)
}

// collectionMethod folds a method on a list/map constant. The list collections
// are not natively backed in the registry, so their methods have no intrinsic;
// the one with a foldable value is list.map, which applies its function argument
// to each element and collects the results into a new list. Anything else (a map
// receiver, or a list method other than map) has no constant value here.
func collectionMethod(ctx evalCtx, recv *ir.Constant, name string, args []*ir.Constant) *ir.Constant {
	if name != "map" || len(args) != 1 || args[0].Kind != ir.ConstFunc {
		return nil
	}
	out := make([]ir.ConstEntry, len(recv.Coll))
	for i, entry := range recv.Coll {
		if entry.Key != nil {
			return nil // map.map (keyed entries) is not foldable
		}
		v := apply(ctx, args[0], []*ir.Constant{entry.Value})
		if v == nil {
			return nil
		}
		out[i] = ir.ConstEntry{Value: v}
	}
	return ir.CollectionConstant(out)
}

// apply folds a function-value constant against the given arguments: it binds the
// parameters to the arguments over the closure's captured environment and folds
// the body's return statement. A body with no return, a wrong argument count, an
// unfoldable return, or an application past the recursion guard yields nil.
func apply(ctx evalCtx, fn *ir.Constant, args []*ir.Constant) *ir.Constant {
	if fn.Fn == nil || len(args) != len(fn.Fn.Params) || ctx.depth >= maxApplyDepth {
		return nil
	}
	locals := make(map[string]*ir.Constant, len(fn.Captured)+len(args))
	maps.Copy(locals, fn.Captured)
	for i, p := range fn.Fn.Params {
		locals[p.Name] = args[i]
	}
	for _, stmt := range fn.Fn.Body {
		if ret, ok := stmt.(*ast.ReturnStmt); ok {
			if ret.Value == nil {
				return nil
			}
			// A function body sees its parameters and captures, never an outer
			// self: a literal has no receiver.
			return evalExpr(ret.Value, evalCtx{env: ctx.env, locals: locals, depth: ctx.depth + 1})
		}
	}
	return nil
}
