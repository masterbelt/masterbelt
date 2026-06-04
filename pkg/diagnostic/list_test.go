package diagnostic

import "testing"

func TestList(t *testing.T) {
	var l List
	if l.Len() != 0 || l.HasErrors() {
		t.Fatalf("zero value should be empty and error-free")
	}

	l.Add(Diagnostic{Severity: Warning, Code: "w"})
	if l.HasErrors() {
		t.Errorf("HasErrors() = true after only a warning")
	}

	l.Add(Diagnostic{Severity: Error, Code: "e"})
	if l.Len() != 2 {
		t.Fatalf("Len() = %d, want 2", l.Len())
	}
	if !l.HasErrors() {
		t.Errorf("HasErrors() = false, want true after an error")
	}

	items := l.Items()
	if items[0].Severity != Warning || items[1].Severity != Error {
		t.Errorf("insertion order not preserved: %v", items)
	}
}
