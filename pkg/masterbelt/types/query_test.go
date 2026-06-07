// This file mirrors query.go: the for-loop element-type query.
package types

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// TestForElement checks the loop-variable type a for reads from each foldable
// shape: a type that opts in at its definition site (a user Bag), the foldable
// interface written in a requirement position, and a bounded type parameter whose
// bound is the interface. of reads the value type V, in the key type K; a
// non-foldable type is not iterable. (The prelude list/map opt-in path is covered
// end to end by the semantic for tests.)
func TestForElement(t *testing.T) {
	reg := builtin.Default()

	foldable := &ir.TypeDef{
		Name:      "foldable",
		Interface: &ir.InterfaceDef{Required: []string{"fold"}},
		Params:    []*ir.TypeParam{{Name: "K"}, {Name: "V"}},
	}
	// Bag = list<int> opts into foldable<int, string> (arbitrary distinct K/V so the
	// of/in distinction is visible).
	bagImpl := &ir.App{Def: foldable, Args: []ir.Type{bt("nint"), bt("string")}}
	bag := &ir.TypeDef{Name: "Bag", Body: bt("nint"), Impls: []ir.Type{bagImpl}}

	cases := []struct {
		name string
		typ  ir.Type
		of   bool
		want string // the element type, or "" when not iterable
	}{
		// A user type that opts in: of reads V, in reads K.
		{"user opt-in of", &ir.Named{Def: bag}, true, "string"},
		{"user opt-in in", &ir.Named{Def: bag}, false, "nint"},
		// The interface in a requirement position (c: foldable<int, string>).
		{"interface of", &ir.App{Def: foldable, Args: []ir.Type{bt("nint"), bt("string")}}, true, "string"},
		{"interface in", &ir.App{Def: foldable, Args: []ir.Type{bt("nint"), bt("string")}}, false, "nint"},
		// A bounded type parameter whose bound is the interface.
		{"bounded typevar of", &ir.TypeVar{Name: "T", Bound: &ir.App{Def: foldable, Args: []ir.Type{bt("nint"), bt("string")}}}, true, "string"},
		{"bounded typevar in", &ir.TypeVar{Name: "T", Bound: &ir.App{Def: foldable, Args: []ir.Type{bt("nint"), bt("string")}}}, false, "nint"},
		// A non-foldable type is not iterable.
		{"non-foldable", bt("nint"), true, ""},
		{"unbounded typevar", &ir.TypeVar{Name: "T"}, true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			elem, ok := ForElement(reg, tc.typ, tc.of)
			if tc.want == "" {
				if ok {
					t.Errorf("ForElement(%s) = (%s, true), want not iterable", tc.typ, elem)
				}
				return
			}
			if !ok {
				t.Fatalf("ForElement(%s) not iterable, want %s", tc.typ, tc.want)
			}
			if got := elem.String(); got != tc.want {
				t.Errorf("ForElement(%s) = %s, want %s", tc.typ, got, tc.want)
			}
		})
	}
}

// TestUnifyAndAssignTypeVar checks two distinct TypeVar instances of the same
// generic parameter unify and assign to each other — the basis for a generic
// signature whose receiver and a self-typed argument are both T (max<T>(a: T,
// b: T): T). Each type position resolves to a fresh TypeVar pointer carrying the
// bound, so the relation must be by name, not identity. A different name does
// not unify.
func TestUnifyAndAssignTypeVar(t *testing.T) {
	reg := builtin.Default()
	comparable := &ir.TypeDef{Name: "comparable", Interface: &ir.InterfaceDef{}}
	a := &ir.TypeVar{Name: "T", Bound: &ir.Named{Def: comparable}}
	b := &ir.TypeVar{Name: "T", Bound: &ir.Named{Def: comparable}}
	other := &ir.TypeVar{Name: "U"}

	if a == b {
		t.Fatal("test setup: a and b must be distinct pointers")
	}
	if got := Unify(reg, a, b); got != a {
		t.Errorf("Unify(T, T) = %v, want the T", got)
	}
	if !Assignable(reg, a, b) {
		t.Error("Assignable(T, T) = false, want true")
	}
	if got := Unify(reg, a, other); got != ir.Invalid {
		t.Errorf("Unify(T, U) = %v, want Invalid", got)
	}
	if Assignable(reg, a, other) {
		t.Error("Assignable(T, U) = true, want false")
	}
}
