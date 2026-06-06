package eval

import (
	"math/big"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/lexer"
	"github.com/masterbelt/masterbelt/pkg/source"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// --- ast builders (nil syntax: evaluation never reads Syntax) ----------------

func intLit(text string) *ast.IntLit { return ast.NewIntLit(text, nil) }
func selfExpr() *ast.SelfExpr        { return ast.NewSelfExpr(nil) }

// binary builds the desugared form of an operator: recv.method(arg).
func binary(recv ast.Expr, method string, arg ast.Expr) *ast.CallExpr {
	m := ast.NewMemberExpr(recv, ast.NewIdentifier(method, nil), nil)
	return ast.NewCallExpr(m, []ast.Expr{arg}, nil)
}

// stubEnv resolves nothing: a refinement predicate references only self and
// literals, so the registry is all the environment it needs.
type stubEnv struct{ reg *builtin.Registry }

func (e stubEnv) Resolve(*ast.Identifier) *ast.ConstDecl            { return nil }
func (e stubEnv) ResolveMember(*ast.MemberExpr) *ast.ConstDecl      { return nil }
func (e stubEnv) ResolveFunc(*ast.Identifier) []*ast.FuncDecl       { return nil }
func (e stubEnv) ResolveFuncMember(*ast.MemberExpr) []*ast.FuncDecl { return nil }
func (e stubEnv) ValueOf(*ast.ConstDecl) *ir.Constant               { return nil }
func (e stubEnv) LookupType(name string) *ir.TypeDef {
	d, _ := e.reg.Lookup(name)
	return d
}
func (e stubEnv) Registry() *builtin.Registry { return e.reg }

// portPredicate is the desugared form of: self >= 1 && self <= 65535.
func portPredicate() ast.Expr {
	return binary(
		binary(selfExpr(), "gteq", intLit("1")),
		"anan",
		binary(selfExpr(), "lteq", intLit("65535")),
	)
}

func TestPredicate(t *testing.T) {
	env := stubEnv{reg: builtin.Default()}
	cases := []struct {
		name string
		self int64
		want bool
	}{
		{"in range", 8080, true},
		{"low boundary", 1, true},
		{"high boundary", 65535, true},
		{"below", 0, false},
		{"above", 70000, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := Predicate(portPredicate(), ir.IntConstant(big.NewInt(tc.self)), nil, env)
			if v == nil || v.Kind != ir.ConstBool {
				t.Fatalf("Predicate = %v, want a bool constant", v)
			}
			if v.Bool != tc.want {
				t.Errorf("Predicate(self=%d) = %t, want %t", tc.self, v.Bool, tc.want)
			}
		})
	}
}

func TestPredicateUnboundSelf(t *testing.T) {
	// Expr never binds self, so a predicate folded through it stays unfoldable.
	if v := Expr(portPredicate(), stubEnv{reg: builtin.Default()}); v != nil {
		t.Errorf("Expr = %v, want nil for an unbound self", v)
	}
}

func TestPredicateNotFoldable(t *testing.T) {
	// An int self carries no type definition (only an enum value does), so a
	// non-intrinsic method on it reaches neither a user-method body nor an
	// intrinsic: the predicate folds to nil.
	pred := binary(selfExpr(), "bogus", intLit("1"))
	if v := Predicate(pred, ir.IntConstant(big.NewInt(1)), nil, stubEnv{reg: builtin.Default()}); v != nil {
		t.Errorf("Predicate = %v, want nil for a non-intrinsic method", v)
	}
}

func TestPredicateNonBool(t *testing.T) {
	// self + 1 folds to an int; deciding what a non-bool predicate means is the
	// caller's business (the semantic layer reports it on the declaration).
	pred := binary(selfExpr(), "add", intLit("1"))
	v := Predicate(pred, ir.IntConstant(big.NewInt(1)), nil, stubEnv{reg: builtin.Default()})
	if v == nil || v.Kind != ir.ConstInt {
		t.Fatalf("Predicate = %v, want an nint constant", v)
	}
	if v.Int.Int64() != 2 {
		t.Errorf("Predicate = %v, want 2", v.Int)
	}
}

