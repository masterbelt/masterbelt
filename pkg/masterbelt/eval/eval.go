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
	"strings"

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
	// ResolveFuncMember returns the overload set a namespace function call
	// (geo.area(...)) refers to: the namespace target's exported functions of
	// that name, or nil.
	ResolveFuncMember(m *ast.MemberExpr) []*ast.FuncDecl
	// ValueOf returns a declaration's evaluated value, or nil when it cannot be
	// evaluated.
	ValueOf(decl *ast.ConstDecl) *ir.Constant
	// LookupType resolves a type name in the scope's annotation universe —
	// the file's own and imported type declarations over the prelude — or nil
	// when the name names no type. A conversion (T(x)) folds through it.
	LookupType(name string) *ir.TypeDef
	// Registry returns the builtin registry the program evaluates against.
	Registry() *builtin.Registry
}

// Decl folds a declaration's value, or nil when it has no initializer. Overflow
// is intentionally not checked here — an integer literal is the arbitrary-
// precision int; the range check happens where the constant's concrete type is
// known.
func Decl(decl *ast.ConstDecl, env Env) *ir.Constant {
	return DeclExpecting(decl, nil, env)
}

// DeclExpecting folds a declaration's value with an expected enum in scope, so
// a bare member (const Top: Rarity = Legend) resolves through it. expected is
// the enum definition the annotation named, or nil when there is none; it is
// pre-resolved by the caller (the value query must not call the type query, so
// the caller resolves the annotation's type expression directly). A nil value
// yields nil.
func DeclExpecting(decl *ast.ConstDecl, expected *ir.TypeDef, env Env) *ir.Constant {
	if decl.Value == nil {
		return nil
	}
	return evalExpr(decl.Value, evalCtx{env: env, expected: expected})
}

// Expr folds an expression to its constant value, or nil when it cannot be
// evaluated. Reading references through env lets the engine track dependencies
// and reuse its cycle guard.
func Expr(e ast.Expr, env Env) *ir.Constant {
	return evalExpr(e, evalCtx{env: env})
}

// ExprExpecting folds an expression with an expected enum in scope, so a bare
// member resolves through it. want is the expected type; when it is an enum's
// named type the bare-member rule applies, otherwise it is ignored. It is how
// an enum member's initializer (typed against the enum's own base) and a const
// initializer pass their expectation to the folder.
func ExprExpecting(e ast.Expr, want ir.Type, env Env) *ir.Constant {
	return evalExpr(e, evalCtx{env: env, expected: expectedEnum(want)})
}

// expectedEnum returns the enum definition a type names, or nil when it is not
// an enum's named type.
func expectedEnum(want ir.Type) *ir.TypeDef {
	if n, ok := want.(*ir.Named); ok && n.Def != nil && n.Def.Enum != nil {
		return n.Def
	}
	return nil
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
	// expected is the enum a bare member resolves through (the const's
	// annotation), or nil. It reaches only the immediate expression — it does
	// not propagate into nested sub-expressions, which set their own context.
	expected *ir.TypeDef
}

// maxApplyDepth caps function-application recursion: a recursive fold that has
// not bottomed out by this depth is treated as unevaluable (nil) — the same
// verdict an engine-level value cycle gets — instead of overflowing the stack.
const maxApplyDepth = 256

// enumMember returns the value of the named member of def (an enum), or nil
// when def is not an enum or has no such member.
func enumMember(def *ir.TypeDef, name string) *ir.Constant {
	if def == nil || def.Enum == nil {
		return nil
	}
	for i, m := range def.Enum.Members {
		if m.Name == name {
			return ir.EnumConstant(def, i)
		}
	}
	return nil
}

// assocConst returns the folded value of a type's named associated constant
// (int8.Max, Level.Max), or nil when the type has no such constant. The value
// was settled at type resolution and stored on the definition, so reading it
// here keeps eval a pure function of the resolved IR.
func assocConst(def *ir.TypeDef, name string) *ir.Constant {
	if def == nil {
		return nil
	}
	for _, c := range def.Consts {
		if c.Name == name {
			return c.Value
		}
	}
	return nil
}

