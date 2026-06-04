package semantic

import (
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/types"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// TestLoadPrelude checks that the embedded prelude parses, resolves, and
// validates against the standard registry.
func TestLoadPrelude(t *testing.T) {
	surface, defs, err := LoadPrelude(builtin.Default())
	if err != nil {
		t.Fatalf("LoadPrelude: %v", err)
	}
	if len(surface) == 0 {
		t.Fatal("the prelude barrel exports no types")
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
	reg := universe().reg
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

// TestPreludeSurfaceIsTheBarrel checks the prelude-as-a-project story: the
// surface every file implicitly imports is exactly what the manifest's entry
// barrel re-exports, definition objects included.
func TestPreludeSurfaceIsTheBarrel(t *testing.T) {
	if !strings.Contains(string(builtin.PreludeManifest()), "entry = \""+builtin.PreludeEntry+"\"") {
		t.Fatalf("the prelude manifest does not name %s as its entry", builtin.PreludeEntry)
	}

	reg := builtin.Default()
	surface, defs, err := LoadPrelude(reg)
	if err != nil {
		t.Fatalf("LoadPrelude: %v", err)
	}
	byName := make(map[string]*ir.TypeDef, len(defs))
	for _, d := range defs {
		byName[d.Name] = d
	}
	for name, def := range surface {
		if byName[name] != def {
			t.Errorf("surface %q is not the resolved prelude definition", name)
		}
	}
	for _, name := range reg.Names() {
		if _, ok := surface[name]; !ok {
			t.Errorf("the barrel does not re-export registry primitive %q", name)
		}
	}
}

// TestPreludeIsTheUniverseBase: primitives resolve through the universe — a
// file's own declaration shadows a prelude name, and the prelude needs no
// import to be in scope.
func TestPreludeIsTheUniverseBase(t *testing.T) {
	p := buildProgram(map[string]string{
		"main.belt": "const a: int64 = 1\n",
	})
	assertClean(t, p, "main.belt")

	// A file's own type declaration shadows the prelude's.
	p = buildProgram(map[string]string{
		"main.belt": "type int64 = { v: int32 }\nconst a: int64 = 1\n",
	})
	findDiag(t, p, "main.belt", CodeTypeMismatch) // 1 is no record: the local int64 won
}

// TestImportShadowsPrelude: an imported type sits between a file's own
// declarations and the prelude — importing a sibling's int64 wins over the
// builtin one.
func TestImportShadowsPrelude(t *testing.T) {
	p := buildProgram(map[string]string{
		"shadow.belt": "pub type int64 = { v: int32 }\n",
		"main.belt":   "use { int64 } from \"shadow.belt\"\nconst a: int64 = 1\n",
	})
	findDiag(t, p, "main.belt", CodeTypeMismatch) // 1 is no record: the import won
}
