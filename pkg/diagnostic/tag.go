package diagnostic

import "fmt"

// Tag is an orthogonal annotation on a diagnostic, independent of its Severity:
// a property of what the diagnostic means rather than how urgent it is, and a
// diagnostic may carry several. The values mirror the Language Server Protocol
// DiagnosticTag scale (Unnecessary = 1, Deprecated = 2) so they reach an editor
// without translation — the same contract Severity keeps with DiagnosticSeverity.
type Tag int

// The tags, numbered to match the Language Server Protocol DiagnosticTag scale.
const (
	// TagUnnecessary marks code the program does not need — unused or
	// unreachable. An editor fades it rather than underlining it, so the note
	// reads as "dead", not "wrong".
	TagUnnecessary Tag = iota + 1
	// TagDeprecated marks a use of something deprecated; an editor strikes it
	// through. No diagnostic carries it yet — it completes the scale so the
	// mapping to the protocol stays a faithful mirror.
	TagDeprecated
)

var tagNames = map[Tag]string{
	TagUnnecessary: "unnecessary",
	TagDeprecated:  "deprecated",
}

func (t Tag) String() string {
	if name, ok := tagNames[t]; ok {
		return name
	}
	return fmt.Sprintf("Tag(%d)", int(t))
}
