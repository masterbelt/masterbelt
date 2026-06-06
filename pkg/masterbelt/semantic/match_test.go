package semantic

import (
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// gameUnion is the shared fixture: two record members and their union.
const gameUnion = "pub type Coin = { amount: int }\npub type Level = { rank: int }\npub type GameValue = Coin | Level\n"

// TestMatchExhaustiveUnionOK checks that a match covering every union member
// analyzes cleanly — no missing_return (return analysis sees the match always
// returns) and no non_exhaustive_match.
func TestMatchExhaustiveUnionOK(t *testing.T) {
	_, diags := analyze(gameUnion + "pub fn d(v: GameValue): string {\n  match v {\n    Coin c -> return \"c\"\n    Level l -> return \"l\"\n  }\n}\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
}

// TestMatchNonExhaustiveUnion checks that a match missing a union member is
// reported, and the missing member is named.
func TestMatchNonExhaustiveUnion(t *testing.T) {
	_, diags := analyze(gameUnion + "pub fn d(v: GameValue): string {\n  match v {\n    Coin c -> return \"c\"\n  }\n}\n")
	if !hasCode(diags, CodeNonExhaustiveMatch) {
		t.Fatalf("want non_exhaustive_match, got %v", codes(diags))
	}
	var msg string
	for _, d := range diags {
		if d.Code == CodeNonExhaustiveMatch {
			msg = d.Message
		}
	}
	if !strings.Contains(msg, "Level") {
		t.Errorf("message should name the missing member Level, got %q", msg)
	}
}

// TestMatchWildcardExhaustive checks that a "_" arm makes a match exhaustive and
// the match returns (the wildcard body returns too).
func TestMatchWildcardExhaustive(t *testing.T) {
	_, diags := analyze(gameUnion + "pub fn c(v: GameValue): int {\n  match v {\n    Coin c -> return c.amount\n    _      -> return 0\n  }\n}\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
}

// TestMatchArmTypeNotInUnion checks that an arm naming a type the union does not
// contain is reported.
func TestMatchArmTypeNotInUnion(t *testing.T) {
	_, diags := analyze(gameUnion + "pub type Gem = { color: int }\npub fn d(v: GameValue): string {\n  match v {\n    Coin c -> return \"c\"\n    Level l -> return \"l\"\n    Gem g -> return \"g\"\n  }\n}\n")
	if !hasCode(diags, CodeArmTypeNotInUnion) {
		t.Fatalf("want arm_type_not_in_union, got %v", codes(diags))
	}
}

// TestMatchDuplicateArm checks that two arms naming the same member type are
// reported.
func TestMatchDuplicateArm(t *testing.T) {
	_, diags := analyze(gameUnion + "pub fn d(v: GameValue): string {\n  match v {\n    Coin c -> return \"c\"\n    Coin c2 -> return \"x\"\n    Level l -> return \"l\"\n  }\n}\n")
	if !hasCode(diags, CodeDuplicateMatchArm) {
		t.Fatalf("want duplicate_match_arm, got %v", codes(diags))
	}
}

// TestMatchAfterWildcardUnreachable checks that a typed arm written after the
// wildcard is reported as unreachable.
func TestMatchAfterWildcardUnreachable(t *testing.T) {
	_, diags := analyze(gameUnion + "pub fn c(v: GameValue): int {\n  match v {\n    Coin c -> return 1\n    _      -> return 0\n    Level l -> return 2\n  }\n}\n")
	if !hasCode(diags, CodeUnreachableArm) {
		t.Fatalf("want unreachable_arm, got %v", codes(diags))
	}
}

// TestMatchNarrowsBinding checks that the arm binding is narrowed to its member
// type: reading a Coin field inside the Coin arm type-checks.
func TestMatchNarrowsBinding(t *testing.T) {
	_, diags := analyze(gameUnion + "pub fn w(v: GameValue): int {\n  match v {\n    Coin c -> return c.amount\n    Level l -> return l.rank\n  }\n}\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
}

// TestMatchArmBindingTypes checks that the lowered match records each arm's
// narrowed member type and binding name — the narrowing the type checker and the
// folder both rely on.
func TestMatchArmBindingTypes(t *testing.T) {
	module, diags := analyze(gameUnion + "pub fn w(v: GameValue): int {\n  match v {\n    Coin c -> return c.amount\n    Level l -> return l.rank\n  }\n}\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	var fn *ir.Function
	for _, f := range module.Funcs {
		if f.Name == "w" {
			fn = f
		}
	}
	if fn == nil || len(fn.Body) != 1 {
		t.Fatalf("function w not lowered to a single statement: %v", fn)
	}
	m, ok := fn.Body[0].(*ir.Match)
	if !ok {
		t.Fatalf("body is %T, want *ir.Match", fn.Body[0])
	}
	if len(m.Arms) != 2 {
		t.Fatalf("got %d arms, want 2", len(m.Arms))
	}
	for i, want := range []struct{ typ, name string }{{"Coin", "c"}, {"Level", "l"}} {
		if got := m.Arms[i].Type.String(); got != want.typ {
			t.Errorf("arm %d type = %q, want %q", i, got, want.typ)
		}
		if m.Arms[i].Name != want.name {
			t.Errorf("arm %d binding = %q, want %q", i, m.Arms[i].Name, want.name)
		}
	}
}

// TestMatchOptionalNull checks the optional (T | null) form: a member arm and a
// null arm cover it exhaustively and the binding narrows.
func TestMatchOptionalNull(t *testing.T) {
	_, diags := analyze(gameUnion + "pub fn a(v: Coin | null, fb: int): int {\n  match v {\n    Coin c -> return c.amount\n    null   -> return fb\n  }\n}\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
}

// TestMatchOptionalMissingNull checks that omitting the null arm of an optional
// is non-exhaustive.
func TestMatchOptionalMissingNull(t *testing.T) {
	_, diags := analyze(gameUnion + "pub fn a(v: Coin | null): int {\n  match v {\n    Coin c -> return c.amount\n  }\n}\n")
	if !hasCode(diags, CodeNonExhaustiveMatch) {
		t.Fatalf("want non_exhaustive_match for a missing null arm, got %v", codes(diags))
	}
}

// TestMatchIndexUnionRecovery checks the E-18 use case: a match over an index
// read recovers V | error with narrowing and is exhaustive.
func TestMatchIndexUnionRecovery(t *testing.T) {
	_, diags := analyze("pub fn f(xs: list<int>, fb: int): int {\n  match xs[0] {\n    int v   -> return v\n    error e -> return fb\n  }\n}\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
}

// TestMatchNonReturningArmFallsThrough checks that a match whose arm does not
// return does not, on its own, satisfy the function's return: the function still
// needs a trailing return, so an exhaustive match with a non-returning arm trips
// missing_return rather than being assumed to return.
func TestMatchNonReturningArmNeedsTrailingReturn(t *testing.T) {
	_, diags := analyze(gameUnion + "pub fn c(v: GameValue): int {\n  match v {\n    Coin c -> { let n = c.amount }\n    Level l -> return l.rank\n  }\n}\n")
	if !hasCode(diags, CodeMissingReturn) {
		t.Fatalf("want missing_return when an arm body does not return, got %v", codes(diags))
	}
}

// TestMatchExhaustiveAllReturnNoTrailing checks the converse: an exhaustive match
// all of whose arms return makes the function return, so no trailing return is
// needed and missing_return does not fire.
func TestMatchExhaustiveAllReturnNoTrailing(t *testing.T) {
	_, diags := analyze(gameUnion + "pub fn c(v: GameValue): int {\n  match v {\n    Coin c -> return c.amount\n    Level l -> return l.rank\n  }\n}\n")
	if hasCode(diags, CodeMissingReturn) {
		t.Fatalf("an exhaustive all-returning match should satisfy the return: %v", codes(diags))
	}
}

// TestMatchFoldsDispatch checks that a match over an index read folds end to end:
// calling firstOr with a non-empty list folds xs[0] to its int and the int arm
// returns it; calling it with an empty list folds xs[0] to an error and the
// error arm returns the fallback. This exercises the value query's type-tag
// dispatch (a ConstInt matching the int arm, a ConstError the error arm) and the
// narrowing of the arm binding (return v reads the narrowed local).
func TestMatchFoldsDispatch(t *testing.T) {
	src := "pub fn firstOr(xs: list<int>, fb: int): int {\n" +
		"  match xs[0] {\n    int v   -> return v\n    error e -> return fb\n  }\n}\n" +
		"const Good = firstOr([10, 20, 30], 0)\n" +
		"const Empty = firstOr([], 7)\n"
	module, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	want := map[string]string{"Good": "10", "Empty": "7"}
	got := map[string]string{}
	for _, c := range module.Consts {
		if _, ok := want[c.Name]; ok {
			if c.Eval == nil {
				t.Fatalf("const %s did not fold", c.Name)
			}
			got[c.Name] = c.Eval.String()
		}
	}
	for name, w := range want {
		if got[name] != w {
			t.Errorf("const %s folded to %q, want %q", name, got[name], w)
		}
	}
}

// evalOrNil returns the folded value of the named const, or nil when it did not
// fold (or could not be found) — without failing the test, the unfolded case the
// soundness guards assert. The program must still analyze cleanly.
func evalOrNil(t *testing.T, src, name string) *ir.Constant {
	t.Helper()
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	for _, c := range m.Consts {
		if c.Name == name {
			return c.Eval
		}
	}
	t.Fatalf("const %s not found", name)
	return nil
}

// TestMatchSameKindMembersNotFolded is the soundness guard for the union-member
// ambiguity: when two arms can back the scrutinee value's kind (two nominal
// int types, Small | Big), a folded value carries no member tag, so the fold
// cannot tell which arm runs — it must not guess. Before the fix the loop chose
// the first kind-matching arm and folded classify(Big(20)) to "small", a wrong
// value with no diagnostic. The fold must leave it undetermined instead.
func TestMatchSameKindMembersNotFolded(t *testing.T) {
	src := "pub type Small = int\npub type Big = int\n" +
		"pub fn classify(v: Small | Big): string {\n  match v {\n    Small s -> return \"small\"\n    Big b -> return \"big\"\n  }\n}\n" +
		"const R = classify(Big(20))\n"
	if v := evalOrNil(t, src, "R"); v != nil {
		t.Errorf("classify(Big(20)) folded to %q; an ambiguous same-kind union must not fold", v.String())
	}
}

// TestMatchSameKindBuiltinMembersNotFolded is the builtin counterpart: int8 and
// int16 both back a ConstInt, so a match over an int8 | int16 cannot decide its
// arm from the value's kind alone and must not fold.
func TestMatchSameKindBuiltinMembersNotFolded(t *testing.T) {
	src := "pub fn classify(v: int8 | int16): string {\n  match v {\n    int8 a  -> return \"a\"\n    int16 b -> return \"b\"\n  }\n}\n" +
		"const R = classify(int16(20))\n"
	if v := evalOrNil(t, src, "R"); v != nil {
		t.Errorf("classify(int16(20)) folded to %q; an ambiguous same-kind union must not fold", v.String())
	}
}

// TestMatchDistinctKindMembersStillFold checks the fix does not over-restrict: an
// int | error union has one arm per kind, so a folded value's kind decides its
// arm unambiguously and the match still folds.
func TestMatchDistinctKindMembersStillFold(t *testing.T) {
	src := "pub fn pick(v: int | error): string {\n  match v {\n    int n   -> return \"int\"\n    error e -> return \"err\"\n  }\n}\n" +
		"const A = pick(7)\n"
	v := evalOrNil(t, src, "A")
	if v == nil || v.String() != "\"int\"" {
		t.Errorf("pick(7) = %v, want \"int\"", v)
	}
}

// TestNamedUnionAssignment checks that a member value flows into a nominal alias
// of a union (type GameValue = Coin | Level) and the named union flows where its
// members are expected — the define-a-union-then-consume-it flow match is built
// for. Before the fix a member assigned to the named union tripped type_mismatch.
func TestNamedUnionAssignment(t *testing.T) {
	cases := map[string]string{
		"member into named-union const":  gameUnion + "const V: GameValue = Coin{ amount: 5 }\n",
		"member into named-union arg":    gameUnion + "pub fn id(v: GameValue): GameValue { return v }\nconst R = id(Coin{ amount: 5 })\n",
		"named union into bare union":    gameUnion + "pub fn w(v: GameValue): Coin | Level { return v }\n",
		"bare union into named union":    gameUnion + "pub fn w(v: Coin | Level): GameValue { return v }\n",
		"named union returned as itself": gameUnion + "pub fn w(v: GameValue): GameValue { return v }\n",
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			if _, diags := analyze(src); len(diags) != 0 {
				t.Errorf("unexpected diagnostics: %v", codes(diags))
			}
		})
	}
}

// TestNonMemberNotAssignableToNamedUnion checks the fix does not over-accept: a
// type that is not a member of the named union is still rejected.
func TestNonMemberNotAssignableToNamedUnion(t *testing.T) {
	src := gameUnion + "pub type Gem = { color: int }\nconst V: GameValue = Gem{ color: 1 }\n"
	if _, diags := analyze(src); !hasCode(diags, CodeTypeMismatch) {
		t.Errorf("want type_mismatch for a non-member assigned to the named union, got %v", codes(diags))
	}
}

// TestMatchNullArmFolds checks that the null arm of an optional folds: calling
// amountOr with null selects the null arm and returns the fallback, and calling
// it with a value selects the member arm. The null value folds to a ConstNull
// that backs only the null arm, so the dispatch is unambiguous.
func TestMatchNullArmFolds(t *testing.T) {
	src := "pub type Coin = { amount: int }\n" +
		"pub fn nameOr(v: int | null, fb: int): int {\n  match v {\n    int n -> return n\n    null  -> return fb\n  }\n}\n" +
		"const Absent = nameOr(null, 7)\n" +
		"const Present = nameOr(5, 7)\n"
	if v := evalOrNil(t, src, "Absent"); v == nil || v.String() != "7" {
		t.Errorf("nameOr(null, 7) = %v, want 7", v)
	}
	if v := evalOrNil(t, src, "Present"); v == nil || v.String() != "5" {
		t.Errorf("nameOr(5, 7) = %v, want 5", v)
	}
}
