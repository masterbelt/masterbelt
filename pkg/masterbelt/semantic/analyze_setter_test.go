package semantic

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
)

const setterType = "pub type Celsius = { deg: nint } impl {\n" +
	"  pub static fn freezing(): Celsius {\n    return Celsius{ deg: 0 }\n  }\n" +
	"  pub set fahrenheit(v: nint): self {\n    return Celsius{ deg: (v - 32) * 5 / 9 }\n  }\n" +
	"}\n"

// TestSetterRebind checks a property write on a let local rebinds it through the
// setter — the setter computes the next value and the local is rebound to it —
// and the result folds.
func TestSetterRebind(t *testing.T) {
	src := setterType +
		"fn boiling(): Celsius {\n  let c: Celsius = Celsius{ deg: 0 }\n  c.fahrenheit = 212\n  return c\n}\n" +
		"const B = boiling()\n" +
		"assert B.deg == 100\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	if !m.Asserts[0].Held() {
		t.Errorf("assert did not hold: %s", m.Asserts[0].Diagram)
	}
}

// TestSetterDiagnostics covers the property-write error paths.
func TestSetterDiagnostics(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want diagnostic.Code
	}{
		{
			"no setter is immutable data",
			"pub type P = { x: nint } impl {}\nfn f(): P {\n  let p: P = P{ x: 0 }\n  p.x = 1\n  return p\n}\n",
			CodeImmutableData,
		},
		{
			"setter value type mismatch",
			setterType + "fn f(): Celsius {\n  let c: Celsius = Celsius{ deg: 0 }\n  c.fahrenheit = true\n  return c\n}\n",
			CodeTypeMismatch,
		},
		{
			"setter on a parameter is immutable",
			setterType + "fn f(c: Celsius): Celsius {\n  c.fahrenheit = 212\n  return c\n}\n",
			CodeImmutableData,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, diags := analyze(tc.src)
			if !hasCode(diags, tc.want) {
				t.Fatalf("codes = %v, want %s", codes(diags), tc.want)
			}
		})
	}
}

// TestSetterNoFieldClash checks a setter write does not also report immutable
// data for the same target: the property write is valid, so it is the only
// finding (none).
func TestSetterCleanWrite(t *testing.T) {
	src := setterType +
		"fn f(): Celsius {\n  let c: Celsius = Celsius{ deg: 0 }\n  c.fahrenheit = 212\n  return c\n}\n"
	_, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("a valid property write should report nothing: %v", codes(diags))
	}
}
