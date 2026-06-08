// This file tests the human-facing diagnostic messages the analyzer renders.
package semantic

import (
	"testing"
)

func TestDiagnosticMessages(t *testing.T) {
	cases := []struct{ src, code, message string }{
		{"const x = 1 && 2\n", string(CodeInvalidOperation), "cannot apply method anan to nint, nint"},
		{"const x = 1 / 0\n", string(CodeDivisionByZero), "division by zero"},
		{"const x: sbyte = true\n", string(CodeTypeMismatch), "cannot use bool as sbyte"},
	}
	for _, tc := range cases {
		_, diags := analyze(tc.src)
		var msg string
		for _, d := range diags {
			if string(d.Code) == tc.code {
				msg = d.Message
			}
		}
		if msg != tc.message {
			t.Errorf("%q: message = %q, want %q", tc.src, msg, tc.message)
		}
	}
}
