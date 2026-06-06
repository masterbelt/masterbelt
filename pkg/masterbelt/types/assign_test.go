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
