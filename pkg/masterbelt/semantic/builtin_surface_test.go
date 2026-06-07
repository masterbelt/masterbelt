package semantic

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
)

// TestExternOutsideBuiltin checks that every extern spelling in a user file —
// a top-level fn (pure or effectful), a method on a user type (effects
// included), and a static fn — errors at its declaration site: nothing
// outside the builtin surface can supply a native, so the declaration is an
// unverifiable claim whatever its effects say.
func TestExternOutsideBuiltin(t *testing.T) {
	cases := []struct {
		name string
		src  string
	}{
		{"pure fn", "extern fn twice(x: nint): nint\n"},
		{"effectful fn", "extern fn io async fetch(url: string): string\n"},
		{"nondet fn", "extern fn nondet roll(): nint\n"},
		{"method on user type", "pub type Bag = list<nint> impl {\n  pub extern fn fold(init: nint): nint\n}\n"},
		{"effectful method", "pub type Conn = string impl {\n  pub extern fn io send(msg: string): bool\n}\n"},
		{"static fn", "pub type Clock = nint impl {\n  pub extern static fn nondet now(): Clock\n}\n"},
		{"method on enum", "pub enum E {\n  A\n} impl {\n  pub extern fn tag(): nint\n}\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, diags := analyze(tc.src)
			if !hasCode(diags, CodeExternOutsideBuiltin) {
				t.Errorf("want extern_outside_builtin, got %v", codes(diags))
			}
		})
	}
}

// TestBuiltinOutsideBuiltin checks the `= builtin` spelling errors the same
// way at every site a user file can write it: a type body and an associated
// constant (type or enum impl). A top-level `const X = builtin` is already a
// parse error (the grammar admits the spelling only on an associated
// constant), so it needs no semantic arm.
func TestBuiltinOutsideBuiltin(t *testing.T) {
	cases := []struct {
		name string
		src  string
	}{
		{"type body", "type Foo = builtin\n"},
		{"assoc const", "pub type Level = sbyte impl {\n  pub const Max = builtin\n}\n"},
		{"enum assoc const", "pub enum E {\n  A\n} impl {\n  pub const Max = builtin\n}\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, diags := analyze(tc.src)
			if !hasCode(diags, CodeBuiltinOutsideBuiltin) {
				t.Errorf("want builtin_outside_builtin, got %v", codes(diags))
			}
		})
	}
}

// TestBuiltinSurfaceSeverityIsError pins the severity: the contract is a
// language rule, not a lint.
func TestBuiltinSurfaceSeverityIsError(t *testing.T) {
	for _, src := range []string{"extern fn f(): nint\n", "type Foo = builtin\n"} {
		_, diags := analyze(src)
		found := false
		for _, d := range diags {
			if d.Code == CodeExternOutsideBuiltin || d.Code == CodeBuiltinOutsideBuiltin {
				found = true
				if d.Severity != diagnostic.Error {
					t.Errorf("%q: severity = %v, want error", src, d.Severity)
				}
			}
		}
		if !found {
			t.Errorf("%q: no builtin-surface diagnostic, got %v", src, codes(diags))
		}
	}
}

// TestBuiltinSurfaceExemptsPrelude pins the boundary: the prelude — full of
// extern and `= builtin` — loads through the trusted channel (LoadPrelude
// resolves it without assembling), so the same spellings stay legal there.
// A failure here means the load channel started reporting user-file rules
// against the builtin surface.
func TestBuiltinSurfaceExemptsPrelude(t *testing.T) {
	if _, _, err := LoadPrelude(builtin.Default()); err != nil {
		t.Fatalf("prelude failed to load: %v", err)
	}
}
