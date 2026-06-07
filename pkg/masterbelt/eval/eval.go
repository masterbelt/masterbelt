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
	return DeclExpecting(decl, nil, env)
}

// DeclExpecting folds a declaration's value against its resolved annotation type
// want, the channel that supplies the immediate value's expectations: the enum a
// bare member resolves through (const Top: Rarity = Legend), the mapness an empty
// collection literal settles to (const m: map<K,V> = []), and the union a value
// is tagged with its member of (const Held: GameValue = Coin{...}). want is
// pre-resolved by the caller through a pure universe lookup — the value query must
// not call the type query — and is nil for an unannotated const. A nil value
// yields nil.
func DeclExpecting(decl *ast.ConstDecl, want ir.Type, env Env) *ir.Constant {
	if decl.Value == nil {
		return nil
	}
	return evalExpr(decl.Value, expectingType(evalCtx{env: env}, want))
}

// expectingType sets the immediate-expression expectation channels a resolved
// annotation type supplies — the enum (bare member), the collection mapness
// (empty literal), and the union (member tag) — on a copy of ctx. It is the one
// place the three channels are derived from an annotation, so every tagging site
// (const/let/param/return/field/argument) seeds them the same way. A nil want
// leaves the channels clear.
func expectingType(ctx evalCtx, want ir.Type) evalCtx {
	ctx.expected = expectedEnum(want)
	ctx.expectedColl = CollKindOf(want)
	ctx.expectedType = want
	return ctx
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

// ExprInExpecting is ExprIn with the annotation-type channel set, so a let's
// declared type reaches its initializer: an empty collection literal (let m:
// map<K,V> = []) folds to the settled empty collection a body-position check
// needs to tell a map upsert from a list write, a bare member (let r: Rarity =
// Legend) resolves, and a member value (let v: GameValue = Coin{...}) is tagged.
// want is nil for an unannotated binding, which then behaves exactly like ExprIn.
func ExprInExpecting(e ast.Expr, locals map[string]*ir.Constant, want ir.Type, env Env) *ir.Constant {
	return evalExpr(e, expectingType(evalCtx{env: env, locals: locals}, want))
}

// ExprExpecting folds an expression against its expected type in scope, so a bare
// member resolves through the type's enum, an empty collection settles its
// mapness, and a member value is tagged with its union member. want is the
// expected type (nil for none). It is how an enum member's initializer (typed
// against the enum's own base) and a const initializer pass their expectation to
// the folder.
func ExprExpecting(e ast.Expr, want ir.Type, env Env) *ir.Constant {
	return evalExpr(e, expectingType(evalCtx{env: env}, want))
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
	// expectedType is the resolved annotation type the immediate expression's
	// value flows into — a const/let/param/result/field/argument union channel. It
	// is the tagging channel: when it is a union (read through types.UnionType, so
	// a nominal or generic alias unwraps) and the value can be assigned to exactly
	// one member, the folded value is tagged with that member, the basis for a
	// confident match dispatch. It is nil outside such a channel. Like the other
	// expectation channels it reaches only the immediate expression; evalExpr
	// consumes and clears it for sub-expressions.
	expectedType ir.Type
	// resultColl is the mapness a return's empty collection settles to — the
	// declared result type of the function or method whose body is being folded,
	// or ir.CollUnknown outside one (or for a non-collection result). It is the
	// result-type channel, threaded through the body so a `return []` reaches it;
	// evalBody hands it to the return expression's expectedColl.
	resultColl ir.CollKind
	// resultType is the declared result type of the function or method whose body
	// is being folded, or nil outside one. It is the return value's tagging
	// channel: a `return v` whose result type is a union tags the returned value
	// with its member, so a tagged value flows back out of the routine. evalReturn
	// hands it to the return expression's expectedType.
	resultType ir.Type
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

// evalExpr folds an expression and tags the result with the union member it
// flowed in as, when the immediate context is a union channel (ctx.expectedType
// a union): a value that settles on exactly one member carries that member as its
// tag, the basis for a confident match dispatch. An untagged result (no union
// channel, or no uniquely selectable member) is the bare fold, unchanged. The
// tagging channel reaches only this expression — like the enum and collection
// channels — so a nested sub-expression sets its own.
func evalExpr(e ast.Expr, ctx evalCtx) *ir.Constant {
	v := evalExprRaw(e, ctx)
	if v == nil || ctx.expectedType == nil {
		return v
	}
	if tag := unionMemberTag(ctx, e, v, ctx.expectedType); tag != nil {
		// The value flows in as this member: an out-of-range integer or a
		// where-predicate violation against the member is not a representable
		// value of it, so it does not fold — the same refusal a scalar conversion
		// makes, keeping a wrong constant out of a union the const-level checks
		// cannot see through (the union's Fits and refinedDef both pass through).
		// The semantic layer reports the diagnostic at the flow site, folding the
		// raw value (no expectation) so the unfolded value here never hides it.
		if !memberAdmits(ctx, tag, v) {
			return nil
		}
		return ir.Tagged(v, tag)
	}
	return v
}

// memberAdmits reports whether the value v is a representable value of the union
// member type it flows in as: an integer within the member's range, and (when the
// member is a refined nominal type) a value its where-predicate folds true for. A
// value that cannot be settled either way (a non-integer kind against a sized
// member, a predicate that does not fold to a bool) is admitted — only a
// definitive violation refuses the fold, the conservative discipline the const
// and conversion range checks share. It is the value half of the member-aware
// soundness check; the semantic layer runs the same selection through MemberFor
// to anchor the diagnostic at the flow site.
func memberAdmits(ctx evalCtx, member ir.Type, v *ir.Constant) bool {
	reg := ctx.env.Registry()
	if v.Kind == ir.ConstInt && !types.Fits(reg, member, v.Int) {
		return false
	}
	if def := refinedMemberDef(member); def != nil {
		p := Predicate(def.Where, v, def, ctx.env)
		if p != nil && p.Kind == ir.ConstBool && !p.Bool {
			return false
		}
	}
	return true
}

// refinedMemberDef returns the definition behind a nominal (or applied) member
// type when it carries a usable where-clause, or nil — the eval twin of the
// semantic layer's refinedDef, kept here so the fold can run a member's predicate
// without depending on the semantic package.
func refinedMemberDef(t ir.Type) *ir.TypeDef {
	var def *ir.TypeDef
	switch t := t.(type) {
	case *ir.Named:
		def = t.Def
	case *ir.App:
		def = t.Def
	}
	if def == nil || def.Where == nil {
		return nil
	}
	return def
}

// MemberFor returns the type a value of expression e flows in as under the
// expected type want: the union member it would be tagged with (the same
// exact→unique selection the fold uses for tagging) when want is a union, or want
// itself when it is not a union or no single member is settled. It is the channel
// the semantic layer's member-aware range and refinement checks resolve their
// effective target through, so the diagnostic checks against exactly the member
// the fold tags — and the value the eval refusal drops. env supplies the
// resolution; the value is folded with no expectation, so the raw value is read.
func MemberFor(e ast.Expr, want ir.Type, env Env) ir.Type {
	if types.UnionType(want) == nil {
		return want
	}
	ctx := evalCtx{env: env}
	v := evalExprRaw(e, ctx)
	if v == nil {
		return want
	}
	if tag := unionMemberTag(ctx, e, v, want); tag != nil {
		return tag
	}
	return want
}

// evalExprRaw folds an expression, resolving an identifier first against the
// context's locals and then against the environment's declarations. The
// expected-enum context reaches only the immediate expression — every recursive
// descent drops it, since a bare member is meaningful only as a const's whole
// value, not nested inside a larger expression.
func evalExprRaw(e ast.Expr, ctx evalCtx) *ir.Constant {
	// The expectation is consumed at this level; sub-expressions evaluate in
	// their own (expectation-free) context. The collection-mapness channel is
	// consumed the same way — an empty literal reads it here, a nested literal
	// does not inherit it. The union tagging channel is consumed the same way.
	sub := ctx
	sub.expected = nil
	sub.expectedColl = ir.CollUnknown
	sub.expectedType = nil
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
	case *ast.RangeExpr:
		return rangeLit(e, sub)
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
		// call on it has a receiver. It is the last value-by-value branch — the
		// type-member and namespace forms above resolve by name, this one by value.
		if recv := evalExpr(e.Receiver, sub); recv != nil {
			if recv.Kind == ir.ConstRecord {
				if v := recordField(recv, e.Member.Name); v != nil {
					return v
				}
			}
			// No field of that name (or a non-record receiver): a getter read
			// value.name folds its body with self bound to the receiver.
			if v, ok := applyGetter(sub, e.Receiver, recv, e.Member.Name); ok {
				return v
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
		// imported function; one whose receiver names a type and member names a
		// static fn applies that — the Type.name(...) path. A local binding shadows
		// both.
		if recv, isIdent := member.Receiver.(*ast.Identifier); isIdent {
			if _, isLocal := ctx.locals[recv.Name]; !isLocal {
				if cands := ctx.env.ResolveFuncMember(member); len(cands) > 0 {
					return applyFunc(cands, e.Arguments, sub)
				}
				if def := ctx.env.LookupType(recv.Name); def != nil {
					if v, ok := applyStatic(sub, def, member.Member.Name, e.Arguments); ok {
						return v
					}
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
		if !ok {
			return nil
		}
		// error("msg") is the one builtin conversion that builds a new value; the
		// scalar primitives (short(20), bool(b)) are the identity on the value when
		// its kind backs the type — the same pass-through a nominal conversion makes
		// (Level(5)), so a sized-integer conversion folds to its integer. This both
		// gives short(20) a value and lets it tag a sized-integer union member.
		v := evalExpr(args[0], ctx)
		if v == nil {
			return nil
		}
		if n.Err {
			if v.Kind != ir.ConstString {
				return nil
			}
			return ir.ErrorConstant(v.Str)
		}
		if !builtinBacksKind(n, v.Kind) {
			return nil
		}
		// An integer outside the target's range does not fold: short(70000) has no
		// representable value, so producing the out-of-range integer would be a
		// wrong constant the match dispatch (and the union overflow check) could not
		// catch. Leaving it unfolded keeps a bad value from ever existing — the
		// type-layer conversion check reports the overflow at the same site.
		if v.Kind == ir.ConstInt && !n.Fits(v.Int) {
			return nil
		}
		return v
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
		// A nominal type over a sized integer (Level = short) range-checks the same
		// way the builtin path does: Level(70000) has no representable value, so it
		// does not fold — keeping a wrong constant out of a union the const-level
		// overflow check cannot see through.
		if v.Kind == ir.ConstInt {
			if n := underlyingPrimitive(reg, def, map[*ir.TypeDef]bool{}); n != nil && !n.Fits(v.Int) {
				return nil
			}
		}
		return v
	}
	if v.Kind == ir.ConstCollection && defBacksKindCollection(def) {
		return v
	}
	return nil
}

// convertRange folds the range constructor range(start, end) and range(start,
// end, step): every bound (and the step) must fold to an integer, and the result
// is a range value over them — the two-argument form's unit-step sequence
// start..end (an end below start being empty), the three-argument form's stepped
// sequence start, start+step, ..., staying on the end side of step. A step that
// folds to zero does not fold to a value (nil): a zero step has no sequence, and
// the semantic layer reports it as the zero-step range diagnostic, so producing a
// value would let a malformed range slip past. The sequence is not materialized
// here; the bounds and step are kept lazily so a wide range is a small value, and
// the fold/for walk over it is bounded separately. A non-two/three argument list
// (a recovered call) or an unfoldable or non-integer bound does not fold.
func convertRange(args []ast.Expr, ctx evalCtx) *ir.Constant {
	if len(args) != 2 && len(args) != 3 {
		return nil
	}
	start := evalExpr(args[0], ctx)
	end := evalExpr(args[1], ctx)
	if start == nil || end == nil || start.Kind != ir.ConstInt || end.Kind != ir.ConstInt {
		return nil
	}
	if len(args) == 2 {
		return ir.RangeConstant(start.Int, end.Int)
	}
	step := evalExpr(args[2], ctx)
	if step == nil || step.Kind != ir.ConstInt || step.Int.Sign() == 0 {
		return nil // an unfoldable, non-integer, or zero step has no range value
	}
	return ir.RangeConstantStep(start.Int, end.Int, step.Int)
}

// rangeLit folds a range literal lo..hi (closed) or lo...hi (half-open) to the
// range value it equals, deciding the direction from the bound values: when both
// bounds fold to integers, the range runs from lo's side toward hi's, so the step
// is +1 when lo <= hi (ascending) and -1 otherwise (descending). The closed form
// is the bounds as written; the half-open form drops the larger end (the max),
// which is hi for an ascending range and lo for a descending one — so 0...9 is
// range(0, 8) and 9...0 is range(8, 0, -1), each the very value the equivalent
// range(...) constructs (an a...a is empty). A bound that does not fold, or folds
// to a non-integer, leaves the literal unevaluated (the type is still range).
func rangeLit(e *ast.RangeExpr, ctx evalCtx) *ir.Constant {
	lo := evalExpr(e.Lower, ctx)
	hi := evalExpr(e.Upper, ctx)
	if lo == nil || hi == nil || lo.Kind != ir.ConstInt || hi.Kind != ir.ConstInt {
		return nil
	}
	one := big.NewInt(1)
	start := new(big.Int).Set(lo.Int)
	end := new(big.Int).Set(hi.Int)
	if lo.Int.Cmp(hi.Int) <= 0 {
		// Ascending (lo <= hi), step +1. The max is the upper end; the half-open
		// form excludes it by pulling the end in by one.
		if e.HalfOpen {
			end.Sub(end, one)
		}
		return ir.RangeConstant(start, end)
	}
	// Descending (lo > hi), step -1. The max is the lower end; the half-open form
	// excludes it by pulling the start in by one.
	if e.HalfOpen {
		start.Sub(start, one)
	}
	return ir.RangeConstantStep(start, end, big.NewInt(-1))
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
// unfoldable field does not fold to a partial value. A named record's declared
// field types are the tagging channel for the field values: a field typed as a
// union tags its value with the member it holds (a { v: GameValue } field's Coin
// value), so a tagged value can live inside a record.
func record(e *ast.RecordLit, ctx evalCtx) *ir.Constant {
	fieldTypes := recordFieldTypes(ctx, e.TypeName)
	fields := make([]ir.ConstField, 0, len(e.Fields))
	for _, f := range e.Fields {
		if f.Name == "" {
			return nil // recovered away; already a parse diagnostic
		}
		fieldCtx := ctx
		fieldCtx.expectedType = fieldTypes[f.Name]
		v := evalExpr(f.Value, fieldCtx)
		if v == nil {
			return nil
		}
		fields = append(fields, ir.ConstField{Name: f.Name, Value: v})
	}
	return ir.RecordConstant(fields)
}

// recordFieldTypes resolves a named record type's field types — the channel a
// record literal's field values are tagged through — by a pure universe lookup,
// never the type query. It returns nil for an inferred record ({...}, no name) or
// a name that resolves to no record, so a field with no declared type is folded
// without a union channel, exactly as before.
func recordFieldTypes(ctx evalCtx, typeName string) map[string]ir.Type {
	if typeName == "" {
		return nil
	}
	rec := recordOf(namedOf(ctx.env.LookupType(typeName)))
	if rec == nil {
		return nil
	}
	out := make(map[string]ir.Type, len(rec.Fields))
	for _, f := range rec.Fields {
		out[f.Name] = f.Type
	}
	return out
}

// unionMemberTag returns the union member a folded value flows in as — the tag
// that lets a later match dispatch it — or nil when the value carries no union
// channel, the channel is not a union, or the member cannot be settled uniquely.
// It is the value half of the tagged-union rule, settling the same member the
// type layer's SelectUnionMember would but from what a value can know (it carries
// no static type):
//
//   - a value that already carries a tag keeps it: it flowed through a union
//     before, and the tag points at the member, so it stays valid as the value
//     moves between a bare union and an alias of it;
//   - a value whose source expression has a syntactic static type (a record
//     literal's TypeName, a conversion's type, a reference's annotation) selects
//     its member by the same exact→unique rule the type layer uses; and
//   - a bare literal (no static type) selects by kind backing: the union member
//     whose underlying primitive backs the value's kind, tagged only when exactly
//     one does — so nint | error tags an integer literal nint and an error error,
//     but short | byte stays untagged (two integer members) and does not fold,
//     exactly the case the type layer reports as ambiguous_union_member.
//
// Tagging only on a unique member keeps the fold from ever choosing a wrong arm:
// an undecidable flow leaves the value untagged, and the match folder then falls
// back to its conservative kind-counting rule.
func unionMemberTag(ctx evalCtx, e ast.Expr, v *ir.Constant, want ir.Type) ir.Type {
	u := types.UnionType(want)
	if u == nil {
		return nil
	}
	// An already-tagged value keeps its member: it flowed through a union earlier,
	// and the member is still meaningful under this (possibly aliased) union.
	if v.UnionTag != nil {
		return v.UnionTag
	}
	// A syntactic static type settles the member by the type layer's exact→unique
	// rule — the record literal, conversion, and reference channels.
	if st := valueStaticType(ctx, e); st != nil {
		if sel, m := types.SelectUnionMember(ctx.env.Registry(), st, want); sel == types.UnionUnique {
			return m
		}
		return nil
	}
	// A bare literal has no static type: select by kind backing, uniquely.
	return uniqueKindMember(ctx, u, v.Kind)
}

// unionMemberTagValue is unionMemberTag without an expression to read a static
// type from — the param-binding site, which sees only the folded value. It keeps
// a value's existing tag (it flowed through a union before) and otherwise selects
// by unique kind backing; it cannot do the exact→static-type selection the
// call-site form does, so an argument needing that is tagged where it is folded
// (tagArguments), and this is the value-only catch-all for the rest.
func unionMemberTagValue(ctx evalCtx, v *ir.Constant, want ir.Type) ir.Type {
	u := types.UnionType(want)
	if u == nil {
		return nil
	}
	if v.UnionTag != nil {
		return v.UnionTag
	}
	return uniqueKindMember(ctx, u, v.Kind)
}

// valueStaticType resolves the syntactic static type of a value expression for
// union tagging — a pure universe lookup, never the type query, the value folder's
// discipline. A record literal's named form gives its nominal type, a conversion
// or reference resolves through recvType's channels (a call result, an
// annotation), and a bare literal (an int, a string) has none, so it returns nil
// and the kind-backing fallback decides. It deliberately does not type a bare
// integer literal as nint here: an exact nint member is matched by the kind
// fallback, and keeping literals out of the static path keeps the exact→unique
// rule from treating an adaptable literal as a fixed width.
func valueStaticType(ctx evalCtx, e ast.Expr) ir.Type {
	if rec, ok := e.(*ast.RecordLit); ok {
		if rec.TypeName == "" {
			return nil
		}
		return defType(ctx.env.LookupType(rec.TypeName))
	}
	switch e.(type) {
	case *ast.Identifier, *ast.MemberExpr, *ast.CallExpr, *ast.SelfExpr:
		// recvType wraps a def as a Named; a builtin member of a union is a Builtin,
		// so normalize a nominal type over a builtin def (short(20)'s result) to the
		// builtin form member selection compares against.
		return normalizeBuiltin(recvType(ctx, e))
	}
	return nil
}

// defType returns the type a definition denotes for member selection — a Builtin
// for a builtin primitive (so it compares against a union's builtin member), a
// Named for any other definition, and nil for a nil def.
func defType(def *ir.TypeDef) ir.Type {
	if def == nil {
		return nil
	}
	if def.Builtin {
		return &ir.Builtin{Name: def.Name}
	}
	return &ir.Named{Def: def}
}

// normalizeBuiltin rewrites a Named over a builtin definition to the Builtin form,
// leaving every other type unchanged — so a conversion result resolved as a
// nominal type (recvType always wraps a def as Named) compares against a union's
// builtin member.
func normalizeBuiltin(t ir.Type) ir.Type {
	if n, ok := t.(*ir.Named); ok && n.Def != nil && n.Def.Builtin {
		return &ir.Builtin{Name: n.Def.Name}
	}
	return t
}

// uniqueKindMember returns the one union member whose underlying primitive backs
// the value kind, or nil when zero or several do. It is the value-side twin of
// the type layer's unique-assignable rule for a bare literal: an integer literal
// into nint | error backs only nint, a null into Coin | null only null. Two
// members of the same kind (short | byte over an integer) leave it nil — the
// value stays untagged and the match does not fold, the ambiguity the type layer
// reports.
func uniqueKindMember(ctx evalCtx, u *ir.Union, kind ir.ConstKind) ir.Type {
	reg := ctx.env.Registry()
	var chosen ir.Type
	n := 0
	for _, m := range u.Members {
		if memberBacksKind(reg, m, kind) {
			chosen = m
			n++
		}
	}
	if n == 1 {
		return chosen
	}
	return nil
}

// memberBacksKind reports whether a union member type can hold a value of the
// given kind: a builtin by its native descriptor, a nominal type by its
// underlying primitive (a Level over an integer). A composite member (a record,
// a union, a function) backs no scalar kind here — a record value reaches its
// member through valueStaticType's nominal channel, not this kind fallback.
func memberBacksKind(reg *builtin.Registry, m ir.Type, kind ir.ConstKind) bool {
	switch m := m.(type) {
	case *ir.Builtin:
		return scalarMatchesBuiltin(reg, &ir.Constant{Kind: kind}, m.Name)
	case *ir.Named:
		return m.Def != nil && defBacksKind(reg, m.Def, kind)
	default:
		return false
	}
}