// evalExpr folds an expression, resolving an identifier first against the
// context's locals and then against the environment's declarations. The
// expected-enum context reaches only the immediate expression — every recursive
// descent drops it, since a bare member is meaningful only as a const's whole
// value, not nested inside a larger expression.
func evalExpr(e ast.Expr, ctx evalCtx) *ir.Constant {
	// The expectation is consumed at this level; sub-expressions evaluate in
	// their own (expectation-free) context.
	sub := ctx
	sub.expected = nil
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
		return collection(e, sub)
	case *ast.RecordLit:
		return record(e, sub)
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
		// A bare member resolves through the expected enum (const Top: Rarity =
		// Legend): a name that is a member of the annotation's enum folds to that
		// member's value. A name not in scope and not a member is unevaluable.
		if v := enumMember(ctx.expected, e.Name); v != nil {
			return v
		}
		return nil
	case *ast.MemberExpr:
		// A member access on a namespace import (geo.Origin) folds to the
		// referenced declaration's value.
		if target := ctx.env.ResolveMember(e); target != nil {
			return ctx.env.ValueOf(target)
		}
		// A member access whose receiver names a type folds to a type member's
		// value — an enum member (Rarity.Common) or an associated constant
		// (int8.Max, Level.Max). A local binding shadows the type name.
		if recv, ok := e.Receiver.(*ast.Identifier); ok {
			if _, isLocal := ctx.locals[recv.Name]; !isLocal {
				if def := ctx.env.LookupType(recv.Name); def != nil {
					if def.Enum != nil {
						if v := enumMember(def, e.Member.Name); v != nil {
							return v
						}
					}
					if v := assocConst(def, e.Member.Name); v != nil {
						return v
					}
				}
			}
		}
		return nil
	case *ast.CallExpr:
		member, ok := e.Callee.(*ast.MemberExpr)
		if !ok {
			// A call whose callee names a type is a conversion (the type wins
			// over a same-named function, as in the type rules); one that names
			// a top-level function applies its body. A local binding shadows
			// both (and a call of a local is not foldable here).
			if id, isIdent := e.Callee.(*ast.Identifier); isIdent {
				if _, isLocal := ctx.locals[id.Name]; !isLocal {
					if def := ctx.env.LookupType(id.Name); def != nil {
						return convert(def, e.Arguments, sub)
					}
					if cands := ctx.env.ResolveFunc(id); len(cands) > 0 {
						return applyFunc(cands, e.Arguments, sub)
					}
				}
			}
			return nil
		}
		// A member-access callee whose receiver names a namespace applies the
		// imported function; a local binding shadows the namespace.
		if recv, isIdent := member.Receiver.(*ast.Identifier); isIdent {
			if _, isLocal := ctx.locals[recv.Name]; !isLocal {
				if cands := ctx.env.ResolveFuncMember(member); len(cands) > 0 {
					return applyFunc(cands, e.Arguments, sub)
				}
			}
		}
		recv := evalExpr(member.Receiver, sub)
		args := make([]*ir.Constant, len(e.Arguments))
		for i, a := range e.Arguments {
			args[i] = evalExpr(a, sub)
		}
		return call(sub, recv, member.Member.Name, args)
	default:
		return nil
	}
}

// convert folds a conversion T(x). The one conversion with a constant value
// today is error("msg") — the error type's constructor — which folds to an
// error constant carrying the message; any other conversion has no constant
// value here.
func convert(def *ir.TypeDef, args []ast.Expr, ctx evalCtx) *ir.Constant {
	if !def.Builtin {
		return nil // a user type shadowing a native name has no native conversion
	}
	n, ok := ctx.env.Registry().Native(def.Name)
	if !ok || !n.Err || len(args) != 1 {
		return nil
	}
	v := evalExpr(args[0], ctx)
	if v == nil || v.Kind != ir.ConstString {
		return nil
	}
	return ir.ErrorConstant(v.Str)
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
	if fd.Extern || len(fd.Effects) > 0 {
		// Only a pure function folds. An effectful one compiles to runtime
		// code for a target — the pure-context check upstream keeps it out of
		// every compile-time position, and this guard keeps eval pure even if
		// one slips through.
		return nil
	}

	locals := make(map[string]*ir.Constant, len(fd.Params))
	for i, p := range fd.Params {
		locals[p.Name] = vals[i]
	}
	return evalBody(fd.Body, evalCtx{env: ctx.env, locals: locals, depth: ctx.depth + 1})
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
		case "error":
			return k == ir.ConstError
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
	if recv.Kind == ir.ConstEnum {
		return enumComparison(recv, name, args)
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
	case ir.ConstError:
		typeName = "error"
	default:
		return nil
	}
	fn, ok := ctx.env.Registry().Intrinsic(typeName, name, kinds)
	if !ok {
		return nil
	}
	return fn(recv, args)
}

