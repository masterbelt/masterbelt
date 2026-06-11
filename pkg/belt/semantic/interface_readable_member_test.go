// These tests pin the interface readable-member requirement: `X: T` (no parens)
// requires a member readable as x.X of type T, satisfied by a field or a getter —
// the requirement side of read/projection symmetry. Conformance is kind-aware: a
// readable requirement is met by a field or getter (not a method), and a method
// requirement `X(): T` is met by a method only (not a getter or static), closing
// the name-only looseness where a getter satisfied a method requirement.
package semantic

import "testing"

func TestReadableRequirementFieldSatisfies(t *testing.T) {
	// A field named like the readable requirement, of the requirement's type,
	// satisfies it: a field is the canonical readable member.
	src := "pub interface Named {\n  name: string\n}\n" +
		"pub type Person = { name: string } impl Named {}\n"
	_, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("want clean (field satisfies readable requirement), got %v", codes(diags))
	}
}

func TestReadableRequirementGetterSatisfies(t *testing.T) {
	// A getter read as x.name (no parens) of the requirement's type satisfies the
	// readable requirement, the same as a field.
	src := "pub interface Named {\n  name: string\n}\n" +
		"pub type Person = { n: string } impl Named {\n  pub get name(): string { return self.n }\n}\n"
	_, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("want clean (getter satisfies readable requirement), got %v", codes(diags))
	}
}

func TestReadableRequirementWrongTypeRejected(t *testing.T) {
	// The readable member exists but reads a different type than the requirement:
	// the read type is checked, so name: nint does not satisfy name: string.
	src := "pub interface Named {\n  name: string\n}\n" +
		"pub type Person = { name: nint } impl Named {}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeMissingReadableMember) {
		t.Fatalf("want missing_readable_member (wrong read type), got %v", codes(diags))
	}
}

func TestReadableRequirementMethodDoesNotSatisfy(t *testing.T) {
	// A method named name (read as x.name(), not x.name) is not a readable member,
	// so it does not satisfy a readable requirement — kind-aware conformance.
	src := "pub interface Named {\n  name: string\n}\n" +
		"pub type Person = { n: string } impl Named {\n  pub fn name(): string { return self.n }\n}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeMissingReadableMember) {
		t.Fatalf("want missing_readable_member (a method is not readable), got %v", codes(diags))
	}
}

func TestReadableRequirementMissingRejected(t *testing.T) {
	// No member of that name at all: the readable requirement is unmet.
	src := "pub interface Named {\n  name: string\n}\n" +
		"pub type Person = { other: string } impl Named {}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeMissingReadableMember) {
		t.Fatalf("want missing_readable_member (no such member), got %v", codes(diags))
	}
}

func TestMethodRequirementGetterDoesNotSatisfy(t *testing.T) {
	// A getter read as x.greet is not callable as x.greet(), so it no longer
	// satisfies a method requirement greet(): string — the name-only looseness is
	// closed.
	src := "pub interface Greeter {\n  greet(): string\n}\n" +
		"pub type P = { g: string } impl Greeter {\n  pub get greet(): string { return self.g }\n}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeMissingRequiredMethod) {
		t.Fatalf("want missing_required_method (a getter is not a method), got %v", codes(diags))
	}
}

func TestMethodRequirementMethodSatisfies(t *testing.T) {
	// A real method satisfies a method requirement, as before.
	src := "pub interface Greeter {\n  greet(): string\n}\n" +
		"pub type P = { g: string } impl Greeter {\n  pub fn greet(): string { return self.g }\n}\n"
	_, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("want clean (method satisfies method requirement), got %v", codes(diags))
	}
}

func TestReadableRequirementBodyRejected(t *testing.T) {
	// A readable-member requirement carries no default, so a stray body is reported
	// and the member stays required — it does not become a provided default that an
	// implementor inherits and satisfies vacuously.
	src := "pub interface Named {\n  name: string { return \"x\" }\n}\n" +
		"pub type Person = { other: nint } impl Named {}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeReadableMemberHasBody) {
		t.Fatalf("want readable_member_has_body, got %v", codes(diags))
	}
	if !hasCode(diags, CodeMissingReadableMember) {
		t.Fatalf("want the requirement still unmet (not vacuously provided), got %v", codes(diags))
	}
}

func TestReadableRequirementEmptyBodyRejected(t *testing.T) {
	// A written body is rejected even when empty: an empty block lowers to a nil
	// statement list, so block presence (not body content) is what is checked.
	src := "pub interface Named {\n  name: string {}\n}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeReadableMemberHasBody) {
		t.Fatalf("want readable_member_has_body for an empty block, got %v", codes(diags))
	}
}

