package semantic

import (
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// The bare-member channels: a bare enum member (Legend, not Rarity.Legend)
// resolves wherever a syntactic enum is the expectation — a let initializer, an
// assignment's right-hand side, an operator argument, a union alias annotation,
// and an associated constant's initializer — through all three layers (type,
// lowering, fold). These tests pin each context to its lowered value and to the
// unknown_enum_member diagnostic a non-member earns.

const bareMemberPrelude = "pub enum Rarity: byte {\n  Common = 1\n  Rare = 2\n  Legend = 10\n}\n"

// containsLine reports whether the IR dump has a line whose trimmed text equals
// want — a tighter check than a substring, so "<none>" cannot hide inside a
// larger line.
func containsLine(dump, want string) bool {
	for _, line := range strings.Split(dump, "\n") {
		if strings.TrimSpace(line) == want {
			return true
		}
	}
	return false
}

func TestBareMemberLetInit(t *testing.T) {
	src := bareMemberPrelude + "pub fn f(): Rarity {\n  let r: Rarity = Legend\n  return r\n}\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	dump := ir.Dump(m)
	if !containsLine(dump, `let "r": Rarity = (Rarity.Legend : Rarity)`) {
		t.Errorf("let initializer did not resolve the bare member:\n%s", dump)
	}
}

func TestBareMemberLetInitUnknown(t *testing.T) {
	src := bareMemberPrelude + "pub fn f(): Rarity {\n  let r: Rarity = Bogus\n  return r\n}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeUnknownEnumMember) {
		t.Errorf("unknown bare member in let init: want unknown_enum_member, got %v", codes(diags))
	}
}

func TestBareMemberAssign(t *testing.T) {
	src := bareMemberPrelude + "pub fn f(): Rarity {\n  let r: Rarity = Common\n  r = Rare\n  return r\n}\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	dump := ir.Dump(m)
	if !containsLine(dump, `assign "r" = (Rarity.Rare : Rarity)`) {
		t.Errorf("assignment did not resolve the bare member:\n%s", dump)
	}
}

func TestBareMemberAssignUnknown(t *testing.T) {
	src := bareMemberPrelude + "pub fn f(): Rarity {\n  let r: Rarity = Common\n  r = Bogus\n  return r\n}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeUnknownEnumMember) {
		t.Errorf("unknown bare member in assign: want unknown_enum_member, got %v", codes(diags))
	}
}

func TestBareMemberCompareArg(t *testing.T) {
	src := bareMemberPrelude + "pub fn f(rarity: Rarity): bool {\n  return rarity == Legend\n}\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	dump := ir.Dump(m)
	if !containsLine(dump, `return ((ParamRef "rarity" : Rarity).eql((Rarity.Legend : Rarity)) : bool)`) {
		t.Errorf("comparison argument did not resolve the bare member:\n%s", dump)
	}
}

func TestBareMemberCompareArgSelf(t *testing.T) {
	// The same channel through a self receiver inside a method body.
	src := "pub enum Rarity: byte {\n  Common = 1\n  Legend = 10\n} impl {\n  pub isTop(): bool {\n    return self == Legend\n  }\n}\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	dump := ir.Dump(m)
	if !containsLine(dump, `return ((self : Rarity).eql((Rarity.Legend : Rarity)) : bool)`) {
		t.Errorf("self comparison argument did not resolve the bare member:\n%s", dump)
	}
}

func TestBareMemberCompareArgUnknown(t *testing.T) {
	src := bareMemberPrelude + "pub fn f(rarity: Rarity): bool {\n  return rarity == Bogus\n}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeUnknownEnumMember) {
		t.Errorf("unknown bare member as compare arg: want unknown_enum_member, got %v", codes(diags))
	}
}

func TestBareMemberCompareArgNonEnumReceiver(t *testing.T) {
	// A non-enum receiver must not invent an enum expectation: a bare name there
	// stays an ordinary unresolved reference, not a member of some enum.
	src := bareMemberPrelude + "pub fn f(n: nint): bool {\n  return n == Legend\n}\n"
	_, diags := analyze(src)
	if hasCode(diags, CodeUnknownEnumMember) {
		t.Errorf("non-enum receiver: want no unknown_enum_member, got %v", codes(diags))
	}
}

func TestBareMemberUnionAlias(t *testing.T) {
	// A named union alias of an enum unwraps the same way the bare union does.
	src := bareMemberPrelude + "pub type Opt = Rarity | error\npub fn f(): Opt {\n  let r: Opt = Legend\n  return r\n}\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	dump := ir.Dump(m)
	if !containsLine(dump, `let "r": Opt = (Rarity.Legend : Rarity)`) {
		t.Errorf("union alias let init did not resolve the bare member:\n%s", dump)
	}
}

func TestBareMemberUnionAliasConst(t *testing.T) {
	// The same alias unwrapping in a const initializer.
	src := bareMemberPrelude + "pub type Opt = Rarity | error\nconst Top: Opt = Legend\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	dump := ir.Dump(m)
	if !containsLine(dump, "value (Rarity.Legend : Rarity)") || !containsLine(dump, "eval Rarity.Legend") {
		t.Errorf("union alias const did not resolve/fold the bare member:\n%s", dump)
	}
}

// TestBareMemberAssocConstInit pins context 5: an enum's own impl-block
// associated constant, whose bare-member initializer folds through the enum's
// annotation. (An enum's members are settled before its impl consts, so its own
// const sees them — the meaningful E-2 case. A cross-type assoc const referencing
// another enum's member does not fold for the bare form any more than for the
// qualified one — a separate, pre-existing assoc-const ordering limitation.)
func TestBareMemberAssocConstInit(t *testing.T) {
	src := "pub enum Rarity: byte {\n  Common = 1\n  Legend = 10\n} impl {\n  pub const Top: Rarity = Legend\n}\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	dump := ir.Dump(m)
	// An associated constant's dumped value is its folded Constant (it carries
	// no value graph), so the line renders bare, without a typed-node wrap.
	if !containsLine(dump, "value Rarity.Legend") {
		t.Errorf("assoc const init did not fold the bare member:\n%s", dump)
	}
}

// TestBareMemberFoldsEndToEnd asserts the resolved values fold all the way to
// compile-time constants: a function that returns a bare-member let, and one
// that compares a parameter to a bare member, both fold under application.
func TestBareMemberFoldsEndToEnd(t *testing.T) {
	src := bareMemberPrelude +
		"pub fn pick(): Rarity {\n  let r: Rarity = Legend\n  return r\n}\n" +
		"pub fn isTop(rarity: Rarity): bool {\n  return rarity == Legend\n}\n" +
		"assert pick() == Rarity.Legend\n" +
		"assert isTop(Rarity.Legend)\n" +
		"assert !isTop(Rarity.Common)\n"
	_, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("end-to-end fold produced diagnostics: %v", codes(diags))
	}
}
