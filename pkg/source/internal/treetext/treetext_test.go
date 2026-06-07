package treetext

import (
	"testing"
)

// TestLinesRoundTrip pins the reader against the writer: lines written at
// their depths read back identically.
func TestLinesRoundTrip(t *testing.T) {
	var w Writer
	w.Line(0, "File")
	w.Line(1, `Ident "x"`)
	w.Line(2, "deep")
	w.Line(0, "Tail")
	lines, err := Lines(w.Bytes())
	if err != nil {
		t.Fatalf("Lines: %v", err)
	}
	want := []Line{
		{Depth: 0, Content: "File", Number: 1},
		{Depth: 1, Content: `Ident "x"`, Number: 2},
		{Depth: 2, Content: "deep", Number: 3},
		{Depth: 0, Content: "Tail", Number: 4},
	}
	if len(lines) != len(want) {
		t.Fatalf("got %d lines, want %d", len(lines), len(want))
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("line %d = %+v, want %+v", i, lines[i], want[i])
		}
	}
}

// TestLinesRejects pins the malformed shapes the writers never produce.
func TestLinesRejects(t *testing.T) {
	for name, input := range map[string]string{
		"tab indentation": "\tFile\n",
		"odd indentation": " File\n",
		"interior blank":  "File\n\nTail\n",
		"all-space line":  "File\n   \n",
	} {
		if _, err := Lines([]byte(input)); err == nil {
			t.Errorf("%s: Lines(%q) accepted, want error", name, input)
		}
	}
}

// TestLinesEdges pins the accepted edges: empty input is zero lines, a missing
// final newline still yields the last line.
func TestLinesEdges(t *testing.T) {
	if lines, err := Lines(nil); err != nil || len(lines) != 0 {
		t.Errorf("Lines(nil) = %v, %v; want no lines, no error", lines, err)
	}
	lines, err := Lines([]byte("File"))
	if err != nil || len(lines) != 1 || lines[0].Content != "File" {
		t.Errorf("Lines(no trailing newline) = %v, %v; want the one line", lines, err)
	}
}
