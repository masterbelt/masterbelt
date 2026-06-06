package cst

import (
	"strings"
	"testing"
)

// TestKindNamesComplete binds the Kind const block to its hand-maintained
// kindNames table: every Kind from 0 to the last real kind must have a non-empty
// name, and String() must never fall back to the "Kind(N)" form for a real kind.
// kindNames is index-keyed and kept in lockstep with the const block by hand;
// without this test a Kind added to the enum but forgotten in kindNames renders
// silently as "Kind(47)" in the lossless CST snapshot for any construct not yet
// present in an example file. The numKinds sentinel makes the bound automatic:
// adding a Kind extends the loop, so the new value must be named or the test
// fails.
func TestKindNamesComplete(t *testing.T) {
	for k := Kind(0); k < numKinds; k++ {
		name := k.String()
		if name == "" {
			t.Errorf("Kind(%d) has an empty name", int(k))
		}
		if strings.HasPrefix(name, "Kind(") {
			t.Errorf("Kind(%d) has no kindNames entry; String() fell back to %q", int(k), name)
		}
	}
}

// TestKindNamesUnique guards against a copy-paste slip in the table: two kinds
// sharing a name would make the snapshot ambiguous (two constructs dumping
// identically).
func TestKindNamesUnique(t *testing.T) {
	seen := map[string]Kind{}
	for k := Kind(0); k < numKinds; k++ {
		name := k.String()
		if prev, dup := seen[name]; dup {
			t.Errorf("Kind name %q is shared by Kind(%d) and Kind(%d)", name, int(prev), int(k))
		}
		seen[name] = k
	}
}

// TestKindStringFallback checks the out-of-range path still degrades gracefully
// (numKinds itself, and a value past it, are not real kinds and take the
// fallback form rather than panicking).
func TestKindStringFallback(t *testing.T) {
	for _, k := range []Kind{numKinds, numKinds + 7, -1} {
		if got := k.String(); !strings.HasPrefix(got, "Kind(") {
			t.Errorf("Kind(%d).String() = %q, want the Kind(N) fallback", int(k), got)
		}
	}
}
