package semantic

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/types"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// TestLoadPrelude checks that the embedded prelude parses, resolves, and
// validates against the standard registry.
func TestLoadPrelude(t *testing.T) {
	defs, err := LoadPrelude(builtin.Default())
	if err != nil {
		t.Fatalf("LoadPrelude: %v", err)
	}

	byName := make(map[string]*ir.TypeDef, len(defs))
	for _, d := range defs {
		byName[d.Name] = d
	}

	// Every registry primitive is declared in the prelude as a builtin.
	for _, name := range builtin.Default().Names() {
		d, ok := byName[name]
		if !ok {
			t.Errorf("prelude is missing registry primitive %q", name)
			continue
		}
		if !d.Builtin {
			t.Errorf("prelude %q is not a builtin", name)
		}
	}

	// int8 carries its operator methods (add, declared extern).
	if int8 := byName["int8"]; int8 == nil || len(int8.Methods) == 0 {
		t.Fatalf("int8 has no methods: %+v", int8)
	} else {
		var hasAdd bool
		for _, m := range int8.Methods {
			if m.Name == "add" {
				hasAdd = true
				if !m.Extern {
					t.Errorf("int8.add should be extern")
				}
			}
		}
		if !hasAdd {
			t.Errorf("int8 has no add method")
		}
	}

	// snumeric is a union alias, not a builtin.
	if sn, ok := byName["snumeric"]; !ok {
		t.Errorf("snumeric alias not loaded")
	} else if sn.Builtin {
		t.Errorf("snumeric should be a union alias, not a builtin")
	}
}

// TestPreludeIsTheMethodSource checks that, once the prelude is installed, a
// primitive's methods come from the prelude — not from the bootstrap registry —
// so uint (declared without neg, being unsigned) has no neg method even though
// the bootstrap registry shares one integer method set.
func TestPreludeIsTheMethodSource(t *testing.T) {
	reg := universe()
	uintT := &ir.Builtin{Name: "uint"}

	if got := types.MethodResult(reg, uintT, "neg", nil); got != ir.Invalid {
		t.Errorf("uint.neg = %s, want invalid (the prelude's uint has no neg)", got)
	}
	if got := types.MethodResult(reg, uintT, "add", []ir.Type{uintT}).String(); got != "uint" {
		t.Errorf("uint.add(uint) = %s, want uint", got)
	}
	// The bootstrap registry, with no prelude installed, still has neg — so the
	// difference is the prelude's doing.
	if got := types.MethodResult(builtin.Default(), uintT, "neg", nil).String(); got != "uint" {
		t.Errorf("bootstrap uint.neg = %s, want uint", got)
	}
}

// TestPreludeValidationCatchesMissingIntrinsic checks that validation fails when
// a builtin declares an extern method the registry has no intrinsic for.
func TestPreludeValidationCatchesMissingIntrinsic(t *testing.T) {
	reg := builtin.Default()
	bogus := &ir.TypeDef{
		Name:    "int8",
		Builtin: true,
		Methods: []*ir.Method{{Name: "frobnicate", Extern: true}},
	}
	if err := validatePrelude(reg, []*ir.TypeDef{bogus}); err == nil {
		t.Fatal("validatePrelude accepted an extern method with no intrinsic")
	}
}
