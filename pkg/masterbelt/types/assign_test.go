// This file mirrors assign.go: the assignability rules for union and nominal
// types.
package types

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

func TestAssignableUnion(t *testing.T) {
	reg := builtin.Default()
	union := &ir.Union{Members: []ir.Type{bt("sbyte"), bt("error")}}

	// A union accepts a value of any of its member types.
	if !Assignable(reg, bt("error"), union) {
		t.Errorf("error should be assignable to sbyte | error")
	}
	if !Assignable(reg, bt("sbyte"), union) {
		t.Errorf("sbyte should be assignable to sbyte | error")
	}
	// The default integer adapts to a union's integer member.
	if !Assignable(reg, bt("nint"), union) {
		t.Errorf("nint should be assignable to sbyte | error")
	}
	// A non-member does not flow in.
	if Assignable(reg, bt("string"), union) {
		t.Errorf("string should not be assignable to sbyte | error")
	}

	// A union-typed value flows into a union that accepts every member it
	// may hold — including itself, reordered — and not into a narrower one.
	same := &ir.Union{Members: []ir.Type{bt("error"), bt("sbyte")}}
	if !Assignable(reg, union, same) {
		t.Errorf("sbyte | error should be assignable to error | sbyte")
	}
	wider := &ir.Union{Members: []ir.Type{bt("sbyte"), bt("string"), bt("error")}}
	if !Assignable(reg, union, wider) {
		t.Errorf("sbyte | error should be assignable to sbyte | string | error")
	}
	if Assignable(reg, wider, union) {
		t.Errorf("sbyte | string | error should not be assignable to sbyte | error")
	}
	// A union does not flow into one of its members.
	if Assignable(reg, union, bt("error")) {
		t.Errorf("sbyte | error should not be assignable to error")
	}
}

// TestAssignableNamedUnion checks that a nominal alias of a union behaves like
// the bare union it stands for: a member value flows into the named union, the
// named union flows where its members (bare, or another alias) are expected, and
// a non-member is still rejected. This is the define-a-union-then-consume-it flow
// match relies on.
func TestAssignableNamedUnion(t *testing.T) {
	reg := builtin.Default()
	coin := &ir.Named{Def: &ir.TypeDef{Name: "Coin", Body: &ir.Record{Fields: []ir.Field{{Name: "amount", Type: bt("nint")}}}}}
	level := &ir.Named{Def: &ir.TypeDef{Name: "Level", Body: &ir.Record{Fields: []ir.Field{{Name: "rank", Type: bt("nint")}}}}}
	gem := &ir.Named{Def: &ir.TypeDef{Name: "Gem", Body: &ir.Record{Fields: []ir.Field{{Name: "color", Type: bt("nint")}}}}}
	bare := &ir.Union{Members: []ir.Type{coin, level}}
	named := &ir.Named{Def: &ir.TypeDef{Name: "GameValue", Body: bare}}

	// A member value flows into the named union.
	if !Assignable(reg, coin, named) {
		t.Errorf("Coin should be assignable to the named union GameValue")
	}
	if !Assignable(reg, level, named) {
		t.Errorf("Level should be assignable to the named union GameValue")
	}
	// A non-member does not.
	if Assignable(reg, gem, named) {
		t.Errorf("Gem should not be assignable to GameValue")
	}
	// The named union flows into the bare union it stands for, and back.
	if !Assignable(reg, named, bare) {
		t.Errorf("GameValue should be assignable to Coin | Level")
	}
	if !Assignable(reg, bare, named) {
		t.Errorf("Coin | Level should be assignable to GameValue")
	}
	// And into another alias of the same members.
	named2 := &ir.Named{Def: &ir.TypeDef{Name: "GV2", Body: &ir.Union{Members: []ir.Type{coin, level}}}}
	if !Assignable(reg, named, named2) {
		t.Errorf("GameValue should be assignable to GV2 (same members)")
	}
}

// named wraps name in a nominal type whose underlying type is body, the building
// block of the base-value-into-nominal adaptation matrix below.
func named(name string, body ir.Type) *ir.Named {
	return &ir.Named{Def: &ir.TypeDef{Name: name, Body: body}}
}

