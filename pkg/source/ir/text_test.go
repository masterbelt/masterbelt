package ir

import (
	"bytes"
	"math/big"
	"reflect"
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/source/internal/treetext"
)

// TestFieldSensitivity is the field-sensitivity gate over the IR: for every
// tree struct, mutating any exported, non-excluded field must change the
// marshal output — the executable definition of "every field appears in the
// format". An overload-resolution gap was once exactly a field the curated
// dump did not render; this pin makes that class of blind spot fail in CI.
func TestFieldSensitivity(t *testing.T) {
	for _, probe := range treeStructs {
		st := reflect.TypeOf(probe).Elem()
		t.Run(st.Name(), func(t *testing.T) {
			base := render(t, reflect.New(st))
			for i := range st.NumField() {
				field := st.Field(i)
				if !field.IsExported() || field.Tag.Get("tree") != "" {
					continue
				}
				mut := reflect.New(st)
				mutate(t, mut.Elem().Field(i), st.Name()+"."+field.Name)
				if got := render(t, mut); bytes.Equal(got, base) {
					t.Errorf("mutating %s.%s does not change the marshal output — the field is missing from the format", st.Name(), field.Name)
				}
			}
		})
	}
}

// TestTypeFieldSensitivity is the hand-written type codec's half of the
// field-sensitivity pin: every field of every Type form must appear in its
// marshal.
func TestTypeFieldSensitivity(t *testing.T) {
	cases := map[string][2]Type{
		"Builtin.Name":   {&Builtin{}, &Builtin{Name: "x"}},
		"Named.Def":      {&Named{}, &Named{Def: &TypeDef{Name: "x"}}},
		"Union.Members":  {&Union{}, &Union{Members: []Type{&Builtin{Name: "x"}}}},
		"Record.Fields":  {&Record{}, &Record{Fields: []Field{{Name: "x"}}}},
		"Record.Field.T": {&Record{Fields: []Field{{Name: "x"}}}, &Record{Fields: []Field{{Name: "x", Type: &Builtin{Name: "b"}}}}},
		"Func.Params":    {&Func{}, &Func{Params: []Type{&Builtin{Name: "x"}}}},
		"Func.Result":    {&Func{}, &Func{Result: &Builtin{Name: "x"}}},
		"TypeVar.Name":   {&TypeVar{}, &TypeVar{Name: "x"}},
		"TypeVar.Bound":  {&TypeVar{}, &TypeVar{Bound: &Builtin{Name: "x"}}},
		"App.Def":        {&App{}, &App{Def: &TypeDef{Name: "x"}}},
		"App.Args":       {&App{}, &App{Args: []Type{&Builtin{Name: "x"}}}},
	}
	for name, pair := range cases {
		a, err := pair[0].MarshalText()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		b, err := pair[1].MarshalText()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if bytes.Equal(a, b) {
			t.Errorf("%s does not change the marshal output", name)
		}
	}
}

// TestExplicitExclusions pins the manifest of deliberate no-output decisions:
// the Syntax backpointers (the detached contract), plus Assert.CondGraph — the
// resolved condition graph kept in memory for the reachability lint and
// find-references, never serialized because an assert's text contract is its
// outcome (Cond, Eval, Diagram). A tree:"-" added to anything else must update
// this pin with a format decision.
func TestExplicitExclusions(t *testing.T) {
	if len(treeExcluded) == 0 {
		t.Fatal("treeExcluded is empty; the IR excludes its Syntax backpointers")
	}
	deliberate := map[string]bool{
		"Syntax":          true,
		"EnumSyntax":      true,
		"InterfaceSyntax": true,
		"CondGraph":       true, // Assert: in-memory condition graph; the outcome is the contract
	}
	for name, fields := range treeExcluded {
		for _, field := range fields {
			if !deliberate[field] {
				t.Errorf("%s.%s is excluded from the format but is not a deliberate exclusion — make the decision explicit here", name, field)
			}
		}
	}
}

// render marshals a tree struct through the generated dispatcher.
func render(t *testing.T, v reflect.Value) []byte {
	t.Helper()
	var w treetext.Writer
	ok, err := writeTree(&w, v.Interface(), 0)
	if err != nil {
		t.Fatalf("writeTree(%T): %v", v.Interface(), err)
	}
	if !ok {
		t.Fatalf("writeTree(%T): not a known tree struct", v.Interface())
	}
	return w.Bytes()
}

