package master

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
)

// fakeFormat is a no-op Format for the registry's resolution tests.
type fakeFormat struct{ name string }

func (f fakeFormat) Name() string                              { return f.name }
func (f fakeFormat) OptionSpecs() []OptionSpec                 { return nil }
func (f fakeFormat) Read(SourceSpec) (Table, *diagnostic.List) { return Table{}, &diagnostic.List{} }

func TestRegistryLookup(t *testing.T) {
	r := NewRegistry()
	if _, ok := r.Lookup("csv"); ok {
		t.Fatal("Lookup on an empty registry = ok, want not found")
	}
	r.Register(fakeFormat{name: "csv"})
	f, ok := r.Lookup("csv")
	if !ok {
		t.Fatal("Lookup(csv) = not found, want the registered format")
	}
	if f.Name() != "csv" {
		t.Errorf("Name = %q, want csv", f.Name())
	}
	if _, ok := r.Lookup("xlsx"); ok {
		t.Error("Lookup(xlsx) = ok, want not found")
	}
}

func TestRegistryRegisterReplaces(t *testing.T) {
	r := NewRegistry()
	r.Register(fakeFormat{name: "csv"})
	second := fakeFormat{name: "csv"}
	r.Register(second)
	if f, _ := r.Lookup("csv"); f != second {
		t.Error("a second registration under the same name did not replace the first")
	}
}