// TestAssignableNominalAdaptation is the invariant matrix of F-1: a builtin base
// value adapts to the nominal type that wraps it — for *every* base, not just the
// integer one that always worked — while the nominal regime is preserved: a
// nominal type does not flow back to its base, nor across to a different nominal
// wrapper of the same base, and an enum (which carries no body) admits no base
// value. The integer rows are the established semantics the rest are generalized
// from.
func TestAssignableNominalAdaptation(t *testing.T) {
	reg := builtin.Default()

	// type Names = list<string> / type Scores = map<string, nint>, built as the
	// nominal wrapper over the (builtin) collection application.
	listDef := &ir.TypeDef{Name: "list", Builtin: true, Params: []*ir.TypeParam{{Name: "T"}}}
	mapDef := &ir.TypeDef{Name: "map", Builtin: true, Params: []*ir.TypeParam{{Name: "K"}, {Name: "V"}}}
	listOf := func(el ir.Type) ir.Type { return &ir.App{Def: listDef, Args: []ir.Type{el}} }
	mapOf := func(k, v ir.Type) ir.Type { return &ir.App{Def: mapDef, Args: []ir.Type{k, v}} }

	// One nominal wrapper per builtin base, plus a chained alias and an enum.
	level := named("Level", bt("sbyte"))
	rank := named("Rank", bt("sbyte")) // a *different* nominal over the same base
	tag := named("Tag", bt("string"))
	flag := named("Flag", bt("bool"))
	birthday := named("Birthday", bt("datetime"))
	wait := named("Wait", bt("duration"))
	names := named("Names", listOf(bt("string")))
	scores := named("Scores", mapOf(bt("string"), bt("nint")))
	// type Alias = Tag (a chained nominal alias resolves to the base).
	alias := named("Alias", tag)
	// An enum keeps Body nil — its base is in Enum.Base — so no base value adapts.
	rarity := &ir.Named{Def: &ir.TypeDef{Name: "Rarity", Enum: &ir.EnumDef{Base: "sbyte"}}}

	cases := []struct {
		name     string
		from, to ir.Type
		want     bool
	}{
		// base value -> nominal wrapper: the generalization, true for every base.
		{"nint -> Level", bt("nint"), level, true},
		{"sbyte -> Level", bt("sbyte"), level, true},
		{"string -> Tag", bt("string"), tag, true},
		{"bool -> Flag", bt("bool"), flag, true},
		{"datetime -> Birthday", bt("datetime"), birthday, true},
		{"duration -> Wait", bt("duration"), wait, true},
		{"list<string> -> Names", listOf(bt("string")), names, true},
		{"map<string,nint> -> Scores", mapOf(bt("string"), bt("nint")), scores, true},
		// a default-int element adapts inside the collection too.
		{"list<nint> -> Names(list<string>) fails on element", listOf(bt("nint")), names, false},
		{"string -> Alias (chained nominal)", bt("string"), alias, true},

		// nominal -> base is rejected (the nominal regime).
		{"Level -> sbyte", level, bt("sbyte"), false},
		{"Tag -> string", tag, bt("string"), false},
		{"Flag -> bool", flag, bt("bool"), false},
		{"Names -> list<string>", names, listOf(bt("string")), false},

		// nominal -> a different nominal of the same base is rejected.
		{"Level -> Rank", level, rank, false},
		{"Tag -> Alias", tag, alias, false},

		// a base value does not adapt into an enum (Body nil).
		{"sbyte -> Rarity (enum)", bt("sbyte"), rarity, false},
		{"nint -> Rarity (enum)", bt("nint"), rarity, false},

		// a base of the wrong kind does not adapt.
		{"string -> Level", bt("string"), level, false},
		{"nint -> Tag", bt("nint"), tag, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Assignable(reg, tc.from, tc.to); got != tc.want {
				t.Errorf("Assignable(%s, %s) = %v, want %v", tc.from, tc.to, got, tc.want)
			}
		})
	}
}