// TestRecordFold covers the record literal fold: fields normalize to the
// canonical (name) order, a duplicate initializer keeps the last value, and an
// unfoldable field keeps the whole record from folding to a partial value.
func TestRecordFold(t *testing.T) {
	env := stubEnv{reg: builtin.Default()}
	field := func(name, value string) *ast.FieldInit { return ast.NewFieldInit(name, intLit(value), nil) }
	rec := func(fields ...*ast.FieldInit) *ast.RecordLit { return ast.NewRecordLit("Point", fields, nil) }

	v := Expr(rec(field("y", "2"), field("x", "1")), env)
	if v == nil || v.Kind != ir.ConstRecord {
		t.Fatalf("Expr = %v, want a record constant", v)
	}
	if got := v.String(); got != "{ x: 1, y: 2 }" {
		t.Errorf("record = %s, want { x: 1, y: 2 } (canonical order)", got)
	}

	if got := Expr(rec(field("x", "1"), field("x", "9")), env).String(); got != "{ x: 9 }" {
		t.Errorf("duplicate field = %s, want { x: 9 } (the last value wins)", got)
	}

	if got := Expr(rec(), env).String(); got != "{}" {
		t.Errorf("empty record = %s, want {}", got)
	}

	unresolved := ast.NewFieldInit("x", ast.NewIdentifier("Missing", nil), nil)
	if v := Expr(rec(unresolved), env); v != nil {
		t.Errorf("Expr = %v, want nil for an unfoldable field", v)
	}

	nested := rec(field("x", "1"), ast.NewFieldInit("pos", rec(field("y", "2")), nil))
	if got := Expr(nested, env).String(); got != "{ pos: { y: 2 }, x: 1 }" {
		t.Errorf("nested record = %s, want { pos: { y: 2 }, x: 1 }", got)
	}
}

// strLit builds a string literal expression.
func strLit(value string) *ast.StringLit { return ast.NewStringLit(value, nil) }

// listLit builds a list literal expression from its element expressions.
func listLit(elems ...ast.Expr) *ast.CollectionLit {
	entries := make([]*ast.CollectionEntry, len(elems))
	for i, e := range elems {
		entries[i] = &ast.CollectionEntry{Value: e}
	}
	return ast.NewCollectionLit(entries, nil)
}

// mapLit builds a map literal expression from alternating key, value expressions.
func mapLit(kv ...ast.Expr) *ast.CollectionLit {
	var entries []*ast.CollectionEntry
	for i := 0; i+1 < len(kv); i += 2 {
		entries = append(entries, &ast.CollectionEntry{Key: kv[i], Value: kv[i+1]})
	}
	return ast.NewCollectionLit(entries, nil)
}

// indexGet builds the desugared form of a read coll[i]: coll.get(i).
func indexGet(coll, index ast.Expr) *ast.CallExpr {
	m := ast.NewMemberExpr(coll, ast.NewIdentifier("get", nil), nil)
	return ast.NewCallExpr(m, []ast.Expr{index}, nil)
}

// indexSet builds the desugared form of a write coll[i] = v: coll.set(i, v).
func indexSet(coll, index, value ast.Expr) *ast.CallExpr {
	m := ast.NewMemberExpr(coll, ast.NewIdentifier("set", nil), nil)
	return ast.NewCallExpr(m, []ast.Expr{index, value}, nil)
}

// TestIndexGetFold covers the subscript read fold: an in-range list index and a
// present map key fold to the element, an out-of-range index or an absent key
// fold to an error value (a miss is a value, not an unfoldable result), and a
// non-integer list index does not fold.
func TestIndexGetFold(t *testing.T) {
	env := stubEnv{reg: builtin.Default()}
	list := listLit(intLit("10"), intLit("20"), intLit("30"))
	m := mapLit(strLit("a"), intLit("1"), strLit("b"), intLit("2"))

	cases := []struct {
		name string
		expr ast.Expr
		want string // the folded constant's String(), or "" for "does not fold"
	}{
		{"list in range first", indexGet(list, intLit("0")), "10"},
		{"list in range last", indexGet(list, intLit("2")), "30"},
		{"list out of range high", indexGet(list, intLit("5")), `error("index out of range")`},
		{"list out of range negative", indexGet(list, intLit("-1")), `error("index out of range")`},
		{"empty list", indexGet(listLit(), intLit("0")), `error("index out of range")`},
		{"map key present", indexGet(m, strLit("a")), "1"},
		{"map key present other", indexGet(m, strLit("b")), "2"},
		{"map key absent", indexGet(m, strLit("z")), `error("key not found")`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := Expr(tc.expr, env)
			if tc.want == "" {
				if v != nil {
					t.Fatalf("Expr = %v, want nil (does not fold)", v)
				}
				return
			}
			if v == nil {
				t.Fatalf("Expr = nil, want %s", tc.want)
			}
			if got := v.String(); got != tc.want {
				t.Errorf("Expr = %s, want %s", got, tc.want)
			}
		})
	}
}

