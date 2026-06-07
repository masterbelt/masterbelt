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
	if sbyte := byName["sbyte"]; sbyte == nil || len(sbyte.Methods) == 0 {
		t.Fatalf("sbyte has no methods: %+v", sbyte)
	} else {
		var hasAdd bool
		for _, m := range sbyte.Methods {
			if m.Name == "add" {
				hasAdd = true
				if !m.Extern {
					t.Errorf("sbyte.add should be extern")
				}
			}
		}
		if !hasAdd {
			t.Errorf("sbyte has no add method")
		}
	}
}

// TestPreludeBoundsMatchRegistry checks that the `= builtin` associated
// constants the prelude declares — the integer bounds Max/Min — fold to exactly
// the registry's value range, so the in-language declaration and the native
// descriptor cannot drift. Each sized integer carries both; the arbitrary-
// precision int/uint declare none (they have no fixed range).
func TestPreludeBoundsMatchRegistry(t *testing.T) {
	reg := builtin.Default()
	_, defs, err := LoadPrelude(reg)
	if err != nil {
		t.Fatalf("LoadPrelude: %v", err)
	}
	byName := make(map[string]*ir.TypeDef, len(defs))
	for _, d := range defs {
		byName[d.Name] = d
	}

	sized := []string{"sbyte", "short", "int", "long", "byte", "ushort", "uint", "ulong"}
	for _, name := range sized {
		d := byName[name]
		if d == nil {
			t.Errorf("prelude is missing %q", name)
			continue
		}
		native, _ := reg.Native(name)
		lo, hi := native.Bounds()
		got := map[string]*ir.Constant{}
		for _, c := range d.Consts {
			if !c.Builtin {
				t.Errorf("%s.%s: a bound should be a `= builtin` const", name, c.Name)
			}
			got[c.Name] = c.Value
		}
		if v := got["Max"]; v == nil || v.Kind != ir.ConstInt || v.Int.Cmp(hi) != 0 {
			t.Errorf("%s.Max = %v, want %v", name, v, hi)
		}
		if v := got["Min"]; v == nil || v.Kind != ir.ConstInt || v.Int.Cmp(lo) != 0 {
			t.Errorf("%s.Min = %v, want %v", name, v, lo)
		}
	}

	// The arbitrary-precision integers have no fixed range, so they declare no
	// bounds (a written nint.Max would resolve to no value, which the
	// agreement test rejects).
	for _, name := range []string{"nint", "nuint"} {
		if d := byName[name]; d != nil && len(d.Consts) != 0 {
			t.Errorf("%s should declare no associated constants, has %d", name, len(d.Consts))
		}
	}
}

// TestPreludeIsTheMethodSource checks that, once the prelude is installed, a
// primitive's methods come from the prelude — not from the bootstrap registry —
// so uint (declared without neg, being unsigned) has no neg method even though
// the bootstrap registry shares one integer method set.
func TestPreludeIsTheMethodSource(t *testing.T) {
	reg := universe().reg
	uintT := &ir.Builtin{Name: "nuint"}

	if got := types.MethodResult(reg, uintT, "neg", nil); got != ir.Invalid {
		t.Errorf("nuint.neg = %s, want invalid (the prelude's nuint has no neg)", got)
	}
	if got := types.MethodResult(reg, uintT, "add", []ir.Type{uintT}).String(); got != "nuint" {
		t.Errorf("nuint.add(nuint) = %s, want nuint", got)
	}
	// The bootstrap registry, with no prelude installed, still has neg — so the
	// difference is the prelude's doing.
	if got := types.MethodResult(builtin.Default(), uintT, "neg", nil).String(); got != "nuint" {
		t.Errorf("bootstrap nuint.neg = %s, want nuint", got)
	}
}

// TestPreludeValidationCatchesMissingIntrinsic checks that validation fails when
// a builtin declares an extern method the registry has no intrinsic for.
func TestPreludeValidationCatchesMissingIntrinsic(t *testing.T) {
	reg := builtin.Default()
	bogus := &ir.TypeDef{
		Name:    "sbyte",
		Builtin: true,
		Methods: []*ir.Method{{Name: "frobnicate", Extern: true}},
	}
	if err := validatePrelude(reg, []*ir.TypeDef{bogus}); err == nil {
		t.Fatal("validatePrelude accepted an extern method with no intrinsic")
	}
}

