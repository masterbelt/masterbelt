// This file is the method-dispatch half of evaluation: it folds a method call to
// its value. A user-defined method with a body folds first (its body evaluated
// with self bound, the way applyBody folds a function's), and a primitive
// receiver otherwise dispatches to its native intrinsic in the builtin registry.
// callable/applyBody/applyFunc carry the shared parameter-binding, overload
// selection, and depth-guard rules, and the receiver-type channels (receiverDef,
// syntacticDef, recvType and friends) resolve a receiver's static definition
// syntactically — never the type query — so eval stays value-blind.
package eval

import (
	"strings"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

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
		val := vals[i]
		// A parameter whose annotation is a union tags its bound value with the
		// member it flows in as, so a match on the parameter inside the body
		// dispatches confidently. The call site tags by the argument's static type
		// (tagArguments); this is the value-only catch-all — it keeps an already
		// tagged value and otherwise settles by unique kind backing.
		if want := annotationType(ctx.env, p.Type); want != nil {
			if tag := unionMemberTagValue(ctx, val, want); tag != nil {
				val = ir.Tagged(val, tag)
			}
		}
		locals[p.Name] = val
		if def := annotationDef(ctx.env, p.Type); def != nil {
			if localDefs == nil {
				localDefs = make(map[string]*ir.TypeDef, len(c.params))
			}
			localDefs[p.Name] = def
		}
	}
	resultType := annotationType(ctx.env, c.result)
	return evalBody(c.body, evalCtx{
		env: ctx.env, locals: locals, self: self,
		selfDef: c.selfDef, localDefs: localDefs, depth: ctx.depth + 1,
		// The declared result type is the body's return channel: its collection
		// mapness settles a `return []` in a map<K,V>-returning routine to an empty
		// map, and its union tags a returned member value. It is resolved once here
		// through the same universe lookup the other channels use.
		resultColl: CollKindOf(resultType),
		resultType: resultType,
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
	// The selected overload's parameter annotations are the argument tagging
	// channel: an argument flowing into a union-typed parameter is tagged with its
	// member, read from the argument's own static type (Big(20) into Small | Big
	// tags Big), in the caller's context. Tagging here, before binding, is what
	// lets a value reach a match inside the body already tagged.
	return applyBody(funcCallable(fd), nil, tagArguments(ctx, fd.Params, args, vals), ctx)
}

// applyStatic folds a static fn call Type.name(args): the arguments fold in the
// caller's context, the static overload whose parameters accept their value kinds
// is selected, and its body folds with no receiver (self unbound). It reports
// (value, true) when def declares a static fn of that name (folded, or left
// unfolded under the depth guard or an undecidable overload), and (nil, false)
// when def has no static fn of that name — so the caller can fall through. The
// selection is the type-blind, conservative one applyFunc uses.
func applyStatic(ctx evalCtx, def *ir.TypeDef, name string, args []ast.Expr) (*ir.Constant, bool) {
	var cands []*ast.MethodDecl
	for _, m := range def.Methods {
		if m.Kind == ir.MethodStatic && m.Name == name && m.Syntax != nil && len(m.Syntax.Body) > 0 {
			cands = append(cands, m.Syntax)
		}
	}
	if len(cands) == 0 {
		return nil, false // not a static fn of this name: let the caller fall through
	}
	if ctx.depth >= maxApplyDepth {
		return nil, true
	}
	vals := make([]*ir.Constant, len(args))
	for i, a := range args {
		if vals[i] = evalExpr(a, ctx); vals[i] == nil {
			return nil, true // an unfoldable argument: the static call does not fold
		}
	}
	var sel *ast.MethodDecl
	n := 0
	for _, cand := range cands {
		// A static fn has no receiver, so a self-typed parameter is undecidable
		// (selfKind -1). A named parameter type is resolved through the env to its
		// underlying kind (envFits), so an overload taking a record (Celsius) is
		// ruled out for an integer argument — the distinction the type-blind fits
		// cannot make without the universe.
		if envFits(ctx.env, cand.Params, vals) {
			sel = cand
			n++
		}
	}
	if n != 1 {
		return nil, true // ambiguous/undecidable: user-defined, but does not fold
	}
	return applyBody(methodCallable(sel, def), nil, tagArguments(ctx, sel.Params, args, vals), ctx), true
}

// envFits is fits with named parameter types resolved through the env to their
// underlying kind, so an overload taking a nominal type (a record-bodied Celsius,
// a wrapped integer) is matched against the argument's kind rather than treated
// as undecidable. It is used for static-fn overload selection, where the env is
// at hand; the type-blind fits remains the shared default for the method and
// function paths. A parameter the env cannot resolve falls back to the spelling
// rule (kindAccepts), keeping the conservative behavior.
func envFits(env Env, params []*ast.ParamDef, vals []*ir.Constant) bool {
	if len(params) != len(vals) {
		return false
	}
	for i, p := range params {
		if !paramAcceptsKind(env, p.Type, vals[i].Kind) {
			return false
		}
	}
	return true
}

// paramAcceptsKind reports whether a parameter type accepts a value of the given
// kind, resolving a named (nominal) type through the env to the kind its
// underlying primitive or composite backs. It falls back to kindAccepts (the
// spelling rule) for a type the env resolves to no def.
func paramAcceptsKind(env Env, t ast.TypeExpr, k ir.ConstKind) bool {
	if def := annotationDef(env, t); def != nil {
		return defBacksKind(env.Registry(), def, k) || defBacksKindCollection(def) && k == ir.ConstCollection
	}
	return kindAccepts(t, k)
}

// tagArguments tags each folded argument with the union member it flows into its
// parameter as — the call-site half of the tagged-union rule. A parameter whose
// resolved annotation is a union and whose argument settles on one member tags
// that value; everything else passes the value through unchanged. The member is
// settled by unionMemberTag in the caller's context, so the argument's static
// type (a conversion, a reference) resolves through the caller's channels. It
// returns a fresh slice when any value is tagged, sharing the originals
// otherwise, so an all-untagged call allocates nothing new.
func tagArguments(ctx evalCtx, params []*ast.ParamDef, argExprs []ast.Expr, vals []*ir.Constant) []*ir.Constant {
	if len(params) != len(vals) || len(argExprs) != len(vals) {
		return vals // arity mismatch (recovered): leave untouched
	}
	out := vals
	copied := false
	for i, p := range params {
		want := annotationType(ctx.env, p.Type)
		if want == nil {
			continue
		}
		if tag := unionMemberTag(ctx, argExprs[i], vals[i], want); tag != nil {
			if !copied {
				out = append([]*ir.Constant(nil), vals...)
				copied = true
			}
			out[i] = ir.Tagged(vals[i], tag)
		}
	}
	return out
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
		case "nint", "sbyte", "short", "int", "long",
			"byte", "ushort", "uint", "ulong":
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

// call evaluates a method call: a collection receiver dispatches to the
// collection intrinsics (collectionMethod), and a primitive receiver dispatches
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
		typeName = "nint"
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

// applyGetter folds a getter read value.name: it resolves the receiver's static
// definition the way a method call does (the same syntactic channels), finds the
// getter named name with a body, and applies it with self bound to the receiver
// and no arguments. It returns (value, true) when a getter of that name is
// declared (folded, or left unevaluated under the depth guard), and (nil, false)
// when the receiver has no such getter — the field reading then stands. Like the
// rest of the folder it depends only on the syntactic channels, never on a type
// query, so it is sound: a receiver whose def cannot be resolved simply does not
// fold.
func applyGetter(ctx evalCtx, recvExpr ast.Expr, recv *ir.Constant, name string) (*ir.Constant, bool) {
	def := receiverDef(ctx, recvExpr, recv)
	if def == nil {
		return nil, false
	}
	sel := getterSyntax(ctx.env.Registry(), def, name)
	if sel == nil {
		return nil, false
	}
	if ctx.depth >= maxApplyDepth {
		return nil, true // the recursion guard fired: a safe, unfoldable result
	}
	return applyBody(methodCallable(sel, def), recv, nil, ctx), true
}

// getterSyntax returns the body-bearing AST declaration of the getter named name
// the definition binds — its own, or one derived from its underlying type — or
// nil when it declares no such getter. A getter takes no overloads, so there is
// at most one. It mirrors methodSyntaxes' shadowing within the getter name space.
func getterSyntax(reg *builtin.Registry, def *ir.TypeDef, name string) *ast.MethodDecl {
	return collectAccessorSyntax(reg, def, name, ir.MethodGetter, map[*ir.TypeDef]bool{})
}

// setterSyntax returns the body-bearing AST declaration of the setter named name
// the definition binds — its own, or one derived from its underlying type — or
// nil when it declares no such setter. It mirrors getterSyntax in the setter name
// space.
func setterSyntax(reg *builtin.Registry, def *ir.TypeDef, name string) *ast.MethodDecl {
	return collectAccessorSyntax(reg, def, name, ir.MethodSetter, map[*ir.TypeDef]bool{})
}

// collectAccessorSyntax is the shared body-bearing-accessor lookup behind
// getterSyntax and setterSyntax: the accessor of the given kind and name a
// definition binds, shadowing within that accessor's name space and deriving from
// the underlying type, exactly as collectMethodSyntaxes does for instance methods.
func collectAccessorSyntax(reg *builtin.Registry, def *ir.TypeDef, name string, kind ir.MethodKind, seen map[*ir.TypeDef]bool) *ast.MethodDecl {
	if def == nil || seen[def] {
		return nil
	}
	seen[def] = true
	declares := false
	for _, m := range def.Methods {
		if m.Name != name || m.Kind != kind {
			continue
		}
		declares = true
		if m.Syntax != nil && len(m.Syntax.Body) > 0 {
			return m.Syntax
		}
	}
	if declares {
		return nil
	}
	if !def.Builtin {
		return collectAccessorSyntax(reg, methodTableDef(reg, def.Body), name, kind, seen)
	}
	return nil
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
		if m.Name != name || m.Kind != ir.MethodNormal {
			continue // an instance call resolves only against instance methods
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
		// A top-level constant reference resolves through its own annotation, or —
		// when unannotated — through its initializer call's result def (const
		// Freezing = Celsius.freezing() is a Celsius), so a getter on it folds. The
		// initializer is resolved syntactically, never through the type query.
		if decl := ctx.env.Resolve(e); decl != nil {
			if def := annotationDef(ctx.env, decl.Type); def != nil {
				return def
			}
			if call, ok := decl.Value.(*ast.CallExpr); ok {
				return callResultDef(ctx, call)
			}
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
				// A static fn call (Celsius.freezing()): its result def lets a let or
				// const initialized by it resolve a later getter/setter on the value.
				if def := ctx.env.LookupType(recv.Name); def != nil {
					if d := staticResultDef(ctx.env, def, callee.Member.Name); d != nil {
						return d
					}
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

// staticResultDef resolves the type definition the static fns named name on def
// return, across their body-bearing overloads. A self result is the owning type
// (a static fn returning self yields its own type). The overloads must agree, or
// the chain does not resolve. It mirrors methodResultDef, filtered to the static
// name space.
func staticResultDef(env Env, def *ir.TypeDef, name string) *ir.TypeDef {
	var result *ir.TypeDef
	found := false
	for _, m := range def.Methods {
		if m.Kind != ir.MethodStatic || m.Name != name || m.Syntax == nil {
			continue
		}
		var d *ir.TypeDef
		if isSelfType(m.Syntax.Result) {
			d = def
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
	// A record-bodied def (a record type, or a nominal type over one) backs a
	// record value: a method or getter on a record receiver reaches its body
	// through the receiver's annotation, the same syntactic channel a primitive
	// uses, so value.name folds on a record exactly as it does on a wrapped int.
	if kind == ir.ConstRecord {
		return recordOf(&ir.Named{Def: def}) != nil
	}
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
