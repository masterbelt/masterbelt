// This file tests parser-wide error recovery and diagnostics: the diagnostics a
// malformed program records and the source offsets they resolve to.
package concrete

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
)

func TestParseDiagnostics(t *testing.T) {
	cases := []struct {
		name string
		src  string
		code diagnostic.Code
	}{
		{"missing name", "const = 1", CodeExpectedIdentifier},
		{"missing assign", "const X\n", CodeExpectedAssign},
		{"missing expr", "const X = \n", CodeExpectedExpression},
		{"missing rhs", "const X = 1 +\n", CodeExpectedOperand},
		{"missing unary operand", "const X = -\n", CodeExpectedOperand},
		{"missing type", "const X: = 1", CodeExpectedType},
		{"stray token", "= 1\n", CodeUnexpectedToken},
		{"param after comma", "const f = fn(x,) { return x }\n", CodeExpectedIdentifier},
		{"func lit without parens", "const f = fn x -> x * 2\n", CodeExpectedParamList},
		{"fat arrow is no body", "const f = fn(x) => x * 2\n", CodeExpectedFuncBody},
		{"block after arrow", "const f = fn(x) -> { return 1 }\n", CodeArrowBlockBody},
		{"missing arrow body", "const f = fn(x) ->\n", CodeExpectedExpression},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, diags := Parse([]byte(tc.src))
			found := false
			for _, d := range diags {
				if d.Code == tc.code {
					found = true
				}
			}
			if !found {
				t.Fatalf("src %q: want diagnostic %s, got %v", tc.src, tc.code, diags)
			}
			// Even malformed input must stay lossless.
			assertLossless(t, tc.src)
		})
	}
}

// TestExpectedOperandNamesOperator checks the operand-expected diagnostic is
// specific to the operator, not the generic "expected expression".
func TestExpectedOperandNamesOperator(t *testing.T) {
	cases := []struct{ src, operator string }{
		{"const X = 1 +\n", "+"},
		{"const X = 1 &&\n", "&&"},
		{"const X = !\n", "!"},
	}
	for _, tc := range cases {
		_, diags := Parse([]byte(tc.src))
		var msg string
		for _, d := range diags {
			if d.Code == CodeExpectedOperand {
				msg = d.Message
			}
		}
		want := "expected operand after '" + tc.operator + "'"
		if msg != want {
			t.Errorf("src %q: message = %q, want %q", tc.src, msg, want)
		}
	}
}

func TestDiagnosticOffsetsResolve(t *testing.T) {
	d := NewDocument([]byte("const = 1\n"))
	diags := d.Diagnostics()
	if len(diags) == 0 {
		t.Fatal("expected a diagnostic")
	}
	// The "expected identifier" points just after "const ".
	span := diags[0].Span(d.Buffer())
	if span.Start.Line != 1 {
		t.Fatalf("diagnostic line = %d, want 1", span.Start.Line)
	}
}
