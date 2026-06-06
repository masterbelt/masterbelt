package semantic

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/types"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// TestEnumAutoContract checks that an enum's definition opts into comparable and
// orderable automatically — it carries the six comparisons (E-2 §3.4: equality
// by index, order by base value), so a generic bound of either is satisfied by
// an enum without the author writing an impl tag.
func TestEnumAutoContract(t *testing.T) {
	m, diags := analyze("pub enum Rarity {\n  Common\n  Rare\n}\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	def := typeDef(m, "Rarity")
	if def == nil {
		t.Fatal("Rarity not resolved")
	}
	reg := universe().reg
	comparable := universe().prelude["comparable"]
	orderable := universe().prelude["orderable"]
	if comparable == nil || orderable == nil {
		t.Fatal("prelude is missing comparable/orderable")
	}
	if !types.Satisfies(reg, &ir.Named{Def: def}, &ir.Named{Def: comparable}) {
		t.Errorf("enum should satisfy comparable")
	}
	if !types.Satisfies(reg, &ir.Named{Def: def}, &ir.Named{Def: orderable}) {
		t.Errorf("enum should satisfy orderable")
	}
}

// TestNominalEmptyImplOptIn checks a nominal type can opt into comparable with an
// empty impl tag, inheriting the underlying type's comparison methods to satisfy
// conformance (the derived-method conformance rule: conformance counts the whole
// method face, not only directly-declared methods).
func TestNominalEmptyImplOptIn(t *testing.T) {
	src := "pub type Level = int impl comparable {}\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("Level should satisfy comparable via its underlying int; got %v", codes(diags))
	}
	def := typeDef(m, "Level")
	if def == nil {
		t.Fatal("Level not resolved")
	}
	reg := universe().reg
	comparable := universe().prelude["comparable"]
	if !types.Satisfies(reg, &ir.Named{Def: def}, &ir.Named{Def: comparable}) {
		t.Errorf("Level should satisfy comparable")
	}
}
