package semantic

import (
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// gameUnion is the shared fixture: two record members and their union.
const gameUnion = "pub type Coin = { amount: nint }\npub type Level = { rank: nint }\npub type GameValue = Coin | Level\n"

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
	_, diags := analyze(gameUnion + "pub fn c(v: GameValue): nint {\n  match v {\n    Coin c -> return c.amount\n    _      -> return 0\n  }\n}\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
}

// TestMatchArmTypeNotInUnion checks that an arm naming a type the union does not
// contain is reported.
func TestMatchArmTypeNotInUnion(t *testing.T) {
	_, diags := analyze(gameUnion + "pub type Gem = { color: nint }\npub fn d(v: GameValue): string {\n  match v {\n    Coin c -> return \"c\"\n    Level l -> return \"l\"\n    Gem g -> return \"g\"\n  }\n}\n")
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
	_, diags := analyze(gameUnion + "pub fn c(v: GameValue): nint {\n  match v {\n    Coin c -> return 1\n    _      -> return 0\n    Level l -> return 2\n  }\n}\n")
	if !hasCode(diags, CodeUnreachableArm) {
		t.Fatalf("want unreachable_arm, got %v", codes(diags))
	}
}

// TestMatchNarrowsBinding checks that the arm binding is narrowed to its member
// type: reading a Coin field inside the Coin arm type-checks.
func TestMatchNarrowsBinding(t *testing.T) {
	_, diags := analyze(gameUnion + "pub fn w(v: GameValue): nint {\n  match v {\n    Coin c -> return c.amount\n    Level l -> return l.rank\n  }\n}\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
}

// TestMatchArmBindingTypes checks that the lowered match records each arm's
// narrowed member type and binding name — the narrowing the type checker and the
// folder both rely on.
func TestMatchArmBindingTypes(t *testing.T) {
	module, diags := analyze(gameUnion + "pub fn w(v: GameValue): nint {\n  match v {\n    Coin c -> return c.amount\n    Level l -> return l.rank\n  }\n}\n")
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
	_, diags := analyze(gameUnion + "pub fn a(v: Coin | null, fb: nint): nint {\n  match v {\n    Coin c -> return c.amount\n    null   -> return fb\n  }\n}\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
}

// TestMatchOptionalMissingNull checks that omitting the null arm of an optional
// is non-exhaustive.
func TestMatchOptionalMissingNull(t *testing.T) {
	_, diags := analyze(gameUnion + "pub fn a(v: Coin | null): nint {\n  match v {\n    Coin c -> return c.amount\n  }\n}\n")
	if !hasCode(diags, CodeNonExhaustiveMatch) {
		t.Fatalf("want non_exhaustive_match for a missing null arm, got %v", codes(diags))
	}
}

// TestMatchIndexUnionRecovery checks that a match over an index
// read recovers V | error with narrowing and is exhaustive.
func TestMatchIndexUnionRecovery(t *testing.T) {
	_, diags := analyze("pub fn f(xs: list<nint>, fb: nint): nint {\n  match xs[0] {\n    nint v   -> return v\n    error e -> return fb\n  }\n}\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
}

// TestMatchNonReturningArmFallsThrough checks that a match whose arm does not
// return does not, on its own, satisfy the function's return: the function still
// needs a trailing return, so an exhaustive match with a non-returning arm trips
// missing_return rather than being assumed to return.
func TestMatchNonReturningArmNeedsTrailingReturn(t *testing.T) {
	_, diags := analyze(gameUnion + "pub fn c(v: GameValue): nint {\n  match v {\n    Coin c -> { let n = c.amount }\n    Level l -> return l.rank\n  }\n}\n")
	if !hasCode(diags, CodeMissingReturn) {
		t.Fatalf("want missing_return when an arm body does not return, got %v", codes(diags))
	}
}

// TestMatchExhaustiveAllReturnNoTrailing checks the converse: an exhaustive match
// all of whose arms return makes the function return, so no trailing return is
// needed and missing_return does not fire.
func TestMatchExhaustiveAllReturnNoTrailing(t *testing.T) {
	_, diags := analyze(gameUnion + "pub fn c(v: GameValue): nint {\n  match v {\n    Coin c -> return c.amount\n    Level l -> return l.rank\n  }\n}\n")
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
	src := "pub fn firstOr(xs: list<nint>, fb: nint): nint {\n" +
		"  match xs[0] {\n    nint v   -> return v\n    error e -> return fb\n  }\n}\n" +
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

// TestMatchSameKindMembersFold is the tagged-union completeness case: two arms
// back the scrutinee value's kind (two nominal int types, Small | Big), but the
// value flows in as Big — an explicit conversion that tags it — so the match
// dispatches confidently to the Big arm. Before tagged unions the fold left this
// undetermined for soundness; the tag now lets it fold to the right arm.
func TestMatchSameKindMembersFold(t *testing.T) {
	src := "pub type Small = nint\npub type Big = nint\n" +
		"pub fn classify(v: Small | Big): string {\n  match v {\n    Small s -> return \"small\"\n    Big b -> return \"big\"\n  }\n}\n" +
		"const R = classify(Big(20))\n"
	v := evalOrNil(t, src, "R")
	if v == nil || v.String() != "\"big\"" {
		t.Errorf("classify(Big(20)) = %v, want \"big\" (the tag selects the Big arm)", v)
	}
}

// TestMatchSameKindBuiltinMembersFold is the builtin counterpart: sbyte and short
// both back a ConstInt, but short(20) tags the value short, so the match folds to
// the short arm — the same-kind union the tag disambiguates.
func TestMatchSameKindBuiltinMembersFold(t *testing.T) {
	src := "pub fn classify(v: sbyte | short): string {\n  match v {\n    sbyte a  -> return \"a\"\n    short b -> return \"b\"\n  }\n}\n" +
		"const R = classify(short(20))\n"
	v := evalOrNil(t, src, "R")
	if v == nil || v.String() != "\"b\"" {
		t.Errorf("classify(short(20)) = %v, want \"b\" (the tag selects the short arm)", v)
	}
}

// TestMatchUntaggedSameKindNotFolded is the remaining soundness guard: when a
// same-kind value reaches the match *without* a tag — a function passes its
// own un-narrowed parameter straight through, so no conversion tags it — the fold
// still cannot tell which arm runs and must leave it undetermined. (g's parameter
// w is a bare Small | Big; classify(w) folds inside g only if w carries a tag,
// and a plain forwarded parameter does not.)
func TestMatchUntaggedSameKindNotFolded(t *testing.T) {
	src := "pub type Small = nint\npub type Big = nint\n" +
		"pub fn classify(v: Small | Big): string {\n  match v {\n    Small s -> return \"small\"\n    Big b -> return \"big\"\n  }\n}\n" +
		"pub fn forward(w: Small | Big): string { return classify(w) }\n"
	// forward is not called with a tagging argument here; analyzing it must not
	// crash or mis-fold, and there is no const to over-fold.
	if _, diags := analyze(src); len(diags) != 0 {
		t.Errorf("unexpected diagnostics: %v", codes(diags))
	}
}

// TestMatchDistinctKindMembersStillFold checks the fix does not over-restrict: an
// int | error union has one arm per kind, so a folded value's kind decides its
// arm unambiguously and the match still folds.
func TestMatchDistinctKindMembersStillFold(t *testing.T) {
	src := "pub fn pick(v: nint | error): string {\n  match v {\n    nint n   -> return \"nint\"\n    error e -> return \"err\"\n  }\n}\n" +
		"const A = pick(7)\n"
	v := evalOrNil(t, src, "A")
	if v == nil || v.String() != "\"nint\"" {
		t.Errorf("pick(7) = %v, want \"nint\"", v)
	}
}

// TestAmbiguousUnionMember is the tagged-union ambiguity diagnostic: an nint
// literal flowing into short | byte matches two integer members with no exact
// tie-break, so which member it tags cannot be told and the program must pin it.
// An explicit conversion (short(1)) makes the member exact and clears the error.
func TestAmbiguousUnionMember(t *testing.T) {
	src := "pub type n = short | byte\nconst a: n = 1\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeAmbiguousUnionMember) {
		t.Fatalf("want ambiguous_union_member for an nint literal into short | byte, got %v", codes(diags))
	}
	// A conversion that pins the member resolves it.
	fixed := "pub type n = short | byte\nconst a: n = short(1)\n"
	if _, diags := analyze(fixed); len(diags) != 0 {
		t.Errorf("short(1) should resolve the ambiguity, got %v", codes(diags))
	}
}

// TestExactUnionMemberNotAmbiguous checks the exact-match tie-break does not
// over-fire: a value type-identical to a member is chosen even when another
// member would also accept it. nint | error with an nint literal, and an
// int8-typed value into int8 | int16, both tag by exactness with no diagnostic.
func TestExactUnionMemberNotAmbiguous(t *testing.T) {
	cases := map[string]string{
		"nint literal into nint | error": "pub type n = nint | error\nconst a: n = 1\n",
		"int8 value into int8 | int16":   "pub type n = sbyte | short\npub fn f(v: sbyte): n { return v }\n",
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			if _, diags := analyze(src); hasCode(diags, CodeAmbiguousUnionMember) {
				t.Errorf("exact member should not be ambiguous, got %v", codes(diags))
			}
		})
	}
}

// TestNominalUnionMemberSelection pins the three union-member-selection
// outcomes for a string base flowing into a union carrying a nominal string
// wrapper — the string twin of the integer rules above:
//
//   - Tag | error + "x": Tag is the only assignable member, so "x" tags Tag
//     (the improvement — previously a type_mismatch);
//   - Tag | Tag2 (two wrappers of the same base) + "x": both accept it with no
//     exact tie-break, so ambiguous_union_member, resolved by Tag("x");
//   - string | Tag + "x": "x" is *exact* on the bare string member, so string
//     wins outright and nothing changes (the exactness rule is untouched).
func TestNominalUnionMemberSelection(t *testing.T) {
	// Tag | error: a bare "x" tags Tag, no diagnostic.
	tagged := "pub type Tag = string\npub type u = Tag | error\nconst a: u = \"x\"\n"
	if _, diags := analyze(tagged); len(diags) != 0 {
		t.Errorf("Tag | error + \"x\" should tag Tag cleanly, got %v", codes(diags))
	}

	// Tag | Tag2: two string wrappers, neither exact — ambiguous.
	ambiguous := "pub type Tag = string\npub type Tag2 = string\npub type u = Tag | Tag2\nconst a: u = \"x\"\n"
	if _, diags := analyze(ambiguous); !hasCode(diags, CodeAmbiguousUnionMember) {
		t.Errorf("Tag | Tag2 + \"x\" should be ambiguous_union_member, got %v", codes(diags))
	}
	// An explicit conversion pins the member and clears it.
	fixed := "pub type Tag = string\npub type Tag2 = string\npub type u = Tag | Tag2\nconst a: u = Tag(\"x\")\n"
	if _, diags := analyze(fixed); len(diags) != 0 {
		t.Errorf("Tag(\"x\") should resolve the ambiguity, got %v", codes(diags))
	}

	// string | Tag: the bare string member is exact, so string wins; no ambiguity.
	exact := "pub type Tag = string\npub type u = string | Tag\nconst a: u = \"x\"\n"
	if _, diags := analyze(exact); len(diags) != 0 {
		t.Errorf("string | Tag + \"x\" should pick string by exactness, got %v", codes(diags))
	}
}

// TestNominalUnionTagFoldParity is the nominal-union fold-parity guarantee: when
// the checker tags a bare string into a nominal-string union member (Tag of
// Tag | error), the folder folds the value tagged with the *same* member — so a
// later match dispatches on the member the type layer chose. The value keeps its
// string, carried under the Tag tag, which is what the .ir dump renders as
// eval (Tag) "tagged".
func TestNominalUnionTagFoldParity(t *testing.T) {
	m, diags := analyze("pub type Tag = string\npub type Labeled = Tag | error\nconst Ok: Labeled = \"tagged\"\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	ev := constEval(m, "Ok")
	if ev == nil {
		t.Fatal("Ok did not fold")
	}
	// The folder kept the string value...
	if ev.Kind != ir.ConstString || ev.Str != "tagged" {
		t.Errorf("Ok eval = %v, want string \"tagged\"", ev)
	}
	// ...tagged with the Tag member the checker selected (fold parity).
	tag, ok := ev.UnionTag.(*ir.Named)
	if !ok || tag.Def == nil || tag.Def.Name != "Tag" {
		t.Errorf("Ok union tag = %v, want the Tag member", ev.UnionTag)
	}
}

// TestMatchNarrowingStripsTag checks the arm narrowing drops the union tag: a
// record union value tagged Coin, dispatched to the Coin arm, narrows its binding
// to the bare Coin, so a field read and arithmetic on the payload fold without the
// tag interfering. worth(Coin{amount: 7}) returns the amount (7) plus one (8).
func TestMatchNarrowingStripsTag(t *testing.T) {
	src := gameUnion +
		"pub fn bump(v: GameValue): nint {\n  match v {\n    Coin c  -> return c.amount.add(1)\n    Level l -> return l.rank\n  }\n}\n" +
		"const R = bump(Coin{ amount: 7 })\n"
	v := evalOrNil(t, src, "R")
	if v == nil || v.String() != "8" {
		t.Errorf("bump(Coin{amount: 7}) = %v, want 8 (narrowed Coin payload + 1)", v)
	}
}

// TestMatchTagFlowsThroughChain checks a tag survives a function hop: a tagged
// member value passed through an identity-typed forwarder reaches the match still
// tagged, so the dispatch folds at the far end of the chain. id returns its
// GameValue argument; worth(id(Coin{...})) folds to the Coin amount.
func TestMatchTagFlowsThroughChain(t *testing.T) {
	src := gameUnion +
		"pub fn worth(v: GameValue): nint {\n  match v {\n    Coin c  -> return c.amount\n    Level l -> return l.rank\n  }\n}\n" +
		"pub fn id(v: GameValue): GameValue { return v }\n" +
		"const R = worth(id(Coin{ amount: 42 }))\n"
	v := evalOrNil(t, src, "R")
	if v == nil || v.String() != "42" {
		t.Errorf("worth(id(Coin{amount: 42})) = %v, want 42 (tag flows through id)", v)
	}
}

// TestMatchTagThroughLet checks a tag survives a let binding: a member value
// bound to a union-typed let carries its tag into a match on the let. The whole
// flow folds inside a single body.
func TestMatchTagThroughLet(t *testing.T) {
	src := gameUnion +
		"pub fn pick(): nint {\n  let v: GameValue = Coin{ amount: 9 }\n  match v {\n    Coin c  -> return c.amount\n    Level l -> return l.rank\n  }\n}\n" +
		"const R = pick()\n"
	v := evalOrNil(t, src, "R")
	if v == nil || v.String() != "9" {
		t.Errorf("pick() with a let-bound tagged Coin = %v, want 9", v)
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
	src := gameUnion + "pub type Gem = { color: nint }\nconst V: GameValue = Gem{ color: 1 }\n"
	if _, diags := analyze(src); !hasCode(diags, CodeTypeMismatch) {
		t.Errorf("want type_mismatch for a non-member assigned to the named union, got %v", codes(diags))
	}
}

// TestMatchNullArmFolds checks that the null arm of an optional folds: calling
// amountOr with null selects the null arm and returns the fallback, and calling
// it with a value selects the member arm. The null value folds to a ConstNull
// that backs only the null arm, so the dispatch is unambiguous.
func TestMatchNullArmFolds(t *testing.T) {
	src := "pub type Coin = { amount: nint }\n" +
		"pub fn nameOr(v: nint | null, fb: nint): nint {\n  match v {\n    nint n -> return n\n    null  -> return fb\n  }\n}\n" +
		"const Absent = nameOr(null, 7)\n" +
		"const Present = nameOr(5, 7)\n"
	if v := evalOrNil(t, src, "Absent"); v == nil || v.String() != "7" {
		t.Errorf("nameOr(null, 7) = %v, want 7", v)
	}
	if v := evalOrNil(t, src, "Present"); v == nil || v.String() != "5" {
		t.Errorf("nameOr(5, 7) = %v, want 5", v)
	}
}
