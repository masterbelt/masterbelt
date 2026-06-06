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
