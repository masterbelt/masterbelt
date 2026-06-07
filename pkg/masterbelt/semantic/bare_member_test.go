package semantic

import (
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

// enumMemberOf reads the enum member a value resolves to, as "Enum.Member",
// stripping the union-inflow Adapt a channel may have wrapped it in; "" when
// the value is no enum member. It is the structural form of the old dump-line
// check: the member identity and its adaption are read off the IR itself.
func enumMemberOf(v ir.Value) string {
	if a, ok := v.(*ir.Adapt); ok {
		v = a.Value
	}
	m, ok := v.(*ir.EnumMemberValue)
	if !ok || m.Def == nil || m.Def.Enum == nil || m.Index < 0 || m.Index >= len(m.Def.Enum.Members) {
		return ""
	}
	return m.Def.Name + "." + m.Def.Enum.Members[m.Index].Name
}

// fnNamed returns the module's function of the given name.
func fnNamed(t *testing.T, m *ir.Module, name string) *ir.Function {
	t.Helper()
	for _, fn := range m.Funcs {
		if fn != nil && fn.Name == name {
			return fn
		}
	}
	t.Fatalf("no function %q in the module", name)
	return nil
}

// methodNamed returns the named method of the named type definition.
func methodNamed(t *testing.T, m *ir.Module, typeName, method string) *ir.Method {
	t.Helper()
	for _, def := range m.Types {
		if def == nil || def.Name != typeName {
			continue
		}
		for _, mt := range def.Methods {
			if mt != nil && mt.Name == method {
				return mt
			}
		}
	}
	t.Fatalf("no method %s.%s in the module", typeName, method)
	return nil
}

func TestBareMemberLetInit(t *testing.T) {
	src := bareMemberPrelude + "pub fn f(): Rarity {\n  let r: Rarity = Legend\n  return r\n}\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	let, ok := fnNamed(t, m, "f").Body[0].(*ir.Let)
	if !ok || let.Name != "r" || enumMemberOf(let.Value) != "Rarity.Legend" {
		t.Errorf("let initializer did not resolve the bare member: %+v", let)
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
	assign, ok := fnNamed(t, m, "f").Body[1].(*ir.Assign)
	if !ok || assign.Name != "r" || enumMemberOf(assign.Value) != "Rarity.Rare" {
		t.Errorf("assignment did not resolve the bare member: %+v", assign)
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
	ret, ok := fnNamed(t, m, "f").Body[0].(*ir.Return)
	if !ok {
		t.Fatal("body did not lower to a return")
	}
	call, ok := ret.Value.(*ir.Call)
	if !ok || call.Method != "eql" {
		t.Fatalf("return value is not an eql call: %+v", ret.Value)
	}
	if _, ok := call.Receiver.(*ir.ParamRef); !ok {
		t.Errorf("receiver is not the parameter: %+v", call.Receiver)
	}
	if len(call.Args) != 1 || enumMemberOf(call.Args[0]) != "Rarity.Legend" {
		t.Errorf("comparison argument did not resolve the bare member: %+v", call.Args)
	}
}

func TestBareMemberCompareArgSelf(t *testing.T) {
	// The same channel through a self receiver inside a method body.
	src := "pub enum Rarity: byte {\n  Common = 1\n  Legend = 10\n} impl {\n  pub isTop(): bool {\n    return self == Legend\n  }\n}\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	ret, ok := methodNamed(t, m, "Rarity", "isTop").Body[0].(*ir.Return)
	if !ok {
		t.Fatal("method body did not lower to a return")
	}
	call, ok := ret.Value.(*ir.Call)
	if !ok || call.Method != "eql" {
		t.Fatalf("return value is not an eql call: %+v", ret.Value)
	}
	if _, ok := call.Receiver.(*ir.SelfValue); !ok {
		t.Errorf("receiver is not self: %+v", call.Receiver)
	}
	if len(call.Args) != 1 || enumMemberOf(call.Args[0]) != "Rarity.Legend" {
		t.Errorf("self comparison argument did not resolve the bare member: %+v", call.Args)
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
	let, ok := fnNamed(t, m, "f").Body[0].(*ir.Let)
	if !ok || let.Name != "r" {
		t.Fatal("body did not lower to the let")
	}
	adapt, ok := let.Value.(*ir.Adapt)
	if !ok || adapt.To == nil || adapt.To.String() != "Opt" {
		t.Errorf("union alias let init is not an adaption to Opt: %+v", let.Value)
	}
	if enumMemberOf(let.Value) != "Rarity.Legend" {
		t.Errorf("union alias let init did not resolve the bare member: %+v", let.Value)
	}
}

func TestBareMemberUnionAliasConst(t *testing.T) {
	// The same alias unwrapping in a const initializer.
	src := bareMemberPrelude + "pub type Opt = Rarity | error\nconst Top: Opt = Legend\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	c := m.Consts[0]
	adapt, ok := c.Value.(*ir.Adapt)
	if !ok || adapt.To == nil || adapt.To.String() != "Opt" || enumMemberOf(c.Value) != "Rarity.Legend" {
		t.Errorf("union alias const did not resolve the bare member: %+v", c.Value)
	}
	// The member flows into the union, so the folded value carries its tag —
	// the dispatch a later match folds through.
	if c.Eval == nil || c.Eval.Kind != ir.ConstEnum || c.Eval.EnumName() != "Legend" ||
		c.Eval.UnionTag == nil || c.Eval.UnionTag.String() != "Rarity" {
		t.Errorf("union alias const did not fold to the tagged member: %v", c.Eval)
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
	// An associated constant carries its folded Constant, no value graph.
	var ac *ir.AssocConst
	for _, def := range m.Types {
		if def != nil && def.Name == "Rarity" && len(def.Consts) > 0 {
			ac = def.Consts[0]
		}
	}
	if ac == nil {
		t.Fatal("no associated constant on Rarity")
	}
	if ac.Value == nil || ac.Value.Kind != ir.ConstEnum || ac.Value.EnumName() != "Legend" {
		t.Errorf("assoc const init did not fold the bare member: %v", ac.Value)
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