// TestIndexSetFold covers the subscript write fold: it returns the new
// collection, leaving the receiver unchanged. A list write replaces the element
// at an in-range index and does not fold past the end; a map write upserts —
// updating an existing key, appending a new one.
func TestIndexSetFold(t *testing.T) {
	env := stubEnv{reg: builtin.Default()}

	t.Run("list replace in range", func(t *testing.T) {
		v := Expr(indexSet(listLit(intLit("1"), intLit("2"), intLit("3")), intLit("0"), intLit("99")), env)
		if v == nil || v.String() != "[99, 2, 3]" {
			t.Fatalf("set = %v, want [99, 2, 3]", v)
		}
	})

	t.Run("list out of range does not fold", func(t *testing.T) {
		if v := Expr(indexSet(listLit(intLit("1")), intLit("9"), intLit("0")), env); v != nil {
			t.Errorf("set = %v, want nil (out of range is reported as index_out_of_range)", v)
		}
	})

	t.Run("map update existing key", func(t *testing.T) {
		v := Expr(indexSet(mapLit(strLit("a"), intLit("1")), strLit("a"), intLit("10")), env)
		if v == nil || v.String() != `["a": 10]` {
			t.Fatalf("set = %v, want [\"a\": 10]", v)
		}
	})

	t.Run("map add new key", func(t *testing.T) {
		v := Expr(indexSet(mapLit(strLit("a"), intLit("1")), strLit("b"), intLit("2")), env)
		if v == nil || v.String() != `["a": 1, "b": 2]` {
			t.Fatalf("set = %v, want [\"a\": 1, \"b\": 2]", v)
		}
	})

	t.Run("receiver unchanged", func(t *testing.T) {
		// The receiver literal folds the same before and after a set on it: the
		// write builds a new collection rather than mutating the original.
		recv := listLit(intLit("1"), intLit("2"))
		_ = Expr(indexSet(recv, intLit("0"), intLit("99")), env)
		if got := Expr(recv, env).String(); got != "[1, 2]" {
			t.Errorf("receiver = %s, want [1, 2] (a set does not mutate it)", got)
		}
	})
}

// TestCollectionKeyEquality covers a map keyed by a composite value (a list or
// a record): get must find the structurally-equal key, and set must upsert it
// rather than append a duplicate. The structural equality constEqual now
// implements for collections and records is what makes the dispatch correct.
func TestCollectionKeyEquality(t *testing.T) {
	env := stubEnv{reg: builtin.Default()}
	listKey := func(a, b string) *ast.CollectionLit { return listLit(intLit(a), intLit(b)) }
	recKey := func(value string) *ast.RecordLit {
		return ast.NewRecordLit("P", []*ast.FieldInit{ast.NewFieldInit("x", intLit(value), nil)}, nil)
	}

	t.Run("list key get present", func(t *testing.T) {
		m := mapLit(listKey("1", "2"), strLit("a"), listKey("3", "4"), strLit("b"))
		if got := Expr(indexGet(m, listKey("1", "2")), env); got == nil || got.Str != "a" {
			t.Errorf("get([1,2]) = %v, want \"a\"", got)
		}
		if got := Expr(indexGet(m, listKey("3", "4")), env); got == nil || got.Str != "b" {
			t.Errorf("get([3,4]) = %v, want \"b\"", got)
		}
	})

	t.Run("list key get absent", func(t *testing.T) {
		m := mapLit(listKey("1", "2"), strLit("a"))
		if got := Expr(indexGet(m, listKey("9", "9")), env); got == nil || got.Kind != ir.ConstError {
			t.Errorf("get([9,9]) = %v, want a key-not-found error", got)
		}
	})

	t.Run("list key set upserts existing", func(t *testing.T) {
		m := mapLit(listKey("1", "2"), strLit("a"))
		got := Expr(indexSet(m, listKey("1", "2"), strLit("b")), env)
		if got == nil || got.String() != `[[1, 2]: "b"]` {
			t.Errorf("set([1,2]) = %v, want a single replaced entry [[1, 2]: \"b\"]", got)
		}
	})

	t.Run("record key get and set", func(t *testing.T) {
		m := mapLit(recKey("1"), strLit("a"))
		if got := Expr(indexGet(m, recKey("1")), env); got == nil || got.Str != "a" {
			t.Errorf("get({x:1}) = %v, want \"a\"", got)
		}
		got := Expr(indexSet(m, recKey("1"), strLit("b")), env)
		if got == nil || got.String() != `[{ x: 1 }: "b"]` {
			t.Errorf("set({x:1}) = %v, want a single replaced entry", got)
		}
	})
}

