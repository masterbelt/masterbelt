// Package eval is the value half of masterbelt's constant analysis: it folds a
// constant expression to its value (ir.Constant). It is the evaluation mirror of
// package types/infer — where infer derives an expression's type, eval derives
// its value — over the same desugared shape: a literal, a value reference, or a
// method call. A method call's value comes from a user-defined method's body
// (evaluated with self bound to the receiver, the way a top-level function's
// body is) when the receiver carries one, or from the receiver type's native
// intrinsic in the builtin registry otherwise.
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
	"github.com/masterbelt/masterbelt/pkg/masterbelt/types"
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

// ReceiverTyper is an optional Env capability: resolving a written type
// annotation to its definition. It is the syntactic type channel a method call
// on a nominal type over a primitive folds through — a value of such a type
// (a plain integer, say) carries no type definition, so the folder reads the
// receiver's static type from the annotations in scope instead. Every channel
// is a declared annotation: a conversion's type name (Level(5)), a constant's
// annotation (const x: Level), a parameter or let's annotation, a method's self
// receiver, a function or method's result type (getLevel().increment()). The
// resolution is a pure name lookup in the universe — never the type query — so
// the value query stays independent of typing, and because every channel is an
// annotation the source already type-checked against, the def it names is the
// one the type checker used. An Env that does not implement this resolves only
// enum receivers (which carry their definition in the value).
type ReceiverTyper interface {
	// TypeExprDef resolves a written type annotation to the type definition it
	// names, or nil when it names no nominal type (an unresolved name, a bare
	// composite like a union or record, a primitive). It is a universe lookup,
	// not a type query.
	TypeExprDef(t ast.TypeExpr) *ir.TypeDef
	// TypeExprType resolves a written type annotation to its full resolved type —
	// a record annotation yielding an *ir.Record, so a field's static type is
	// readable for a field-receiver method call (p.lv.increment()). It is the
	// same universe resolution TypeExprDef runs, minus the nominal-only filter,
	// so it is never the type query. It returns nil for a nil or unresolvable
	// annotation.
	TypeExprType(t ast.TypeExpr) ir.Type
}

// Decl folds a declaration's value, or nil when it has no initializer. Overflow
// is intentionally not checked here — an integer literal is the arbitrary-
// precision int; the range check happens where the constant's concrete type is
// known.
func Decl(decl *ast.ConstDecl, env Env) *ir.Constant {
	return DeclExpecting(decl, nil, ir.CollUnknown, env)
}

// DeclExpecting folds a declaration's value with an expected enum and an expected
// collection mapness in scope, both from the const's annotation. expected is the
// enum definition the annotation named (a bare member const Top: Rarity = Legend
// resolves through it), or nil; coll is the mapness an empty collection literal
// settles to (an empty const m: map<K,V> = [] folding to an empty map), or
// ir.CollUnknown. Both are pre-resolved by the caller — the value query must not
// call the type query, so the caller resolves the annotation directly. A nil
// value yields nil.
func DeclExpecting(decl *ast.ConstDecl, expected *ir.TypeDef, coll ir.CollKind, env Env) *ir.Constant {
	if decl.Value == nil {
		return nil
	}
	return evalExpr(decl.Value, evalCtx{env: env, expected: expected, expectedColl: coll})
}

// Expr folds an expression to its constant value, or nil when it cannot be
// evaluated. Reading references through env lets the engine track dependencies
// and reuse its cycle guard.
func Expr(e ast.Expr, env Env) *ir.Constant {
	return evalExpr(e, evalCtx{env: env})
}

// ExprIn folds an expression with a set of local bindings in scope, so a
// reference to a body-local (a let, a parameter the caller has a value for)
// folds to its value. It is how a body-position check folds an expression the
// query engine cannot reach on its own — the local environment a function body
// carries is not in env. A nil locals behaves exactly like Expr.
func ExprIn(e ast.Expr, locals map[string]*ir.Constant, env Env) *ir.Constant {
	return evalExpr(e, evalCtx{env: env, locals: locals})
}

// ExprInExpecting is ExprIn with the collection-mapness channel set, so an empty
// collection literal a let annotation typed (let m: map<K,V> = []) folds to the
// settled empty collection a body-position check needs to tell a map upsert from
// a list write. coll is ir.CollUnknown for a non-collection (or unannotated)
// binding, which then behaves exactly like ExprIn.
func ExprInExpecting(e ast.Expr, locals map[string]*ir.Constant, coll ir.CollKind, env Env) *ir.Constant {
	return evalExpr(e, evalCtx{env: env, locals: locals, expectedColl: coll})
}

// ExprExpecting folds an expression with an expected enum in scope, so a bare
// member resolves through it. want is the expected type; when it is an enum's
// named type the bare-member rule applies, otherwise it is ignored. It is how
// an enum member's initializer (typed against the enum's own base) and a const
// initializer pass their expectation to the folder.
func ExprExpecting(e ast.Expr, want ir.Type, env Env) *ir.Constant {
	return evalExpr(e, evalCtx{env: env, expected: expectedEnum(want)})
}

// expectedEnum returns the enum definition a type carries, or nil when it
// carries none — the folder's name for types.EnumDef. A union carrying an enum
// (R | error), and — unwrapped through the union helper — a named or generic
// union alias of one (optional<Rarity>) all resolve, so a bare member folds under
// an alias expectation exactly as under the bare enum.
func expectedEnum(want ir.Type) *ir.TypeDef {
	return types.EnumDef(want)
}

// CollKindOf returns the mapness a resolved type names — CollMap for a map<K,V>
// (or a nominal type whose underlying is a map), CollList for a list<T> (or a
// nominal list), and CollUnknown for anything else. It is the syntactic channel
// an empty collection literal settles through, derived from an annotation or
// result type the type resolver already resolved (a pure universe lookup, never
// the value-blind type query). A union is deliberately left CollUnknown: a member
// being a collection does not pin which kind the literal is, so the literal stays
// undecided rather than being settled wrong. It is exported so the semantic layer
// can resolve a const annotation's kind and feed it to DeclExpecting, mirroring
// annotationEnum for the enum channel.
func CollKindOf(want ir.Type) ir.CollKind {
	switch w := want.(type) {
	case *ir.App:
		if w.Def == nil {
			return ir.CollUnknown
		}
		switch w.Def.Name {
		case "map":
			return ir.CollMap
		case "list":
			return ir.CollList
		}
		return collKindFromDef(w.Def)
	case *ir.Named:
		return collKindFromDef(w.Def)
	}
	return ir.CollUnknown
}

// collKindFromDef returns the mapness a nominal type bottoms out at — a Bag (=
// list<int>) yields CollList — by following its underlying collection definition,
// or CollUnknown for a type with no list/map underlying.
func collKindFromDef(def *ir.TypeDef) ir.CollKind {
	d := underlyingCollectionDef(def, map[*ir.TypeDef]bool{})
	if d == nil {
		return ir.CollUnknown
	}
	switch d.Name {
	case "map":
		return ir.CollMap
	case "list":
		return ir.CollList
	}
	return ir.CollUnknown
}

// Predicate folds a refinement predicate with the self keyword bound to self and
// its static type being selfDef — the type being refined, so a self-method call
// in the predicate (where self.isValid()) resolves the method on selfDef and
// folds its body, the way a method call on a self receiver does inside a method
// body. selfDef is nil when the refined type is a bare primitive with no
// definition to read methods from (a predicate using only operators still
// folds). It returns nil when the predicate cannot be folded.
func Predicate(pred ast.Expr, self *ir.Constant, selfDef *ir.TypeDef, env Env) *ir.Constant {
	return evalExpr(pred, evalCtx{env: env, self: self, selfDef: selfDef})
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
	// expectedColl is the mapness an empty collection literal settles to — the
	// list/map distinction a syntactic channel supplies for a literal that has no
	// key to read (a const/let annotation, a function/method result type). It is
	// ir.CollUnknown when there is no channel (a bare []). Like expected it reaches
	// only the immediate expression; a nested literal sets its own.
	expectedColl ir.CollKind
	// resultColl is the mapness a return's empty collection settles to — the
	// declared result type of the function or method whose body is being folded,
	// or ir.CollUnknown outside one (or for a non-collection result). It is the
	// result-type channel, threaded through the body so a `return []` reaches it;
	// evalBody hands it to the return expression's expectedColl.
	resultColl ir.CollKind
	// selfDef is the type definition the self keyword has inside an applied
	// method body — the method's owning type — or nil outside one. It is the
	// syntactic type channel for a self-receiver method call (self.increment()).
	selfDef *ir.TypeDef
	// localDefs records the static type definition of a let-bound or parameter
	// local whose annotation names a nominal type, so a method call on that local
	// folds. A local absent from the map has no statically-known definition (its
	// type is a primitive, a composite, or could not be read syntactically); only
	// nominal-typed locals appear. It shares the lifetime of locals (the applied
	// body's environment), and like it is nil at the top level.
	localDefs map[string]*ir.TypeDef
}

// maxApplyDepth caps function-application recursion: a recursive fold that has
// not bottomed out by this depth is treated as unevaluable (nil) — the same
// verdict an engine-level value cycle gets — instead of overflowing the stack.
const maxApplyDepth = 256

// maxRangeIterations caps the number of elements a range fold or for visits at
// compile time. A range is constructed lazily from its bounds, so range(0,
// 1_000_000_000) is a small value; only walking it would materialize the
// sequence. Folding or iterating a range wider than this bound is treated as
// unevaluable (nil / undecided) — the same conservative verdict the depth guard
// gives — so a wide range neither hangs the folder nor exhausts memory. It is a
// compile-time evaluation limit, not a language limit: a range of any width is a
// valid value; it simply does not fold past this many steps. The list-folding
// path needs no such cap because a list is already materialized — its length is
// its memory — whereas a range's width is unbounded by its representation.
const maxRangeIterations = 1 << 20

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

