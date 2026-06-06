package lsp

import (
	"strings"
	"testing"

	protocol "github.com/owenrumney/go-lsp/lsp"
)

// rarityEnum is a three-member enum reused across the expected-enum completion
// tests; each test prepends it to its own value position.
const rarityEnum = "pub enum Rarity: uint8 {\n  Common = 1\n  Rare = 2\n  Legend = 10\n}\n"

// completeAt returns the completion items at the end of marker within src.
func completeAt(t *testing.T, src, marker string) map[string]protocol.CompletionItem {
	t.Helper()
	idx := strings.Index(src, marker)
	if idx < 0 {
		t.Fatalf("marker %q not found in source", marker)
	}
	return byLabel(completion(testView(src), idx+len(marker)).Items)
}

// assertBareMembers asserts every member name is offered as an enum member with
// a value label, and that the value namespace is offered alongside (the members
// are added, not exclusive).
func assertBareMembers(t *testing.T, got map[string]protocol.CompletionItem, names ...string) {
	t.Helper()
	for _, name := range names {
		item, ok := got[name]
		if !ok {
			t.Errorf("expected bare member %q; got %v", name, keysOf(got))
			continue
		}
		if item.Kind == nil || *item.Kind != protocol.CompletionItemKindEnumMember {
			t.Errorf("%s kind = %v, want EnumMember", name, item.Kind)
		}
		if !strings.HasPrefix(item.Detail, "= ") {
			t.Errorf("%s detail = %q, want a value label", name, item.Detail)
		}
	}
}

func keysOf(m map[string]protocol.CompletionItem) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

func TestExpectedEnumConstInitializer(t *testing.T) {
	// A const annotated with an enum offers its bare members in the value
	// position, each labelled with its base value, alongside the value namespace.
	src := rarityEnum + "const top: Rarity = \n"
	got := completeAt(t, src, "Rarity = ")
	assertBareMembers(t, got, "Common", "Rare", "Legend")
	if got["Legend"].Detail != "= 10" {
		t.Errorf("Legend detail = %q, want = 10", got["Legend"].Detail)
	}
	// The members are added to the namespace, not offered exclusively: the value
	// literals are still there.
	if _, ok := got["true"]; !ok {
		t.Error("expected the value namespace alongside the bare members")
	}
}

func TestExpectedEnumConstInitializerPartialWord(t *testing.T) {
	// With a partial member typed, the members still complete — the cursor sits
	// just past the partial word at end of file.
	src := rarityEnum + "const top: Rarity = C"
	got := completeAt(t, src, "Rarity = C")
	assertBareMembers(t, got, "Common", "Rare", "Legend")
}

func TestExpectedEnumConstUnionAnnotation(t *testing.T) {
	// A union annotation carrying an enum (Rarity | error) accepts a bare member
	// exactly as the bare enum does, so the members are offered there too.
	src := rarityEnum + "const top: Rarity | error = \n"
	got := completeAt(t, src, "error = ")
	assertBareMembers(t, got, "Common", "Rare", "Legend")
}

func TestExpectedEnumNotOfferedForNonEnumAnnotation(t *testing.T) {
	// An int-annotated const expects no bare member.
	src := "const n: int = \n"
	got := completeAt(t, src, "int = ")
	for _, name := range []string{"Common", "Rare", "Legend"} {
		if _, ok := got[name]; ok {
			t.Errorf("non-enum annotation offered bare member %q", name)
		}
	}
}

func TestExpectedEnumNotOfferedForUnknownAnnotation(t *testing.T) {
	// An annotation naming no type in scope resolves to no enum, so no bare
	// member is offered (offering one would propose a candidate that cannot
	// resolve).
	src := "const x: Bogus = \n"
	got := completeAt(t, src, "Bogus = ")
	if _, ok := got["Common"]; ok {
		t.Error("unknown annotation offered a bare member")
	}
}

func TestExpectedEnumNotOfferedForGenericAliasAnnotation(t *testing.T) {
	// A generic union alias (optional<Rarity>) does not resolve a bare member in
	// the lowering — the App alias is not unwrapped there — so completion must not
	// offer one either, matching what the program would accept.
	src := rarityEnum + "const x: optional<Rarity> = \n"
	got := completeAt(t, src, "Rarity> = ")
	if _, ok := got["Common"]; ok {
		t.Error("generic alias annotation offered a bare member the lowering leaves undefined")
	}
}

func TestExpectedEnumNotOfferedInAnnotationPosition(t *testing.T) {
	// Inside the annotation itself (a type position) the bare members are not
	// offered — only the value position expects them.
	src := rarityEnum + "const x: Rarity = \n"
	got := completeAt(t, src, ": Rar")
	if _, ok := got["Common"]; ok {
		t.Error("the annotation type position offered a bare member")
	}
}