// TestAssignableNominalUnionArmOrder pins the arm order: a base value flowing
// into a *named union* (type GameValue = Tag | error) is matched member-wise by
// the union arm, which precedes the nominal-wrap arm — so a string selects Tag
// inside the union rather than being adapted to the whole union body. Were the
// nominal-wrap arm to run first, the named union (Body non-nil) would swallow the
// value through its body and the member-selection semantics would change.
func TestAssignableNominalUnionArmOrder(t *testing.T) {
	reg := builtin.Default()
	tag := named("Tag", bt("string"))
	// type GameValue = Tag | error — a named union over a nominal member.
	gameValue := named("GameValue", &ir.Union{Members: []ir.Type{tag, bt("error")}})

	// A string flows into the named union: the union arm finds Tag (the string
	// adapts to it), exactly as the bare union Tag | error would.
	if !Assignable(reg, bt("string"), gameValue) {
		t.Errorf("string should be assignable to the named union GameValue (= Tag | error)")
	}
	// And it equally flows into the bare union.
	bare := &ir.Union{Members: []ir.Type{tag, bt("error")}}
	if !Assignable(reg, bt("string"), bare) {
		t.Errorf("string should be assignable to Tag | error")
	}
}

// TestSelectUnionMember pins the tagged-union member-selection rule: an exact
// member wins outright (even when others would also accept), a single assignable
// member is chosen, no member is UnionNoMember, and two assignable members with
// no exact tie-break are UnionAmbiguous — the ambiguous_union_member case an
// explicit conversion resolves. A non-union target is UnionNotAUnion.
func TestSelectUnionMember(t *testing.T) {
	reg := builtin.Default()

	// nint | error: an nint literal is exact on nint, an error value exact on
	// error — the V | error / optional path that already type-checks.
	nintErr := &ir.Union{Members: []ir.Type{bt("nint"), bt("error")}}
	if sel, m := SelectUnionMember(reg, bt("nint"), nintErr); sel != UnionUnique || m != nintErr.Members[0] {
		t.Errorf("nint into nint | error: sel=%d member=%v, want unique nint", sel, m)
	}
	if sel, m := SelectUnionMember(reg, bt("error"), nintErr); sel != UnionUnique || m != nintErr.Members[1] {
		t.Errorf("error into nint | error: sel=%d member=%v, want unique error", sel, m)
	}

	// short | error: a default-int literal has one integer member — a single
	// assignable member chosen by exactness (nint adapts to short).
	shortErr := &ir.Union{Members: []ir.Type{bt("short"), bt("error")}}
	if sel, m := SelectUnionMember(reg, bt("nint"), shortErr); sel != UnionUnique || m != shortErr.Members[0] {
		t.Errorf("nint into short | error: sel=%d member=%v, want unique short", sel, m)
	}

	// short | byte and a default-int literal: two integer members, neither exact
	// — ambiguous. An explicit conversion to short makes short exact.
	shortByte := &ir.Union{Members: []ir.Type{bt("short"), bt("byte")}}
	if sel, _ := SelectUnionMember(reg, bt("nint"), shortByte); sel != UnionAmbiguous {
		t.Errorf("nint into short | byte: sel=%d, want ambiguous", sel)
	}
	if sel, m := SelectUnionMember(reg, bt("short"), shortByte); sel != UnionUnique || m != shortByte.Members[0] {
		t.Errorf("short into short | byte: sel=%d member=%v, want unique short", sel, m)
	}

	// A non-member is UnionNoMember; a non-union target is UnionNotAUnion.
	if sel, _ := SelectUnionMember(reg, bt("string"), shortByte); sel != UnionNoMember {
		t.Errorf("string into short | byte: sel=%d, want no member", sel)
	}
	if sel, _ := SelectUnionMember(reg, bt("nint"), bt("error")); sel != UnionNotAUnion {
		t.Errorf("nint into error (not a union): sel=%d, want not-a-union", sel)
	}

	// A record union: a record member is exact by its nominal identity, so a
	// Coin-typed value tags Coin even though Level shares its kind.
	coin := &ir.Named{Def: &ir.TypeDef{Name: "Coin", Body: &ir.Record{Fields: []ir.Field{{Name: "amount", Type: bt("nint")}}}}}
	level := &ir.Named{Def: &ir.TypeDef{Name: "Level", Body: &ir.Record{Fields: []ir.Field{{Name: "rank", Type: bt("nint")}}}}}
	gameValue := &ir.Named{Def: &ir.TypeDef{Name: "GameValue", Body: &ir.Union{Members: []ir.Type{coin, level}}}}
	if sel, m := SelectUnionMember(reg, coin, gameValue); sel != UnionUnique || m != coin {
		t.Errorf("Coin into GameValue: sel=%d member=%v, want unique Coin", sel, m)
	}
}
