// This file is the IR interpreter's call dispatch: a method call folds through
// a user-defined method's body (the IR statements resolved onto the
// definition) or the receiver's native intrinsic; a function call applies its
// bound *ir.Function; a static call its def's static fn. The checker-selected
// overload (Resolved) drives an annotated call; a type-blind one falls back to
// the conservative value-kind selection — the rules the AST folder shares,
// over resolved signatures instead of written annotations.
package eval

import (
	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// graphCallable is callable over the IR: a pure routine's resolved parameters
// and lowered statement body, the shape graphApplyBody folds. A method and a
// function share it, exactly as the AST folder's callable does.
type graphCallable struct {
	params    []ir.Param
	body      []ir.Stmt
	extern    bool
	effectful bool
	selfDef   *ir.TypeDef
	result    ir.Type // the declared result type: the return channel
}

func graphFuncCallable(fn *ir.Function) graphCallable {
	return graphCallable{params: fn.Params, body: fn.Body, extern: fn.Extern, effectful: len(fn.Effects) > 0, result: fn.Result}
}

func graphMethodCallable(m *ir.Method, selfDef *ir.TypeDef) graphCallable {
	return graphCallable{params: m.Params, body: m.Body, extern: m.Extern, effectful: len(m.Effects) > 0, selfDef: selfDef, result: m.Result}
}

// graphApplyBody folds a pure routine's body against already-folded argument
// values, with self bound for a method — the IR twin of applyBody. A
// union-typed parameter tags its bound value with the member it flows in as
// (the value-only catch-all; the call site's explicit Adapt tagged what the
// static types could settle), and the declared result type threads the return
// channels.
func graphApplyBody(c graphCallable, self *ir.Constant, vals []*ir.Constant, ctx graphCtx) *ir.Constant {
	if c.extern || c.effectful {
		return nil
	}
	locals := make(map[string]*ir.Constant, len(c.params))
	for i, p := range c.params {
		val := vals[i]
		if p.Type != nil {
			if tag := graphUnionTagValue(ctx, val, p.Type); tag != nil {
				val = ir.Tagged(val, tag)
			}
		}
		locals[p.Name] = val
	}
	return graphBody(c.body, graphCtx{
		env: ctx.env, locals: locals, self: self,
		selfDef: c.selfDef, depth: ctx.depth + 1, budgetHit: ctx.budgetHit,
		resultColl: CollKindOf(c.result),
		resultType: c.result,
	})
}

// graphUnionTagValue is the value-only reading of graphUnionTag: the member a
// folded value flows into a union-typed position as when there is no source
// node to read a static type from (a parameter binding) — its existing tag,
// or a unique kind backing. It delegates with a nil node, whose static type
// reads as nothing, so the two selections cannot drift.
func graphUnionTagValue(ctx graphCtx, v *ir.Constant, want ir.Type) ir.Type {
	return graphUnionTag(ctx, nil, v, want)
}

// graphApplyValue folds a call of a function value fn(args): the argument
// nodes fold in the caller's context, then the closure applies over its
// captured environment.
func graphApplyValue(ctx graphCtx, fn *ir.Constant, argNodes []ir.Value) *ir.Constant {
	args := make([]*ir.Constant, len(argNodes))
	for i, a := range argNodes {
		if args[i] = graphValue(a, ctx); args[i] == nil {
			return nil
		}
	}
	return graphApply(ctx, fn, args)
}

// graphFuncCall folds a call of a top-level function: the checker-selected
// overload when recorded, else the lowering's certain target (a sole or
// arity-unique candidate — an ambiguous set lowers with no target), whose
// resolved parameters must accept the argument kinds.
func graphFuncCall(v *ir.FuncCall, ctx graphCtx) *ir.Constant {
	fn := v.Resolved
	if fn == nil {
		fn = v.Target
	}
	if fn == nil || len(v.Args) != len(fn.Params) {
		return nil
	}
	if ctx.depth >= maxApplyDepth {
		ctx.noteBudget()
		return nil
	}
	vals := make([]*ir.Constant, len(v.Args))
	for i, a := range v.Args {
		// A union-typed parameter is the argument's tagging channel, exactly
		// as the AST folder tags at the call site (an annotated graph already
		// wrapped the argument in an Adapt, which folds to a tagged value and
		// keeps its tag here).
		argCtx := ctx
		argCtx.expectedType = fn.Params[i].Type
		if vals[i] = graphValue(a, argCtx); vals[i] == nil {
			return nil
		}
	}
	// Without a checker selection the target is the lowering's guess among the
	// declared overloads; the argument kinds must accept it, the conservative
	// rule that keeps a wrong body from ever applying.
	if v.Resolved == nil && !graphFits(ctx.env.Registry(), fn.Params, vals, -1) {
		return nil
	}
	return graphApplyBody(graphFuncCallable(fn), nil, vals, ctx)
}

// graphStaticCall folds a static fn call Type.name(args): the checker's
// selection when recorded, else the one body-bearing static overload whose
// resolved parameters accept the argument kinds.
func graphStaticCall(v *ir.StaticCall, ctx graphCtx) *ir.Constant {
	if v.Def == nil {
		return nil
	}
	var cands []*ir.Method
	for _, m := range v.Def.Methods {
		if m.Kind == ir.MethodStatic && m.Name == v.Name && len(m.Body) > 0 {
			cands = append(cands, m)
		}
	}
	if len(cands) == 0 {
		return nil
	}
	if ctx.depth >= maxApplyDepth {
		ctx.noteBudget()
		return nil
	}
	sel := v.Resolved
	if sel != nil && (len(sel.Body) == 0 || len(sel.Params) != len(v.Args)) {
		sel = nil
	}
	vals := make([]*ir.Constant, len(v.Args))
	for i, a := range v.Args {
		argCtx := ctx
		if sel != nil && i < len(sel.Params) {
			argCtx.expectedType = sel.Params[i].Type
		} else if len(cands) == 1 && i < len(cands[0].Params) {
			argCtx.expectedType = cands[0].Params[i].Type
		}
		if vals[i] = graphValue(a, argCtx); vals[i] == nil {
			return nil
		}
	}
	if sel == nil {
		n := 0
		for _, cand := range cands {
			if graphFits(ctx.env.Registry(), cand.Params, vals, -1) {
				sel = cand
				n++
			}
		}
		if n != 1 {
			return nil
		}
	}
	return graphApplyBody(graphMethodCallable(sel, v.Def), nil, vals, ctx)
}

// graphCall folds a method call node: short-circuiting connectives first, then
// a user-defined method's body (selected by the checker's record, or the
// value-kind rule), then the receiver's native intrinsic — the same resolution
// order the AST folder keeps.
func graphCall(v *ir.Call, ctx graphCtx) *ir.Constant {
	recv := graphValue(v.Receiver, ctx)
	if c, ok := graphShortCircuit(recv, v.Method, v.Args, ctx); ok {
		return c
	}
	if recv == nil {
		return nil
	}
	args := make([]*ir.Constant, len(v.Args))
	for i, a := range v.Args {
		argCtx := ctx
		if v.Resolved != nil && i < len(v.Resolved.Params) {
			// The selected overload's parameter is the argument's union
			// tagging channel, mirroring tagArguments.
			argCtx.expectedType = v.Resolved.Params[i].Type
		}
		if args[i] = graphValue(a, argCtx); args[i] == nil {
			return nil
		}
	}
	return dispatchCall(ctx, v, recv, v.Method, args)
}

// graphShortCircuit folds a boolean connective whose receiver already decides
// the result — false && _, true || _ — without folding the dead operand.
func graphShortCircuit(recv *ir.Constant, name string, args []ir.Value, _ graphCtx) (*ir.Constant, bool) {
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

// dispatchCall resolves and folds a method call against a folded receiver: a
// user-defined body first, then the collection/range/enum value dispatch, then
// the native intrinsic keyed on the receiver's kind.
func dispatchCall(ctx graphCtx, v *ir.Call, recv *ir.Constant, name string, args []*ir.Constant) *ir.Constant {
	if c, ok := graphUserMethod(ctx, v, recv, name, args); ok {
		return c
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
	kinds := make([]ir.ConstKind, len(args))
	for i, a := range args {
		kinds[i] = a.Kind
	}
	typeName, ok := intrinsicTypeName(recv.Kind)
	if !ok {
		return nil
	}
	fn, ok := ctx.env.Registry().Intrinsic(typeName, name, kinds)
	if !ok {
		return nil
	}
	return fn(recv, args)
}

// intrinsicTypeName is the registry key a scalar receiver's intrinsics live
// under, by value kind.
func intrinsicTypeName(kind ir.ConstKind) (string, bool) {
	switch kind {
	case ir.ConstInt:
		return "nint", true
	case ir.ConstBool:
		return "bool", true
	case ir.ConstString:
		return "string", true
	case ir.ConstDatetime:
		return "datetime", true
	case ir.ConstDuration:
		return "duration", true
	case ir.ConstError:
		return "error", true
	default:
		return "", false
	}
}

// graphUserMethod folds a call of a user-defined (body-bearing) method. It
// reports whether it handled the call, exactly as applyUserMethod does: the
// receiver's definition comes from the value (an enum, a collection, a range)
// or the receiver node's settled type (the annotated graph's channel — a
// type-blind node carries none and resolves only by value); the overload is
// the checker's selection when recorded, else the one candidate the argument
// kinds accept.
func graphUserMethod(ctx graphCtx, v *ir.Call, recv *ir.Constant, name string, args []*ir.Constant) (*ir.Constant, bool) {
	def := graphReceiverDef(ctx, v.Receiver, recv)
	if def == nil {
		return nil, false
	}
	cands := bodyMethods(ctx.env.Registry(), def, name)
	if sel := v.Resolved; sel != nil && len(sel.Body) > 0 && len(sel.Params) == len(args) {
		if ctx.depth >= maxApplyDepth {
			ctx.noteBudget()
			return nil, true
		}
		return graphApplyBody(graphMethodCallable(sel, def), recv, args, ctx), true
	}
	var sel *ir.Method
	n := 0
	for _, m := range cands {
		if graphFits(ctx.env.Registry(), m.Params, args, recv.Kind) {
			sel = m
			n++
		}
	}
	if n == 0 {
		return nil, false
	}
	if ctx.depth >= maxApplyDepth {
		ctx.noteBudget()
		return nil, true
	}
	if n != 1 {
		return nil, true // ambiguous: user-defined, but does not fold
	}
	return graphApplyBody(graphMethodCallable(sel, def), recv, args, ctx), true
}

// graphGetter folds a getter read value.name on the graph: the receiver's
// definition resolves as a method call's does, the body-bearing getter
// applies with self bound and no arguments.
func graphGetter(ctx graphCtx, recvNode ir.Value, recv *ir.Constant, name string) (*ir.Constant, bool) {
	def := graphReceiverDef(ctx, recvNode, recv)
	if def == nil {
		return nil, false
	}
	sel := bodyAccessor(ctx.env.Registry(), def, name, ir.MethodGetter)
	if sel == nil {
		return nil, false
	}
	if ctx.depth >= maxApplyDepth {
		ctx.noteBudget()
		return nil, true
	}
	return graphApplyBody(graphMethodCallable(sel, def), recv, nil, ctx), true
}

// graphReceiverDef determines the receiver's type definition: from the value
// when it names one (an enum, a collection, a range), from the enclosing self
// definition for a SelfValue, else from the receiver node's settled type — the
// annotated graph's channel. The definition must back the receiver's value
// kind, so a wrong def never applies.
func graphReceiverDef(ctx graphCtx, recvNode ir.Value, recv *ir.Constant) *ir.TypeDef {
	if recv.Kind == ir.ConstEnum {
		return recv.EnumDef
	}
	if recv.Kind == ir.ConstCollection {
		return ctx.env.LookupType(collectionTypeName(recv))
	}
	if recv.Kind == ir.ConstRange {
		return ctx.env.LookupType(builtin.NameRange)
	}
	def := methodTableDef(ctx.env.Registry(), receiverNodeType(ctx, recvNode))
	if def == nil || !defBacksKind(ctx.env.Registry(), def, recv.Kind) {
		return nil
	}
	return def
}

// receiverNodeType reads a receiver node's static type: its settled type when
// the graph is annotated, the enclosing self definition for a SelfValue probed
// before annotation (a refinement witness, a type-blind predicate fold), and
// a conversion's target on any graph.
func receiverNodeType(ctx graphCtx, recvNode ir.Value) ir.Type {
	if t := ir.TypeOf(recvNode); t != nil {
		return t
	}
	switch v := recvNode.(type) {
	case *ir.SelfValue:
		return namedOf(ctx.selfDef)
	case *ir.Conversion:
		return v.Type
	case *ir.Adapt:
		return v.To
	}
	return nil
}

// bodyMethods collects the body-bearing instance methods named name the
// definition binds — its own, an opted-in interface's provided defaults, or
// the underlying type's — with the same shadowing collectMethodSyntaxes keeps,
// over the resolved ir.Methods instead of their syntax.
func bodyMethods(reg *builtin.Registry, def *ir.TypeDef, name string) []*ir.Method {
	return collectBodyMethods(reg, def, name, map[*ir.TypeDef]bool{})
}

func collectBodyMethods(reg *builtin.Registry, def *ir.TypeDef, name string, seen map[*ir.TypeDef]bool) []*ir.Method {
	if def == nil || seen[def] {
		return nil
	}
	seen[def] = true
	declares := false
	var out []*ir.Method
	for _, m := range def.Methods {
		if m.Name != name || m.Kind != ir.MethodNormal {
			continue
		}
		declares = true
		if len(m.Body) > 0 {
			out = append(out, m)
		}
	}
	if declares {
		return out
	}
	for _, impl := range def.Impls {
		if idef := methodTableDef(reg, impl); idef != nil && idef.Interface != nil {
			if ms := collectBodyMethods(reg, idef, name, seen); len(ms) > 0 {
				out = append(out, ms...)
			}
		}
	}
	if len(out) > 0 {
		return out
	}
	if !def.Builtin {
		return collectBodyMethods(reg, methodTableDef(reg, def.Body), name, seen)
	}
	return out
}

// bodyAccessor is bodyMethods for the accessor name spaces: the one
// body-bearing getter or setter of the given name the definition binds.
func bodyAccessor(reg *builtin.Registry, def *ir.TypeDef, name string, kind ir.MethodKind) *ir.Method {
	return collectBodyAccessor(reg, def, name, kind, map[*ir.TypeDef]bool{})
}

func collectBodyAccessor(reg *builtin.Registry, def *ir.TypeDef, name string, kind ir.MethodKind, seen map[*ir.TypeDef]bool) *ir.Method {
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
		if len(m.Body) > 0 {
			return m
		}
	}
	if declares {
		return nil
	}
	if !def.Builtin {
		return collectBodyAccessor(reg, methodTableDef(reg, def.Body), name, kind, seen)
	}
	return nil
}

// graphFits reports whether a resolved parameter list could accept the folded
// argument values — the conservative value-kind selection over resolved types
// (the AST folder decides the same by annotation spelling). selfKind is the
// receiver's kind for a method (-1 when unknown), deciding a self-typed
// parameter.
func graphFits(reg *builtin.Registry, params []ir.Param, vals []*ir.Constant, selfKind ir.ConstKind) bool {
	if len(params) != len(vals) {
		return false
	}
	for i, p := range params {
		if _, isSelf := p.Type.(*ir.SelfType); isSelf {
			if selfKind >= 0 && vals[i].Kind != selfKind {
				return false
			}
			continue
		}
		if !typeAcceptsKind(reg, p.Type, vals[i].Kind) {
			return false
		}
	}
	return true
}

// typeAcceptsKind reports whether a resolved type can hold a constant of the
// given kind, answering true for anything it cannot decide (a union — by alias
// or written —, a type variable) — the conservative discipline kindAccepts
// keeps, over resolved types instead of spellings: a wrong overload is never
// ruled in, only a decidably wrong one kept out.
func typeAcceptsKind(reg *builtin.Registry, t ir.Type, k ir.ConstKind) bool {
	switch t := t.(type) {
	case nil:
		return true
	case *ir.Builtin:
		if t.Name == builtin.NameList || t.Name == builtin.NameMap {
			return k == ir.ConstCollection
		}
		if t.Name == builtin.NameRange {
			return k == ir.ConstRange
		}
		n, ok := reg.Native(t.Name)
		if !ok {
			return true
		}
		return builtinBacksKind(n, k)
	case *ir.Named:
		return defAcceptsKind(reg, t.Def, k)
	case *ir.App:
		if t.Def != nil {
			switch t.Def.Name {
			case builtin.NameList, builtin.NameMap:
				return k == ir.ConstCollection
			case builtin.NameRange:
				return k == ir.ConstRange
			}
		}
		return defAcceptsKind(reg, t.Def, k)
	case *ir.Record:
		return k == ir.ConstRecord
	case *ir.Func:
		return k == ir.ConstFunc
	default:
		return true
	}
}

// defAcceptsKind decides whether a definition's values can be of the given
// kind, where that is decidable: an enum holds only enum members, a builtin
// or primitive-bodied nominal its native kinds, a record-bodied def records,
// a collection-bodied def collections. A def whose body decides nothing — a
// union alias, an interface, an unresolved body — accepts conservatively.
func defAcceptsKind(reg *builtin.Registry, def *ir.TypeDef, k ir.ConstKind) bool {
	if def == nil {
		return true
	}
	if def.Enum != nil {
		return k == ir.ConstEnum
	}
	if def.Builtin {
		if n, ok := reg.Native(def.Name); ok {
			return builtinBacksKind(n, k)
		}
		if def.Name == builtin.NameList || def.Name == builtin.NameMap {
			return k == ir.ConstCollection
		}
		if def.Name == builtin.NameRange {
			return k == ir.ConstRange
		}
		return true
	}
	if underlyingPrimitive(reg, def, map[*ir.TypeDef]bool{}) != nil {
		return defBacksKind(reg, def, k)
	}
	if recordOf(&ir.Named{Def: def}) != nil {
		return k == ir.ConstRecord
	}
	if defBacksKindCollection(def) {
		return k == ir.ConstCollection
	}
	return true
}
