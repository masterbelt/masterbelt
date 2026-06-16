package sql

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// TestSqlSummable pins which column element types the SQL sum can honor: a
// fixed-width builtin numeric can; an arbitrary-precision integer (nint/nuint)
// cannot, because the sum may exceed the int64 the engine reads it into; and a type
// that declares its own add cannot, because SQL's sum would not carry that
// arithmetic — the aggregate twin of the comparison lowering's custom-operator guard.
func TestSqlSummable(t *testing.T) {
	bt := func(name string) ir.Type { return &ir.Builtin{Name: name} }
	// A user numeric type whose add has a body — its arithmetic SQL cannot reproduce.
	customAdd := &ir.Named{Def: &ir.TypeDef{Name: "Money", Body: bt("nint"),
		Methods: []*ir.Method{{Name: "add", Extern: false}}}}
	// A type whose add is the builtin (extern) operator — SQL's sum carries it.
	externAdd := &ir.Named{Def: &ir.TypeDef{Name: "Score", Body: bt("nint"),
		Methods: []*ir.Method{{Name: "add", Extern: true}}}}

	cases := []struct {
		name string
		elem ir.Type
		want bool
	}{
		{"int", bt("int"), true},
		{"nint (arbitrary precision)", bt("nint"), false},
		{"nuint (arbitrary precision)", bt("nuint"), false},
		{"custom add", customAdd, false},
		{"extern add", externAdd, true},
		{"nullable int", &ir.Union{Members: []ir.Type{bt("int"), bt("null")}}, true},
		{"nullable custom add", &ir.Union{Members: []ir.Type{customAdd, bt("null")}}, false},
	}
	for _, tc := range cases {
		if got := sqlSummable(tc.elem); got != tc.want {
			t.Errorf("%s: sqlSummable = %v, want %v", tc.name, got, tc.want)
		}
	}
}
