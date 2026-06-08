// These tests drive the IR interpreter over hand-built value graphs — no
// syntax anywhere, which is itself the point: a fold needs nothing but the IR
// and the registry. The semantic package's corpus, examples, and
// parity gates cover the end-to-end channels; these pin the interpreter's own
// value-level rules.
package eval

import (
	"math/big"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/belt/builtin"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// stubEnv supplies the registry and a name→definition surface; constants
// resolve to nothing (the graphs under test reference none, or carry their
// values through shells' published Eval).
type stubEnv struct {
	reg  *builtin.Registry
	defs map[string]*ir.TypeDef
}

func newStubEnv() stubEnv {
	e := stubEnv{reg: builtin.Default(), defs: map[string]*ir.TypeDef{}}
	// The prelude-declared collection builtins the bare registry does not
	// model — the production universe installs them; the stub declares the
	// marker defs the interpreter resolves by name.
	for _, name := range []string{"range", "list", "map"} {
		e.defs[name] = &ir.TypeDef{Name: name, Builtin: true}
	}
	return e
}

func (e stubEnv) ConstValue(c *ir.Const) *ir.Constant { return c.Eval }
func (e stubEnv) LookupType(name string) *ir.TypeDef {
	if d, ok := e.defs[name]; ok {
		return d
	}
	d, _ := e.reg.Lookup(name)
	return d
}
func (e stubEnv) Registry() *builtin.Registry { return e.reg }

func intLit(text string) *ir.IntLiteral { return &ir.IntLiteral{Text: text} }

func call(recv ir.Value, method string, args ...ir.Value) *ir.Call {
	return &ir.Call{Receiver: recv, Method: method, Args: args}
}

func wantInt(t *testing.T, v *ir.Constant, want int64) {
	t.Helper()
	if v == nil || v.Kind != ir.ConstInt || !v.Int.IsInt64() || v.Int.Int64() != want {
		t.Fatalf("fold = %s, want %d", v, want)
	}
}

func wantNil(t *testing.T, v *ir.Constant) {
	t.Helper()
	if v != nil {
		t.Fatalf("fold = %s, want no value", v)
	}
}

// TestGraphIntrinsics pins the scalar dispatch: an operator method on a folded
// receiver reaches its native intrinsic by value kind.
func TestGraphIntrinsics(t *testing.T) {
	env := newStubEnv()
	wantInt(t, Graph(call(intLit("1"), "add", intLit("2")), env), 3)
	wantNil(t, Graph(call(intLit("1"), "div", intLit("0")), env)) // division by zero has no value
	v := Graph(call(&ir.StringLiteral{Value: "a"}, "add", &ir.StringLiteral{Value: "b"}), env)
	if v == nil || v.Kind != ir.ConstString || v.Str != "ab" {
		t.Fatalf(`"a" + "b" = %s, want "ab"`, v)
	}
}

// TestGraphShortCircuit pins the connectives: a deciding receiver never folds
// the dead operand.
func TestGraphShortCircuit(t *testing.T) {
	env := newStubEnv()
	dead := call(intLit("1"), "div", intLit("0")) // would refuse to fold
	v := Graph(call(&ir.BoolLiteral{Value: false}, "anan", dead), env)
	if v == nil || v.Kind != ir.ConstBool || v.Bool {
		t.Fatalf("false && _ = %s, want false", v)
	}
	v = Graph(call(&ir.BoolLiteral{Value: true}, "oror", dead), env)
	if v == nil || v.Kind != ir.ConstBool || !v.Bool {
		t.Fatalf("true || _ = %s, want true", v)
	}
}

// TestGraphReference pins the reference reading: a bound constant's published
// value folds through the environment.
func TestGraphReference(t *testing.T) {
	env := newStubEnv()
	c := &ir.Const{Name: "N", Eval: ir.IntConstant(big.NewInt(41))}
	wantInt(t, Graph(call(&ir.Reference{Target: c}, "add", intLit("1")), env), 42)
}

// TestGraphConversion pins the conversion fold: the identity on an in-range
// value, the refusal of an out-of-range one, and the error and range
// constructors.
func TestGraphConversion(t *testing.T) {
	env := newStubEnv()
	short := &ir.Conversion{Type: &ir.Builtin{Name: "short"}, Args: []ir.Value{intLit("20")}}
	wantInt(t, Graph(short, env), 20)
	over := &ir.Conversion{Type: &ir.Builtin{Name: "short"}, Args: []ir.Value{intLit("70000")}}
	wantNil(t, Graph(over, env))
	errv := Graph(&ir.Conversion{Type: &ir.Builtin{Name: "error"}, Args: []ir.Value{&ir.StringLiteral{Value: "boom"}}}, env)
	if errv == nil || errv.Kind != ir.ConstError || errv.Str != "boom" {
		t.Fatalf("error fold = %s", errv)
	}
	rng := Graph(&ir.Conversion{Type: &ir.Builtin{Name: "range"}, Args: []ir.Value{intLit("0"), intLit("10"), intLit("2")}}, env)
	if rng == nil || rng.Kind != ir.ConstRange {
		t.Fatalf("range fold = %s", rng)
	}
	wantNil(t, Graph(&ir.Conversion{Type: &ir.Builtin{Name: "range"}, Args: []ir.Value{intLit("0"), intLit("10"), intLit("0")}}, env))
}

// TestGraphCollections pins the collection intrinsics over a literal graph:
// len, push, get, set (a list write past the end refusing), and fold with a
// closure.
func TestGraphCollections(t *testing.T) {
	env := newStubEnv()
	list := &ir.CollectionLiteral{Entries: []ir.CollectionEntry{
		{Value: intLit("1")}, {Value: intLit("2")}, {Value: intLit("3")},
	}}
	wantInt(t, Graph(call(list, "len"), env), 3)
	wantInt(t, Graph(call(list, "get", intLit("1")), env), 2)
	wantNil(t, Graph(call(list, "set", intLit("9"), intLit("0")), env)) // out of range
	if v := Graph(call(list, "push", intLit("4")), env); v == nil || len(v.Coll) != 4 {
		t.Fatalf("push fold = %s", v)
	}

	// fold(0, fn(acc, k, v) -> acc + v) over the closure's lowered body.
	step := &ir.FuncLiteral{
		Params: []string{"acc", "k", "v"},
		Body:   []ir.Stmt{&ir.Return{Value: call(&ir.ParamRef{Name: "acc"}, "add", &ir.ParamRef{Name: "v"})}},
	}
	wantInt(t, Graph(call(list, "fold", intLit("0"), step), env), 6)
}

// TestGraphApply pins the function-value application: an Apply node folds the
// closure's lowered body with the parameters bound, and the depth guard turns
// runaway recursion into the depth classification.
func TestGraphApply(t *testing.T) {
	env := newStubEnv()
	inc := &ir.FuncLiteral{
		Params: []string{"x"},
		Body:   []ir.Stmt{&ir.Return{Value: call(&ir.ParamRef{Name: "x"}, "add", intLit("1"))}},
	}
	wantInt(t, Graph(&ir.Apply{Callee: inc, Args: []ir.Value{intLit("41")}}, env), 42)

	// A function applying itself forever: the budget guard classifies depth.
	fn := &ir.Function{Name: "loop", Params: []ir.Param{{Name: "n", Type: &ir.Builtin{Name: "nint"}}}}
	fn.Body = []ir.Stmt{&ir.Return{Value: &ir.FuncCall{Target: fn, Args: []ir.Value{&ir.ParamRef{Name: "n"}}}}}
	callLoop := &ir.FuncCall{Target: fn, Args: []ir.Value{intLit("1")}}
	wantNil(t, Graph(callLoop, env))
	if reason := GraphFailure(callLoop, nil, env); reason != FailureDepth {
		t.Fatalf("failure reason = %q, want %q", reason, FailureDepth)
	}
}

// TestGraphMethodBody pins a user-defined method fold: the receiver's settled
// type (a conversion is born typed) reaches the definition, and the body folds
// with self bound.
func TestGraphMethodBody(t *testing.T) {
	env := newStubEnv()
	level := &ir.TypeDef{Name: "Level", Body: &ir.Builtin{Name: "sbyte"}}
	level.Methods = []*ir.Method{{
		Name:   "increment",
		Result: &ir.SelfType{},
		Body:   []ir.Stmt{&ir.Return{Value: call(&ir.SelfValue{}, "add", intLit("1"))}},
	}}
	env.defs["Level"] = level
	recv := &ir.Conversion{Type: &ir.Named{Def: level}, Args: []ir.Value{intLit("5")}}
	wantInt(t, Graph(call(recv, "increment"), env), 6)
}

// TestGraphAdaptUnion pins the explicit adaption's execution: a union inflow
// tags the value with the inner node's member and refuses a value the member
// cannot represent — out of range, or rejected by its refinement predicate.
func TestGraphAdaptUnion(t *testing.T) {
	env := newStubEnv()
	port := &ir.TypeDef{Name: "Port", Body: &ir.Builtin{Name: "sbyte"}}
	port.Where = call(&ir.SelfValue{}, "gt", intLit("0")) // self > 0
	env.defs["Port"] = port

	union := &ir.Union{Members: []ir.Type{&ir.Named{Def: port}, &ir.Builtin{Name: "error"}}}
	adapt := func(text string) ir.Value {
		inner := &ir.Adapt{Value: intLit(text), To: &ir.Named{Def: port}}
		return &ir.Adapt{Value: inner, To: union}
	}
	if v := Graph(adapt("5"), env); v == nil || v.UnionTag == nil {
		t.Fatalf("5 into Port | error = %s, want a tagged value", v)
	}
	neg := &ir.Adapt{Value: &ir.Adapt{Value: call(intLit("5"), "neg"), To: &ir.Named{Def: port}}, To: union}
	wantNil(t, Graph(neg, env))          // -5 violates self > 0: not a representable Port
	wantNil(t, Graph(adapt("200"), env)) // out of sbyte range
}

// TestGraphPredicateSelfMethod pins the predicate fold with a self-method
// call: the SelfValue receiver resolves through the supplied definition even
// on a type-blind graph.
func TestGraphPredicateSelfMethod(t *testing.T) {
	env := newStubEnv()
	lvl := &ir.TypeDef{Name: "Lvl", Body: &ir.Builtin{Name: "sbyte"}}
	lvl.Methods = []*ir.Method{{
		Name:   "isValid",
		Result: &ir.Builtin{Name: "bool"},
		Body:   []ir.Stmt{&ir.Return{Value: call(&ir.SelfValue{}, "gteq", intLit("0"))}},
	}}
	env.defs["Lvl"] = lvl
	pred := call(&ir.SelfValue{}, "isValid")
	v := GraphPredicate(pred, ir.IntConstant(big.NewInt(3)), lvl, env)
	if v == nil || v.Kind != ir.ConstBool || !v.Bool {
		t.Fatalf("isValid(3) = %s, want true", v)
	}
	v = GraphPredicate(pred, ir.IntConstant(big.NewInt(-3)), lvl, env)
	if v == nil || v.Kind != ir.ConstBool || v.Bool {
		t.Fatalf("isValid(-3) = %s, want false", v)
	}
}

// TestGraphMatchDispatch pins the match fold: a tagged scrutinee dispatches
// confidently on its member, and an untagged value over two same-kind arms
// refuses rather than guessing.
func TestGraphMatchDispatch(t *testing.T) {
	env := newStubEnv()
	small := &ir.TypeDef{Name: "Small", Body: &ir.Builtin{Name: "sbyte"}}
	bigDef := &ir.TypeDef{Name: "Big", Body: &ir.Builtin{Name: "int"}}
	env.defs["Small"], env.defs["Big"] = small, bigDef

	armed := func(scrut ir.Value) *ir.Function {
		fn := &ir.Function{Name: "pick", Result: &ir.Builtin{Name: "nint"}}
		fn.Body = []ir.Stmt{&ir.Match{
			Scrutinee: scrut,
			Arms: []ir.MatchArm{
				{Type: &ir.Named{Def: small}, Body: []ir.Stmt{&ir.Return{Value: intLit("1")}}},
				{Type: &ir.Named{Def: bigDef}, Body: []ir.Stmt{&ir.Return{Value: intLit("2")}}},
			},
		}}
		return fn
	}

	// Tagged: an Adapt into the union pins the member, so the dispatch folds.
	union := &ir.Union{Members: []ir.Type{&ir.Named{Def: small}, &ir.Named{Def: bigDef}}}
	tagged := &ir.Adapt{Value: &ir.Adapt{Value: intLit("7"), To: &ir.Named{Def: bigDef}}, To: union}
	wantInt(t, Graph(&ir.FuncCall{Target: armed(tagged), Args: nil}, env), 2)

	// Untagged: two nominal-over-int arms could both hold the value — refuse.
	wantNil(t, Graph(&ir.FuncCall{Target: armed(intLit("7")), Args: nil}, env))
}

// TestGraphBodyExecution pins the statement interpreter: lets bind, an if
// guards a reassignment that persists, a for accumulates, and a switch
// dispatches by folded equality.
func TestGraphBodyExecution(t *testing.T) {
	env := newStubEnv()
	list := &ir.CollectionLiteral{Entries: []ir.CollectionEntry{
		{Value: intLit("1")}, {Value: intLit("2")}, {Value: intLit("3")},
	}}
	fn := &ir.Function{Name: "sum", Result: &ir.Builtin{Name: "nint"}}
	fn.Body = []ir.Stmt{
		&ir.Let{Name: "acc", Type: &ir.Builtin{Name: "nint"}, Value: intLit("0")},
		&ir.For{Var: "v", Of: true, Iter: list, Body: []ir.Stmt{
			&ir.Assign{Name: "acc", Value: call(&ir.LocalRef{Name: "acc"}, "add", &ir.LocalRef{Name: "v"})},
		}},
		&ir.If{
			Cond: call(&ir.LocalRef{Name: "acc"}, "gt", intLit("5")),
			Then: []ir.Stmt{&ir.Assign{Name: "acc", Value: call(&ir.LocalRef{Name: "acc"}, "add", intLit("10"))}},
		},
		&ir.Switch{
			Scrutinee: &ir.LocalRef{Name: "acc"},
			Arms:      []ir.SwitchArm{{Values: []ir.Value{intLit("16")}, Body: []ir.Stmt{&ir.Return{Value: intLit("100")}}}},
			Else:      []ir.Stmt{&ir.Return{Value: intLit("0")}},
		},
	}
	wantInt(t, Graph(&ir.FuncCall{Target: fn, Args: nil}, env), 100)
}

// TestGraphEnumComparison pins the enum fold: equality by member identity and
// ordering by base value.
func TestGraphEnumComparison(t *testing.T) {
	env := newStubEnv()
	rarity := &ir.TypeDef{Name: "Rarity", Enum: &ir.EnumDef{Base: "byte", Members: []ir.EnumMember{
		{Name: "Common", Value: ir.IntConstant(big.NewInt(1))},
		{Name: "Legend", Value: ir.IntConstant(big.NewInt(10))},
	}}}
	env.defs["Rarity"] = rarity
	common := &ir.EnumMemberValue{Def: rarity, Index: 0}
	legend := &ir.EnumMemberValue{Def: rarity, Index: 1}
	v := Graph(call(common, "lt", legend), env)
	if v == nil || v.Kind != ir.ConstBool || !v.Bool {
		t.Fatalf("Common < Legend = %s, want true", v)
	}
	v = Graph(call(common, "eql", legend), env)
	if v == nil || v.Kind != ir.ConstBool || v.Bool {
		t.Fatalf("Common == Legend = %s, want false", v)
	}
}

// TestGraphEmptyCollectionChannels pins the mapness settle: the expectation
// channel (GraphExpecting) and the annotated node's own type both settle an
// empty literal, and a bare one stays undecided for the operations that need
// the distinction.
func TestGraphEmptyCollectionChannels(t *testing.T) {
	env := newStubEnv()
	mapT := &ir.App{Def: env.defs["map"], Args: []ir.Type{&ir.Builtin{Name: "string"}, &ir.Builtin{Name: "nint"}}}

	// The expectation channel: an empty literal under a map type upserts.
	empty := &ir.CollectionLiteral{}
	v := GraphExpecting(empty, mapT, env)
	if v == nil || !v.IsMap() {
		t.Fatalf("[] under map<...> = %s, want a settled empty map", v)
	}

	// The node's own settled type: the annotated graph needs no channel.
	annotated := &ir.CollectionLiteral{Type: mapT}
	v = Graph(annotated, env)
	if v == nil || !v.IsMap() {
		t.Fatalf("annotated [] = %s, want a settled empty map", v)
	}

	// A bare [] folds but stays undecided: a set write cannot tell an upsert
	// from an out-of-range list write, so it does not fold.
	bare := Graph(&ir.CollectionLiteral{}, env)
	if bare == nil || bare.CollMapness != ir.CollUnknown {
		t.Fatalf("bare [] = %s, want an unknown-mapness empty collection", bare)
	}
	wantNil(t, Graph(call(&ir.CollectionLiteral{}, "set", &ir.StringLiteral{Value: "k"}, intLit("1")), env))
	// The mapness-independent reads still fold.
	wantInt(t, Graph(call(&ir.CollectionLiteral{}, "len"), env), 0)
}
