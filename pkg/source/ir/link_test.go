package ir

import (
	"reflect"
	"testing"
)

// TestLinkRelinksMasterRowChecks pins that a master's per-row validate check is
// relinked across a text round-trip: its condition carries the same by-name
// references a body does, so Link must re-point them to the module's own
// declarations or the check keeps a detached placeholder.
func TestLinkRelinksMasterRowChecks(t *testing.T) {
	limit := &Const{Name: "Limit", Anchor: "belt:/Limit", Type: &Builtin{Name: "int"}, Value: &IntLiteral{Text: "100"}}
	skill := &TypeDef{
		Name:   "Skill",
		Anchor: "belt:/Skill",
		Master: &MasterDef{
			Row:       &Record{Fields: []Field{{Name: "id", Type: &Builtin{Name: "int"}}}},
			Primary:   []string{"id"},
			RowChecks: []*AssertStmt{{Cond: &Reference{Target: limit}}},
		},
	}
	data, err := (&Module{Consts: []*Const{limit}, Types: []*TypeDef{skill}}).MarshalText()
	if err != nil {
		t.Fatal(err)
	}
	var back Module
	if err := back.UnmarshalText(data); err != nil {
		t.Fatal(err)
	}
	if err := back.Link(Resolver{}); err != nil {
		t.Fatal(err)
	}
	var backLimit *Const
	for _, c := range back.Consts {
		if c.Name == "Limit" {
			backLimit = c
		}
	}
	var master *TypeDef
	for _, td := range back.Types {
		if td.Master != nil {
			master = td
		}
	}
	if backLimit == nil || master == nil || len(master.Master.RowChecks) != 1 {
		t.Fatalf("round-trip lost the constant or the row check")
	}
	ref, ok := master.Master.RowChecks[0].Cond.(*Reference)
	if !ok || ref.Target != backLimit {
		t.Fatalf("row check condition = %#v, want it relinked to the module's Limit const", master.Master.RowChecks[0].Cond)
	}
}

// TestLinkRelinksMasterRowFields pins that a master's row field types are
// relinked after a text round-trip, the way a body's references are: a field
// typed by another declaration is re-pointed to the resolved definition instead
// of being left the by-name placeholder unmarshalling produces.
func TestLinkRelinksMasterRowFields(t *testing.T) {
	rarity := &TypeDef{Name: "Rarity", Anchor: "belt:/Rarity", Enum: &EnumDef{Base: "nint"}}
	skill := &TypeDef{
		Name:   "Skill",
		Anchor: "belt:/Skill",
		Master: &MasterDef{
			Row:     &Record{Fields: []Field{{Name: "rarity", Type: &Named{Def: rarity}}}},
			Primary: []string{"rarity"},
		},
	}
	data, err := (&Module{Types: []*TypeDef{rarity, skill}}).MarshalText()
	if err != nil {
		t.Fatal(err)
	}
	var back Module
	if err := back.UnmarshalText(data); err != nil {
		t.Fatal(err)
	}
	// Unmarshalling makes each Named reference a by-name placeholder; Link
	// re-points it to the module's own declaration. Rarity is in-module, so the
	// relinked field type must be back's Rarity def, not a leftover placeholder.
	if err := back.Link(Resolver{}); err != nil {
		t.Fatal(err)
	}
	var backRarity, master *TypeDef
	for _, td := range back.Types {
		if td.Name == "Rarity" {
			backRarity = td
		}
		if td.Master != nil {
			master = td
		}
	}
	if master == nil || backRarity == nil {
		t.Fatal("round-trip lost a declaration")
	}
	row, ok := master.Master.Row.(*Record)
	if !ok || len(row.Fields) != 1 {
		t.Fatalf("master row = %#v, want a one-field record", master.Master.Row)
	}
	n, ok := row.Fields[0].Type.(*Named)
	if !ok || n.Def != backRarity {
		t.Fatalf("master row field type = %#v, want it relinked to the module's Rarity def", row.Fields[0].Type)
	}
}

