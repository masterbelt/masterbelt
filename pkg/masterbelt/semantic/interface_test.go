package semantic

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/types"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// typeDef returns the resolved type definition of the given name in the module,
// or nil.
func typeDef(m *ir.Module, name string) *ir.TypeDef {
	for _, t := range m.Types {
		if t.Name == name {
			return t
		}
	}
	return nil
}

const foldableSrc = "" +
	"pub interface foldable<K, V> {\n" +
	"  fold<A>(init: A, step: fn(acc: A, key: K, value: V): A): A\n" +
	"  pub count(): int {\n" +
	"    return fold(0, fn(acc, key, value) -> acc + 1)\n" +
	"  }\n" +
	"}\n"

// TestInterfaceResolvesAsTypeDef checks that an interface declaration becomes a
// TypeDef carrying its required and provided method names and the methods
// themselves (so a value typed as the interface resolves them).
func TestInterfaceResolvesAsTypeDef(t *testing.T) {
	m, diags := analyze(foldableSrc)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	def := typeDef(m, "foldable")
	if def == nil || def.Interface == nil {
		t.Fatalf("foldable not resolved as an interface: %+v", m.Types)
	}
	if len(def.Interface.Required) != 1 || def.Interface.Required[0] != "fold" {
		t.Errorf("required = %v, want [fold]", def.Interface.Required)
	}
	if len(def.Interface.Provided) != 1 || def.Interface.Provided[0] != "count" {
		t.Errorf("provided = %v, want [count]", def.Interface.Provided)
	}
	// Both the required and provided members are on the def, so resolution sees
	// them; the provided body lowers to a self-call of the required fold.
	if len(def.Methods) != 2 {
		t.Fatalf("methods = %d, want 2", len(def.Methods))
	}
}

// TestConformingImplOK checks that a type declaring the required method of an
// interface it impls produces no diagnostic, and records the impl.
func TestConformingImplOK(t *testing.T) {
	src := foldableSrc +
		"pub type Bag<T> = list<T> impl foldable<int, T> {\n" +
		"  fold<A>(init: A, step: fn(acc: A, key: int, value: T): A): A {\n" +
		"    return init\n" +
		"  }\n" +
		"}\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	bag := typeDef(m, "Bag")
	if bag == nil || len(bag.Impls) != 1 {
		t.Fatalf("Bag impls = %+v, want one (foldable)", bag)
	}
}

// TestMissingRequiredMethod checks that a type that impls an interface but does
// not declare its required method is reported.
func TestMissingRequiredMethod(t *testing.T) {
	src := foldableSrc +
		"pub type Bag<T> = list<T> impl foldable<int, T> {\n" +
		"  pub size(): int {\n" +
		"    return 0\n" +
		"  }\n" +
		"}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeMissingRequiredMethod) {
		t.Fatalf("want missing_required_method, got %v", codes(diags))
	}
}

// TestProvidedMethodOnImplementor checks that a type implementing an interface
// gains the interface's provided methods (count) through method resolution,
// even though it declares only the required fold. The required method is found
// directly; the provided one is reached through the impl'd interface.
func TestProvidedMethodOnImplementor(t *testing.T) {
	src := foldableSrc +
		"pub type Bag<T> = list<T> impl foldable<int, T> {\n" +
		"  fold<A>(init: A, step: fn(acc: A, key: int, value: T): A): A {\n" +
		"    return init\n" +
		"  }\n" +
		"}\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	bag := typeDef(m, "Bag")
	if bag == nil {
		t.Fatal("Bag not resolved")
	}
	reg := builtin.Default()
	recv := &ir.App{Def: bag, Args: []ir.Type{&ir.Builtin{Name: "int"}}}
	if _, _, ok := types.Candidates(reg, recv, "fold"); !ok {
		t.Error("fold (required, declared directly) does not resolve on Bag")
	}
	if _, _, ok := types.Candidates(reg, recv, "count"); !ok {
		t.Error("count (provided by foldable) does not resolve on Bag through the impl")
	}
}

