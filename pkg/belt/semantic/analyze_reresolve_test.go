package semantic

// Regression gates for the write-back re-resolution of a bare enum member
// returned through a function's result type. The value never carries the member
// the lowering resolved (the lowering is type-blind and emits a placeholder); the
// post-check write-back fills it from the checker's resolution. These gates pin
// that the fill is re-derived per analysis — never a stale fact carried across an
// incremental edit — so reordering the enum's members or changing which member a
// body returns re-resolves correctly on the incremental path, the same as a full
// re-analysis would, and editing a returned member to a non-member withholds the
// fold rather than keeping the prior value.

import (
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/source"
)

const reresolveSrc = "pub enum R: byte {\n  A = 1\n  B = 2\n}\n" +
	"pub fn pick(): R {\n  return B\n}\n" +
	"const X = pick()\n"

// editReplacing applies a single text replacement to the editable, failing if the
// target text is absent.
func editReplacing(t *testing.T, e *editable, src, find, repl string) {
	t.Helper()
	i := strings.Index(src, find)
	if i < 0 {
		t.Fatalf("setup: %q not in source", find)
	}
	e.edit(source.Edit{Start: i, End: i + len(find), NewText: []byte(repl)})
}

// TestReResolveEnumReorderReturn pins that reordering an enum's members re-resolves
// a returned bare member by name on the incremental path: the member's index moves
// but the value query's placeholder is re-filled from the checker's fresh
// resolution, so the const still folds to the member it names — not to whatever now
// sits at the old index, which a stale write-back fact would produce.
func TestReResolveEnumReorderReturn(t *testing.T) {
	e := newEditable([]byte(reresolveSrc))
	if _, eval := constInfo(e.prog, soleFileID, "X"); eval != "R.B" {
		t.Fatalf("before reorder: X = %s, want R.B", eval)
	}

	// Swap the member declarations: B moves from index 1 to index 0 (its value
	// stays 2). A stale index would now read A; correct re-resolution keeps B.
	editReplacing(t, e, reresolveSrc, "  A = 1\n  B = 2\n", "  B = 2\n  A = 1\n")

	if diags := codesOf(e.prog, soleFileID); diags != "" {
		t.Fatalf("after reorder: unexpected diagnostics [%s]", diags)
	}
	if _, eval := constInfo(e.prog, soleFileID, "X"); eval != "R.B" {
		t.Errorf("after reorder: X = %s, want R.B (the returned member re-resolved by name, not by stale index)", eval)
	}
}

// TestReResolveReturnedMemberChange pins that changing which member a body returns
// re-resolves on the incremental path: the const folds to the newly named member,
// so the write-back fact tracks the edited source rather than the prior analysis.
func TestReResolveReturnedMemberChange(t *testing.T) {
	e := newEditable([]byte(reresolveSrc))
	if _, eval := constInfo(e.prog, soleFileID, "X"); eval != "R.B" {
		t.Fatalf("before edit: X = %s, want R.B", eval)
	}

	// The body now returns A. The placeholder must re-resolve to A.
	editReplacing(t, e, reresolveSrc, "return B", "return A")

	if diags := codesOf(e.prog, soleFileID); diags != "" {
		t.Fatalf("after edit: unexpected diagnostics [%s]", diags)
	}
	if _, eval := constInfo(e.prog, soleFileID, "X"); eval != "R.A" {
		t.Errorf("after edit: X = %s, want R.A (the returned member re-resolved)", eval)
	}
}

// TestReResolveReturnInvalidatesOnUnknown pins the negative incremental path:
// editing a valid returned member to a non-member reports unknown_enum_member and
// withholds the fold, so a stale resolved value never survives the edit.
func TestReResolveReturnInvalidatesOnUnknown(t *testing.T) {
	e := newEditable([]byte(reresolveSrc))
	if _, eval := constInfo(e.prog, soleFileID, "X"); eval != "R.B" {
		t.Fatalf("before edit: X = %s, want R.B", eval)
	}

	editReplacing(t, e, reresolveSrc, "return B", "return Z")

	if !hasCode(e.prog.Diagnostics(soleFileID), CodeUnknownEnumMember) {
		t.Errorf("after edit to a non-member: want unknown_enum_member, got [%s]", codesOf(e.prog, soleFileID))
	}
	if _, eval := constInfo(e.prog, soleFileID, "X"); eval != "<nil>" {
		t.Errorf("after edit to a non-member: X = %s, want the fold withheld (no stale value)", eval)
	}
}
