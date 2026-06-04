package diagnostic

import "fmt"

// Severity classifies a diagnostic. The values mirror the Language Server
// Protocol DiagnosticSeverity scale (Error = 1, Warning = 2, Info = 3,
// Hint = 4) so they can be surfaced to an editor without translation.
type Severity int

const (
	Error Severity = iota + 1
	Warning
	Info
	Hint
)

var severityNames = map[Severity]string{
	Error:   "error",
	Warning: "warning",
	Info:    "info",
	Hint:    "hint",
}

func (s Severity) String() string {
	if name, ok := severityNames[s]; ok {
		return name
	}
	return fmt.Sprintf("Severity(%d)", int(s))
}