func TestInheritedReadableAndMethodSameNameAllowed(t *testing.T) {
	// A child interface may add a method name() over a parent's readable name: a
	// readable member and a method are distinct members (as a field and a method of
	// the same name are on a concrete type), so it is not an override.
	src := "pub interface HasName {\n  name: string\n}\n" +
		"pub interface Both: HasName {\n  name(): string\n}\n"
	_, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("want clean (readable and method of one name are distinct), got %v", codes(diags))
	}
	// A same-kind redeclaration is still an override.
	override := "pub interface A {\n  name: string\n}\n" +
		"pub interface B: A {\n  name: string\n}\n"
	if _, diags := analyze(override); !hasCode(diags, CodeInterfaceMemberOverride) {
		t.Fatalf("want interface_member_override for a same-kind redeclaration, got %v", codes(diags))
	}
}

func TestReadableRequirementSelfType(t *testing.T) {
	// A readable requirement typed self is met by a self-returning getter: both the
	// requirement's self and the getter's self resolve to the implementing type, so
	// they compare equal rather than self-against-receiver.
	src := "pub interface Ident {\n  me: self\n}\n" +
		"pub type Person = { n: nint } impl Ident {\n  pub get me(): self { return self }\n}\n"
	_, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("want clean (self requirement met by self getter), got %v", codes(diags))
	}
}

func TestSameNameReadableAndMethodRequirements(t *testing.T) {
	// An interface requiring both a readable name and a method name checks each by
	// its own kind: a type with only a name field meets the readable requirement but
	// not the method one, so the method requirement is reported — the two are not
	// collapsed to the first by name.
	src := "pub interface Both {\n  name: string\n  name(): string\n}\n" +
		"pub type P = { name: string } impl Both {}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeMissingRequiredMethod) {
		t.Fatalf("want missing_required_method (the method requirement is distinct), got %v", codes(diags))
	}
}

func TestReadableRequirementInheritedGenericGetter(t *testing.T) {
	// A getter inherited through a concrete alias of a generic (StringBox =
	// Box<string>, Box<T> supplying get value(): T) is read with the application's
	// argument substituted, so StringBox.value reads string and meets value: string
	// — the receiver's arguments are not dropped when walking the underlying type.
	src := "pub interface HasValue<T> {\n  value: T\n}\n" +
		"pub type Box<T> = { v: T } impl {\n  pub get value(): T { return self.v }\n}\n" +
		"pub type StringBox = Box<string> impl HasValue<string> {}\n"
	_, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("want clean (inherited generic getter reads string), got %v", codes(diags))
	}
}

func TestInheritedGenericParentReadableRead(t *testing.T) {
	// Reading a readable member inherited from a generic parent binds the parent's
	// parameter through the inheritance: Child<string> inherits value: U from
	// Has<T> : value reads string, not the free U.
	src := "pub interface Has<U> {\n  value: U\n}\n" +
		"pub interface Child<T>: Has<T> {}\n" +
		"pub fn f(c: Child<string>): string {\n  return c.value\n}\n"
	_, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("want clean (inherited generic-parent readable reads string), got %v", codes(diags))
	}
}

func TestInheritedGenericParentMethodCall(t *testing.T) {
	// The same parent-parameter binding applies to an inherited method, through two
	// levels of generic inheritance: C<string> reaches foo(): U on A<U> with U bound
	// to string, so the call type-checks (the fix is not readable-specific).
	src := "pub interface A<U> {\n  foo(): U\n}\n" +
		"pub interface B<V>: A<V> {}\n" +
		"pub interface C<W>: B<W> {}\n" +
		"pub fn f(c: C<string>): string {\n  return c.foo()\n}\n"
	_, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("want clean (inherited generic-parent method binds the parameter), got %v", codes(diags))
	}
}

func TestReadableRequirementGenericInterface(t *testing.T) {
	// A readable requirement on a generic interface checks the read type with the
	// interface's argument substituted: impl Box<string> needs a value readable as
	// string; a field value: string satisfies it, value: nint does not.
	ok := "pub interface Box<T> {\n  value: T\n}\n" +
		"pub type SBox = { value: string } impl Box<string> {}\n"
	if _, diags := analyze(ok); len(diags) != 0 {
		t.Fatalf("want clean (value: string satisfies Box<string>), got %v", codes(diags))
	}
	bad := "pub interface Box<T> {\n  value: T\n}\n" +
		"pub type SBox = { value: nint } impl Box<string> {}\n"
	if _, diags := analyze(bad); !hasCode(diags, CodeMissingReadableMember) {
		t.Fatalf("want missing_readable_member (value: nint not string), got %v", codes(diags))
	}
}
