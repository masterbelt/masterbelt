package diagnostic

// List accumulates diagnostics produced during a phase such as lexing or
// parsing. The zero value is an empty, ready-to-use list. Diagnostics are added
// via Add (taking a value built by a generated constructor); there is no
// free-text constructor.
type List struct {
	items []Diagnostic
}

// Add appends d to the list.
func (l *List) Add(d Diagnostic) {
	l.items = append(l.items, d)
}

// Items returns the recorded diagnostics in insertion order. The result aliases
// the list's backing storage; do not mutate it.
func (l *List) Items() []Diagnostic {
	return l.items
}

// Len reports how many diagnostics have been recorded.
func (l *List) Len() int {
	return len(l.items)
}

// HasErrors reports whether any recorded diagnostic has Error severity.
func (l *List) HasErrors() bool {
	for _, d := range l.items {
		if d.Severity == Error {
			return true
		}
	}
	return false
}
