// This file is the IR interpreter: the fold over the resolved value graph.
// Where the AST folder walks syntax and reads static types through
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

	"github.com/masterbelt/masterbelt/pkg/belt/builtin"
	"github.com/masterbelt/masterbelt/pkg/belt/types"
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

// RelationFolder folds a relation aggregate — a count() or sum() over a master's
// relation — against loaded data. The belt evaluator interprets the body a query
// sits in (a static fn's lets and arithmetic, a helper, a conditional) but cannot
// run the query itself (it has no rows, and the one-way layer rule keeps it from the
// query driver), so when it reaches such an aggregate it delegates here. A
// GraphEnv that carries loaded rows implements this; one without it (a pure
// compile-time fold) does not, and the aggregate stays unfoldable as before.
type RelationFolder interface {
	// FoldRelationAggregate folds a count()/sum() over a master relation — the
	// self-contained chain value, with any let-bound relation already inlined — to its
	// integer value over the loaded rows, or ok=false when it is not such an aggregate
	// or cannot be run (a different master, a predicate the driver cannot express).
	FoldRelationAggregate(chain ir.Value) (*ir.Constant, bool)
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

// GraphFailure classifies why folding a value graph against want produced no
// value: it re-runs the fold with the budget channel armed and reports
// FailureDepth when a budget guard refused it, FailureGap otherwise — the
// graph twin of the AST classifier, run only on the error path.
func GraphFailure(v ir.Value, want ir.Type, env GraphEnv) string {
	if v == nil {
		return FailureGap
	}
	hit := false
	graphValue(v, graphExpectingType(graphCtx{env: env, budgetHit: &hit}, want))
	if hit {
		return FailureDepth
	}
	return FailureGap
}

// GraphMemberFor returns the type a value graph's fold flows in as under the
// expected type want: the union member it would be tagged with when want is a
// union, or want itself — the channel the member-aware range and refinement
// checks resolve their effective target through, the graph twin of the old
// expression form. The value folds raw, so the member selection reads the
// same value the checks will.
func GraphMemberFor(v ir.Value, want ir.Type, env GraphEnv) ir.Type {
	if types.UnionType(want) == nil {
		return want
	}
	ctx := graphCtx{env: env}
	c := graphValueRaw(v, ctx)
	if c == nil {
		return want
	}
	if tag := graphUnionTag(ctx, v, c, want); tag != nil {
		return tag
	}
	return want
}

// GraphPredicate folds a refinement predicate graph with self bound to self
// and its owning definition selfDef in scope — so a self-method call in the
// predicate (where self.isValid()) resolves its method even on a type-blind
// graph, where the SelfValue node carries no settled type yet.
func GraphPredicate(pred ir.Value, self *ir.Constant, selfDef *ir.TypeDef, env GraphEnv) *ir.Constant {
	return graphValue(pred, graphCtx{env: env, self: self, selfDef: selfDef})
}

// GraphTableCheck folds a per-table validate check, resolving the relation count
// to count — the row count the data layer computed from the loaded rows. There is
// no self (the subject is the table, not a row); selfDef carries the master for a
// relation-method call's static channel. A check that does not fold to a definite
// true fails, the same fail-safe a per-row check uses.
func GraphTableCheck(pred ir.Value, count *ir.Constant, selfDef *ir.TypeDef, env GraphEnv) *ir.Constant {
	return graphValue(pred, graphCtx{env: env, selfDef: selfDef, relationCount: count})
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
	// subst is the type-variable solution the routine whose body is being folded
	// runs under — the checker-settled T = nint of a generic call. A match arm
	// over a type variable (the T arm of an optional<T> scrutinee) resolves
	// through it so the dispatch can decide which arm a concrete value takes; it
	// is nil for a non-generic body, where every arm type is already concrete.
	subst map[string]ir.Type
	// budgetHit is the failure-classification channel: a budget guard that
	// refuses to fold sets it. See evalCtx.budgetHit.
	budgetHit *bool
	// relationCount is the value a RelationCount folds to — the row count the data
	// layer computed from the loaded rows for a per-table validate check. It is nil
	// outside that path (a refinement or per-row fold), where a relation count
	// cannot be evaluated and stays unfoldable.
	relationCount *ir.Constant
	// relationLocals binds each let-bound relation in scope to its chain value — a
	// let m = self.where(...) records the where chain here rather than folding to a
	// (non-existent) constant, so a query reached through the local (m.count()) is
	// inlined back to the full chain before the relation folder runs it. It is set
	// per routine body (a static fn's lets are its own) and nil where no relation can
	// be bound (a refinement or per-row fold), leaving relation chains unfoldable.
	relationLocals map[string]ir.Value
	// refining is set while folding a refined type's own predicate, so the data-aware
	// admission check (graphAdmitsTyped) does not run on the predicate's literals — they
	// would otherwise recurse back into the same refinement check. See graphMemberAdmits.
	refining bool
}

// unfoldable folds the values the evaluator does not reduce on its own: an await
// (an effectful suspension point, never foldable), a relation count (the row count,
// foldable only when the data layer supplied it for a per-table check via
// relationCount), and a master relation (a query base the query driver evaluates
// against the loaded data, never the belt evaluator) — nil, unevaluable, otherwise.
func (ctx graphCtx) unfoldable(v ir.Value) *ir.Constant {
	if _, ok := v.(*ir.RelationCount); ok {
		return ctx.relationCount
	}
	return nil
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

// graphAdmitsTyped reports whether a data-dependent value inhabits a non-union type
// it is adapted into — an integer's range and a refined type's predicate. It admits
// freely outside a data-aware fold (a relation folder present), since a compile-time
// value's type was settled by the analyzer, and while folding a refinement predicate
// (the refining guard), whose own literals would otherwise recurse back through this
// check. A union target is settled by member tagging, not here.
func graphAdmitsTyped(ctx graphCtx, want ir.Type, c *ir.Constant) bool {
	if want == nil || want == ir.Invalid || ctx.refining {
		return true
	}
	if _, dataAware := ctx.env.(RelationFolder); !dataAware {
		return true
	}
	if types.UnionType(want) != nil {
		return true
	}
	return graphMemberAdmits(ctx, want, c)
}

// graphValueRaw folds one value node. The expectation channels are consumed at
// this level; sub-expressions evaluate in their own (expectation-free)
// context, exactly as the AST folder scopes them.
// every case delegates to its form's helper, so the length is the case count,
// not control complexity (the Lexer.Next class of exception).
//
//nolint:funlen // a flat exhaustive dispatch over the 26 sealed Value forms:
func graphValueRaw(v ir.Value, ctx graphCtx) *ir.Constant {
	sub := ctx
	sub.expectedColl = ir.CollUnknown
	sub.expectedType = nil
	switch v := v.(type) {
	case *ir.Adapt:
		return executeAdapt(v, sub)
	case *ir.IntLiteral:
		return graphIntLiteral(v)
	case *ir.StringLiteral:
		return ir.StringConstant(v.Value)
	case *ir.BoolLiteral:
		return ir.BoolConstant(v.Value)
	case *ir.NullValue:
		return ir.NullConstant()
	case *ir.DatetimeLiteral:
		return graphDatetimeLiteral(v)
	case *ir.DurationLiteral:
		return graphDurationLiteral(v)
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
		return graphEnumMember(v)
	case *ir.AssocConstValue:
		return graphAssocConst(v)
	case *ir.TypeValue:
		// A reified type folds to a type constant — its denoted type carried as a
		// comptime value. It is gone before codegen; the fold is what lets a const
		// bound to it (const x = int8) publish a value.
		return &ir.Constant{Kind: ir.ConstType, Reified: v.Reified}
	case *ir.CollectionLiteral:
		collCtx := sub
		collCtx.expectedColl = ctx.expectedColl
		return graphCollection(v, collCtx)
	case *ir.RecordValue:
		return graphRecord(v, sub)
	case *ir.Ternary:
		return graphTernary(v, sub)
	case *ir.RangeLit:
		return graphRangeLit(v, sub)
	case *ir.FuncLiteral:
		// A function literal folds to a closure over the bindings in scope —
		// the IR node itself, whose lowered Body the application interprets.
		return ir.FuncConstant(v, maps.Clone(ctx.locals))
	case *ir.FieldAccess:
		return graphFieldAccess(v, sub)
	case *ir.Conversion:
		return graphConvert(v, sub)
	case *ir.Await, *ir.RelationCount, *ir.MasterRelation:
		return ctx.unfoldable(v)
	case *ir.Apply:
		return graphApplyCallee(v, sub)
	case *ir.Call:
		return graphCall(v, sub)
	case *ir.FuncCall:
		return graphFuncCall(v, sub)
	case *ir.StaticCall:
		return graphStaticCall(v, sub)
	case *ir.Unresolved:
		return graphUnresolved(v, ctx)
	case nil:
		return nil
	default:
		panic(unhandledValue(v))
	}
}

// graphIntLiteral folds an integer literal to its arbitrary-precision value,
// honouring a 0b/0o/0x radix prefix.
func graphIntLiteral(v *ir.IntLiteral) *ir.Constant {
	n, ok := ParseIntLiteral(v.Text)
	if !ok {
		return nil
	}
	return ir.IntConstant(n)
}

// ParseIntLiteral parses an integer literal's text to its arbitrary-precision
// value, honouring the language's radix rules (a 0b/0o/0x prefix; a bare leading
// zero stays decimal). It is the one place that reading happens, so a consumer
// outside the evaluator — the SQL lowering binding a literal — agrees with the
// evaluator rather than re-deriving the radix with different rules.
func ParseIntLiteral(text string) (*big.Int, bool) {
	digits, base := intRadix(text)
	return new(big.Int).SetString(digits, base)
}

// intRadix splits an integer literal's text into the digits to parse and the
// base to parse them in: a case-insensitive 0b/0o/0x prefix selects base 2/8/16
// and is stripped, anything else is decimal. A bare leading zero stays decimal
// (0100 is 100, not octal), matching the lexer, which tags a radix literal only
// when its prefix letter is present.
func intRadix(text string) (digits string, base int) {
	if len(text) >= 2 && text[0] == '0' {
		switch text[1] {
		case 'b', 'B':
			return text[2:], 2
		case 'o', 'O':
			return text[2:], 8
		case 'x', 'X':
			return text[2:], 16
		}
	}
	return text, 10
}

// graphDatetimeLiteral folds a datetime literal to its UTC epoch instant.
func graphDatetimeLiteral(v *ir.DatetimeLiteral) *ir.Constant {
	if ms, ok := DatetimeMillis(v.Text); ok {
		return ir.DatetimeConstant(ms)
	}
	return nil
}

// graphDurationLiteral folds a duration literal to its total milliseconds.
func graphDurationLiteral(v *ir.DurationLiteral) *ir.Constant {
	if ms, ok := DurationMillis(v.Text); ok {
		return ir.DurationConstant(ms)
	}
	return nil
}

// graphEnumMember folds a resolved enum member reference to its constant.
func graphEnumMember(v *ir.EnumMemberValue) *ir.Constant {
	if v.Def == nil || v.Def.Enum == nil || v.Index < 0 || v.Index >= len(v.Def.Enum.Members) {
		return nil
	}
	return ir.EnumConstant(v.Def, v.Index)
}

// graphAssocConst folds a resolved associated-constant reference to the
// owning definition's published value.
func graphAssocConst(v *ir.AssocConstValue) *ir.Constant {
	if v.Def == nil || v.Index < 0 || v.Index >= len(v.Def.Consts) {
		return nil
	}
	return v.Def.Consts[v.Index].Value
}

// graphTernary folds a conditional value: only the taken branch is evaluated.
func graphTernary(v *ir.Ternary, ctx graphCtx) *ir.Constant {
	cond := graphValue(v.Cond, ctx)
	if cond == nil || cond.Kind != ir.ConstBool {
		return nil
	}
	if cond.Bool {
		return graphValue(v.Then, ctx)
	}
	return graphValue(v.Else, ctx)
}

// graphFieldAccess folds a record field read or a getter call sharing the
// surface form.
func graphFieldAccess(v *ir.FieldAccess, ctx graphCtx) *ir.Constant {
	recv := graphValue(v.Receiver, ctx)
	if recv == nil {
		return nil
	}
	if recv.Kind == ir.ConstRecord {
		if f := recordField(recv, v.Field); f != nil {
			return f
		}
	}
	if c, ok := graphGetter(ctx, v.Receiver, recv, v.Field); ok {
		return c
	}
	return nil
}

// graphApplyCallee folds the application of a function value: the callee must
// fold to a closure, which then applies to the arguments.
func graphApplyCallee(v *ir.Apply, ctx graphCtx) *ir.Constant {
	fn := graphValue(v.Callee, ctx)
	if fn == nil || fn.Kind != ir.ConstFunc {
		return nil
	}
	return graphApplyValue(ctx, fn, v.Args)
}

// executeAdapt runs an explicit adaption: the inner value folds, then carries
// at the adapted-to type. A union inflow tags the value with the member the
// inner node's type names (the same member the write-back selected), refusing
// a value the member cannot represent (out of its range, or rejected by its
// refinement predicate) — the same refusal the expectation-driven tagging
// makes, so a wrong constant is never tagged into a union the flow checks
// cannot see through. A width settle or nominal adaption is the identity on a
// compile-time value, whose range and predicate the analyzer already checked. A
// data-dependent value (a relation aggregate the rows decide), though, the
// analyzer could not check, so it is admitted here against the adapted-to type's
// range and refinement — the one seam every narrowing flows through (an argument,
// an annotated let, a return, a reassignment, a collection element, a record
// field, an explicit conversion), so an out-of-range or predicate-violating
// aggregate leaves the position unfoldable rather than passing as if it inhabited
// the type. The refining guard (graphAdmitsTyped) keeps a refined type's own
// predicate, whose comparisons adapt their literals to the type, from recursing.
func executeAdapt(a *ir.Adapt, ctx graphCtx) *ir.Constant {
	v := graphValue(a.Value, ctx)
	if v == nil {
		return nil
	}
	if types.UnionType(a.To) == nil {
		if !graphAdmitsTyped(ctx, a.To, v) {
			return nil
		}
		return v
	}
	// The member is the inner node's settled type — the write-back nests a
	// width settle inside the tag, so the inner type is the member.
	tag := normalizeBuiltin(ir.TypeOf(a.Value))
	if tag == nil {
		return nil
	}
	if types.UnionType(tag) != nil {
		// A union-typed source pins no member (a value moving into a wider
		// union, or between aliases): its own tag — carried from the union it
		// came through — stays the member, the contract UnionTag documents.
		// Tagging with the union itself would break every later dispatch.
		return v
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
		// The predicate folds with refining set, so a data-dependent value flowing
		// into the refined type's own literals does not re-enter this check.
		p := graphValue(def.Where, graphCtx{env: ctx.env, self: v, selfDef: def, refining: true})
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
	st := graphStaticType(ctx, v)
	if _, bare := st.(*ir.Union); st == nil || bare {
		return uniqueKindMemberOf(ctx.env.Registry(), u, c.Kind)
	}
	sel, m := types.SelectUnionMember(ctx.env.Registry(), st, want)
	if sel != types.UnionUnique {
		return nil
	}
	// A member still carrying a type variable is no concrete tag: a generic body
	// returning into its own optional<T> selects the T member, but an
	// unsubstituted T is not a value's union member. Leave it untagged — the
	// value's kind is the fact — so the fold matches the one over the same call
	// freshly lowered, where the generic member likewise pins nothing.
	if types.HasTypeVar(m) {
		return nil
	}
	return m
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
		// A builtin the registry does not natively model (range, list, map —
		// prelude-declared) resolves through the universe instead.
		if b, ok := v.Type.(*ir.Builtin); ok {
			def = ctx.env.LookupType(b.Name)
		}
	}
	if def == nil {
		return nil
	}
	if def.Builtin && def.Name == builtin.NameRange {
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
		return convertToNative(n, arg)
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

// convertToNative applies a builtin conversion's value rules: error(msg)
// constructs from its message, every other native accepts a value its kind
// backs and — for a sized integer — its range fits; an inadmissible value
// refuses to fold (the conversion site carries the diagnostic).
func convertToNative(n *builtin.NativeType, arg *ir.Constant) *ir.Constant {
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

// graphUnresolved folds a placeholder the write-back did not reach — a bare enum
// member returned through a body the module write-back could not fill (a closure
// captured by the value query, an imported function applied before its provider's
// write-back). It resolves the member against the expectation already threaded
// for the position (the function's declared result type), a member-by-name
// reading of the same enum the checker resolved. The expectation is a universe
// lookup, not the type query, so the value fold stays independent of typing.
//
// Only a direct enum result folds here, and only as the immediate value. A union
// carrying the enum would need the member tagged into it, a self result would
// need the receiver definition resolved, and a placeholder nested in a returned
// ternary or collection carries no expectation at this point — each would
// reproduce a slice of the write-back's resolution in the fold, and a half-done
// union fold would publish an untagged value that diverges from the qualified
// path. So those are left holes (the qualified form folds them), never a wrong
// value; a name that is no member of the enum is a hole too (the checker
// reported it).
func graphUnresolved(v *ir.Unresolved, ctx graphCtx) *ir.Constant {
	named, ok := ctx.expectedType.(*ir.Named)
	if !ok || named.Def == nil || named.Def.Enum == nil {
		return nil
	}
	for i, m := range named.Def.Enum.Members {
		if m.Name == v.Name {
			return ir.EnumConstant(named.Def, i)
		}
	}
	return nil
}

// unhandledValue is the panic value an ir.Value walker raises for a node kind
// it has no case for, mirroring the dump oracle's discipline: a new value form
// must be interpreted, never silently unevaluated.
func unhandledValue(v ir.Value) string {
	return fmt.Sprintf("eval: unhandled ir.Value kind %T", v)
}
