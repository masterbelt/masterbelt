package diagnostic

import "testing"

func TestSeverityString(t *testing.T) {
	cases := map[Severity]string{
		Error:        "error",
		Warning:      "warning",
		Info:         "info",
		Hint:         "hint",
		Severity(99): "Severity(99)",
	}
	for sev, want := range cases {
		if got := sev.String(); got != want {
			t.Errorf("Severity(%d).String() = %q, want %q", int(sev), got, want)
		}
	}
}