func TestPreludeValidationCatchesMissingOverloadIntrinsic(t *testing.T) {
	// The check is per overload arm, not per name: duration.add has
	// kind-keyed intrinsics for a duration and a datetime argument, so a
	// declared third arm (add(n: int)) with no implementation must fail even
	// though the name has intrinsics.
	reg := builtin.Default()
	withDuration := func(d *ir.TypeDef) []*ir.TypeDef {
		// Every other registry primitive declared minimally, so the
		// declaration check stays out of the way of the arm check.
		defs := []*ir.TypeDef{d}
		for _, name := range reg.Names() {
			if name != d.Name {
				defs = append(defs, &ir.TypeDef{Name: name, Builtin: true})
			}
		}
		return defs
	}

	bogus := &ir.TypeDef{
		Name:    "duration",
		Builtin: true,
		Methods: []*ir.Method{{
			Name:   "add",
			Extern: true,
			Params: []ir.Param{{Name: "n", Type: &ir.Builtin{Name: "nint"}}},
		}},
	}
	if err := validatePrelude(reg, withDuration(bogus)); err == nil {
		t.Fatal("validatePrelude accepted an overload arm with no intrinsic for its argument kinds")
	}
	// The genuinely implemented arms still validate.
	ok := &ir.TypeDef{
		Name:    "duration",
		Builtin: true,
		Methods: []*ir.Method{
			{Name: "add", Extern: true, Params: []ir.Param{{Name: "other", Type: &ir.SelfType{}}}},
			{Name: "add", Extern: true, Params: []ir.Param{{Name: "at", Type: &ir.Builtin{Name: "datetime"}}}},
		},
	}
	if err := validatePrelude(reg, withDuration(ok)); err != nil {
		t.Fatalf("validatePrelude rejected the implemented overload arms: %v", err)
	}
}

// TestPreludeSurfaceIsTheExports checks the prelude-as-one-file story: the
// surface every file implicitly imports is exactly what the prelude file
// exports, definition objects included.
func TestPreludeSurfaceIsTheExports(t *testing.T) {
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
			t.Errorf("the prelude does not export registry primitive %q", name)
		}
	}
}

// TestPreludeIsTheUniverseBase: primitives resolve through the universe — a
// file's own declaration shadows a prelude name, and the prelude needs no
// import to be in scope.
func TestPreludeIsTheUniverseBase(t *testing.T) {
	p := buildProgram(map[string]string{
		"main.belt": "const a: long = 1\n",
	})
	assertClean(t, p, "main.belt")

	// A file's own type declaration shadows the prelude's.
	p = buildProgram(map[string]string{
		"main.belt": "type long = { v: int }\nconst a: long = 1\n",
	})
	findDiag(t, p, "main.belt", CodeTypeMismatch) // 1 is no record: the local int64 won
}

// TestImportShadowsPrelude: an imported type sits between a file's own
// declarations and the prelude — importing a sibling's int64 wins over the
// builtin one.
func TestImportShadowsPrelude(t *testing.T) {
	p := buildProgram(map[string]string{
		"shadow.belt": "pub type long = { v: int }\n",
		"main.belt":   "use { long } from \"shadow.belt\"\nconst a: long = 1\n",
	})
	findDiag(t, p, "main.belt", CodeTypeMismatch) // 1 is no record: the import won
}

// TestPreludeValidationChecksEffectfulExtern checks the effectful branch of
// the validation: an effectful extern is backed by the registry's
// effectful-native record (never an intrinsic), the declared effects must
// match the recorded ones, and an unrecorded one fails.
func TestPreludeValidationChecksEffectfulExtern(t *testing.T) {
	reg := builtin.Default()
	withDatetime := func(d *ir.TypeDef) []*ir.TypeDef {
		defs := []*ir.TypeDef{d}
		for _, name := range reg.Names() {
			if name != d.Name {
				defs = append(defs, &ir.TypeDef{Name: name, Builtin: true})
			}
		}
		return defs
	}

	// The recorded native validates.
	ok := &ir.TypeDef{
		Name:    "datetime",
		Builtin: true,
		Methods: []*ir.Method{{Name: "now", Extern: true, Kind: ir.MethodStatic, Effects: []string{"nondet"}}},
	}
	if err := validatePrelude(reg, withDatetime(ok)); err != nil {
		t.Fatalf("validatePrelude rejected the recorded effectful native: %v", err)
	}

	// An effectful extern the registry records nothing for fails.
	unrecorded := &ir.TypeDef{
		Name:    "datetime",
		Builtin: true,
		Methods: []*ir.Method{{Name: "sleep", Extern: true, Kind: ir.MethodStatic, Effects: []string{"io"}}},
	}
	if err := validatePrelude(reg, withDatetime(unrecorded)); err == nil {
		t.Fatal("validatePrelude accepted an effectful extern with no registry record")
	}

	// A declared effect list that disagrees with the record fails.
	wrongEffects := &ir.TypeDef{
		Name:    "datetime",
		Builtin: true,
		Methods: []*ir.Method{{Name: "now", Extern: true, Kind: ir.MethodStatic, Effects: []string{"io"}}},
	}
	if err := validatePrelude(reg, withDatetime(wrongEffects)); err == nil {
		t.Fatal("validatePrelude accepted an effectful extern whose effects disagree with the record")
	}
}
