package ast

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/source/internal/treetext"
)

// TestFieldSensitivity is the field-sensitivity gate: for every tree struct,
// mutating any exported, non-excluded field must change the marshal output —
// the executable definition of "every field appears in the format". A field
// the codec dropped would marshal identically before and after the mutation
// and fail here, which is exactly the blind spot the exact format exists to
// kill (an overload-resolution gap, where a field the curated dump did not
// render went unnoticed, was this shape).
func TestFieldSensitivity(t *testing.T) {
	for _, probe := range treeStructs {
		st := reflect.TypeOf(probe).Elem()
		t.Run(st.Name(), func(t *testing.T) {
			base := render(t, reflect.New(st))
			for i := range st.NumField() {
				field := st.Field(i)
				if !field.IsExported() || field.Tag.Get("tree") == "-" {
					continue
				}
				mut := reflect.New(st)
				mutate(t, mut.Elem().Field(i), field.Name)
				if got := render(t, mut); bytes.Equal(got, base) {
					t.Errorf("mutating %s.%s does not change the marshal output — the field is missing from the format", st.Name(), field.Name)
				}
			}
		})
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
	case reflect.Int:
		f.SetInt(1)
	case reflect.Slice:
		elem := f.Type().Elem()
		if elem.Kind() == reflect.String {
			f.Set(reflect.ValueOf([]string{"x"}))
			return
		}
		s := reflect.MakeSlice(f.Type(), 1, 1)
		s.Index(0).Set(instanceOf(t, elem, name))
		f.Set(s)
	case reflect.Pointer, reflect.Interface:
		f.Set(instanceOf(t, f.Type(), name))
	default:
		t.Fatalf("field %s: no mutation for kind %s — extend the field-sensitivity pin", name, f.Kind())
	}
}

// instanceOf builds a minimal non-nil value assignable to t: for a pointer, a
// zero instance of its struct; for an interface, a zero instance of the first
// registered implementer.
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

// TestNoSilentExclusions pins the explicit-exclusion manifest: the AST
// excludes nothing exported from its format today, and the unexported syntax
// backpointers are out by construction. A tree:"-" added later must extend
// this pin — the "never an implicit omission" rule of the exact format.
func TestNoSilentExclusions(t *testing.T) {
	if len(treeExcluded) != 0 {
		t.Errorf("treeExcluded = %v; the AST format excludes no exported fields — update this pin only with a deliberate format decision", treeExcluded)
	}
	for _, probe := range treeStructs {
		st := reflect.TypeOf(probe).Elem()
		for i := range st.NumField() {
			field := st.Field(i)
			if !field.IsExported() && field.Name != "syntax" && field.Name != "hidden" {
				t.Errorf("%s.%s: an unexported field that is not the syntax backpointer — decide its format status explicitly", st.Name(), field.Name)
			}
		}
	}
}