// TestInterfaceAsParamType checks that an interface written in a parameter
// position types the parameter (not invalid) and that calling an interface
// method on it resolves.
func TestInterfaceAsParamType(t *testing.T) {
	src := foldableSrc +
		"pub fn total(c: foldable<int, int>): int {\n" +
		"  return c.fold(0, fn(acc, key, value) -> acc + value)\n" +
		"}\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	var total *ir.Function
	for _, f := range m.Funcs {
		if f.Name == "total" {
			total = f
		}
	}
	if total == nil || len(total.Params) != 1 {
		t.Fatalf("total not resolved: %+v", m.Funcs)
	}
	if got := total.Params[0].Type.String(); got != "foldable<int, int>" {
		t.Errorf("param type = %s, want foldable<int, int>", got)
	}
}

// TestPreludeFoldable checks that the prelude declares foldable, re-exports it
// from the barrel, and that list and map opt into it — so a list<int> resolves
// the required fold and the provided count/map through the impl.
func TestPreludeFoldable(t *testing.T) {
	reg := builtin.Default()
	surface, _, err := LoadPrelude(reg)
	if err != nil {
		t.Fatalf("LoadPrelude: %v", err)
	}
	fold, ok := surface["foldable"]
	if !ok || fold.Interface == nil {
		t.Fatalf("foldable is not on the prelude surface as an interface")
	}
	reg.Install([]*ir.TypeDef{fold, surface["list"], surface["map"]})

	listDef := surface["list"]
	if listDef == nil {
		t.Fatal("list not on the surface")
	}
	listOfInt := &ir.App{Def: listDef, Args: []ir.Type{&ir.Builtin{Name: "int"}}}
	// fold is the required method list declares; count/any/all are foldable's
	// provided methods reached through the impl; map is list's own inherent
	// method (it shadows nothing here).
	for _, name := range []string{"fold", "count", "any", "all", "map"} {
		if _, _, ok := types.Candidates(reg, listOfInt, name); !ok {
			t.Errorf("list<int> does not resolve %q (fold + provided foldable methods)", name)
		}
	}

	mapDef := surface["map"]
	mapOfStrInt := &ir.App{Def: mapDef, Args: []ir.Type{&ir.Builtin{Name: "string"}, &ir.Builtin{Name: "int"}}}
	for _, name := range []string{"fold", "count", "any", "all"} {
		if _, _, ok := types.Candidates(reg, mapOfStrInt, name); !ok {
			t.Errorf("map<string, int> does not resolve %q", name)
		}
	}
}

// TestPreludeFoldableListReturning checks that foldable's list-returning provided
// methods — map, filter, keys, values — resolve to the right list<...> type on an
// implementor, instantiating both the receiver's K/V and the method's own U. The
// methods' bodies type-check during LoadPrelude (a body that did not would fail
// the load), so reaching this far already exercises the implementation; here we
// pin the result types the caller sees.
func TestPreludeFoldableListReturning(t *testing.T) {
	reg := builtin.Default()
	surface, defs, err := LoadPrelude(reg)
	if err != nil {
		t.Fatalf("LoadPrelude: %v", err)
	}
	reg.Install(defs)

	intT := &ir.Builtin{Name: "int"}
	strT := &ir.Builtin{Name: "string"}
	boolT := &ir.Builtin{Name: "bool"}

	mapOfStrInt := &ir.App{Def: surface["map"], Args: []ir.Type{strT, intT}}
	// keys/values draw on the receiver's K and V; filter on V; map on its own U
	// (bound here from f: fn(value: int): bool).
	cases := []struct {
		method string
		args   []ir.Type
		want   string
	}{
		{"keys", nil, "list<string>"},
		{"values", nil, "list<int>"},
		{"filter", []ir.Type{&ir.Func{Params: []ir.Type{intT}, Result: boolT}}, "list<int>"},
		{"map", []ir.Type{&ir.Func{Params: []ir.Type{intT}, Result: boolT}}, "list<bool>"},
	}
	for _, c := range cases {
		if got := types.MethodResult(reg, mapOfStrInt, c.method, c.args).String(); got != c.want {
			t.Errorf("map<string, int>.%s = %s, want %s", c.method, got, c.want)
		}
	}
}

// TestNotAnInterfaceTag checks that an impl tag naming a non-interface type is
// reported.
func TestNotAnInterfaceTag(t *testing.T) {
	src := "pub type Pair = int8\n" +
		"pub type Bag = list<int> impl Pair {\n" +
		"  pub size(): int {\n" +
		"    return 0\n" +
		"  }\n" +
		"}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeNotAnInterface) {
		t.Fatalf("want not_an_interface, got %v", codes(diags))
	}
}