// enumComparison folds one of an enum value's six comparison methods. Equality
// (eql/neq) compares member identity — the enum definition and the member index
// — which the no-duplicate-value rule makes equivalent to comparing the base
// values. Ordering (lt/lteq/gt/gteq) compares the base values: an integer base
// numerically, a string base lexicographically. The argument must be the same
// enum (the type checker has already required it); a mismatch or an unevaluable
// base value folds to nothing.
func enumComparison(recv *ir.Constant, name string, args []*ir.Constant) *ir.Constant {
	if len(args) != 1 {
		return nil
	}
	other := args[0]
	if other.Kind != ir.ConstEnum || other.EnumDef != recv.EnumDef {
		return nil
	}
	switch name {
	case "eql":
		return ir.BoolConstant(recv.EnumIndex == other.EnumIndex)
	case "neq":
		return ir.BoolConstant(recv.EnumIndex != other.EnumIndex)
	}
	// The ordering comparisons read the members' base values.
	cmp, ok := compareEnumValues(recv.EnumValue(), other.EnumValue())
	if !ok {
		return nil
	}
	switch name {
	case "lt":
		return ir.BoolConstant(cmp < 0)
	case "lteq":
		return ir.BoolConstant(cmp <= 0)
	case "gt":
		return ir.BoolConstant(cmp > 0)
	case "gteq":
		return ir.BoolConstant(cmp >= 0)
	}
	return nil
}

// compareEnumValues compares two enum members' base values, returning the sign
// of the comparison and whether it could be made: integers compare numerically,
// strings lexicographically. Two values of differing or unsupported kinds (or a
// nil value) cannot be compared.
func compareEnumValues(a, b *ir.Constant) (int, bool) {
	if a == nil || b == nil || a.Kind != b.Kind {
		return 0, false
	}
	switch a.Kind {
	case ir.ConstInt:
		return a.Int.Cmp(b.Int), true
	case ir.ConstString:
		return strings.Compare(a.Str, b.Str), true
	default:
		return 0, false
	}
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
	// A function body sees its parameters and captures, never an outer self: a
	// literal has no receiver.
	return evalBody(fn.Fn.Body, evalCtx{env: ctx.env, locals: locals, depth: ctx.depth + 1})
}

// evalBody runs a statement body to its returned value, or nil when no path
// reaches a return. It executes a switch by folding the scrutinee and running
// the first arm whose value patterns it equals (the wildcard last), and an if by
// folding the condition and running the taken branch — a guard whose condition
// is false falls through to the next statement. A bare expression statement has
// no value and is skipped. It is the compile-time execution model the const
// folder shares with a function application, kept in step with the runtime's
// first-match dispatch.
func evalBody(body []ast.Stmt, ctx evalCtx) *ir.Constant {
	for _, stmt := range body {
		switch stmt := stmt.(type) {
		case *ast.ReturnStmt:
			if stmt.Value == nil {
				return nil
			}
			return evalExpr(stmt.Value, ctx)
		case *ast.SwitchStmt:
			if v := evalSwitch(stmt, ctx); v != nil {
				return v
			}
			// A switch whose scrutinee or arms cannot be folded — or that
			// selected an arm with no return — leaves the body unevaluated:
			// nothing after a guaranteed-return switch is reachable, and a
			// partial switch cannot fold.
			return nil
		case *ast.IfStmt:
			v, out := evalIf(stmt, ctx)
			if out == ifFellThrough {
				continue // the taken branch (or no branch) ran without returning
			}
			// ifReturned yields the branch's value; ifUnknown (an unfoldable
			// condition or branch) leaves v nil, which stops folding here.
			return v
		}
	}
	return nil
}

// ifOutcome is the result of folding an if statement at compile time.
type ifOutcome int

const (
	ifUnknown     ifOutcome = iota // the condition or the taken branch could not be folded
	ifReturned                     // the taken branch returned a value
	ifFellThrough                  // no branch returned; execution continues after the if
)

