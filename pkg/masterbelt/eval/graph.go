// This file is the IR interpreter: the fold over the resolved value graph
// (F-3 §2.6). Where the AST folder walks syntax and reads static types through
// annotation channels, this interpreter reads everything off the graph — a
// Reference is bound, a bare member is an EnumMemberValue, a call carries its
// checker-selected overload (Resolved), every node its settled type, and every
// implicit conversion an explicit Adapt — so a fold needs nothing but the IR
// (and the builtin registry's native table): the completeness invariant made
// executable. A type-blind graph (the eager value query's, lowered before the
// checker runs) carries none of those annotations; the interpreter then falls
// back to the conservative value-kind rules, folding what the values alone
// decide and refusing the rest.
package eval

import (
	"fmt"
	"maps"
	"math/big"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/types"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// GraphEnv is what the IR interpreter needs from its driver: the value of a
// referenced constant (the engine supplies a memoizing, cycle-guarded
// implementation), the universe's type definitions by name (the channel a
// record literal's nominal type and a collection's prelude def resolve
// through — a name lookup, never the type query), and the builtin registry.
type GraphEnv interface {
	// ConstValue returns a referenced constant's evaluated value, or nil when
	// it cannot be evaluated.
	ConstValue(c *ir.Const) *ir.Constant
	// LookupType resolves a type name in the scope's universe, or nil.
	LookupType(name string) *ir.TypeDef
	// Registry returns the builtin registry the program evaluates against.
	Registry() *builtin.Registry
}

// Graph folds a value graph to its constant value, or nil when it cannot be
// evaluated.
func Graph(v ir.Value, env GraphEnv) *ir.Constant {
	return graphValue(v, graphCtx{env: env})
}

// GraphExpecting folds a value graph against the resolved annotation type the
// value flows into — the channel that settles an empty collection literal's
// mapness and tags a union inflow on a type-blind graph (an annotated graph
// carries explicit Adapt nodes instead, which the interpreter executes).
func GraphExpecting(v ir.Value, want ir.Type, env GraphEnv) *ir.Constant {
	return graphValue(v, graphExpectingType(graphCtx{env: env}, want))
}

// GraphIn folds a value graph with a set of local bindings in scope, so a
// ParamRef or LocalRef folds to its value — how a body-position check folds a
// sub-graph the engine cannot reach on its own.
func GraphIn(v ir.Value, locals map[string]*ir.Constant, env GraphEnv) *ir.Constant {
	return graphValue(v, graphCtx{env: env, locals: locals})
}

// GraphInExpecting is GraphIn with the annotation-type channel set, the let
// twin of GraphExpecting.
func GraphInExpecting(v ir.Value, locals map[string]*ir.Constant, want ir.Type, env GraphEnv) *ir.Constant {
	return graphValue(v, graphExpectingType(graphCtx{env: env, locals: locals}, want))
}

// GraphPredicate folds a refinement predicate graph with self bound to self
// and its owning definition selfDef in scope — so a self-method call in the
// predicate (where self.isValid()) resolves its method even on a type-blind
// graph, where the SelfValue node carries no settled type yet.
func GraphPredicate(pred ir.Value, self *ir.Constant, selfDef *ir.TypeDef, env GraphEnv) *ir.Constant {
	return graphValue(pred, graphCtx{env: env, self: self, selfDef: selfDef})
}

// graphCtx carries the interpretation context through the recursive fold: the
// driver's environment, the local bindings of the enclosing applied routine or
// function literals, the self value (and its owning definition, the static
// channel a type-blind predicate probe needs), the application depth the
// recursion guard counts, and the expectation channels a type-blind graph
// folds its top expression against.
type graphCtx struct {
	env    GraphEnv
	locals map[string]*ir.Constant
	self   *ir.Constant
	// selfDef is the owning definition of self — a method's receiver type, a
	// refinement predicate's refined type — the static channel a SelfValue
	// receiver resolves its methods through when the node carries no settled
	// type (a type-blind graph).
	selfDef *ir.TypeDef
	depth   int
	// expectedColl is the mapness an empty collection literal settles to — the
	// list/map distinction a declared type supplies for a literal with no key
	// to read. It reaches only the immediate expression.
	expectedColl ir.CollKind
	// expectedType is the resolved annotation type the immediate expression's
	// value flows into — the union tagging channel of a type-blind graph (an
	// annotated graph carries Adapt nodes instead). It reaches only the
	// immediate expression.
	expectedType ir.Type
	// resultColl / resultType are the declared result type's channels of the
	// routine whose body is being folded, threaded to each return expression.
	resultColl ir.CollKind
	resultType ir.Type
	// budgetHit is the failure-classification channel: a budget guard that
	// refuses to fold sets it. See evalCtx.budgetHit.
	budgetHit *bool
}

// noteBudget mirrors evalCtx.noteBudget for the graph context.
func (ctx graphCtx) noteBudget() {
	if ctx.budgetHit != nil {
		*ctx.budgetHit = true
	}
}

// graphExpectingType seeds the expectation channels a resolved annotation type
// supplies on a copy of ctx — the graph twin of expectingType.
func graphExpectingType(ctx graphCtx, want ir.Type) graphCtx {
	ctx.expectedColl = CollKindOf(want)
	ctx.expectedType = want
	return ctx
}

// graphValue folds one value node and tags the result with the union member it
// flowed in as when the immediate context is a union channel — the type-blind
// twin of the Adapt execution an annotated graph carries explicitly.
func graphValue(v ir.Value, ctx graphCtx) *ir.Constant {
	c := graphValueRaw(v, ctx)
	if c == nil || ctx.expectedType == nil {
		return c
	}
	if tag := graphUnionTag(ctx, v, c, ctx.expectedType); tag != nil {
		if !graphMemberAdmits(ctx, tag, c) {
			return nil
		}
		return ir.Tagged(c, tag)
	}
	return c
}

// graphValueRaw folds one value node. The expectation channels are consumed at
// this level; sub-expressions evaluate in their own (expectation-free)
// context, exactly as the AST folder scopes them.
func graphValueRaw(v ir.Value, ctx graphCtx) *ir.Constant {
	sub := ctx
	sub.expectedColl = ir.CollUnknown
	sub.expectedType = nil
	switch v := v.(type) {
	case *ir.Adapt:
		return executeAdapt(v, sub)
	case *ir.IntLiteral:
		n, ok := new(big.Int).SetString(v.Text, 10)
		if !ok {
			return nil
		}
		return ir.IntConstant(n)
	case *ir.StringLiteral:
		return ir.StringConstant(v.Value)
	case *ir.BoolLiteral:
		return ir.BoolConstant(v.Value)
	case *ir.NullValue:
		return ir.NullConstant()
	case *ir.DatetimeLiteral:
		if ms, ok := DatetimeMillis(v.Text); ok {
			return ir.DatetimeConstant(ms)
		}
		return nil
	case *ir.DurationLiteral:
		if ms, ok := DurationMillis(v.Text); ok {
			return ir.DurationConstant(ms)
		}
		return nil
	case *ir.SelfValue:
		return ctx.self
	case *ir.ParamRef:
		return ctx.locals[v.Name]
	case *ir.LocalRef:
		return ctx.locals[v.Name]
	case *ir.Reference:
		if v.Target == nil {
			return nil
		}
		return ctx.env.ConstValue(v.Target)
	case *ir.EnumMemberValue:
		if v.Def == nil || v.Def.Enum == nil || v.Index < 0 || v.Index >= len(v.Def.Enum.Members) {
			return nil
		}
		return ir.EnumConstant(v.Def, v.Index)
	case *ir.AssocConstValue:
		if v.Def == nil || v.Index < 0 || v.Index >= len(v.Def.Consts) {
			return nil
		}
		return v.Def.Consts[v.Index].Value
	case *ir.CollectionLiteral:
		collCtx := sub
		collCtx.expectedColl = ctx.expectedColl
		return graphCollection(v, collCtx)
	case *ir.RecordValue:
		return graphRecord(v, sub)
	case *ir.Ternary:
		cond := graphValue(v.Cond, sub)
		if cond == nil || cond.Kind != ir.ConstBool {
			return nil
		}
		if cond.Bool {
			return graphValue(v.Then, sub)
		}
		return graphValue(v.Else, sub)
	case *ir.RangeLit:
		return graphRangeLit(v, sub)
	case *ir.FuncLiteral:
		// A function literal folds to a closure over the bindings in scope —
		// the IR node itself, whose lowered Body the application interprets.
		return ir.FuncConstant(v, maps.Clone(ctx.locals))
	case *ir.FieldAccess:
		recv := graphValue(v.Receiver, sub)
		if recv == nil {
			return nil
		}
		if recv.Kind == ir.ConstRecord {
			if f := recordField(recv, v.Field); f != nil {
				return f
			}
		}
		if c, ok := graphGetter(sub, v.Receiver, recv, v.Field); ok {
			return c
		}
		return nil
	case *ir.Conversion:
		return graphConvert(v, sub)
	case *ir.Await:
		// await marks the suspension point and adds nothing to the value; an
		// awaited value is effectful by construction, so it does not fold.
		return nil
	case *ir.Apply:
		fn := graphValue(v.Callee, sub)
		if fn == nil || fn.Kind != ir.ConstFunc {
			return nil
		}
		return graphApplyValue(sub, fn, v.Args)
	case *ir.Call:
		return graphCall(v, sub)
	case *ir.FuncCall:
		return graphFuncCall(v, sub)
	case *ir.StaticCall:
		return graphStaticCall(v, sub)
	case nil:
		return nil
	default:
		panic(unhandledValue(v))
	}
}

// executeAdapt runs an explicit adaption: the inner value folds, then carries
// at the adapted-to type. A union inflow tags the value with the member the
// inner node's type names (the same member the write-back selected), refusing
// a value the member cannot represent (out of its range, or rejected by its
// refinement predicate) — the same refusal the expectation-driven tagging
// makes, so a wrong constant is never tagged into a union the flow checks
// cannot see through. A width settle or nominal adaption is the identity on
// the value: the representation is unchanged, and whether the value satisfies
// the target's range or predicate is the flow checks' diagnostic (folding the
// raw value is what lets them read it) — also what keeps a refined type's own
// predicate, whose comparisons adapt their literals to the type, from running
// itself recursively.
func executeAdapt(a *ir.Adapt, ctx graphCtx) *ir.Constant {
	v := graphValue(a.Value, ctx)
	if v == nil {
		return nil
	}
	if types.UnionType(a.To) == nil {
		return v
	}
	// The member is the inner node's settled type — the write-back nests a
	// width settle inside the tag, so the inner type is the member.
	tag := normalizeBuiltin(ir.TypeOf(a.Value))
	if tag == nil {
		return nil
	}
	if !graphMemberAdmits(ctx, tag, v) {
		return nil
	}
	return ir.Tagged(v, tag)
}

// graphMemberAdmits is memberAdmits over the graph context: whether v is a
// representable value of the member type it flows in as — within an integer
// member's range, and satisfying a refined member's predicate (folded on the
// definition's own Where graph). Only a definitive violation refuses.
func graphMemberAdmits(ctx graphCtx, member ir.Type, v *ir.Constant) bool {
	reg := ctx.env.Registry()
	if v.Kind == ir.ConstInt && !types.Fits(reg, member, v.Int) {
		return false
	}
	if def := refinedMemberDef(member); def != nil {
		p := GraphPredicate(def.Where, v, def, ctx.env)
		if p != nil && p.Kind == ir.ConstBool && !p.Bool {
			return false
		}
	}
	return true
}

// graphUnionTag settles the union member a folded value flows in as on a
// type-blind graph — the structural twin of unionMemberTag: an already-tagged
// value keeps its tag; a node with a structural static type (a typed record
// literal, a conversion, an enum member, an annotated node) selects by the
// type layer's exact→unique rule; a bare literal selects by unique kind
// backing.
func graphUnionTag(ctx graphCtx, v ir.Value, c *ir.Constant, want ir.Type) ir.Type {
	u := types.UnionType(want)
	if u == nil {
		return nil
	}
	if c.UnionTag != nil {
		return c.UnionTag
	}
	// A source whose static type is a bare union pins no member (which member
	// its value is comes from the value, not the source's type), so it falls
	// through to the kind backing exactly as a source with no static type
	// does. An alias of a union (optional<nint>) keeps the member-selection
	// path, whose no-member verdict leaves the value untagged — the same
	// asymmetry the expectation-driven folder settles through its written-
	// annotation channels.
	if st := graphStaticType(ctx, v); st != nil {
		if _, bare := st.(*ir.Union); !bare {
			if sel, m := types.SelectUnionMember(ctx.env.Registry(), st, want); sel == types.UnionUnique {
				return m
			}
			return nil
		}
	}
	return uniqueKindMemberOf(ctx.env.Registry(), u, c.Kind)
}

// graphStaticType reads a node's static type for union member selection: the
// settled node type when the graph is annotated, else the structural channels
// a type-blind graph still carries — a typed record literal's nominal type, a
// conversion's target, an enum member's enum. A bare literal deliberately
// reads as nothing (its width adapts), leaving the kind-backing fallback to
// decide — the same discipline valueStaticType keeps.
func graphStaticType(ctx graphCtx, v ir.Value) ir.Type {
	switch v := v.(type) {
	case *ir.IntLiteral, *ir.StringLiteral, *ir.BoolLiteral,
		*ir.DatetimeLiteral, *ir.DurationLiteral, *ir.NullValue:
		return nil
	case *ir.RecordValue:
		if v.TypeName == "" {
			return nil
		}
		return defType(ctx.env.LookupType(v.TypeName))
	case *ir.Conversion:
		return normalizeBuiltin(v.Type)
	case *ir.EnumMemberValue:
		if v.Def == nil {
			return nil
		}
		return &ir.Named{Def: v.Def}
	default:
		return normalizeBuiltin(ir.TypeOf(v))
	}
}

// uniqueKindMemberOf is uniqueKindMember keyed off the registry directly, the
// shared kind-backing fallback of both folders.
func uniqueKindMemberOf(reg *builtin.Registry, u *ir.Union, kind ir.ConstKind) ir.Type {
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

// graphCollection folds a collection literal node: every entry must fold, and
// an empty literal settles its mapness from the expectation channel.
func graphCollection(v *ir.CollectionLiteral, ctx graphCtx) *ir.Constant {
	entries := make([]ir.ConstEntry, 0, len(v.Entries))
	for _, entry := range v.Entries {
		var key *ir.Constant
		if entry.Key != nil {
			if key = graphValue(entry.Key, ctx); key == nil {
				return nil
			}
		}
		val := graphValue(entry.Value, ctx)
		if val == nil {
			return nil
		}
		entries = append(entries, ir.ConstEntry{Key: key, Value: val})
	}
	if len(entries) == 0 {
		kind := ctx.expectedColl
		if kind == ir.CollUnknown {
			// An annotated literal carries the settled type the channel would
			// have supplied — the node's own mapness.
			kind = CollKindOf(v.Type)
		}
		return ir.CollectionConstantOf(entries, kind)
	}
	return ir.CollectionConstant(entries)
}

// graphRecord folds a record literal node. A named record's declared field
// types are the field values' tagging channel on a type-blind graph (an
// annotated graph wraps the fields in Adapt nodes, which fold first).
func graphRecord(v *ir.RecordValue, ctx graphCtx) *ir.Constant {
	fieldTypes := graphRecordFieldTypes(ctx, v)
	fields := make([]ir.ConstField, 0, len(v.Fields))
	for _, f := range v.Fields {
		if f.Name == "" {
			return nil // recovered away; already a parse diagnostic
		}
		fieldCtx := ctx
		fieldCtx.expectedType = fieldTypes[f.Name]
		c := graphValue(f.Value, fieldCtx)
		if c == nil {
			return nil
		}
		fields = append(fields, ir.ConstField{Name: f.Name, Value: c})
	}
	return ir.RecordConstant(fields)
}

// graphRecordFieldTypes resolves a named record literal's declared field types
// — through the node's settled type when annotated, else the universe lookup
// of its written name.
func graphRecordFieldTypes(ctx graphCtx, v *ir.RecordValue) map[string]ir.Type {
	rec := recordOf(v.Type)
	if rec == nil && v.TypeName != "" {
		rec = recordOf(namedOf(ctx.env.LookupType(v.TypeName)))
	}
	if rec == nil {
		return nil
	}
	out := make(map[string]ir.Type, len(rec.Fields))
	for _, f := range rec.Fields {
		out[f.Name] = f.Type
	}
	return out
}

// graphRangeLit folds a range literal node — the same bound-driven direction
// and half-open trim rangeLit settles.
func graphRangeLit(v *ir.RangeLit, ctx graphCtx) *ir.Constant {
	lo := graphValue(v.Lower, ctx)
	hi := graphValue(v.Upper, ctx)
	if lo == nil || hi == nil || lo.Kind != ir.ConstInt || hi.Kind != ir.ConstInt {
		return nil
	}
	one := big.NewInt(1)
	start := new(big.Int).Set(lo.Int)
	end := new(big.Int).Set(hi.Int)
	if lo.Int.Cmp(hi.Int) <= 0 {
		if v.HalfOpen {
			end.Sub(end, one)
		}
		return ir.RangeConstant(start, end)
	}
	if v.HalfOpen {
		start.Sub(start, one)
	}
	return ir.RangeConstantStep(start, end, big.NewInt(-1))
}

// graphConvert folds a conversion node T(x) — the pass-through identity with
// the range refusal, the error and range constructors included; the convert
// twin over resolved types.
func graphConvert(v *ir.Conversion, ctx graphCtx) *ir.Constant {
	def := methodTableDef(ctx.env.Registry(), v.Type)
	if def == nil {
		return nil
	}
	if def.Builtin && def.Name == "range" {
		return graphConvertRange(v.Args, ctx)
	}
	if len(v.Args) != 1 {
		return nil
	}
	arg := graphValue(v.Args[0], ctx)
	if arg == nil {
		return nil
	}
	reg := ctx.env.Registry()
	if def.Builtin {
		n, ok := reg.Native(def.Name)
		if !ok {
			return nil
		}
		if n.Err {
			if arg.Kind != ir.ConstString {
				return nil
			}
			return ir.ErrorConstant(arg.Str)
		}
		if !builtinBacksKind(n, arg.Kind) {
			return nil
		}
		if arg.Kind == ir.ConstInt && !n.Fits(arg.Int) {
			return nil
		}
		return arg
	}
	if defBacksKind(reg, def, arg.Kind) {
		if arg.Kind == ir.ConstInt {
			if n := underlyingPrimitive(reg, def, map[*ir.TypeDef]bool{}); n != nil && !n.Fits(arg.Int) {
				return nil
			}
		}
		return arg
	}
	if arg.Kind == ir.ConstCollection && defBacksKindCollection(def) {
		return arg
	}
	return nil
}

// graphConvertRange folds the range constructor's graph form — convertRange
// over folded argument nodes.
func graphConvertRange(args []ir.Value, ctx graphCtx) *ir.Constant {
	if len(args) != 2 && len(args) != 3 {
		return nil
	}
	start := graphValue(args[0], ctx)
	end := graphValue(args[1], ctx)
	if start == nil || end == nil || start.Kind != ir.ConstInt || end.Kind != ir.ConstInt {
		return nil
	}
	if len(args) == 2 {
		return ir.RangeConstant(start.Int, end.Int)
	}
	step := graphValue(args[2], ctx)
	if step == nil || step.Kind != ir.ConstInt || step.Int.Sign() == 0 {
		return nil
	}
	return ir.RangeConstantStep(start.Int, end.Int, step.Int)
}

// unhandledValue is the panic value an ir.Value walker raises for a node kind
// it has no case for, mirroring the dump oracle's discipline: a new value form
// must be interpreted, never silently unevaluated.
func unhandledValue(v ir.Value) string {
	return fmt.Sprintf("eval: unhandled ir.Value kind %T", v)
}
