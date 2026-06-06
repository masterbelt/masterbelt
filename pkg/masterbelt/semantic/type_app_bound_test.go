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