// evalIf folds an if statement: it evaluates the condition, runs the matching
// branch (the then body when the condition is true, otherwise the else-if chain
// or the else body), and reports whether that branch returned a value, fell
// through, or could not be determined. A branch with no return falls through to
// the statement after the if, exactly as it does at runtime.
func evalIf(s *ast.IfStmt, ctx evalCtx) (*ir.Constant, ifOutcome) {
	cond := evalExpr(s.Cond, ctx)
	if cond == nil || cond.Kind != ir.ConstBool {
		return nil, ifUnknown // an unfoldable (or non-bool) condition: cannot dispatch
	}
	if cond.Bool {
		return branchOutcome(s.Then, ctx)
	}
	switch {
	case s.ElseIf != nil:
		return evalIf(s.ElseIf, ctx)
	case s.Else != nil:
		return branchOutcome(s.Else, ctx)
	default:
		return nil, ifFellThrough // a false guard with no else: continue past it
	}
}

// branchOutcome runs a taken branch body and classifies how it ended: a return
// of a folded value (ifReturned), a fall-through to after the if (ifFellThrough
// when no statement returned), or an unfoldable return (ifUnknown). It mirrors
// evalBody but distinguishes "ran to the end without returning" from "could not
// fold", which the if needs to decide whether to continue the outer body.
func branchOutcome(body []ast.Stmt, ctx evalCtx) (*ir.Constant, ifOutcome) {
	for _, stmt := range body {
		switch stmt := stmt.(type) {
		case *ast.ReturnStmt:
			if stmt.Value == nil {
				return nil, ifUnknown
			}
			if v := evalExpr(stmt.Value, ctx); v != nil {
				return v, ifReturned
			}
			return nil, ifUnknown
		case *ast.SwitchStmt:
			if v := evalSwitch(stmt, ctx); v != nil {
				return v, ifReturned
			}
			return nil, ifUnknown
		case *ast.IfStmt:
			v, out := evalIf(stmt, ctx)
			if out == ifFellThrough {
				continue
			}
			if out == ifReturned {
				return v, ifReturned
			}
			return nil, ifUnknown
		}
	}
	return nil, ifFellThrough // the branch ran to its end without returning
}

// evalSwitch selects and runs the matching arm of a switch: it folds the
// scrutinee, compares it for equality against each arm's folded value patterns
// in order, and runs the first matching arm's body — the wildcard arm last. It
// returns nil when the scrutinee or a needed pattern cannot be folded, or when
// no arm (and no wildcard) matches, so a switch only folds when its dispatch is
// fully determined.
func evalSwitch(sw *ast.SwitchStmt, ctx evalCtx) *ir.Constant {
	scrut := evalExpr(sw.Scrutinee, ctx)
	if scrut == nil {
		return nil
	}
	for _, arm := range sw.Arms {
		for _, v := range arm.Values {
			cv := evalExpr(v, expectingScrutinee(ctx, scrut))
			if cv == nil {
				return nil // an unfoldable pattern: the dispatch is undetermined
			}
			if constEqual(scrut, cv) {
				return evalBody(arm.Body, ctx)
			}
		}
	}
	if sw.Else != nil {
		return evalBody(sw.Else, ctx)
	}
	return nil
}

// expectingScrutinee folds an arm value with the scrutinee's enum in scope, so
// a bare member (Common) folds to that member — the value rule a switch shares
// with a const initializer's bare member.
func expectingScrutinee(ctx evalCtx, scrut *ir.Constant) evalCtx {
	if scrut.Kind == ir.ConstEnum {
		ctx.expected = scrut.EnumDef
	}
	return ctx
}

// constEqual reports whether two folded constants are equal for switch
// dispatch: enum members compare by identity (definition and index), and the
// scalar kinds by their value. Differing kinds are never equal.
func constEqual(a, b *ir.Constant) bool {
	if a == nil || b == nil || a.Kind != b.Kind {
		return false
	}
	switch a.Kind {
	case ir.ConstInt:
		return a.Int.Cmp(b.Int) == 0
	case ir.ConstBool:
		return a.Bool == b.Bool
	case ir.ConstString:
		return a.Str == b.Str
	case ir.ConstEnum:
		return a.EnumDef == b.EnumDef && a.EnumIndex == b.EnumIndex
	case ir.ConstDatetime, ir.ConstDuration:
		return a.Millis == b.Millis
	default:
		return false
	}
}
