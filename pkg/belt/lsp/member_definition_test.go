package lsp

import (
	"strings"
	"testing"
)

// memberDefSrc declares a master with a static fn and a scope entry, a plain type
// with a value method, and a function that calls each — so every user-declared
// member kind has both a declaration and a call site to navigate between.
const memberDefSrc = "master Cards {\n" +
	"  record { id: int, cost: int } impl {\n" +
	"    pub static fn zero(): nint {\n      return 0\n    }\n" +
	"  }\n" +
	"  scope {\n    pub expensive() -> where(fn(c) -> c.cost > 100)\n  }\n" +
	"  primary id\n}\n" +
	"pub type Counter = int impl {\n  pub fn inc(): self {\n    return self\n  }\n}\n" +
	"fn probe(x: Counter): nint {\n" +
	"  let a = Cards.zero()\n" +
	"  let b = Cards.expensive()\n" +
	"  let d = x.inc()\n" +
	"  return a\n}\n"

// TestDefinitionMemberMethod pins go-to-definition on a member-access method call:
// a master's static fn (Cards.zero()), a scope entry desugared to one
// (Cards.expensive()), and a value method on a plain type (x.inc()) each jump to
// their declaration name, resolved through the receiver's type — the same resolution
// hover uses, now wired into definition.
func TestDefinitionMemberMethod(t *testing.T) {
	doc := testView(memberDefSrc)
	buf := doc.Buffer()
	for _, c := range []struct{ name, call, declName string }{
		{"static fn", "Cards.zero()", "zero"},
		{"scope entry", "Cards.expensive()", "expensive"},
		{"value method", "x.inc()", "inc"},
	} {
		t.Run(c.name, func(t *testing.T) {
			off := strings.Index(memberDefSrc, c.call) + strings.Index(c.call, ".") + 1
			locs := definition(doc, off)
			if len(locs) != 1 {
				t.Fatalf("definition(%s) = %d locations, want 1", c.call, len(locs))
			}
			start := fromPosition(buf, locs[0].Range.Start)
			end := fromPosition(buf, locs[0].Range.End)
			if got := memberDefSrc[start:end]; got != c.declName {
				t.Errorf("definition(%s) covers %q, want the declaration name %q", c.call, got, c.declName)
			}
			// It must jump to the declaration, which precedes the call site, not stay
			// on the call's own member token.
			if start >= off {
				t.Errorf("definition(%s) stayed at/after the call (offset %d >= %d); want the earlier declaration", c.call, start, off)
			}
		})
	}
}

// TestDefinitionMemberCallInAssocConst pins that a method call inside an associated
// constant's initializer navigates: the editor's expression walk reaches the impl
// block's const initializers, and the resolution walk covers their value graphs, so
// Counter(0).inc() in const Next = ... resolves inc to its declaration.
func TestDefinitionMemberCallInAssocConst(t *testing.T) {
	src := "pub type Counter = int impl {\n  pub fn inc(): self {\n    return self\n  }\n  pub const Next: Counter = Counter(0).inc()\n}\n"
	doc := testView(src)
	off := strings.Index(src, "Counter(0).inc()") + len("Counter(0).")
	locs := definition(doc, off)
	if len(locs) != 1 {
		t.Fatalf("definition(inc in assoc const) = %d locations, want 1", len(locs))
	}
	start := fromPosition(doc.Buffer(), locs[0].Range.Start)
	want := strings.Index(src, "pub fn inc") + len("pub fn ")
	if start != want {
		t.Errorf("definition start = %d, want the inc declaration at %d", start, want)
	}
}

// TestDefinitionMemberCallInAssert pins that a method call inside a top-level assertion
// navigates: the resolution walk covers assert condition graphs, so the inc of
// assert C.inc() == C resolves to its declaration.
func TestDefinitionMemberCallInAssert(t *testing.T) {
	src := "pub type Counter = int impl {\n  pub fn inc(): self {\n    return self\n  }\n}\n" +
		"const C: Counter = 0\nassert C.inc() == C\n"
	doc := testView(src)
	off := strings.Index(src, "C.inc()") + len("C.")
	locs := definition(doc, off)
	if len(locs) != 1 {
		t.Fatalf("definition(inc in assert) = %d locations, want 1", len(locs))
	}
	start := fromPosition(doc.Buffer(), locs[0].Range.Start)
	want := strings.Index(src, "pub fn inc") + len("pub fn ")
	if start != want {
		t.Errorf("definition start = %d, want the inc declaration at %d", start, want)
	}
}

// TestDefinitionSetterInLambda pins that a setter write nested in a function literal
// navigates: the statement walk descends into lambda bodies, so the fahrenheit of a
// c.fahrenheit = 212 written inside a lambda resolves to the setter declaration.
func TestDefinitionSetterInLambda(t *testing.T) {
	src := accessorType +
		"fn boil(): Celsius {\n" +
		"  let bump = fn() {\n    let c = Celsius.freezing()\n    c.fahrenheit = 212\n    return c\n  }\n" +
		"  return bump()\n}\n"
	doc := testView(src)
	off := strings.Index(src, "c.fahrenheit = 212") + len("c.")
	locs := definition(doc, off)
	want := strings.Index(src, "pub set fahrenheit") + len("pub set ")
	if len(locs) != 1 || fromPosition(doc.Buffer(), locs[0].Range.Start) != want {
		t.Errorf("a setter write in a lambda should navigate to the setter at %d; got %+v", want, locs)
	}
}

