package semantic

import "testing"

// TestAssocConstBuiltinBounds checks that the builtin integer bounds the prelude
// declares resolve and fold to the registry's value range, and adapt to the
// annotated type like an integer literal does.
func TestAssocConstBuiltinBounds(t *testing.T) {
	cases := []struct {
		src  string
		name string
		want string
	}{
		{"const X = sbyte.Max\n", "X", "127"},
		{"const X = sbyte.Min\n", "X", "-128"},
		{"const X = byte.Max\n", "X", "255"},
		{"const X = byte.Min\n", "X", "0"},
		{"const X = short.Max\n", "X", "32767"},
		{"const X = int.Max\n", "X", "2147483647"},
		{"const X: int = short.Max\n", "X", "32767"}, // the bound adapts to int32
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			m, diags := analyze(tc.src)
			if len(diags) != 0 {
				t.Fatalf("src %q: unexpected diagnostics: %v", tc.src, codes(diags))
			}
			c := m.Consts[0]
			if c.Name != tc.name {
				t.Fatalf("first const = %q, want %q", c.Name, tc.name)
			}
			if c.Eval == nil || c.Eval.String() != tc.want {
				t.Errorf("%s = %v, want %s", tc.name, c.Eval, tc.want)
			}
		})
	}
}

// TestAssocConstInRefinement checks the magic-number elimination the builtin
// bounds enable: a refinement may name a builtin bound, and the per-constant
// check uses its folded value.
func TestAssocConstInRefinement(t *testing.T) {
	src := "pub type Port = int where self >= 1 && self <= short.Max\n"
	if _, diags := analyze(src + "const Good: Port = 8080\n"); len(diags) != 0 {
		t.Errorf("8080 within short.Max: unexpected diagnostics %v", codes(diags))
	}
	if _, diags := analyze(src + "const Bad: Port = 40000\n"); !hasCode(diags, CodeRefinementViolation) {
		t.Errorf("40000 exceeds short.Max: want a refinement violation, got %v", codes(diags))
	}
}

// TestAssocConstIntHasNoBound checks that the arbitrary-precision integers
// expose no bound: int.Max is an unknown associated constant, since int declares
// none (it has no fixed range).
func TestAssocConstIntHasNoBound(t *testing.T) {
	if _, diags := analyze("const X = nint.Max\n"); !hasCode(diags, CodeUnknownAssociatedConst) {
		t.Errorf("nint.Max: want unknown_associated_const, got %v", codes(diags))
	}
}
