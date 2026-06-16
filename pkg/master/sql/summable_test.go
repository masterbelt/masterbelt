package sql

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// TestSqlSummable pins which column element types the SQL sum can honor and read
// back as the column's type: a fixed-width signed-or-small integer can; an
// arbitrary-precision integer (nint/nuint) or a 64-bit unsigned (ulong) cannot,
// because the sum (or, for ulong, a single cell) can exceed the int64 the engine
// reads it into — reached through an alias too; a type that declares its own add
// cannot, because SQL's sum would not carry that arithmetic; and a refinement cannot,
// because an empty relation's manufactured zero need not satisfy the where.
func TestSqlSummable(t *testing.T) {
	bt := func(name string) ir.Type { return &ir.Builtin{Name: name} }
	// A user numeric type whose add has a body — its arithmetic SQL cannot reproduce.
	customAdd := &ir.Named{Def: &ir.TypeDef{Name: "Money", Body: bt("int"),
		Methods: []*ir.Method{{Name: "add", Extern: false}}}}
	// A type whose add is the builtin (extern) operator over a summable body.
	externAdd := &ir.Named{Def: &ir.TypeDef{Name: "Score", Body: bt("int"),
		Methods: []*ir.Method{{Name: "add", Extern: true}}}}
	aliasInt := &ir.Named{Def: &ir.TypeDef{Name: "Count", Body: bt("int")}}
	aliasNint := &ir.Named{Def: &ir.TypeDef{Name: "Big", Body: bt("nint")}}
	refined := &ir.Named{Def: &ir.TypeDef{Name: "Positive", Body: bt("int"), Where: &ir.BoolLiteral{Value: true}}}

	cases := []struct {
		name string
		elem ir.Type
		want bool
	}{
		{"int", bt("int"), true},
		{"long", bt("long"), true},
		{"uint (fits int64)", bt("uint"), true},
		{"nint (arbitrary precision)", bt("nint"), false},
		{"nuint (arbitrary precision)", bt("nuint"), false},
		{"ulong (64-bit unsigned)", bt("ulong"), false},
		{"custom add", customAdd, false},
		{"extern add over int", externAdd, true},
		{"alias of int", aliasInt, true},
		{"alias of nint", aliasNint, false},
		{"refinement of int", refined, false},
		{"nullable int", &ir.Union{Members: []ir.Type{bt("int"), bt("null")}}, true},
		{"nullable custom add", &ir.Union{Members: []ir.Type{customAdd, bt("null")}}, false},
		{"nullable alias of nint", &ir.Union{Members: []ir.Type{aliasNint, bt("null")}}, false},
	}
	for _, tc := range cases {
		if got := sqlSummable(tc.elem); got != tc.want {
			t.Errorf("%s: sqlSummable = %v, want %v", tc.name, got, tc.want)
		}
	}
}