// recordField returns the value of a record constant's named field, or nil when
// the record has no such field (a malformed program the checker reports). It is
// how a field access (p.lv) reads its value once the record has folded.
func recordField(recv *ir.Constant, name string) *ir.Constant {
	for _, f := range recv.Fields {
		if f.Name == name {
			return f.Value
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
	// their own (expectation-free) context. The collection-mapness channel is
	// consumed the same way — an empty literal reads it here, a nested literal
	// does not inherit it.
	sub := ctx
	sub.expected = nil
	sub.expectedColl = ir.CollUnknown
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
	case *ast.NullLit:
		// The null literal folds to the null value, so an optional's null arm and a
		// null-valued binding fold like any other literal.
		return ir.NullConstant()
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
		// The bound self value, or nil outside a self-binding context. It is set
		// by a refinement predicate (self bound to the checked constant) and by a
		// folded method body (self bound to the receiver).
		return ctx.self
	case *ast.CollectionLit:
		// An empty literal consumes the mapness channel here (off ctx, not the
		// cleared sub); collection folds its entries through sub-contexts that have
		// it cleared, so a nested literal sets its own.
		collCtx := sub
		collCtx.expectedColl = ctx.expectedColl
		return collection(e, collCtx)
	case *ast.RecordLit:
		return record(e, sub)
	case *ast.TernaryExpr:
		return ternary(e, sub)
	case *ast.FuncLit:
		// A function literal folds to a closure over the bindings in scope, so it
		// can be applied later (by list.map) or stored in a constant. The capture
		// is a snapshot: a let or assignment that mutates the body's environment
		// after the closure is built must not reach back into it.
		return ir.FuncConstant(e, maps.Clone(ctx.locals))
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
		// A member access whose receiver folds to a record value reads the named
		// field (p.lv), so a field's value participates in folding and a method
		// call on it has a receiver. It is the last branch — the type-member and
		// namespace forms above resolve by name, this one by value.
		if recv := evalExpr(e.Receiver, sub); recv != nil && recv.Kind == ir.ConstRecord {
			return recordField(recv, e.Member.Name)
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
				// A local bound to a function value is applied — a higher-order
				// parameter (a foldable's pred/f) called by name inside the body. It
				// shadows a type or function of the same name, exactly as the type
				// rules read the local.
				if v, isLocal := ctx.locals[id.Name]; isLocal {
					if v != nil && v.Kind == ir.ConstFunc {
						return applyLocalFunc(sub, v, e.Arguments)
					}
					return nil // a call of a non-function local does not fold
				}
				if def := ctx.env.LookupType(id.Name); def != nil {
					return convert(def, e.Arguments, sub)
				}
				if cands := ctx.env.ResolveFunc(id); len(cands) > 0 {
					return applyFunc(cands, e.Arguments, sub)
				}
				// A bare call inside a method body whose name is a method of self
				// is an implicit self-call (the self omitted) — the form an
				// interface's provided method uses to call the required fold. It
				// dispatches exactly as a written self.name(...) would, through
				// the self value and its owning definition.
				if v, ok := selfCall(ctx, sub, id.Name, e.Arguments); ok {
					return v
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
		// The boolean connectives short-circuit: && (anan) with a false receiver
		// is false and || (oror) with a true receiver is true, without ever
		// folding the right operand — exactly as the runtime (and a ternary)
		// never evaluates the untaken side. This both matches the dynamic
		// semantics and keeps an unfoldable (or would-not-fold) dead operand from
		// blocking the fold.
		if v, ok := shortCircuit(recv, member.Member.Name, e.Arguments); ok {
			return v
		}
		// A bare member as an operator/method argument folds through the receiver's
		// static enum (rarity == Legend, desugared to rarity.eql(Legend)): the
		// enum is read syntactically from the receiver's annotation (recvType, never
		// the type query), so a comparison whose argument is a bare member folds.
		// The expectation reaches only the immediate argument, like every other
		// expected-enum channel; a non-enum receiver yields nil and changes nothing.
		argCtx := sub
		argCtx.expected = expectedEnum(recvType(sub, member.Receiver))
		args := make([]*ir.Constant, len(e.Arguments))
		for i, a := range e.Arguments {
			args[i] = evalExpr(a, argCtx)
		}
		return call(sub, member.Receiver, recv, member.Member.Name, args)
	default:
		return nil
	}
}

// applyLocalFunc folds a call of a function-valued local fn(args): the arguments
// fold in the caller's context, then the closure applies over its captured
// environment. It is how a higher-order parameter — a foldable's pred or f,
// called by name inside the provided method's fold step — folds.
func applyLocalFunc(ctx evalCtx, fn *ir.Constant, argExprs []ast.Expr) *ir.Constant {
	args := make([]*ir.Constant, len(argExprs))
	for i, a := range argExprs {
		if args[i] = evalExpr(a, ctx); args[i] == nil {
			return nil
		}
	}
	return apply(ctx, fn, args)
}

// selfCall folds an implicit self-call name(args) inside a method body: a bare
// call whose name resolves to a method of self's owning type, dispatched the way
// a written self.name(args) is. It reports whether it handled the call — false
// when there is no self in scope, leaving the bare-call path's normal failure.
// The receiver expression is the synthetic self, so the call's receiver-def
// channel resolves to ctx.selfDef exactly as self.name(...) does, and a
// collection self (a foldable provided method calling fold) resolves by value.
func selfCall(ctx, sub evalCtx, name string, argExprs []ast.Expr) (*ir.Constant, bool) {
	if ctx.self == nil {
		return nil, false
	}
	args := make([]*ir.Constant, len(argExprs))
	for i, a := range argExprs {
		args[i] = evalExpr(a, sub)
	}
	return call(sub, &ast.SelfExpr{}, ctx.self, name, args), true
}

// shortCircuit folds a boolean connective whose receiver already decides the
// result: false && _ is false, true || _ is true. It reports whether it handled
// the call — true with the folded value when the receiver short-circuits, false
// when it does not (a non-bool/unfoldable receiver, the non-deciding side, or a
// method that is not a connective), leaving the normal eager path to fold the
// argument and dispatch.
func shortCircuit(recv *ir.Constant, name string, args []ast.Expr) (*ir.Constant, bool) {
	if recv == nil || recv.Kind != ir.ConstBool || len(args) != 1 {
		return nil, false
	}
	switch name {
	case "anan":
		if !recv.Bool {
			return ir.BoolConstant(false), true
		}
	case "oror":
		if recv.Bool {
			return ir.BoolConstant(true), true
		}
	}
	return nil, false
}

// convert folds a conversion or constructor T(x). A nominal conversion over a
// primitive (Level(5), where Level = int8) is the identity on the value — a
// Level is represented as its underlying integer — so the argument's folded
// value passes through when its kind backs the type, which both gives Level(5) a
// value and lets a method fold on it. The error constructor error("msg") folds
// to an error constant carrying the message; the range constructor range(start,
// end) folds to a range value over the two folded bounds. Any other conversion
// has no constant value here.
func convert(def *ir.TypeDef, args []ast.Expr, ctx evalCtx) *ir.Constant {
	// range(start, end) is the one two-argument constructor: it folds to a range
	// value when both bounds fold to integers. It is a builtin the registry does
	// not natively back (like list/map), so it is handled here by name rather than
	// through a native descriptor.
	if def.Builtin && def.Name == "range" {
		return convertRange(args, ctx)
	}
	if len(args) != 1 {
		return nil
	}
	if def.Builtin {
		n, ok := ctx.env.Registry().Native(def.Name)
		if !ok || !n.Err {
			return nil // only error has a constant-valued native conversion
		}
		v := evalExpr(args[0], ctx)
		if v == nil || v.Kind != ir.ConstString {
			return nil
		}
		return ir.ErrorConstant(v.Str)
	}
	// A nominal type over a primitive (or a list/map): the value is the argument's,
	// unchanged, when its kind matches the underlying type — a Level is its integer,
	// a Bag (= list<int>) is its collection — so the conversion passes the folded
	// value through, which gives the value an identity and lets a method (and a
	// for) fold on it. A conversion to a composite (a record, a union) has no
	// constant identity value here.
	v := evalExpr(args[0], ctx)
	if v == nil {
		return nil
	}
	reg := ctx.env.Registry()
	if defBacksKind(reg, def, v.Kind) {
		return v
	}
	if v.Kind == ir.ConstCollection && defBacksKindCollection(def) {
		return v
	}
	return nil
}

// convertRange folds the range constructor range(start, end): both bounds must
// fold to integers, and the result is a range value over them — the half-open
// sequence start..end-1 (an end at or below start being the empty range). The
// sequence is not materialized here; the bounds are kept lazily so a wide range
// is a small value, and the fold/for walk over it is bounded separately. A
// non-two argument list (a recovered or step-form call) or an unfoldable or
// non-integer bound does not fold.
func convertRange(args []ast.Expr, ctx evalCtx) *ir.Constant {
	if len(args) != 2 {
		return nil
	}
	start := evalExpr(args[0], ctx)
	end := evalExpr(args[1], ctx)
	if start == nil || end == nil || start.Kind != ir.ConstInt || end.Kind != ir.ConstInt {
		return nil
	}
	return ir.RangeConstant(start.Int, end.Int)
}

// callable is the body-bearing shape applyBody folds: a pure (non-extern,
// effect-free) routine's parameters and statement body. A top-level function
// and a user-defined method share this — a method is a function with a self
// receiver — so the parameter-binding, purity, and depth-guard rules live in
// one place. extern reports an extern declaration (a native intrinsic, no
// body) and effectful one that interacts with the world; neither folds. selfDef
// is the method's owning type (the syntactic type of self in the body), nil for
// a function.
type callable struct {
	params    []*ast.ParamDef
	body      []ast.Stmt
	extern    bool
	effectful bool
	selfDef   *ir.TypeDef
	result    ast.TypeExpr // the declared result type, the collection-mapness channel for a return []
}

func funcCallable(fd *ast.FuncDecl) callable {
	return callable{params: fd.Params, body: fd.Body, extern: fd.Extern, effectful: len(fd.Effects) > 0, result: fd.Result}
}

func methodCallable(md *ast.MethodDecl, selfDef *ir.TypeDef) callable {
	return callable{params: md.Params, body: md.Body, extern: md.Extern, effectful: len(md.Effects) > 0, selfDef: selfDef, result: md.Result}
}

// applyBody folds a pure routine's body against already-folded argument values,
// with self bound for a method (nil for a top-level function). The body sees
// only its parameters (and self) — never the caller's locals — exactly as a
// function or method body sees its parameters and the program's declarations
// through env. An extern or effectful routine does not fold: only a pure one
// has a compile-time value, and the upstream pure-context check keeps an
// effectful one out of every const position; this guard keeps eval pure even if
// one slips through. The depth guard counts the application, turning runaway
// recursion (direct or mutual, through functions and methods alike) into an
// unevaluated value rather than a stack overflow.
//
// The static type channels are seeded for the body: self's definition (a
// method's owning type) and each parameter whose annotation names a nominal type
// — so a method call on self or on a nominal-typed parameter inside the body
// resolves its receiver's definition syntactically, the way the caller resolved
// this call's receiver.
func applyBody(c callable, self *ir.Constant, vals []*ir.Constant, ctx evalCtx) *ir.Constant {
	if c.extern || c.effectful {
		return nil
	}
	locals := make(map[string]*ir.Constant, len(c.params))
	var localDefs map[string]*ir.TypeDef
	for i, p := range c.params {
		locals[p.Name] = vals[i]
		if def := annotationDef(ctx.env, p.Type); def != nil {
			if localDefs == nil {
				localDefs = make(map[string]*ir.TypeDef, len(c.params))
			}
			localDefs[p.Name] = def
		}
	}
	return evalBody(c.body, evalCtx{
		env: ctx.env, locals: locals, self: self,
		selfDef: c.selfDef, localDefs: localDefs, depth: ctx.depth + 1,
		// The declared result type is the collection-mapness channel for the body's
		// returns: a `return []` in a map<K,V>-returning routine folds to an empty
		// map. It is resolved through the same universe lookup the other channels use.
		resultColl: CollKindOf(annotationType(ctx.env, c.result)),
	})
}

// annotationDef resolves a written type annotation to its definition through the
// Env's optional ReceiverTyper, or nil when the Env does not supply one or the
// annotation names no nominal type. A nil annotation (an omitted type) resolves
// to nil.
func annotationDef(env Env, t ast.TypeExpr) *ir.TypeDef {
	if t == nil {
		return nil
	}
	rt, ok := env.(ReceiverTyper)
	if !ok {
		return nil
	}
	return rt.TypeExprDef(t)
}

// applyFunc folds a call of a top-level function: the arguments fold in the
// caller's context, the overload whose parameters accept their value kinds is
// selected, and its body's return folds with only the parameter bindings in
// scope. Evaluation is type-blind, so the selection is by value kind and
// conservative: when more than one candidate could plausibly accept the
// arguments — same-kind overloads like int8/int32, or a parameter type it
// cannot decide — the call simply does not fold, so a wrong overload's body is
// never applied. The depth guard turns runaway recursion into an unevaluated
// value.
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
		if fits(cand.Params, vals) {
			fd = cand
			n++
		}
	}
	if n != 1 {
		return nil
	}
	return applyBody(funcCallable(fd), nil, vals, ctx)
}

// fits reports whether a top-level function's parameter list could accept the
// folded argument values: the arity must agree, and each parameter's written
// type must accept its argument's value kind. A function has no self-typed
// parameter, so the receiver kind is unknown (-1). It is the type-blind,
// conservative selection both the function and the method overload paths share —
// kindAccepts rules in only the kinds a parameter can hold and answers true for
// anything it cannot decide, so a wrong overload is never ruled in, only an
// undecidable one kept out.
func fits(params []*ast.ParamDef, vals []*ir.Constant) bool {
	return fitsWithSelf(params, vals, -1)
}

// fitsWithSelf is fits with the receiver's value kind known (a method call), so
// a self-typed parameter — common in operator-style methods (merge(points: self))
// — accepts only that kind rather than being undecidable. A selfKind of -1 means
// the receiver kind is unknown (a function, or a method whose receiver kind could
// not be determined), and a self-typed parameter is then undecidable as before.
func fitsWithSelf(params []*ast.ParamDef, vals []*ir.Constant, selfKind ir.ConstKind) bool {
	if len(params) != len(vals) {
		return false
	}
	for i, p := range params {
		if isSelfType(p.Type) {
			if selfKind >= 0 && vals[i].Kind != selfKind {
				return false
			}
			continue // an undecidable self (unknown receiver kind): accept
		}
		if !kindAccepts(p.Type, vals[i].Kind) {
			return false
		}
	}
	return true
}

// kindAccepts reports whether a parameter's written type can hold a constant
// of the given kind. It decides by spelling for the prelude's primitive names
// and the structural type forms, and answers true for anything it cannot
// decide (a named alias, a union, a qualified name) — so a wrong overload is
// never ruled in, only an undecidable set kept from folding. A self-typed
// parameter is handled by fitsWithSelf (which knows the receiver kind), not here.
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

// ternary folds a conditional value cond ? then : else: it folds the condition
// and, when it is a bool constant, folds and returns only the taken branch — the
// untaken one is never evaluated, exactly as the runtime (and a switch arm)
// only runs the selected path. An unfoldable or non-bool condition leaves the
// whole expression unevaluated (nil), since the dispatch is undetermined.
func ternary(e *ast.TernaryExpr, ctx evalCtx) *ir.Constant {
	cond := evalExpr(e.Cond, ctx)
	if cond == nil || cond.Kind != ir.ConstBool {
		return nil
	}
	if cond.Bool {
		return evalExpr(e.Then, ctx)
	}
	return evalExpr(e.Else, ctx)
}

// collection folds a collection literal: each entry's value (and key, for a map)
// is folded, in order. It returns nil if any element is unevaluated, so a
// collection with an unfoldable element does not fold to a partial value. A
// non-empty literal settles its mapness from its entries; an empty literal — which
// has no key to read — takes the mapness the syntactic channel supplied (ctx's
// expectedColl: a map/list-typed annotation or result type), staying CollUnknown
// when there is no channel (a bare []).
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
	if len(entries) == 0 {
		return ir.CollectionConstantOf(entries, ctx.expectedColl)
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
//
// recvExpr is the receiver's source expression, the syntactic type channel a
// nominal-typed receiver resolves its definition through (its value carries no
// type); it is nil for an internally synthesized receiver, which then resolves
// only by value (an enum).
func call(ctx evalCtx, recvExpr ast.Expr, recv *ir.Constant, name string, args []*ir.Constant) *ir.Constant {
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
	// A user-defined method with a body folds first — its body is evaluated with
	// self bound — so a method call (Element.Fire.isFire(), Level(5).increment())
	// reaches its result the way a top-level function call does. The receiver's
	// type is read either from the value (an enum constant carries its EnumDef) or
	// syntactically from the declared annotation in scope (a nominal type over a
	// primitive), never from the type query — so eval stays value-blind. An extern
	// method (the six enum comparisons, a native intrinsic) has no body and falls
	// through to the intrinsic path below, preserving the existing resolution
	// order — a user-defined method only when one exists, the intrinsic otherwise.
	if v, ok := applyUserMethod(ctx, recvExpr, recv, name, args); ok {
		return v
	}
	if recv.Kind == ir.ConstCollection {
		return collectionMethod(ctx, recv, name, args)
	}
	if recv.Kind == ir.ConstRange {
		return rangeMethod(ctx, recv, name, args)
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

// applyUserMethod folds a call of a user-defined method. It reports whether it
// handled the call: true with the folded value (or nil when the body did not
// fold), false when the receiver's type could not be determined or has no
// body-bearing method of that name, leaving the caller's intrinsic dispatch to
// run. The receiver's definition comes from the value (an enum) or, failing
// that, from the receiver expression's declared annotation (a nominal type); a
// definition that does not back the receiver's value kind is rejected, so a
// wrong def (a string-based type over an integer value, say) never applies. The
// selection mirrors a function overload's: of the methods of that name that have
// a body, the one whose parameters accept the argument value kinds is chosen,
// and the call folds only when exactly one fits — an ambiguous or undecidable
// set does not fold (nil), so a wrong overload's body is never applied. The
// depth guard (shared with applyFunc through ctx.depth) turns runaway recursion
// — a method calling itself, or a method/function cycle — into an unevaluated
// value rather than a stack overflow.
func applyUserMethod(ctx evalCtx, recvExpr ast.Expr, recv *ir.Constant, name string, args []*ir.Constant) (*ir.Constant, bool) {
	def := receiverDef(ctx, recvExpr, recv)
	if def == nil {
		return nil, false
	}
	var sel *ast.MethodDecl
	n := 0
	for _, m := range methodSyntaxes(ctx.env.Registry(), def, name) {
		// The receiver's value kind decides a self-typed parameter, so an
		// operator-style overload (merge(points: self) vs merge(active: bool))
		// resolves the way the type checker did.
		if fitsWithSelf(m.Params, args, recv.Kind) {
			sel = m
			n++
		}
	}
	if n == 0 {
		return nil, false // no body-bearing overload: let the intrinsic path run
	}
	if ctx.depth >= maxApplyDepth {
		return nil, true // the recursion guard fired: a safe, unfoldable result
	}
	if n != 1 {
		return nil, true // ambiguous: do not fold, but the method is user-defined
	}
	return applyBody(methodCallable(sel, def), recv, args, ctx), true
}

// methodSyntaxes collects the body-bearing AST declarations of the named method
// the definition binds: its own declarations, and — when it declares the name
// nowhere — the provided defaults of each interface it opts into (a list's
// count/any/all/map/filter/keys/values reached through impl foldable). It
// mirrors the type system's findMethods shadowing — a definition that declares
// the name at all shadows the interface's provided one (a body or an extern
// alike) — and derives from the underlying type, so the rule the folder uses to
// reach a method is the rule the type checker used to resolve it. A method with
// no AST syntax or an empty body (an extern operator, the native fold/len) is
// left to the intrinsic path.
func methodSyntaxes(reg *builtin.Registry, def *ir.TypeDef, name string) []*ast.MethodDecl {
	return collectMethodSyntaxes(reg, def, name, map[*ir.TypeDef]bool{})
}

func collectMethodSyntaxes(reg *builtin.Registry, def *ir.TypeDef, name string, seen map[*ir.TypeDef]bool) []*ast.MethodDecl {
	if def == nil || seen[def] {
		return nil
	}
	seen[def] = true
	// A definition that declares the name itself shadows everything it would
	// derive, exactly as findMethods does. The shadow is by name, so an extern
	// declaration (no body) of the name still suppresses an interface's provided
	// default — the inherent list.map shadows foldable's provided map.
	declares := false
	var out []*ast.MethodDecl
	for _, m := range def.Methods {
		if m.Name != name {
			continue
		}
		declares = true
		if m.Syntax != nil && len(m.Syntax.Body) > 0 {
			out = append(out, m.Syntax)
		}
	}
	if declares {
		return out
	}
	for _, impl := range def.Impls {
		if idef := methodTableDef(reg, impl); idef != nil && idef.Interface != nil {
			if ms := collectMethodSyntaxes(reg, idef, name, seen); len(ms) > 0 {
				out = append(out, ms...)
			}
		}
	}
	if len(out) > 0 {
		return out
	}
	if !def.Builtin {
		return collectMethodSyntaxes(reg, methodTableDef(reg, def.Body), name, seen)
	}
	return out
}

// methodTableDef returns the definition behind a Named or App type — the def a
// method table is read from — resolving a Builtin name through the registry so a
// prelude collection (list, map) reached as a Builtin body still yields its def.
// Any other type form (a union, record, function, type variable) has no def.
func methodTableDef(reg *builtin.Registry, t ir.Type) *ir.TypeDef {
	switch t := t.(type) {
	case *ir.Named:
		return t.Def
	case *ir.App:
		return t.Def
	case *ir.Builtin:
		d, _ := reg.Lookup(t.Name)
		return d
	}
	return nil
}

// receiverDef determines the static type definition of a method call's receiver:
// from the value when it carries one (an enum constant), otherwise from the
// receiver expression's declared annotation, a syntactic type channel. The
// syntactic def must back the receiver's value kind — a Level (int8) def over an
// integer value, never a string-based def over it — so a def read from an
// annotation that does not match the value is rejected (the value won't type-check
// against it, so this only fires on a malformed program, but it keeps a wrong def
// from ever applying). It returns nil when neither channel yields a usable def.
func receiverDef(ctx evalCtx, recvExpr ast.Expr, recv *ir.Constant) *ir.TypeDef {
	// The enum channel is highest priority: an enum value names its own type, no
	// annotation needed.
	if recv.Kind == ir.ConstEnum {
		return recv.EnumDef
	}
	// A collection value names its type by shape: a keyed value is a map, an
	// unkeyed (or empty) one a list. The def is a universe lookup — never the type
	// query — so a foldable's provided method (count, keys, ...) reaches its body
	// through the collection's impl while eval stays value-blind. The provided
	// bodies are shape-agnostic (they fold over the same entries either way), so
	// the empty-collection-reads-as-list default folds correctly.
	if recv.Kind == ir.ConstCollection {
		return ctx.env.LookupType(collectionTypeName(recv))
	}
	// A range value names its own type directly — it is the sole inhabitant kind of
	// the range builtin — so its foldable provided methods (count, any, map, ...)
	// reach their bodies through range's impl by a universe lookup, the same
	// value-named channel a collection uses, without needing a receiver annotation.
	if recv.Kind == ir.ConstRange {
		return ctx.env.LookupType("range")
	}
	def := syntacticDef(ctx, recvExpr)
	if def == nil || !defBacksKind(ctx.env.Registry(), def, recv.Kind) {
		return nil
	}
	return def
}

// collectionTypeName is the prelude type name a folded collection binds its
// methods through when no receiver annotation supplies a def: a settled map binds
// through map, everything else (a list, or an unknown empty collection) through
// list — the same conservative default that keeps the mapness-independent methods
// (len, fold, count, ...) folding on an unknown empty collection.
func collectionTypeName(recv *ir.Constant) string {
	if recv.IsMap() {
		return "map"
	}
	return "list"
}

// syntacticDef resolves a receiver expression to the type definition its
// declared static type names, through the annotation channels — never the type
// query:
//
//   - a conversion call Level(5): the callee names the type directly;
//   - self: the enclosing method's owning type (ctx.selfDef);
//   - a parameter or let local: its annotation's def (ctx.localDefs), set when
//     the binding was introduced;
//   - a top-level constant reference: the constant's annotation;
//   - a call's result: the callee function or method's declared result type, so a
//     chain (getLevel().increment()) resolves left to right.
//
// It returns nil when no channel applies or the Env supplies no ReceiverTyper —
// the conservative failure that leaves the call unfolded.
func syntacticDef(ctx evalCtx, recvExpr ast.Expr) *ir.TypeDef {
	switch e := recvExpr.(type) {
	case *ast.SelfExpr:
		return ctx.selfDef
	case *ast.Identifier:
		// A body-local (a let or parameter) shadows a top-level name; its def was
		// recorded from its annotation when the binding was introduced.
		if _, isLocal := ctx.locals[e.Name]; isLocal {
			return ctx.localDefs[e.Name]
		}
		// A top-level constant reference resolves through its own annotation.
		if decl := ctx.env.Resolve(e); decl != nil {
			return annotationDef(ctx.env, decl.Type)
		}
		return nil
	case *ast.MemberExpr:
		// A record field access (p.lv): the field's static type is read from the
		// base receiver's record type, which is itself resolved through the same
		// channels. A nested path (a.b.c) recurses, each step reading the next
		// record's field annotation, never the type query.
		return fieldDef(ctx, e)
	case *ast.CallExpr:
		return callResultDef(ctx, e)
	default:
		return nil
	}
}

// fieldDef resolves a record field access (p.lv) to the field's static type
// definition: it reads the base receiver's record type, finds the named field,
// and returns the def its annotation names. It returns nil when the base is not
// a record, the field is absent, or the field's type names no nominal def — the
// conservative failure that leaves a method call on the field unfolded.
func fieldDef(ctx evalCtx, e *ast.MemberExpr) *ir.TypeDef {
	rec := recordOf(recvType(ctx, e.Receiver))
	if rec == nil {
		return nil
	}
	for _, f := range rec.Fields {
		if f.Name == e.Member.Name {
			return methodTableDef(ctx.env.Registry(), f.Type)
		}
	}
	return nil
}

// recvType resolves a receiver expression to its static type — the full resolved
// type, so a record annotation yields an *ir.Record whose fields a field access
// reads. It is the type companion of syntacticDef, sharing its channels: self's
// owning type, a local's or constant's annotation, a field of a record, or a
// call's result. It returns nil when no channel applies. The env's universe
// resolution (TypeExprType) is a pure lookup, never the type query.
func recvType(ctx evalCtx, recvExpr ast.Expr) ir.Type {
	switch e := recvExpr.(type) {
	case *ast.SelfExpr:
		return namedOf(ctx.selfDef)
	case *ast.Identifier:
		if _, isLocal := ctx.locals[e.Name]; isLocal {
			return namedOf(ctx.localDefs[e.Name])
		}
		if decl := ctx.env.Resolve(e); decl != nil {
			return annotationType(ctx.env, decl.Type)
		}
		return nil
	case *ast.MemberExpr:
		rec := recordOf(recvType(ctx, e.Receiver))
		if rec == nil {
			return nil
		}
		for _, f := range rec.Fields {
			if f.Name == e.Member.Name {
				return f.Type
			}
		}
		return nil
	case *ast.CallExpr:
		return namedOf(callResultDef(ctx, e))
	default:
		return nil
	}
}

// recordOf unwraps a static type to the record it ultimately is: a record type
// directly, or a nominal type (or applied generic) whose definition's body is a
// record. It returns nil for any non-record type. A seen-free single-step unwrap
// suffices — a record annotation is at most one Named deep here (a field's type
// is its resolved annotation, and a nominal record def's body is the record).
func recordOf(t ir.Type) *ir.Record {
	switch t := t.(type) {
	case *ir.Record:
		return t
	case *ir.Named:
		if t.Def != nil {
			if r, ok := t.Def.Body.(*ir.Record); ok {
				return r
			}
		}
	case *ir.App:
		if t.Def != nil {
			if r, ok := t.Def.Body.(*ir.Record); ok {
				return r
			}
		}
	}
	return nil
}

// namedOf wraps a definition as its nominal type, or nil for a nil def — the
// bridge from the def channels (which return *ir.TypeDef) to the type recvType
// threads.
func namedOf(def *ir.TypeDef) ir.Type {
	if def == nil {
		return nil
	}
	return &ir.Named{Def: def}
}

// annotationType resolves a written annotation to its full type through the
// Env's ReceiverTyper, or nil when the Env supplies none or the annotation is
// absent. It is annotationDef's type-returning companion, used where a record
// annotation's structure (not just a nominal def) is needed.
func annotationType(env Env, t ast.TypeExpr) ir.Type {
	if t == nil {
		return nil
	}
	rt, ok := env.(ReceiverTyper)
	if !ok {
		return nil
	}
	return rt.TypeExprType(t)
}

// callResultDef resolves the type definition of a call expression's result,
// through the callee's declared result annotation. A conversion (the callee
// names a type) resolves to that type itself; a top-level function call resolves
// to the function's result; a method call resolves to the method's result, the
// method found on the inner receiver's own (recursively resolved) definition. A
// local binding shadows a type or function name, exactly as in the value rules.
// An overload set resolves only when its members agree on a result def, so an
// undecidable overload does not fold the chain.
func callResultDef(ctx evalCtx, e *ast.CallExpr) *ir.TypeDef {
	switch callee := e.Callee.(type) {
	case *ast.Identifier:
		if _, isLocal := ctx.locals[callee.Name]; isLocal {
			return nil
		}
		// A conversion's result is the named type itself (Level(5) is a Level).
		if def := ctx.env.LookupType(callee.Name); def != nil {
			return def
		}
		return funcResultDef(ctx.env, ctx.env.ResolveFunc(callee))
	case *ast.MemberExpr:
		if recv, isIdent := callee.Receiver.(*ast.Identifier); isIdent {
			if _, isLocal := ctx.locals[recv.Name]; !isLocal {
				if cands := ctx.env.ResolveFuncMember(callee); len(cands) > 0 {
					return funcResultDef(ctx.env, cands)
				}
			}
		}
		// A method call: find the method on the inner receiver's definition and
		// read its declared result.
		inner := syntacticDef(ctx, callee.Receiver)
		if inner == nil {
			return nil
		}
		return methodResultDef(ctx.env, inner, callee.Member.Name)
	default:
		return nil
	}
}

// funcResultDef resolves a function overload set to the type definition its
// result annotation names, or nil when the set is empty, the overloads disagree
// on the result def, or the result names no nominal type. Requiring agreement
// keeps an undecidable overload from resolving the chain to a wrong def.
func funcResultDef(env Env, cands []*ast.FuncDecl) *ir.TypeDef {
	var def *ir.TypeDef
	for i, fd := range cands {
		d := annotationDef(env, fd.Result)
		if i == 0 {
			def = d
			continue
		}
		if d != def {
			return nil
		}
	}
	return def
}

// methodResultDef resolves the type definition a method's declared result names,
// across the body-bearing overloads of that name on def. A result type self is
// the receiver's own definition (increment(): self returns a Level), so a chain
// of self-returning methods keeps its type. The overloads must agree, or the
// chain does not resolve.
func methodResultDef(env Env, def *ir.TypeDef, name string) *ir.TypeDef {
	var result *ir.TypeDef
	found := false
	for _, m := range def.Methods {
		if m.Name != name || m.Syntax == nil {
			continue
		}
		var d *ir.TypeDef
		if isSelfType(m.Syntax.Result) {
			d = def // a self result keeps the receiver's type
		} else {
			d = annotationDef(env, m.Syntax.Result)
		}
		if !found {
			result, found = d, true
			continue
		}
		if d != result {
			return nil
		}
	}
	return result
}

// isSelfType reports whether a written type annotation is the self receiver type
// — the result spelling (increment(): self) a method uses to return its own type.
func isSelfType(t ast.TypeExpr) bool {
	n, ok := t.(*ast.NamedType)
	return ok && n.Namespace == "" && n.Name == "self" && len(n.Args) == 0
}

// defBacksKind reports whether a type definition's underlying primitive can hold
// a value of the given kind: a Level (= int8) backs an integer, a Locale (=
// string) backs a string. It walks the def's body to the underlying primitive
// and checks its native descriptor against the kind; a def with no underlying
// primitive (a record, a union, an unresolved body) backs no scalar kind. It is
// the guard that keeps a def read from an annotation from applying to a value of
// the wrong kind.
func defBacksKind(reg *builtin.Registry, def *ir.TypeDef, kind ir.ConstKind) bool {
	n := underlyingPrimitive(reg, def, map[*ir.TypeDef]bool{})
	if n == nil {
		return false
	}
	switch kind {
	case ir.ConstInt:
		return n.IsInteger()
	case ir.ConstBool:
		return n.Bool
	case ir.ConstString:
		return n.Str
	case ir.ConstDatetime:
		return n.Datetime
	case ir.ConstDuration:
		return n.Duration
	case ir.ConstError:
		return n.Err
	case ir.ConstNull:
		return n.Null
	default:
		return false
	}
}

// defBacksKind, for a collection: a nominal type whose underlying is a list or a
// map (a Bag = list<int>) backs a ConstCollection. A scalar def is handled by the
// primitive path above; this is the collection arm, so a conversion to such a
// type (Bag([...])) passes the folded collection through, exactly as Level(5)
// passes its integer through — which lets a method, and a for, fold on the value.
func defBacksKindCollection(def *ir.TypeDef) bool {
	return underlyingCollectionDef(def, map[*ir.TypeDef]bool{}) != nil
}

// underlyingCollectionDef returns the list/map definition a nominal type bottoms
// out at — a Bag (= list<int>) yields the list def — by following the chain of
// Named/App bodies. It reports nil for a type with no collection underlying.
func underlyingCollectionDef(def *ir.TypeDef, seen map[*ir.TypeDef]bool) *ir.TypeDef {
	if def == nil || seen[def] {
		return nil
	}
	seen[def] = true
	switch body := def.Body.(type) {
	case *ir.App:
		if body.Def != nil && (body.Def.Name == "list" || body.Def.Name == "map") {
			return body.Def
		}
		return underlyingCollectionDef(body.Def, seen)
	case *ir.Named:
		return underlyingCollectionDef(body.Def, seen)
	default:
		return nil
	}
}

// underlyingPrimitive returns the native descriptor of the primitive a nominal
// type bottoms out at — a Level (= int8) yields the int8 descriptor — by
// following the chain of Named bodies to a Builtin. It reports nil for a type
// with no scalar underlying (a record, a union, an enum, an unresolved body) or
// a cyclic alias chain.
func underlyingPrimitive(reg *builtin.Registry, def *ir.TypeDef, seen map[*ir.TypeDef]bool) *builtin.NativeType {
	if def == nil || seen[def] {
		return nil
	}
	seen[def] = true
	switch body := def.Body.(type) {
	case *ir.Builtin:
		n, _ := reg.Native(body.Name)
		return n
	case *ir.Named:
		return underlyingPrimitive(reg, body.Def, seen)
	default:
		return nil
	}
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
// the foldable ones are append, map (over a list), get (a subscript read), set (a
// subscript write), and the fold primitive. Anything else has no constant value
// here.
//
// A collection carries an explicit mapness (list/map/unknown), settled from its
// entries for a non-empty one and from a syntactic channel for an empty one.
// Each method is classed by whether it depends on that mapness:
//
//   - mapness-independent — fold even for an unknown empty collection, since both
//     a list and a map read the same: len/fold (count/any/all are built on it) and
//     get (a miss either way on an empty collection).
//   - mapness-dependent — does not fold for an unknown empty collection, since a
//     list and a map disagree: set (a list's out-of-range write versus a map's
//     upsert). A settled map's set is the upsert this fold's main case folds.
func collectionMethod(ctx evalCtx, recv *ir.Constant, name string, args []*ir.Constant) *ir.Constant {
	switch name {
	case "len":
		return collectionLen(recv, args)
	case "fold":
		return collectionFold(ctx, recv, args)
	case "append":
		return collectionAppend(recv, args)
	case "map":
		return collectionMap(ctx, recv, args)
	case "get":
		return collectionGet(recv, args)
	case "set":
		return collectionSet(recv, args)
	default:
		return nil
	}
}

// collectionAppend folds list.append(v) to a new list with the value at the end,
// leaving the receiver unchanged (data is immutable). It is the builder the
// list-returning provided methods (map, filter, keys, values) accumulate through,
// so the fold of those methods bottoms out in a real list constant. append is a
// list-only operation — a map has none — so a settled map does not fold; an
// unknown empty receiver does (appending an element makes the result a list), and
// the result is always a list.
func collectionAppend(recv *ir.Constant, args []*ir.Constant) *ir.Constant {
	if len(args) != 1 || recv.IsMap() {
		return nil
	}
	out := make([]ir.ConstEntry, len(recv.Coll), len(recv.Coll)+1)
	copy(out, recv.Coll)
	out = append(out, ir.ConstEntry{Value: args[0]})
	return ir.CollectionConstantOf(out, ir.CollList)
}

// collectionLen folds list.len() and map.len() to the element/entry count. It is
// the intrinsic E-18 supplied for neither list nor map; the count is the same
// for both — the number of entries the folded collection carries.
func collectionLen(recv *ir.Constant, args []*ir.Constant) *ir.Constant {
	if len(args) != 0 {
		return nil
	}
	return ir.IntConstant(big.NewInt(int64(len(recv.Coll))))
}

// collectionFold folds the native fold — the foldable primitive every provided
// method (count, any, all, map, filter, keys, values) is built on. It threads an
// accumulator from init through the step function, visiting every entry in fold
// order: the step sees (acc, key, value), where a map's key is the entry's key
// and a list's is the element index. An unfoldable step application (a non-
// function step, a body that does not fold, or the recursion guard) leaves the
// whole fold unevaluated.
func collectionFold(ctx evalCtx, recv *ir.Constant, args []*ir.Constant) *ir.Constant {
	if len(args) != 2 || args[1].Kind != ir.ConstFunc {
		return nil
	}
	acc := args[0]
	step := args[1]
	for i, entry := range recv.Coll {
		key := entry.Key
		if key == nil {
			key = ir.IntConstant(big.NewInt(int64(i))) // a list's key is the index
		}
		acc = apply(ctx, step, []*ir.Constant{acc, key, entry.Value})
		if acc == nil {
			return nil
		}
	}
	return acc
}

// rangeMethod folds a method on a range constant. range is not natively backed
// in the registry, so its only native method is the foldable primitive fold —
// the same model list/map follow, where the provided methods (count, any, all,
// map, filter, keys, values) reach the body through the foldable impl and bottom
// out in this fold. Anything else has no constant value here.
func rangeMethod(ctx evalCtx, recv *ir.Constant, name string, args []*ir.Constant) *ir.Constant {
	if name == "fold" {
		return rangeFold(ctx, recv, args)
	}
	return nil
}

// rangeFold folds range.fold — the foldable primitive every provided method is
// built on. It threads an accumulator over the half-open sequence start..end-1,
// the step seeing (acc, key, value) where the key is the element's 0-based
// position (a range's key is its index, like a list's) and the value is the
// element. An end at or below start is the empty range, which folds to the
// initial accumulator. The walk is bounded by maxRangeIterations: a range wider
// than the cap does not fold (nil), so a wide range never hangs the folder or
// exhausts memory. An unfoldable step application (a non-function step, a body
// that does not fold, or the recursion guard) also leaves the fold unevaluated.
func rangeFold(ctx evalCtx, recv *ir.Constant, args []*ir.Constant) *ir.Constant {
	if len(args) != 2 || args[1].Kind != ir.ConstFunc {
		return nil
	}
	if recv.Start == nil || recv.End == nil {
		return nil
	}
	// The element count is end - start, clamped at zero. A count past the cap does
	// not fold — checked on the big.Int before any iteration, so a wide range is
	// rejected in O(1) rather than walked.
	count := new(big.Int).Sub(recv.End, recv.Start)
	if count.Sign() <= 0 {
		return args[0] // the empty range folds to the initial accumulator
	}
	if count.Cmp(big.NewInt(maxRangeIterations)) > 0 {
		return nil // wider than the compile-time iteration bound: do not fold
	}
	acc := args[0]
	step := args[1]
	cur := new(big.Int).Set(recv.Start)
	one := big.NewInt(1)
	for i := int64(0); cur.Cmp(recv.End) < 0; i++ {
		key := ir.IntConstant(big.NewInt(i))           // the 0-based position
		value := ir.IntConstant(new(big.Int).Set(cur)) // the element
		acc = apply(ctx, step, []*ir.Constant{acc, key, value})
		if acc == nil {
			return nil
		}
		cur.Add(cur, one)
	}
	return acc
}

// collectionMap folds list.map: it applies the function argument to each element
// and collects the results into a new list. A settled map (keyed entries) reaches
// its provided foldable.map through the def channel instead, so a keyed entry here
// does not fold; an unknown empty receiver folds to the empty list — map over no
// elements is the empty list whichever kind it is. The result is always a list.
func collectionMap(ctx evalCtx, recv *ir.Constant, args []*ir.Constant) *ir.Constant {
	if len(args) != 1 || args[0].Kind != ir.ConstFunc {
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
	return ir.CollectionConstantOf(out, ir.CollList)
}

// collectionGet folds a subscript read coll.get(i). A read can miss — a list
// index out of range, a map key not present — and a miss is a value, an error
// constant, not an unfoldable result: the read folds to that error so a caller
// can branch on it. get is mapness-independent: an empty collection has no element
// whichever kind it is, so the read always misses and folds — an empty map (or a
// non-integer index, which only a map accepts) misses by key, anything else by
// index.
func collectionGet(recv *ir.Constant, args []*ir.Constant) *ir.Constant {
	if len(args) != 1 {
		return nil
	}
	key := args[0]
	if recv.IsMap() {
		for _, entry := range recv.Coll {
			if entry.Key != nil && constEqual(entry.Key, key) {
				return entry.Value
			}
		}
		return ir.ErrorConstant("key not found")
	}
	i, ok := intIndex(key)
	if !ok {
		// A non-integer index reaches here only on an empty unknown collection (a
		// settled list rejects it as a type error, a map took the branch above): with
		// no element to read, the miss is a key-not-found, the same error a map gives.
		if len(recv.Coll) == 0 {
			return ir.ErrorConstant("key not found")
		}
		return nil // a non-integer index on a list is a type error the checker reports
	}
	if i < 0 || i >= int64(len(recv.Coll)) {
		return ir.ErrorConstant("index out of range")
	}
	return recv.Coll[int(i)].Value
}

// collectionSet folds a subscript write coll.set(i, v) to the new collection it
// returns, leaving the receiver unchanged (data is immutable). set is the one
// mapness-dependent write: a map's set is an upsert (an existing key's value is
// replaced, a new key appended — it always succeeds, so an empty map's set folds
// to the single-entry map, the main case this whole change enables), while a
// list's replaces the element at an in-range index (an out-of-range index does not
// fold, the compile-time write past the end the semantic layer reports as
// index_out_of_range). An unknown empty collection — whose mapness no channel
// settled — does not fold (nil) rather than guess between the two; the result
// keeps the receiver's mapness.
func collectionSet(recv *ir.Constant, args []*ir.Constant) *ir.Constant {
	if len(args) != 2 {
		return nil
	}
	value := args[1]
	if recv.IsMap() {
		key := args[0]
		out := make([]ir.ConstEntry, len(recv.Coll))
		replaced := false
		for i, entry := range recv.Coll {
			if entry.Key != nil && constEqual(entry.Key, key) {
				out[i] = ir.ConstEntry{Key: entry.Key, Value: value}
				replaced = true
				continue
			}
			out[i] = entry
		}
		if !replaced {
			out = append(out, ir.ConstEntry{Key: key, Value: value})
		}
		return ir.CollectionConstantOf(out, ir.CollMap)
	}
	if !recv.IsList() {
		return nil // an unknown empty collection: do not guess list-versus-map
	}
	i, ok := intIndex(args[0])
	if !ok {
		return nil
	}
	if i < 0 || i >= int64(len(recv.Coll)) {
		return nil // out of range: a compile-time error, reported as index_out_of_range
	}
	out := make([]ir.ConstEntry, len(recv.Coll))
	copy(out, recv.Coll)
	out[int(i)] = ir.ConstEntry{Value: value}
	return ir.CollectionConstantOf(out, ir.CollList)
}

// intIndex reads an integer constant as a list index, reporting whether it is an
// integer that fits an int64. A negative or oversized index is out of range,
// which the caller turns into a miss (for a read) or an unfoldable write — both
// compared against the collection's length as an int64.
func intIndex(c *ir.Constant) (int64, bool) {
	if c == nil || c.Kind != ir.ConstInt || !c.Int.IsInt64() {
		return 0, false
	}
	return c.Int.Int64(), true
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
// is false falls through to the next statement. A let introduces a mutable
// block-local and an assignment updates one, both folding in the body's local
// environment; a bare expression statement has no value and is skipped. It is
// the compile-time execution model the const folder shares with a function
// application, kept in step with the runtime's first-match dispatch.
//
// The local environment (ctx.locals) is mutated in place, so a nested block's
// assignment reaches an outer local. Block scoping is restored on return: a
// shadowing let in this block is undone (blockScope), so its binding does not
// leak to the caller — while an assignment to an outer local persists.
func evalBody(body []ast.Stmt, ctx evalCtx) *ir.Constant {
	scope := newBlockScope(ctx.locals, ctx.localDefs)
	ctx.localDefs = scope.localDefs // share the scope's def map (it may allocate one)
	defer scope.restore()
	for _, stmt := range body {
		switch stmt := stmt.(type) {
		case *ast.ReturnStmt:
			if stmt.Value == nil {
				return nil
			}
			return evalReturn(stmt.Value, ctx)
		case *ast.LetStmt:
			if !evalLet(stmt, ctx, scope) {
				return nil // an unfoldable initializer: the body cannot fold past it
			}
		case *ast.AssignStmt:
			if !evalAssign(stmt, ctx) {
				return nil // an unfoldable value (or invalid target): cannot fold on
			}
		case *ast.SwitchStmt:
			v, out := evalSwitch(stmt, ctx)
			if out == switchFellThrough {
				continue // the selected arm ran without returning; carry on
			}
			// switchReturned yields the arm's value; switchUnknown (an
			// unfoldable scrutinee/pattern, or no arm matched) leaves v nil,
			// which stops folding here.
			return v
		case *ast.MatchStmt:
			v, out := evalMatch(stmt, ctx)
			if out == matchFellThrough {
				continue // the selected arm ran without returning; carry on
			}
			// matchReturned yields the arm's value; matchUnknown (an unfoldable
			// scrutinee, an undecidable arm, or no arm matched) leaves v nil,
			// which stops folding here.
			return v
		case *ast.IfStmt:
			v, out := evalIf(stmt, ctx)
			if out == ifFellThrough {
				continue // the taken branch (or no branch) ran without returning
			}
			// ifReturned yields the branch's value; ifUnknown (an unfoldable
			// condition or branch) leaves v nil, which stops folding here.
			return v
		case *ast.ForStmt:
			v, out := evalFor(stmt, ctx)
			if out == ifFellThrough {
				continue // the loop ran every element without returning; carry on
			}
			// ifReturned yields an early return's value; ifUnknown (an unfoldable
			// iter or body) leaves v nil, which stops folding here.
			return v
		case *ast.ExprStmt:
			// A bare expression yields no binding and cannot return, so folding
			// the body steps over it. Listed so a new statement kind hits the
			// default rather than being silently skipped here too.
			continue
		default:
			panic(ast.UnhandledStmt(stmt))
		}
	}
	return nil
}

// blockScope records the let bindings a block introduces so they can be undone
// when the block ends, restoring any outer binding they shadowed. Assignments to
// an outer local are not recorded — they mutate the shared environment and
// persist past the block, exactly as at runtime.
type blockScope struct {
	locals    map[string]*ir.Constant
	localDefs map[string]*ir.TypeDef
	shadows   map[string]*ir.Constant // the prior value of each name a let shadows
	defShadow map[string]*ir.TypeDef  // the prior static def of each shadowed name
	added     map[string]bool         // names this block's lets introduced fresh
}

// newBlockScope begins tracking a block's let bindings over the body's locals
// and their static defs. A nil locals (a body with no environment) still yields
// a usable scope whose restore is a no-op, since no let can run without an
// environment to bind into. When locals is non-nil the def map is allocated
// here if absent — so it is shared with the body ctx in place, the way the
// locals map is, and a let's def written by bind is visible to the statements
// that follow.
func newBlockScope(locals map[string]*ir.Constant, localDefs map[string]*ir.TypeDef) *blockScope {
	if locals != nil && localDefs == nil {
		localDefs = map[string]*ir.TypeDef{}
	}
	return &blockScope{locals: locals, localDefs: localDefs}
}

// bind records a let of name and writes its value (and, when its annotation
// names a nominal type, its static def) into the environment, saving what it
// shadows so restore can put it back. The environment must be non-nil (a
// function or method body always has one); a nil one means a let appeared where
// it cannot bind, which bind reports by returning false.
//
// Only the first let of a name in this block records what it shadows: a later
// rebind (two lets of the same name, illegal but tolerated) overwrites the
// value, and restore still returns the binding the block inherited.
func (s *blockScope) bind(name string, v *ir.Constant, def *ir.TypeDef) bool {
	if s.locals == nil {
		return false
	}
	if !s.recorded(name) {
		if prior, ok := s.locals[name]; ok {
			if s.shadows == nil {
				s.shadows = map[string]*ir.Constant{}
			}
			s.shadows[name] = prior
			if s.defShadow == nil {
				s.defShadow = map[string]*ir.TypeDef{}
			}
			s.defShadow[name] = s.localDefs[name] // nil when the shadowed name had no def
		} else {
			if s.added == nil {
				s.added = map[string]bool{}
			}
			s.added[name] = true
		}
	}
	s.locals[name] = v
	s.setDef(name, def)
	return true
}

// setDef records a let's static def into the local-def map, allocating it on
// first use. A nil def clears any inherited def for the name, so a binding whose
// annotation names no nominal type does not read an outer name's def.
func (s *blockScope) setDef(name string, def *ir.TypeDef) {
	if def == nil {
		delete(s.localDefs, name)
		return
	}
	if s.localDefs == nil {
		s.localDefs = map[string]*ir.TypeDef{}
	}
	s.localDefs[name] = def
}

// recorded reports whether this block already saved what a let of name shadows
// (or noted it as freshly added), so a rebind does not overwrite that record.
func (s *blockScope) recorded(name string) bool {
	if _, ok := s.shadows[name]; ok {
		return true
	}
	return s.added[name]
}

// restore undoes this block's let bindings: a shadowed outer binding (value and
// static def) is put back, and a freshly added one is removed, leaving the
// environment as the caller had it (save for assignments to outer locals, which
// persist).
func (s *blockScope) restore() {
	for name, prior := range s.shadows {
		s.locals[name] = prior
		if def := s.defShadow[name]; def != nil {
			s.localDefs[name] = def
		} else {
			delete(s.localDefs, name)
		}
	}
	for name := range s.added {
		delete(s.locals, name)
		delete(s.localDefs, name)
	}
}

// evalLet folds a let's initializer and binds the local, recording its static
// def when the let's annotation names a nominal type (so a method call on the
// let folds). It returns false when the initializer cannot be folded (so the
// body cannot fold past the let) or when there is no environment to bind into.
// evalReturn folds a return's value, threading the body's result-type collection
// channel to the immediate expression so a `return []` in a map<K,V>-returning
// routine folds to an empty map. The channel is carried on ctx.resultColl (set
// when the body began) and handed to the expression's expectedColl, which
// evalExpr consumes for an empty literal exactly as the const/let channels are.
func evalReturn(value ast.Expr, ctx evalCtx) *ir.Constant {
	ctx.expectedColl = ctx.resultColl
	return evalExpr(value, ctx)
}

func evalLet(s *ast.LetStmt, ctx evalCtx, scope *blockScope) bool {
	if s.Value == nil {
		return false
	}
	// A let annotation is the collection-mapness channel for the initializer, so
	// let m: map<K,V> = [] folds to an empty map exactly as the const form does,
	// and the expected-enum channel, so let r: Rarity = Legend folds the bare
	// member — the body twin of a const initializer's rule. Both are read from the
	// annotation (annotationType, never the type query). Set on a copy of ctx:
	// evalExpr consumes (and clears) them for the immediate value, and ctx's other
	// fields are unaffected for the bind below.
	letCtx := ctx
	annType := annotationType(ctx.env, s.Type)
	letCtx.expectedColl = CollKindOf(annType)
	letCtx.expected = expectedEnum(annType)
	v := evalExpr(s.Value, letCtx)
	if v == nil {
		return false
	}
	return scope.bind(s.Name, v, annotationDef(ctx.env, s.Type))
}

// evalAssign folds an assignment's value and updates the target local in place,
// so a later read (and an outer block) sees the new value. It returns false when
// the target is not a plain local name (an immutable-data error the checker
// already reported), the local is not in scope, or the value cannot be folded.
func evalAssign(s *ast.AssignStmt, ctx evalCtx) bool {
	id, ok := s.Target.(*ast.Identifier)
	if !ok || ctx.locals == nil {
		return false
	}
	if _, inScope := ctx.locals[id.Name]; !inScope {
		return false
	}
	if s.Value == nil {
		return false
	}
	// A bare member on the right folds through the target local's static enum (r =
	// Common, where r is a Rarity let), read syntactically from the local's
	// annotation (recvType, never the type query) — the assignment twin of the
	// let-initializer rule. The expectation reaches only the immediate value.
	assignCtx := ctx
	assignCtx.expected = expectedEnum(recvType(ctx, s.Target))
	v := evalExpr(s.Value, assignCtx)
	if v == nil {
		return false
	}
	ctx.locals[id.Name] = v
	return true
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

// evalFor folds a for statement: it folds the iterated expression and runs the
// body once per element in fold order, binding the loop variable to each element
// (the value for an of-loop, the key — a map's entry key, a list's or a range's
// index — for an in-loop) as a fresh per-iteration local. It iterates a folded
// collection or a folded range; the walk is bounded by the element count (a
// range's is capped by maxRangeIterations), so it always terminates — the same
// finite walks collectionFold and rangeFold make. An iteration whose body
// returns ends the for with that value (ifReturned); a body that runs to its end
// falls through to the next element, and once every element is visited the for
// falls through to the statement after it (ifFellThrough). An unfoldable iter, an
// unfoldable body, or a value of no iterable kind leaves the for undecided
// (ifUnknown), which stops the enclosing fold.
//
// The loop variable is block-scoped to each iteration (bound and undone per
// element), so it does not leak past the loop, while an assignment the body makes
// to an outer let local persists across iterations — which is what lets a for
// accumulate into a let, exactly as it does at runtime.
func evalFor(s *ast.ForStmt, ctx evalCtx) (*ir.Constant, ifOutcome) {
	if s.Iter == nil {
		return nil, ifUnknown
	}
	iter := evalExpr(s.Iter, ctx)
	if iter == nil {
		return nil, ifUnknown // an unfoldable iter: cannot iterate
	}
	of := s.Kind == ast.ForOf
	switch iter.Kind {
	case ir.ConstCollection:
		return evalForCollection(s, iter, of, ctx)
	case ir.ConstRange:
		return evalForRange(s, iter, of, ctx)
	default:
		return nil, ifUnknown // a value of no iterable kind
	}
}

// evalForCollection runs a for over a folded list/map: each entry binds the loop
// variable (the value for of, the key — a list's index, a map's entry key — for
// in) and runs the body. It is the collection arm of evalFor; see it for the
// outcome semantics.
func evalForCollection(s *ast.ForStmt, coll *ir.Constant, of bool, ctx evalCtx) (*ir.Constant, ifOutcome) {
	for i, entry := range coll.Coll {
		// The loop variable: the value for of, the key for in. A list entry has no
		// key, so its key is the element index — the same rule collectionFold uses.
		elem := entry.Value
		if !of {
			elem = entry.Key
			if elem == nil {
				elem = ir.IntConstant(big.NewInt(int64(i)))
			}
		}
		v, out := iterationOutcome(s, elem, ctx)
		switch out {
		case ifFellThrough:
			continue // the body ran without returning; on to the next element
		case ifReturned:
			return v, ifReturned // an early return ends the whole loop
		default:
			return nil, ifUnknown // an unfoldable body stops the fold
		}
	}
	return nil, ifFellThrough // every element visited without returning
}

// evalForRange runs a for over a folded range: each element of the half-open
// sequence start..end-1 binds the loop variable (the element for of, its 0-based
// position for in — the same key rangeFold threads) and runs the body. The walk
// is bounded by maxRangeIterations: a range wider than the cap leaves the for
// undecided (ifUnknown) rather than iterating, so a wide range never hangs the
// folder — the same verdict rangeFold gives. An empty range (end at or below
// start) falls through without running the body. The outcome semantics match the
// collection arm.
func evalForRange(s *ast.ForStmt, rng *ir.Constant, of bool, ctx evalCtx) (*ir.Constant, ifOutcome) {
	if rng.Start == nil || rng.End == nil {
		return nil, ifUnknown
	}
	count := new(big.Int).Sub(rng.End, rng.Start)
	if count.Sign() <= 0 {
		return nil, ifFellThrough // the empty range: the body never runs
	}
	if count.Cmp(big.NewInt(maxRangeIterations)) > 0 {
		return nil, ifUnknown // wider than the compile-time iteration bound
	}
	cur := new(big.Int).Set(rng.Start)
	one := big.NewInt(1)
	for i := int64(0); cur.Cmp(rng.End) < 0; i++ {
		// The loop variable: the element for of, its 0-based position for in — the
		// same key rangeFold threads.
		elem := ir.IntConstant(new(big.Int).Set(cur))
		if !of {
			elem = ir.IntConstant(big.NewInt(i))
		}
		v, out := iterationOutcome(s, elem, ctx)
		switch out {
		case ifFellThrough:
			cur.Add(cur, one)
			continue // the body ran without returning; on to the next element
		case ifReturned:
			return v, ifReturned // an early return ends the whole loop
		default:
			return nil, ifUnknown // an unfoldable body stops the fold
		}
	}
	return nil, ifFellThrough // every element visited without returning
}

// iterationOutcome runs one for-iteration: it binds the loop variable for this
// element in a fresh block scope and runs the body through branchOutcome, the
// shared body executor. The binding is block-scoped to the iteration (restored on
// return), so it does not leak to the next element or past the loop, while an
// assignment the body makes to an outer local persists through ctx.locals. A
// loop with no variable name (recovered away) or no environment to bind into
// runs the body unbound.
func iterationOutcome(s *ast.ForStmt, elem *ir.Constant, ctx evalCtx) (*ir.Constant, ifOutcome) {
	scope := newBlockScope(ctx.locals, ctx.localDefs)
	ctx.localDefs = scope.localDefs
	defer scope.restore()
	if s.Var != "" && elem != nil {
		scope.bind(s.Var, elem, nil)
	}
	return branchOutcome(s.Body, ctx)
}

// branchOutcome runs a taken branch body and classifies how it ended: a return
// of a folded value (ifReturned), a fall-through to after the if (ifFellThrough
// when no statement returned), or an unfoldable return (ifUnknown). It mirrors
// evalBody but distinguishes "ran to the end without returning" from "could not
// fold", which the if needs to decide whether to continue the outer body. A let
// in the branch is block-scoped to it (and undone on exit); an assignment to an
// outer local persists, so a guarded reassignment is visible after the if.
func branchOutcome(body []ast.Stmt, ctx evalCtx) (*ir.Constant, ifOutcome) {
	scope := newBlockScope(ctx.locals, ctx.localDefs)
	ctx.localDefs = scope.localDefs // share the scope's def map (it may allocate one)
	defer scope.restore()
	for _, stmt := range body {
		switch stmt := stmt.(type) {
		case *ast.ReturnStmt:
			if stmt.Value == nil {
				return nil, ifUnknown
			}
			if v := evalReturn(stmt.Value, ctx); v != nil {
				return v, ifReturned
			}
			return nil, ifUnknown
		case *ast.LetStmt:
			if !evalLet(stmt, ctx, scope) {
				return nil, ifUnknown
			}
		case *ast.AssignStmt:
			if !evalAssign(stmt, ctx) {
				return nil, ifUnknown
			}
		case *ast.SwitchStmt:
			v, sout := evalSwitch(stmt, ctx)
			switch sout {
			case switchFellThrough:
				continue
			case switchReturned:
				return v, ifReturned
			default:
				return nil, ifUnknown
			}
		case *ast.MatchStmt:
			v, mout := evalMatch(stmt, ctx)
			switch mout {
			case matchFellThrough:
				continue
			case matchReturned:
				return v, ifReturned
			default:
				return nil, ifUnknown
			}
		case *ast.IfStmt:
			v, out := evalIf(stmt, ctx)
			if out == ifFellThrough {
				continue
			}
			if out == ifReturned {
				return v, ifReturned
			}
			return nil, ifUnknown
		case *ast.ForStmt:
			v, out := evalFor(stmt, ctx)
			if out == ifFellThrough {
				continue
			}
			if out == ifReturned {
				return v, ifReturned
			}
			return nil, ifUnknown
		case *ast.ExprStmt:
			// As in evalBody: a bare expression neither binds nor returns, so the
			// branch steps over it. Listed so a new kind hits the default.
			continue
		default:
			panic(ast.UnhandledStmt(stmt))
		}
	}
	return nil, ifFellThrough // the branch ran to its end without returning
}

// switchOutcome mirrors ifOutcome for a switch: the same three cases the body
// walk threads to decide whether to continue past the statement or stop.
type switchOutcome int

const (
	switchUnknown     switchOutcome = iota // scrutinee/pattern unfoldable, or no arm matched
	switchReturned                         // the selected arm returned a value
	switchFellThrough                      // the selected arm ran to its end without returning
)

// evalSwitch selects and runs the matching arm of a switch: it folds the
// scrutinee, compares it for equality against each arm's folded value patterns
// in order, and runs the first matching arm's body — the wildcard arm last. It
// classifies how the selected arm ended (switchReturned / switchFellThrough)
// like an if, so a fall-through arm continues the outer body carrying any
// assignment it made to an outer local. It is switchUnknown when the scrutinee
// or a needed pattern cannot be folded, or when no arm (and no wildcard)
// matches, so a switch only folds when its dispatch is fully determined.
func evalSwitch(sw *ast.SwitchStmt, ctx evalCtx) (*ir.Constant, switchOutcome) {
	scrut := evalExpr(sw.Scrutinee, ctx)
	if scrut == nil {
		return nil, switchUnknown
	}
	for _, arm := range sw.Arms {
		for _, v := range arm.Values {
			cv := evalExpr(v, expectingScrutinee(ctx, scrut))
			if cv == nil {
				return nil, switchUnknown // an unfoldable pattern: undetermined
			}
			if constEqual(scrut, cv) {
				return switchArmOutcome(branchOutcome(arm.Body, ctx))
			}
		}
	}
	if sw.Else != nil {
		return switchArmOutcome(branchOutcome(sw.Else, ctx))
	}
	return nil, switchUnknown
}

// switchArmOutcome translates an arm body's ifOutcome — branchOutcome is the
// shared block runner — into the switch's own outcome: a return yields its
// value, a fall-through continues the outer body, and an unfoldable branch is
// undetermined.
func switchArmOutcome(v *ir.Constant, out ifOutcome) (*ir.Constant, switchOutcome) {
	switch out {
	case ifReturned:
		return v, switchReturned
	case ifFellThrough:
		return nil, switchFellThrough
	default:
		return nil, switchUnknown
	}
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

// matchOutcome mirrors switchOutcome for a match: the same three cases the body
// walk threads to decide whether to continue past the statement or stop.
type matchOutcome int

const (
	matchUnknown     matchOutcome = iota // scrutinee unfoldable, arm undecidable, or no arm matched
	matchReturned                        // the selected arm returned a value
	matchFellThrough                     // the selected arm ran to its end without returning
)

// evalMatch selects and runs the matching arm of a match: it folds the
// scrutinee, finds the arm whose member type the scrutinee's value is, and runs
// that arm's body with the arm's binding bound to the scrutinee value (the
// narrowing). It classifies how the selected arm ended (matchReturned /
// matchFellThrough) like a switch.
//
// Soundness over completeness: the dispatch folds only when exactly one arm can
// hold the value. A union value carries no member tag, so when two arms could
// back the value's kind — two nominal-over-int members (Small | Big), or two
// int-family builtins (int8 | int16) over a folded integer — the fold cannot tell
// which arm the runtime would run, and leaves the result matchUnknown rather than
// guessing. A scrutinee that does not fold, or an arm whose match cannot be
// decided syntactically (a nominal record value, which carries no member tag), is
// undetermined the same way — the discipline the switch and index folders use.
// The arm types are read through the Env's ReceiverTyper (a universe lookup), so
// the value query stays independent of the type query.
func evalMatch(m *ast.MatchStmt, ctx evalCtx) (*ir.Constant, matchOutcome) {
	scrut := evalExpr(m.Scrutinee, ctx)
	if scrut == nil {
		return nil, matchUnknown
	}
	// Scan every arm first: a fold is sound only when exactly one arm can hold the
	// value (a union value has no tag to break a tie). An undecidable arm (a
	// record member, an unresolvable type) makes the whole dispatch undetermined,
	// since it might be the runtime's chosen arm.
	selected := -1
	for i, arm := range m.Arms {
		matched, certain := constMatchesArm(ctx, scrut, arm.Type)
		if !certain {
			return nil, matchUnknown
		}
		if matched {
			if selected != -1 {
				return nil, matchUnknown // two arms back this value's kind: ambiguous
			}
			selected = i
		}
	}
	if selected != -1 {
		arm := m.Arms[selected]
		return matchArmOutcome(branchOutcome(arm.Body, narrowMatchBinding(ctx, arm.Bind, scrut)))
	}
	if m.Else != nil {
		return matchArmOutcome(branchOutcome(m.Else, ctx))
	}
	return nil, matchUnknown
}

// matchArmOutcome translates an arm body's ifOutcome — branchOutcome is the
// shared block runner — into the match's own outcome, exactly as
// switchArmOutcome does for a switch.
func matchArmOutcome(v *ir.Constant, out ifOutcome) (*ir.Constant, matchOutcome) {
	switch out {
	case ifReturned:
		return v, matchReturned
	case ifFellThrough:
		return nil, matchFellThrough
	default:
		return nil, matchUnknown
	}
}

// narrowMatchBinding binds a match arm's binding name to the scrutinee value for
// the arm body, so a reference to it folds. The narrowed value is the scrutinee
// itself (narrowing changes the static type, not the runtime value). A nameless
// arm binds nothing. The locals map is copied so the binding reaches only this
// arm body, not a sibling arm or the enclosing block.
func narrowMatchBinding(ctx evalCtx, name string, scrut *ir.Constant) evalCtx {
	if name == "" {
		return ctx
	}
	locals := make(map[string]*ir.Constant, len(ctx.locals)+1)
	for k, v := range ctx.locals {
		locals[k] = v
	}
	locals[name] = scrut
	ctx.locals = locals
	return ctx
}

// constMatchesArm reports whether a folded scrutinee value is of a match arm's
// member type, and whether that could be decided at all. The arm type is
// resolved through the Env's ReceiverTyper (a universe lookup, never the type
// query); the value's kind is then tested against it:
//
//   - an enum value matches the arm's enum definition by identity;
//   - a scalar value (int/bool/string/datetime/duration/error) matches a builtin
//     or nominal-over-primitive arm type whose underlying primitive backs that
//     kind; and
//   - a nominal record value carries no member tag, so a record arm type is
//     undecidable — the second result is false and the match does not fold.
//
// A nil or unresolvable arm type is undecidable too. Returning (false, false)
// keeps the fold from ever choosing the wrong arm.
func constMatchesArm(ctx evalCtx, scrut *ir.Constant, armType ast.TypeExpr) (matched, certain bool) {
	t := annotationType(ctx.env, armType)
	if t == nil {
		return false, false // no type channel, or an unresolvable arm type
	}
	switch t := t.(type) {
	case *ir.Builtin:
		return scalarMatchesBuiltin(ctx.env.Registry(), scrut, t.Name), true
	case *ir.Named:
		if t.Def == nil {
			return false, false
		}
		if t.Def.Enum != nil {
			return scrut.Kind == ir.ConstEnum && scrut.EnumDef == t.Def, true
		}
		// A nominal type over a primitive (a refinement type) matches by the
		// underlying kind; a nominal record carries no tag, so it is undecidable.
		if underlyingPrimitive(ctx.env.Registry(), t.Def, map[*ir.TypeDef]bool{}) != nil {
			return defBacksKind(ctx.env.Registry(), t.Def, scrut.Kind), true
		}
		return false, false
	default:
		// A record, function, union, or collection arm type carries no value tag
		// the fold can test (a record value's nominal identity is unknown), so the
		// dispatch is left undetermined.
		return false, false
	}
}

// scalarMatchesBuiltin reports whether a folded value is of the builtin type
// named name — the scalar kinds keyed on the registry's native classification,
// so a new primitive added to the registry is matched without a hardcoded list.
func scalarMatchesBuiltin(reg *builtin.Registry, scrut *ir.Constant, name string) bool {
	n, ok := reg.Native(name)
	if !ok {
		return false
	}
	switch scrut.Kind {
	case ir.ConstInt:
		return n.IsInteger()
	case ir.ConstBool:
		return n.Bool
	case ir.ConstString:
		return n.Str
	case ir.ConstDatetime:
		return n.Datetime
	case ir.ConstDuration:
		return n.Duration
	case ir.ConstError:
		return n.Err
	case ir.ConstNull:
		return n.Null
	default:
		return false
	}
}

// constEqual reports whether two folded constants are structurally equal — the
// equality a switch dispatches on and a map keys by. It is ir.ConstantsEqual,
// the single shared definition the semantic engine's early cutoff also uses;
// see its doc for the per-kind rules. A nil constant never reaches the map-key
// and switch-scrutinee comparisons that call this, but ConstantsEqual handles
// it (two nil constants are equal) so the contract is one consistent equality.
func constEqual(a, b *ir.Constant) bool {
	return ir.ConstantsEqual(a, b)
}
