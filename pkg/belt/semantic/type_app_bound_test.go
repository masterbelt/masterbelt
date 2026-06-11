package semantic

import (
	"testing"
)

// TestUserGenericTypeBound checks the type-application bound enforcement is
// generic over any user-declared type's parameters. A parameter declared with a
// bound (pair<T: comparable>) is satisfied only by an argument that opts into
// the bound's interface; a non-comparable argument is reported as
// bound_not_satisfied at the argument's syntax. The application is written in a
// function signature, where the bound check fires against the argument without
// depending on collection-literal assignment.
func TestUserGenericTypeBound(t *testing.T) {
	src := "pub type pair<T: comparable> = list<T>\n"

	t.Run("comparable arg ok", func(t *testing.T) {
		_, diags := analyze(src + "pub fn take(p: pair<string>): nint {\n  return 0\n}\n")
		if hasCode(diags, CodeBoundNotSatisfied) {
			t.Fatalf("string is comparable; want no bound_not_satisfied, got %v", codes(diags))
		}
	})

	t.Run("non-comparable arg reported", func(t *testing.T) {
		_, diags := analyze(src + "pub fn take(p: pair<{x: nint}>): nint {\n  return 0\n}\n")
		if !hasCode(diags, CodeBoundNotSatisfied) {
			t.Fatalf("anonymous record is not comparable; want bound_not_satisfied, got %v", codes(diags))
		}
	})

	t.Run("annotation site reported", func(t *testing.T) {
		_, diags := analyze(src + "const bad: pair<{x: nint}> = []\n")
		if !hasCode(diags, CodeBoundNotSatisfied) {
			t.Fatalf("anonymous record key in an annotation is not comparable; want bound_not_satisfied, got %v", codes(diags))
		}
	})
}

// TestTypeApplicationArity pins the type-application arity check: a declared
// generic applied to the wrong number of type arguments is type_arity_mismatch,
// reported wherever an application is resolved — a type alias, a field type, a
// projection (resolved or forward), a const annotation, and a function signature
// — instead of the silent invalid type it used to fold to. A correctly-applied
// generic, a builtin generic (whose parameters are not tracked on the def), and a
// non-generic name given stray arguments are all left clean.
func TestTypeApplicationArity(t *testing.T) {
	reported := []struct{ name, src string }{
		{"alias too many", "pub type Box<T> = { value: T }\npub type B = Box<long, string>\n"},
		{"alias too few", "pub type Pair<A, B> = { a: A, b: B }\npub type P = Pair<long>\n"},
		{"forward alias too many", "pub type B = Box<long, string>\npub type Box<T> = { value: T }\n"},
		{"field type too many", "pub type Box<T> = { value: T }\npub type S = { v: Box<long, string> }\n"},
		{"projection resolved too many", "pub type Box<T> = { value: T }\npub type S = { v: Box.value<long, string> }\n"},
		{"projection forward too many", "pub type S = { v: Box.value<long, string> }\npub type Box<T> = { value: T }\n"},
		{"getter projection forward too many", "pub type E = LateBox.item<long, string>\n" +
			"pub type LateBox<T> = { v: T } impl {\n  pub get item(): T { return self.v }\n}\n"},
		{"function parameter too many", "pub type Box<T> = { value: T }\npub fn f(x: Box<long, string>): nint {\n  return 1\n}\n"},
	}
	for _, c := range reported {
		t.Run(c.name, func(t *testing.T) {
			_, diags := analyze(c.src)
			if !hasCode(diags, CodeTypeArityMismatch) {
				t.Fatalf("want type_arity_mismatch, got %v", codes(diags))
			}
		})
	}

	clean := []struct{ name, src string }{
		{"generic applied correctly", "pub type Box<T> = { value: T }\npub type B = Box<long>\n"},
		{"two-parameter applied correctly", "pub type Pair<A, B> = { a: A, b: B }\npub type P = Pair<long, string>\n"},
		{"builtin list", "pub type L = list<long>\n"},
		{"builtin map", "pub type M = map<string, long>\n"},
		{"builtin optional", "pub type O = optional<long>\n"},
		{"non-generic projection ignores stray args", "pub type Level = sbyte\n" +
			"pub type Item = { level: Level }\npub type X = Item.level<long>\n"},
	}
	for _, c := range clean {
		t.Run(c.name, func(t *testing.T) {
			_, diags := analyze(c.src)
			if hasCode(diags, CodeTypeArityMismatch) {
				t.Fatalf("want no type_arity_mismatch, got %v", codes(diags))
			}
		})
	}
}
