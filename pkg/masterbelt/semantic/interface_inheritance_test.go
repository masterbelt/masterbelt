package semantic

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/types"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// Interface inheritance (supertraits): an interface may list one or more parent
// interfaces after a colon, inheriting their whole contract. These tests pin the
// parse-to-semantic behaviour: parents resolve onto Interface.Parents, a child
// re-declaring an ancestor member is rejected (override), an ambiguous name from
// two unrelated ancestors is rejected (conflict), a cycle is rejected, a diamond
// is fine, conformance covers the whole ancestor closure, opting into a child
// materializes its ancestors, the bound implication holds (T: child satisfies an
// ancestor bound — including a switch over it), and an interface value is
// assignable to an ancestor interface.

// twoParentSrc declares a small inheritance graph: comparable-derived printable
// and rankable, joined by ordered (a diamond over comparable).
const twoParentSrc = "" +
	"pub interface printable: comparable {\n  show(): string\n}\n" +
	"pub interface rankable: comparable {\n  rank(): nint\n}\n" +
	"pub interface ordered: printable, rankable {\n  before(other: self): bool\n}\n"

// TestInterfaceParentsResolve checks that a child's parents resolve onto its
// Interface.Parents, in declaration order.
func TestInterfaceParentsResolve(t *testing.T) {
	m, diags := analyze(twoParentSrc)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	ordered := typeDef(m, "ordered")
	if ordered == nil || ordered.Interface == nil {
		t.Fatalf("ordered not resolved as an interface: %+v", m.Types)
	}
	if len(ordered.Interface.Parents) != 2 {
		t.Fatalf("ordered parents = %d, want 2", len(ordered.Interface.Parents))
	}
	if got := ordered.Interface.Parents[0].String(); got != "printable" {
		t.Errorf("first parent = %s, want printable", got)
	}
	if got := ordered.Interface.Parents[1].String(); got != "rankable" {
		t.Errorf("second parent = %s, want rankable", got)
	}
}

// TestInterfaceParentNotAnInterface checks that a parent that does not name an
// interface is reported, exactly as a non-interface impl tag is.
func TestInterfaceParentNotAnInterface(t *testing.T) {
	src := "pub type Thing = nint\n" +
		"pub interface bad: Thing {\n  m(): nint\n}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeNotAnInterface) {
		t.Fatalf("a non-interface parent; want not_an_interface, got %v", codes(diags))
	}
}

// TestInterfaceOverrideRejected checks that a child re-declaring an ancestor's
// member (required or provided) is rejected: inheritance carries the contract
// whole, so a child may only add to it.
func TestInterfaceOverrideRejected(t *testing.T) {
	// eql is comparable's required member; printable re-declares it.
	src := "pub interface printable: comparable {\n  eql(other: self): bool\n}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeInterfaceMemberOverride) {
		t.Fatalf("a child re-declaring an inherited member; want interface_member_override, got %v", codes(diags))
	}
}

// TestInterfaceUnrelatedConflictRejected checks that a name two unrelated
// ancestors both contribute is ambiguous and is reported at the child.
func TestInterfaceUnrelatedConflictRejected(t *testing.T) {
	src := "pub interface a {\n  m(): nint\n}\n" +
		"pub interface b {\n  m(): nint\n}\n" +
		"pub interface c: a, b {\n  other(): nint\n}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeInterfaceMemberConflict) {
		t.Fatalf("a name from two unrelated ancestors; want interface_member_conflict, got %v", codes(diags))
	}
}

// TestInterfaceDiamondOK checks that a member reached through a single shared
// ancestor along two paths is not a conflict — the closure dedups by definition
// identity, so the diamond closes cleanly.
func TestInterfaceDiamondOK(t *testing.T) {
	_, diags := analyze(twoParentSrc)
	if hasCode(diags, CodeInterfaceMemberConflict) {
		t.Fatalf("a shared comparable ancestor is not a conflict; got %v", codes(diags))
	}
	if hasCode(diags, CodeInterfaceMemberOverride) {
		t.Fatalf("the diamond declares no ancestor member; got %v", codes(diags))
	}
}

// TestInterfaceCycleRejected checks that a cycle in the parent graph (a: b, b: a)
// is rejected, reusing cyclic_reference.
func TestInterfaceCycleRejected(t *testing.T) {
	src := "pub interface a: b {\n  m(): nint\n}\n" +
		"pub interface b: a {\n  n(): nint\n}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeCyclicReference) {
		t.Fatalf("a cyclic parent graph; want cyclic_reference, got %v", codes(diags))
	}
}

// TestConformanceClosure checks that conformance covers the whole ancestor
// closure: a type that impls a child must supply the parents' required methods
// too. Marker = nint inherits nint's eql/neq (comparable), so the only missing
// required method is the child's own show/rank/before when omitted.
func TestConformanceClosure(t *testing.T) {
	// Tag supplies the child's members and inherits comparable from nint: clean.
	ok := twoParentSrc +
		"pub type Tag = nint impl ordered {\n" +
		"  show(): string {\n    return \"t\"\n  }\n" +
		"  rank(): nint {\n    return 1\n  }\n" +
		"  before(other: self): bool {\n    return self.lt(other)\n  }\n" +
		"}\n"
	if _, diags := analyze(ok); hasCode(diags, CodeMissingRequiredMethod) {
		t.Fatalf("Tag supplies the whole closure; want no missing_required_method, got %v", codes(diags))
	}

	// A record base supplies no comparable, so the inherited comparable is unmet
	// even though the child's own members are present.
	bad := twoParentSrc +
		"pub type Tag = { v: nint } impl ordered {\n" +
		"  show(): string {\n    return \"t\"\n  }\n" +
		"  rank(): nint {\n    return 1\n  }\n" +
		"  before(other: self): bool {\n    return self.before(other)\n  }\n" +
		"}\n"
	if _, diags := analyze(bad); !hasCode(diags, CodeMissingRequiredMethod) {
		t.Fatalf("a record base supplies no comparable; want missing_required_method, got %v", codes(diags))
	}
}

