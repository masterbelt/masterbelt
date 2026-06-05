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

func (e stubEnv) Resolve(*ast.Identifier) *ast.ConstDecl       { return nil }
func (e stubEnv) ResolveMember(*ast.MemberExpr) *ast.ConstDecl { return nil }
func (e stubEnv) ResolveFunc(*ast.Identifier) []*ast.FuncDecl  { return nil }
func (e stubEnv) ValueOf(*ast.ConstDecl) *ir.Constant          { return nil }
func (e stubEnv) Registry() *builtin.Registry                  { return e.reg }

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
			v := Predicate(portPredicate(), ir.IntConstant(big.NewInt(tc.self)), env)
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
	// A user-defined method has no intrinsic: the predicate folds to nil.
	pred := binary(selfExpr(), "bogus", intLit("1"))
	if v := Predicate(pred, ir.IntConstant(big.NewInt(1)), stubEnv{reg: builtin.Default()}); v != nil {
		t.Errorf("Predicate = %v, want nil for a non-intrinsic method", v)
	}
}

func TestPredicateNonBool(t *testing.T) {
	// self + 1 folds to an int; deciding what a non-bool predicate means is the
	// caller's business (the semantic layer reports it on the declaration).
	pred := binary(selfExpr(), "add", intLit("1"))
	v := Predicate(pred, ir.IntConstant(big.NewInt(1)), stubEnv{reg: builtin.Default()})
	if v == nil || v.Kind != ir.ConstInt {
		t.Fatalf("Predicate = %v, want an int constant", v)
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