// TestConstEqual covers the structural equality switch dispatch and map keys
// rely on, across the scalar, composite, and error kinds.
func TestConstEqual(t *testing.T) {
	env := stubEnv{reg: builtin.Default()}
	fold := func(e ast.Expr) *ir.Constant { return Expr(e, env) }
	list := func(xs ...ast.Expr) ast.Expr { return listLit(xs...) }
	rec := func(name, value string) ast.Expr {
		return ast.NewRecordLit("P", []*ast.FieldInit{ast.NewFieldInit(name, intLit(value), nil)}, nil)
	}

	cases := []struct {
		name string
		a, b ast.Expr
		want bool
	}{
		{"equal lists", list(intLit("1"), intLit("2")), list(intLit("1"), intLit("2")), true},
		{"differing element", list(intLit("1"), intLit("2")), list(intLit("1"), intLit("9")), false},
		{"differing length", list(intLit("1")), list(intLit("1"), intLit("2")), false},
		{"nested equal lists", list(list(intLit("1"))), list(list(intLit("1"))), true},
		{"equal records", rec("x", "1"), rec("x", "1"), true},
		{"differing record value", rec("x", "1"), rec("x", "2"), false},
		{"differing record field", rec("x", "1"), rec("y", "1"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, b := fold(tc.a), fold(tc.b)
			if a == nil || b == nil {
				t.Fatalf("operand did not fold: a=%v b=%v", a, b)
			}
			if got := constEqual(a, b); got != tc.want {
				t.Errorf("constEqual = %t, want %t", got, tc.want)
			}
		})
	}

	// An error compares by message, and two differing kinds are never equal.
	if !constEqual(ir.ErrorConstant("x"), ir.ErrorConstant("x")) {
		t.Error("equal error messages should be equal")
	}
	if constEqual(ir.ErrorConstant("x"), ir.ErrorConstant("y")) {
		t.Error("differing error messages should be unequal")
	}
	if constEqual(fold(list(intLit("1"))), ir.IntConstant(big.NewInt(1))) {
		t.Error("a list and an nint are never equal")
	}
}

// TestShortCircuitFold covers the boolean connectives' short-circuit: && with a
// false left folds to false without evaluating its right, and || with a true
// left folds to true the same way — so an unfoldable (or would-not-fold) right
// operand does not block the fold. When the left does not decide the result the
// right is still folded.
func TestShortCircuitFold(t *testing.T) {
	env := stubEnv{reg: builtin.Default()}
	boolLit := func(b bool) ast.Expr { return ast.NewBoolLit(b, nil) }
	// 1 / 0 == 0 — a right operand that does not fold on its own (div by zero).
	deadRHS := binary(binary(intLit("1"), "div", intLit("0")), "eql", intLit("0"))

	cases := []struct {
		name string
		expr ast.Expr
		want string // "" means "does not fold"
	}{
		{"false anan dead", binary(boolLit(false), "anan", deadRHS), "false"},
		{"true oror dead", binary(boolLit(true), "oror", deadRHS), "true"},
		{"true anan true", binary(boolLit(true), "anan", boolLit(true)), "true"},
		{"true anan false", binary(boolLit(true), "anan", boolLit(false)), "false"},
		{"false oror true", binary(boolLit(false), "oror", boolLit(true)), "true"},
		{"false oror false", binary(boolLit(false), "oror", boolLit(false)), "false"},
		// The left does not short-circuit: the right is needed and unfoldable, so
		// the whole expression does not fold.
		{"true anan dead", binary(boolLit(true), "anan", deadRHS), ""},
		{"false oror dead", binary(boolLit(false), "oror", deadRHS), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := Expr(tc.expr, env)
			if tc.want == "" {
				if v != nil {
					t.Fatalf("Expr = %v, want nil (does not fold)", v)
				}
				return
			}
			if v == nil || v.String() != tc.want {
				t.Fatalf("Expr = %v, want %s", v, tc.want)
			}
		})
	}
}

