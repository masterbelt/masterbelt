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
