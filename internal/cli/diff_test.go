package cli

import "testing"

func TestUnifiedDiffIdentical(t *testing.T) {
	if got := unifiedDiff("x", "x", "a\nb\n", "a\nb\n"); got != "" {
		t.Errorf("identical inputs diff = %q, want empty", got)
	}
}

func TestUnifiedDiffSingleChange(t *testing.T) {
	a := "one\ntwo\nthree\n"
	b := "one\nTWO\nthree\n"
	want := "--- a/f\n+++ b/f\n@@ -1,3 +1,3 @@\n one\n-two\n+TWO\n three\n"
	if got := unifiedDiff("f", "f", a, b); got != want {
		t.Errorf("diff =\n%q\nwant\n%q", got, want)
	}
}

func TestUnifiedDiffInsertAndDelete(t *testing.T) {
	// A pure insertion shows a zero-length original side anchored at the line
	// it follows; a pure deletion mirrors it.
	if got := unifiedDiff("f", "f", "a\nb\n", "a\nx\nb\n"); got != "--- a/f\n+++ b/f\n@@ -1,2 +1,3 @@\n a\n+x\n b\n" {
		t.Errorf("insertion diff =\n%q", got)
	}
	if got := unifiedDiff("f", "f", "a\nx\nb\n", "a\nb\n"); got != "--- a/f\n+++ b/f\n@@ -1,3 +1,2 @@\n a\n-x\n b\n" {
		t.Errorf("deletion diff =\n%q", got)
	}
}

func TestUnifiedDiffNoTrailingNewline(t *testing.T) {
	got := unifiedDiff("f", "f", "a", "a\n")
	want := "--- a/f\n+++ b/f\n@@ -1 +1 @@\n-a\n\\ No newline at end of file\n+a\n"
	if got != want {
		t.Errorf("diff =\n%q\nwant\n%q", got, want)
	}
}

func TestUnifiedDiffSeparateHunks(t *testing.T) {
	// Two changes far apart (more than 2*context kept lines between them) split
	// into two @@ blocks rather than one.
	a := "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\n"
	b := "X\n2\n3\n4\n5\n6\n7\n8\n9\n10\nY\n"
	got := unifiedDiff("f", "f", a, b)
	hunks := 0
	for i := 0; i+2 < len(got); i++ {
		if got[i] == '@' && got[i+1] == '@' && got[i+2] == ' ' {
			hunks++
		}
	}
	if hunks != 2 {
		t.Errorf("expected 2 hunks, got %d in:\n%s", hunks, got)
	}
}