// TestDefinitionMemberCallInEnumMember pins that a method call in an enum member's
// initializer navigates: the editor's expression walk reaches enum member
// initializers, so inc in a = Counter(1).inc() resolves to its declaration.
func TestDefinitionMemberCallInEnumMember(t *testing.T) {
	src := "pub type Counter = int impl {\n  pub fn inc(): self {\n    return self\n  }\n}\n" +
		"enum E {\n  a = Counter(1).inc()\n}\n"
	doc := testView(src)
	off := strings.Index(src, "Counter(1).inc()") + len("Counter(1).")
	locs := definition(doc, off)
	want := strings.Index(src, "pub fn inc") + len("pub fn ")
	if len(locs) != 1 || fromPosition(doc.Buffer(), locs[0].Range.Start) != want {
		t.Errorf("a method call in an enum member initializer should navigate to inc at %d; got %+v", want, locs)
	}
}

// TestDefinitionMemberCallInRefinement pins that a method call in a refinement
// predicate navigates: the editor's expression walk reaches a type's where clause, so
// positive in where self.positive() resolves to its declaration.
func TestDefinitionMemberCallInRefinement(t *testing.T) {
	src := "pub type Lvl = sbyte where self.positive() impl {\n  pub fn positive(): bool {\n    return self > 0\n  }\n}\n"
	doc := testView(src)
	off := strings.Index(src, "self.positive()") + len("self.")
	locs := definition(doc, off)
	want := strings.Index(src, "pub fn positive") + len("pub fn ")
	if len(locs) != 1 || fromPosition(doc.Buffer(), locs[0].Range.Start) != want {
		t.Errorf("a method call in a refinement predicate should navigate to positive at %d; got %+v", want, locs)
	}
}

// TestDefinitionInterfaceRequirement pins that a call of an interface requirement
// navigates to the requirement's declaration in the interface: x.f() where x: I and
// interface I { f(): nint } resolves to f, recovered from the interface member even
// though a required method carries no method-declaration backpointer.
func TestDefinitionInterfaceRequirement(t *testing.T) {
	src := "pub interface I {\n  f(): nint\n}\n" +
		"pub fn g(x: I): nint {\n  return x.f()\n}\n"
	doc := testView(src)
	off := strings.Index(src, "x.f()") + len("x.")
	locs := definition(doc, off)
	want := strings.Index(src, "f(): nint")
	if len(locs) != 1 || fromPosition(doc.Buffer(), locs[0].Range.Start) != want {
		t.Errorf("an interface requirement call should navigate to the requirement f at %d; got %+v", want, locs)
	}
}

// TestDefinitionSetterWriteOnTypedReceiver pins that a setter write whose receiver type
// the checker already knows (a parameter) resolves to the setter, not the getter: the
// write is detected before the getter read, so go-to-definition on the assignment
// target finds the setter even when a getter of the same name exists.
func TestDefinitionSetterWriteOnTypedReceiver(t *testing.T) {
	src := accessorType + "fn f(c: Celsius): Celsius {\n  c.fahrenheit = 212\n  return c\n}\n"
	doc := testView(src)
	off := strings.Index(src, "c.fahrenheit = 212") + len("c.")
	locs := definition(doc, off)
	want := strings.Index(src, "pub set fahrenheit") + len("pub set ")
	if len(locs) != 1 || fromPosition(doc.Buffer(), locs[0].Range.Start) != want {
		t.Errorf("a setter write on a typed receiver should navigate to the setter at %d, not the getter; got %+v", want, locs)
	}
}

// TestDefinitionRelationBuiltinHasNoLocation pins that a relation builtin (count,
// assembled from the prelude) has no navigable declaration: it resolves to zero
// locations rather than a phantom position, since the prelude is in no workspace file.
func TestDefinitionRelationBuiltinHasNoLocation(t *testing.T) {
	src := relationMaster + "fn probe(): nint {\n  return Cards.count()\n}\n"
	doc := testView(src)
	off := strings.Index(src, "Cards.count()") + len("Cards.")
	if locs := definition(doc, off); len(locs) != 0 {
		t.Errorf("a relation builtin has no navigable declaration; want 0 locations, got %d: %+v", len(locs), locs)
	}
}

