package ast

import "testing"

// TestRenderTypeProjectionWithArgs pins that a projected generic type renders
// with the projection segments before the generic arguments. The parser binds
// the arguments to the whole dotted head, so Order.customer.id<string> must
// render in that order — rendering the arguments on a middle segment
// (Order.customer<string>.id) would re-parse with .id dropped, silently changing
// the type.
func TestRenderTypeProjectionWithArgs(t *testing.T) {
	nt := &NamedType{
		Namespace:   "Order",
		Name:        "customer",
		Projections: []string{"id"},
		Args:        []TypeExpr{&NamedType{Name: "string"}},
	}
	if got := renderType(nt); got != "Order.customer.id<string>" {
		t.Fatalf("renderType = %q, want Order.customer.id<string>", got)
	}
}