// TestImplMaterializesAncestors checks the automatic carry: opting into a child
// records the whole ancestor closure on the type's Impls, deduped (the diamond's
// comparable lands once). The child alone is written.
func TestImplMaterializesAncestors(t *testing.T) {
	src := twoParentSrc +
		"pub type Tag = nint impl ordered {\n" +
		"  show(): string {\n    return \"t\"\n  }\n" +
		"  rank(): nint {\n    return 1\n  }\n" +
		"  before(other: self): bool {\n    return self.lt(other)\n  }\n" +
		"}\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	tag := typeDef(m, "Tag")
	if tag == nil {
		t.Fatalf("Tag not resolved")
	}
	want := map[string]bool{"ordered": false, "printable": false, "rankable": false, "comparable": false}
	count := map[string]int{}
	for _, impl := range tag.Impls {
		if idef := interfaceDefOf(impl); idef != nil {
			count[idef.Name]++
			if _, ok := want[idef.Name]; ok {
				want[idef.Name] = true
			}
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("Tag is missing materialized impl %s; impls = %v", name, count)
		}
	}
	if count["comparable"] != 1 {
		t.Errorf("comparable should be materialized once (diamond), got %d", count["comparable"])
	}
}

// TestBoundImplicationSatisfies checks that a type opting into a child satisfies
// an ancestor bound (Tag satisfies comparable), through the materialized impls —
// the path Satisfies reads with no special-casing.
func TestBoundImplicationSatisfies(t *testing.T) {
	src := twoParentSrc +
		"pub type Tag = nint impl ordered {\n" +
		"  show(): string {\n    return \"t\"\n  }\n" +
		"  rank(): nint {\n    return 1\n  }\n" +
		"  before(other: self): bool {\n    return self.lt(other)\n  }\n" +
		"}\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	tag := typeDef(m, "Tag")
	cmp := universe().prelude["comparable"]
	if tag == nil || cmp == nil {
		t.Fatalf("Tag or comparable not resolved")
	}
	if !types.Satisfies(builtin.Default(), &ir.Named{Def: tag}, &ir.Named{Def: cmp}) {
		t.Errorf("Tag impls ordered, so it should satisfy the inherited comparable bound")
	}
}

// TestBoundImplicationInheritedMethod checks the bound implication at a call
// site: a function bounded by a child may call an inherited ancestor method
// (eql, from comparable, on a T: ordered) — the method resolves through the
// inheritance chain and the body type-checks clean.
func TestBoundImplicationInheritedMethod(t *testing.T) {
	src := twoParentSrc +
		"pub fn equal<T: ordered>(a: T, b: T): bool {\n  return a.eql(b)\n}\n"
	if _, diags := analyze(src); len(diags) != 0 {
		t.Fatalf("eql is inherited by ordered from comparable; want clean, got %v", codes(diags))
	}
}

// TestSwitchInheritedComparableBoundOK checks the switch-special-case
// replacement: a switch over a T: orderable scrutinee is accepted, because
// orderable inherits comparable and Satisfies now walks the parent chain. Before
// the change, only a bound that was comparable itself was admitted.
func TestSwitchInheritedComparableBoundOK(t *testing.T) {
	src := "pub fn pick<T: orderable>(a: T, b: T): nint {\n" +
		"  switch a {\n    b -> return 1\n    _ -> return 0\n  }\n" +
		"}\n"
	_, diags := analyze(src)
	if hasCode(diags, CodeScrutineeNotComparable) {
		t.Fatalf("orderable inherits comparable, so a T: orderable switch is comparable; got %v", codes(diags))
	}
}

// TestSwitchUserChildBoundOK checks the same implication over a user interface:
// a switch over a T bounded by a user child of comparable is accepted.
func TestSwitchUserChildBoundOK(t *testing.T) {
	src := twoParentSrc +
		"pub fn pick<T: ordered>(a: T, b: T): nint {\n" +
		"  switch a {\n    b -> return 1\n    _ -> return 0\n  }\n" +
		"}\n"
	_, diags := analyze(src)
	if hasCode(diags, CodeScrutineeNotComparable) {
		t.Fatalf("ordered inherits comparable; want a comparable scrutinee, got %v", codes(diags))
	}
}

// TestInterfaceValueAssignableToAncestor checks the width subtyping: a value
// typed as a child interface flows to a parameter typed as an ancestor
// interface. consume takes a comparable; g passes its ordered value to it.
func TestInterfaceValueAssignableToAncestor(t *testing.T) {
	src := twoParentSrc +
		"pub fn consume(c: comparable): bool {\n  return c.eql(c)\n}\n" +
		"pub fn g(o: ordered): bool {\n  return consume(o)\n}\n"
	_, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("an ordered value is assignable to a comparable parameter; got %v", codes(diags))
	}
}