// TestDefinitionMemberValueBindingWins pins that a value binding whose name shadows a
// type wins the member call: a parameter named Cards typed as another type, calling
// Cards.zero(), navigates to that type's zero method, not the master's same-named
// static fn — definition reads the checker's lowered call, which resolved the value
// binding, rather than re-deriving the static fn through the shadowed type name.
func TestDefinitionMemberValueBindingWins(t *testing.T) {
	src := "master Cards {\n  record { id: int } impl {\n    pub static fn zero(): nint {\n      return 0\n    }\n  }\n  primary id\n}\n" +
		"pub type Other = int impl {\n  pub fn zero(): nint {\n    return 0\n  }\n}\n" +
		"fn f(Cards: Other): nint {\n  return Cards.zero()\n}\n"
	doc := testView(src)
	off := strings.Index(src, "Cards.zero()") + len("Cards.")
	locs := definition(doc, off)
	if len(locs) != 1 {
		t.Fatalf("definition(Cards.zero()) = %d locations, want 1", len(locs))
	}
	start := fromPosition(doc.Buffer(), locs[0].Range.Start)
	wantOther := strings.Index(src, "pub fn zero(): nint") + len("pub fn ")
	wrongStatic := strings.Index(src, "pub static fn zero(): nint") + len("pub static fn ")
	if start == wrongStatic {
		t.Errorf("definition jumped to the master's static fn; the parameter Cards: Other shadows it, want Other.zero")
	}
	if start != wantOther {
		t.Errorf("definition start offset = %d, want Other.zero at %d", start, wantOther)
	}
}

// TestDefinitionMemberReadIsNotMethod pins that a member access used as a read, not a
// call, does not navigate to a same-named method: x.inc (a method value) yields no
// location, while x.inc() (a call) navigates to inc — method and read are distinct
// member spaces, so only the call resolves the method.
func TestDefinitionMemberReadIsNotMethod(t *testing.T) {
	src := "pub type Counter = int impl {\n  pub fn inc(): self {\n    return self\n  }\n}\n" +
		"fn f(x: Counter): Counter {\n  let y = x.inc\n  return x.inc()\n}\n"
	doc := testView(src)

	read := strings.Index(src, "x.inc\n") + len("x.")
	if locs := definition(doc, read); len(locs) != 0 {
		t.Errorf("a member read is not a method call; x.inc must navigate nowhere, got %d: %+v", len(locs), locs)
	}

	call := strings.Index(src, "x.inc()") + len("x.")
	locs := definition(doc, call)
	if len(locs) != 1 {
		t.Fatalf("definition(x.inc()) = %d locations, want 1 (the method)", len(locs))
	}
	start := fromPosition(doc.Buffer(), locs[0].Range.Start)
	if got := src[start : start+len("inc")]; got != "inc" {
		t.Errorf("definition(x.inc()) covers %q, want the method name inc", got)
	}
}

// TestDefinitionMetatypeMethodNotStatic pins that a metatype method call on a bare
// type name resolves to the metatype method, not a same-named static fn: Level.eql(Level)
// compares two type values through the builtin metatype eql even though Level also
// declares a static fn eql, so definition navigates nowhere (the metatype method has no
// declaration) rather than jumping to the static fn the checker did not resolve.
func TestDefinitionMetatypeMethodNotStatic(t *testing.T) {
	src := "pub type Level = sbyte impl {\n  pub static fn eql(o: Level): bool {\n    return true\n  }\n}\n" +
		"assert Level.eql(Level)\n"
	doc := testView(src)
	off := strings.Index(src, "Level.eql(Level)\n") + len("Level.")
	if locs := definition(doc, off); len(locs) != 0 {
		t.Errorf("Level.eql resolves to the metatype method, not the static fn; want 0 locations, got %d: %+v", len(locs), locs)
	}
}

// TestDefinitionAccessor pins that a getter read and a setter write navigate to the
// accessor declaration — the member access that is not a call resolves through the
// receiver's getters and setters, so it lands on the property's accessor rather than
// falling through to no location.
func TestDefinitionAccessor(t *testing.T) {
	src := accessorType +
		"const Cref: Celsius = Celsius{ deg: 0 }\n" +
		"const RF: nint = Cref.fahrenheit\n" +
		"fn boil(): Celsius {\n  let c = Celsius.freezing()\n  c.fahrenheit = 212\n  return c\n}\n"
	doc := testView(src)
	buf := doc.Buffer()
	getterDecl := strings.Index(src, "pub get fahrenheit") + len("pub get ")
	setterDecl := strings.Index(src, "pub set fahrenheit") + len("pub set ")

	t.Run("getter read navigates to the getter only", func(t *testing.T) {
		off := strings.Index(src, "Cref.fahrenheit") + len("Cref.")
		locs := definition(doc, off)
		if len(locs) != 1 || fromPosition(buf, locs[0].Range.Start) != getterDecl {
			t.Errorf("a getter read should navigate to the getter declaration at %d only, not the setter; got %+v", getterDecl, locs)
		}
	})
	t.Run("setter write navigates to the setter only", func(t *testing.T) {
		off := strings.Index(src, "c.fahrenheit = 212") + len("c.")
		locs := definition(doc, off)
		if len(locs) != 1 || fromPosition(buf, locs[0].Range.Start) != setterDecl {
			t.Errorf("a setter write should navigate to the setter declaration at %d only, not the getter; got %+v", setterDecl, locs)
		}
	})
}