// TestDatetimeMillis covers the datetime literal normalization: ISO instants
// (with and without milliseconds) to UTC epoch milliseconds, offsets
// normalized away, and malformed text folding to nothing.
func TestDatetimeMillis(t *testing.T) {
	cases := []struct {
		text string
		want int64
		ok   bool
	}{
		{"D1970-01-01T00:00:00.000Z", 0, true},
		{"D1970-01-01T00:00:00Z", 0, true},
		{"D1970-01-01T00:00:00.001Z", 1, true},
		{"D1970-01-02T00:00:00.000Z", 24 * 60 * 60 * 1000, true},
		// +09:00 is nine hours behind the same UTC wall clock.
		{"D1970-01-01T09:00:00.000+09:00", 0, true},
		{"D1969-12-31T23:59:59.000Z", -1000, true}, // pre-epoch instants are negative
		{"D1970-13-01T00:00:00.000Z", 0, false},    // month out of range
		{"1970-01-01T00:00:00.000Z", 0, false},     // no D prefix
		{"D", 0, false},
		{"", 0, false},
	}
	for _, c := range cases {
		got, ok := DatetimeMillis(c.text)
		if got != c.want || ok != c.ok {
			t.Errorf("datetimeMillis(%q) = (%d, %v), want (%d, %v)", c.text, got, ok, c.want, c.ok)
		}
	}
}

// TestDurationMillis covers the duration literal totalling: each unit's worth,
// concatenated groups, and the overflow and malformed cases folding to nothing.
func TestDurationMillis(t *testing.T) {
	cases := []struct {
		text string
		want int64
		ok   bool
	}{
		{"1ms", 1, true},
		{"1s", 1000, true},
		{"1m", 60_000, true},
		{"1h", 3_600_000, true},
		{"1d", 86_400_000, true},
		{"1w", 604_800_000, true},
		{"3w4d5h6m7s8ms", 3*604_800_000 + 4*86_400_000 + 5*3_600_000 + 6*60_000 + 7*1000 + 8, true},
		{"90m", 5_400_000, true}, // kept as written; the canonical form is the constant's
		{"0ms", 0, true},
		{"9223372036854775807ms", 9223372036854775807, true}, // exactly int64
		{"9223372036854775808ms", 0, false},                  // one past int64
		{"99999999999999999w", 0, false},                     // a single group overflows
		{"9223372036854775807ms1ms", 0, false},               // the running total overflows
		{"3x", 0, false},
		{"5", 0, false},
		{"", 0, true}, // vacuously zero; the lexer never produces it
	}
	for _, c := range cases {
		got, ok := DurationMillis(c.text)
		if got != c.want || ok != c.ok {
			t.Errorf("durationMillis(%q) = (%d, %v), want (%d, %v)", c.text, got, ok, c.want, c.ok)
		}
	}
}

// TestDatetimeLexEvalParity pins the two layers' datetime validation
// together: the lexer diagnoses a literal exactly when the fold rejects it,
// so a diagnosed literal never silently evaluates and an accepted one never
// silently fails to. The lexer's verdict is read off its diagnostics.
func TestDatetimeLexEvalParity(t *testing.T) {
	cases := []string{
		// Accepted by both.
		"D2009-03-31T23:59:59.000Z",
		"D1970-01-01T00:00:00Z",
		"D2026-06-05T09:00:00.000+09:00",
		"D2026-06-05T09:00:00.000-23:59",
		"D2026-06-05T09:00:00.000+23:59",
		// Rejected by both — including the offsets time.Parse alone would let
		// through (+24:00, +12:60).
		"D2026-06-05T00:00:00.000+24:00",
		"D2026-06-05T00:00:00.000+12:60",
		"D2026-06-05T00:00:00.000-24:00",
		"D2009-13-40T99:99:99.000Z",
		"D2009-02-30T00:00:00.000Z",
		"D2009-03-31T23:59:59.00Z",
	}
	for _, src := range cases {
		file := source.NewFile("p.belt", []byte(src))
		lex := lexer.New(file)
		lex.Tokens()
		lexerAccepts := len(lex.Diagnostics()) == 0
		_, evalAccepts := DatetimeMillis(src)
		if lexerAccepts != evalAccepts {
			t.Errorf("%q: lexer accepts %v, eval accepts %v — the layers disagree", src, lexerAccepts, evalAccepts)
		}
	}
}
