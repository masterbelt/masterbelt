package semantic

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/ir"
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

	// Every registry primitive is declared in the prelude as a builtin (null
	// excepted: it is a keyword, provided lexically rather than declared).
	for _, name := range builtin.Default().Names() {
		if name == "null" {
			continue
		}
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