// mutate sets a field to a value distinct from its zero value, recursing only
// one level: node-valued fields get a minimal instance of an implementer.
func mutate(t *testing.T, f reflect.Value, name string) {
	t.Helper()
	switch f.Kind() {
	case reflect.Bool:
		f.SetBool(true)
	case reflect.String:
		f.SetString("x")
	case reflect.Int, reflect.Int64:
		f.SetInt(1)
	case reflect.Slice:
		elem := f.Type().Elem()
		if elem.Kind() == reflect.String {
			f.Set(reflect.ValueOf([]string{"x"}))
			return
		}
		s := reflect.MakeSlice(f.Type(), 1, 1)
		if elem.Kind() == reflect.Struct {
			// A value-struct element is fine at its zero value: the element's
			// presence alone must change the output.
			f.Set(s)
			return
		}
		if elem == reflect.TypeOf((*Type)(nil)).Elem() {
			s.Index(0).Set(reflect.ValueOf(Type(&Builtin{Name: "x"})))
			f.Set(s)
			return
		}
		s.Index(0).Set(instanceOf(t, elem, name))
		f.Set(s)
	case reflect.Map:
		m := reflect.MakeMap(f.Type())
		m.SetMapIndex(reflect.ValueOf("x"), reflect.Zero(f.Type().Elem()))
		f.Set(m)
	case reflect.Pointer:
		if f.Type().Elem() == reflect.TypeOf(big.Int{}) {
			f.Set(reflect.ValueOf(big.NewInt(1)))
			return
		}
		f.Set(instanceOf(t, f.Type(), name))
	case reflect.Interface:
		if f.Type() == reflect.TypeOf((*Type)(nil)).Elem() {
			f.Set(reflect.ValueOf(Type(&Builtin{Name: "x"})))
			return
		}
		f.Set(instanceOf(t, f.Type(), name))
	default:
		t.Fatalf("field %s: no mutation for kind %s — extend the field-sensitivity pin", name, f.Kind())
	}
}

// instanceOf builds a minimal non-nil value assignable to typ: for a pointer,
// a zero instance of its struct; for an interface, a zero instance of the
// first registered implementer.
func instanceOf(t *testing.T, typ reflect.Type, name string) reflect.Value {
	t.Helper()
	if typ.Kind() == reflect.Pointer {
		return reflect.New(typ.Elem())
	}
	for _, probe := range treeStructs {
		pt := reflect.TypeOf(probe)
		if pt.Implements(typ) {
			return reflect.New(pt.Elem())
		}
	}
	t.Fatalf("field %s: no registered implementer of %s", name, typ)
	return reflect.Value{}
}

// TestModuleRoundTrip pins the unit round trip: a small hand-built module
// marshals, unmarshals, links against itself, and re-marshals byte-
// identically — the full marshal round trip in miniature, plus the relink
// resolving a reference and an enum definition by name.
func TestModuleRoundTrip(t *testing.T) {
	target := &Const{Name: "A", Type: &Builtin{Name: "nint"},
		Value: &IntLiteral{Text: "1", Type: &Builtin{Name: "nint"}},
		Eval:  IntConstant(big.NewInt(1))}
	enum := &TypeDef{Name: "Rarity", Enum: &EnumDef{Base: "byte", Members: []EnumMember{
		{Name: "Common", Value: IntConstant(big.NewInt(1))},
	}}}
	m := &Module{
		Consts: []*Const{
			target,
			{Name: "B", Type: &Builtin{Name: "nint"},
				Value: &Reference{Target: target, Type: &Builtin{Name: "nint"}},
				Eval:  IntConstant(big.NewInt(1))},
			{Name: "C", Type: &Named{Def: enum},
				Value: &EnumMemberValue{Def: enum, Index: 0, Type: &Named{Def: enum}},
				Eval:  EnumConstant(enum, 0)},
		},
		Types: []*TypeDef{enum},
	}
	first, err := m.MarshalText()
	if err != nil {
		t.Fatalf("MarshalText: %v", err)
	}
	var back Module
	if err := back.UnmarshalText(first); err != nil {
		t.Fatalf("UnmarshalText: %v", err)
	}
	if err := back.Link(Resolver{}); err != nil {
		t.Fatalf("Link: %v", err)
	}
	if back.Consts[1].Value.(*Reference).Target != back.Consts[0] {
		t.Error("the reference did not relink to the module's own constant")
	}
	if back.Consts[2].Value.(*EnumMemberValue).Def != back.Types[0] {
		t.Error("the enum member did not relink to the module's own definition")
	}
	if back.Consts[2].Eval.EnumDef != back.Types[0] {
		t.Error("the folded enum constant did not relink")
	}
	second, err := back.MarshalText()
	if err != nil {
		t.Fatalf("re-MarshalText: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Errorf("re-marshal is not byte-identical:\n--- first ---\n%s--- second ---\n%s", first, second)
	}
}

// TestLinkReportsUnresolved pins the loud half of the relink: a reference
// nobody supplies is an error naming the reference, never a silent dangling
// placeholder.
func TestLinkReportsUnresolved(t *testing.T) {
	m := &Module{Consts: []*Const{
		{Name: "B", Value: &Reference{Target: &Const{Name: "Missing"}}},
	}}
	err := m.Link(Resolver{})
	if err == nil || !strings.Contains(err.Error(), "Missing") {
		t.Errorf("Link = %v, want an unresolved-reference error naming Missing", err)
	}
}

// TestLinkResolverSupplies pins the external channel: a name the module does
// not declare resolves through the caller's resolver — the prelude/use story.
func TestLinkResolverSupplies(t *testing.T) {
	external := &Const{Name: "X"}
	m := &Module{Consts: []*Const{
		{Name: "B", Value: &Reference{Target: &Const{Name: "X"}}},
	}}
	err := m.Link(Resolver{Const: func(name string) *Const {
		if name == "X" {
			return external
		}
		return nil
	}})
	if err != nil {
		t.Fatalf("Link: %v", err)
	}
	if m.Consts[0].Value.(*Reference).Target != external {
		t.Error("the external reference did not resolve through the resolver")
	}
}