func TestExpectedEnumSwitchArmEmpty(t *testing.T) {
	// A switch over an enum-typed parameter offers the scrutinee's bare members
	// in a fresh arm value position.
	src := rarityEnum + "pub fn f(r: Rarity): int {\n  switch r {\n    \n  }\n}\n"
	got := completeAt(t, src, "switch r {\n    ")
	assertBareMembers(t, got, "Common", "Rare", "Legend")
}

func TestExpectedEnumSwitchArmAfterFirstArm(t *testing.T) {
	// A fresh arm after a complete one still offers the members.
	src := rarityEnum + "pub fn f(r: Rarity): int {\n  switch r {\n    Common -> return 1\n    \n  }\n}\n"
	got := completeAt(t, src, "return 1\n    ")
	assertBareMembers(t, got, "Common", "Rare", "Legend")
}

func TestExpectedEnumSwitchSelf(t *testing.T) {
	// A switch over self in an enum's method reads the receiver type, so the bare
	// members are offered — the self channel the lowering uses.
	src := rarityEnum + "impl Rarity {\n  pub fn color(): int {\n    switch self {\n      \n    }\n  }\n}\n"
	got := completeAt(t, src, "switch self {\n      ")
	assertBareMembers(t, got, "Common", "Rare", "Legend")
}

func TestExpectedEnumSwitchLetLocal(t *testing.T) {
	// A switch over a let local of enum type reads the local's settled type, so
	// the members are offered — the let channel the lowering uses.
	src := rarityEnum + "pub fn f(): int {\n  let r: Rarity = Rarity.Common\n  switch r {\n    \n  }\n}\n"
	got := completeAt(t, src, "switch r {\n    ")
	assertBareMembers(t, got, "Common", "Rare", "Legend")
}

func TestExpectedEnumSwitchNonEnumScrutinee(t *testing.T) {
	// A switch over an int parameter expects no bare member (it is a scalar
	// switch, which carries a wildcard, not enum members).
	src := "pub fn f(n: int): int {\n  switch n {\n    \n  }\n}\n"
	got := completeAt(t, src, "switch n {\n    ")
	if _, ok := got["Common"]; ok {
		t.Error("a non-enum switch offered a bare member")
	}
}

func TestExpectedEnumNotOfferedInArmBody(t *testing.T) {
	// The arm body after the arrow is the function's return position, not the
	// arm-value position: a bare member of the scrutinee enum must not be offered
	// there (the return type int expects none).
	src := rarityEnum + "pub fn f(r: Rarity): int {\n  switch r {\n    Common -> return \n  }\n}\n"
	got := completeAt(t, src, "Common -> return ")
	if _, ok := got["Rare"]; ok {
		t.Error("the arm body offered a scrutinee bare member")
	}
}

func TestExpectedEnumLetInitializerNotOffered(t *testing.T) {
	// A let initializer in a function body does not fold a bare member through its
	// annotation (only the top-level const path and the switch arm path do), so
	// even an enum-annotated let must not be offered one — the candidate would not
	// resolve.
	src := rarityEnum + "pub fn f(): int {\n  let r: Rarity = \n  return 1\n}\n"
	got := completeAt(t, src, "let r: Rarity = ")
	if _, ok := got["Common"]; ok {
		t.Error("a let initializer offered a bare member the lowering leaves undefined")
	}
}

func TestExpectedEnumAssignmentNotOffered(t *testing.T) {
	// An assignment's right-hand side does not fold a bare member either, so an
	// assignment to an enum-typed let must not be offered one.
	src := rarityEnum + "pub fn f(): Rarity {\n  let r: Rarity = Rarity.Common\n  r = \n  return r\n}\n"
	got := completeAt(t, src, "Common\n  r = ")
	if _, ok := got["Common"]; ok {
		t.Error("an assignment offered a bare member the lowering leaves undefined")
	}
}

func TestExpectedEnumComparisonRHSNotOffered(t *testing.T) {
	// A comparison's right-hand side does not resolve a bare member (it is a
	// method-call argument the bidirectional channels do not reach), so it must
	// not be offered there — the candidate would not resolve.
	src := rarityEnum + "pub fn f(r: Rarity): bool {\n  return r == \n}\n"
	got := completeAt(t, src, "r == ")
	if _, ok := got["Common"]; ok {
		t.Error("a comparison right-hand side offered a bare member that does not resolve")
	}
}

func TestExpectedEnumAssocConstNotOffered(t *testing.T) {
	// An associated constant inside an impl block resolves on a path that does
	// not supply the expected enum, so a bare member there stays undefined — the
	// completion must not offer one (only a top-level const initializer does).
	src := rarityEnum + "pub type Holder = int8 impl {\n  pub const Default: Rarity = \n}\n"
	got := completeAt(t, src, "Default: Rarity = ")
	if _, ok := got["Common"]; ok {
		t.Error("an associated const initializer offered a bare member the lowering leaves undefined")
	}
}