// TestLinkRelinksMethodTypeParamBounds pins that a generic method's type-parameter
// bound is relinked across a text round-trip — the method twin of the function
// relink: the bound names another declaration (the interface it constrains to), so
// Link must re-point it to the module's own def or it keeps the by-name placeholder
// unmarshalling produces.
func TestLinkRelinksMethodTypeParamBounds(t *testing.T) {
	numeric := &TypeDef{Name: "Numeric", Anchor: "belt:/Numeric", Interface: &InterfaceDef{}}
	box := &TypeDef{
		Name:   "Box",
		Anchor: "belt:/Box",
		Body:   &Record{Fields: []Field{{Name: "v", Type: &Builtin{Name: "int"}}}},
		Methods: []*Method{{
			Name:       "sum",
			Anchor:     "belt:/Box.sum",
			TypeParams: []*TypeParam{{Name: "T", Bound: &Named{Def: numeric}}},
			Result:     &Builtin{Name: "int"},
		}},
	}
	data, err := (&Module{Types: []*TypeDef{numeric, box}}).MarshalText()
	if err != nil {
		t.Fatal(err)
	}
	var back Module
	if err := back.UnmarshalText(data); err != nil {
		t.Fatal(err)
	}
	if err := back.Link(Resolver{}); err != nil {
		t.Fatal(err)
	}
	var backNumeric, backBox *TypeDef
	for _, td := range back.Types {
		switch td.Name {
		case "Numeric":
			backNumeric = td
		case "Box":
			backBox = td
		}
	}
	if backNumeric == nil || backBox == nil || len(backBox.Methods) != 1 {
		t.Fatal("round-trip lost a declaration or the method")
	}
	tps := backBox.Methods[0].TypeParams
	if len(tps) != 1 {
		t.Fatalf("method type params = %#v, want one", tps)
	}
	n, ok := tps[0].Bound.(*Named)
	if !ok || n.Def != backNumeric {
		t.Fatalf("method bound = %#v, want it relinked to the module's Numeric def", tps[0].Bound)
	}
}

// TestLinkCoversEveryValue drives the relink walk over every value form: the
// linker is an exhaustive switch guarded by a panic, and this pin turns an
// unhandled form into a failing test rather than a runtime crash on the first
// module that carries it. (Minimal instances have nil references, so the
// dispatch is what is exercised — exactly like the interpreter's coverage
// pin.)
func TestLinkCoversEveryValue(t *testing.T) {
	for _, v := range ValueKinds() {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("Link panicked on %T: %v", v, r)
				}
			}()
			m := &Module{Consts: []*Const{{Name: "X", Value: v}}}
			if err := m.Link(Resolver{}); err != nil {
				t.Errorf("Link(%T) = %v", v, err)
			}
		}()
	}
}

// TestLinkCoversEveryStmt is the statement half of the linker pin.
func TestLinkCoversEveryStmt(t *testing.T) {
	for _, s := range StmtKinds() {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("Link panicked on %T: %v", s, r)
				}
			}()
			m := &Module{Funcs: []*Function{{Name: "f", Body: []Stmt{s}}}}
			if err := m.Link(Resolver{}); err != nil {
				t.Errorf("Link(%T) = %v", s, err)
			}
		}()
	}
}

// TestTypeCodecCoversEveryType drives every Type form through the four
// hand-written dispatch switches — heading, marshal, decode, and relink — so
// a form added to the sealed interface without teaching the codec fails here
// instead of panicking on the first module that carries it.
func TestTypeCodecCoversEveryType(t *testing.T) {
	for _, typ := range TypeKinds() {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("the type codec panicked on %T: %v", typ, r)
				}
			}()
			text, err := typ.MarshalText()
			if err != nil {
				t.Errorf("MarshalText(%T): %v", typ, err)
				return
			}
			if len(text) == 0 {
				t.Errorf("MarshalText(%T) is empty", typ)
				return
			}
			m := &Module{Consts: []*Const{{Name: "X", Type: typ}}}
			data, err := m.MarshalText()
			if err != nil {
				t.Errorf("module marshal with a %T: %v", typ, err)
				return
			}
			var back Module
			if err := back.UnmarshalText(data); err != nil {
				t.Errorf("module with a %T does not unmarshal: %v", typ, err)
				return
			}
			if err := back.Link(Resolver{TypeDef: func(string) *TypeDef { return &TypeDef{} }}); err != nil {
				t.Errorf("module with a %T does not link: %v", typ, err)
			}
		}()
	}
}

// TestTypeKindsRegistryComplete asserts TypeKinds() lists exactly the types
// in this package that satisfy the Type interface, discovered by scanning the
// package source for typ() implementers — the same cross-check StmtKinds and
// ValueKinds carry, extended to the hand-written codec's sealed set.
func TestTypeKindsRegistryComplete(t *testing.T) {
	registered := map[string]bool{}
	for _, typ := range TypeKinds() {
		name := reflect.TypeOf(typ).Elem().Name() // *Builtin -> Builtin, *invalid -> invalid
		if registered[name] {
			t.Errorf("TypeKinds() lists %s more than once", name)
		}
		registered[name] = true
	}

	actual := implementersInSource(t, "typ")
	if len(actual) == 0 {
		t.Fatal("found no typ() implementers in the package source; the scan is broken")
	}
	for name := range actual {
		if !registered[name] {
			t.Errorf("type %s implements Type but is missing from TypeKinds() — add it so the codec pins cover it", name)
		}
	}
	for name := range registered {
		if !actual[name] {
			t.Errorf("TypeKinds() lists %s, which does not implement Type in the package source", name)
		}
	}
}
