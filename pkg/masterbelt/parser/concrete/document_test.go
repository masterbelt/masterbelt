package concrete

import (
	"math/rand"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/source"
	"github.com/masterbelt/masterbelt/pkg/source/cst"
)

// naiveSplice is the obvious reference for applying an edit to bytes.
func naiveSplice(src []byte, start, end int, repl []byte) []byte {
	out := make([]byte, 0, len(src)-(end-start)+len(repl))
	out = append(out, src[:start]...)
	out = append(out, repl...)
	out = append(out, src[end:]...)
	return out
}

// assertMatchesFullParse is the oracle: the incrementally maintained tree and
// diagnostics must be identical to parsing the current content from scratch.
func assertMatchesFullParse(t *testing.T, d *Document, content []byte) {
	t.Helper()

	oracleRoot, oracleDiags := Parse(content)

	if !cst.Equal(d.Root(), oracleRoot) {
		t.Fatalf("tree mismatch (content %q)\n--- got ---\n%s--- want ---\n%s",
			content, cst.Sprint(d.Buffer(), d.Root()), cst.Sprint(source.NewFile("", content), oracleRoot))
	}

	// Losslessness: the leaves must still reproduce the source.
	if got := leafText(d.Buffer(), d.Root()); got != string(content) {
		t.Fatalf("tree not lossless after edit\n content: %q\n leaves:  %q", content, got)
	}

	got, want := d.Diagnostics(), oracleDiags
	if len(got) != len(want) {
		t.Fatalf("diagnostic count = %d, want %d (content %q)\n got:  %v\n want: %v",
			len(got), len(want), content, got, want)
	}
	for i := range want {
		if got[i].Code != want[i].Code || got[i].Severity != want[i].Severity ||
			got[i].Offset != want[i].Offset || got[i].Width != want[i].Width ||
			got[i].Message != want[i].Message {
			t.Fatalf("diagnostic[%d] = %+v, want %+v (content %q)", i, got[i], want[i], content)
		}
	}
}

func TestDocumentScriptedEdits(t *testing.T) {
	cases := []struct {
		name    string
		initial string
		edit    source.Edit
	}{
		{"insert inside identifier", "const MaxLevel = 1", source.Edit{Start: 9, End: 9, NewText: []byte("X")}},
		{"append digit merging int", "const x = 1", source.Edit{Start: 11, End: 11, NewText: []byte("2")}},
		{"delete the initializer value", "const x = 100", source.Edit{Start: 10, End: 13, NewText: nil}},
		{"add a type annotation", "const x = 1", source.Edit{Start: 7, End: 7, NewText: []byte(": int64")}},
		{"make a decl public", "const x = 1\nconst y = 2\n", source.Edit{Start: 12, End: 12, NewText: []byte("pub ")}},
		{"insert a whole decl between", "const x = 1\nconst y = 2\n", source.Edit{Start: 12, End: 12, NewText: []byte("const z = 3\n")}},
		{"join two decls by deleting newline", "const x = 1\nconst y = 2\n", source.Edit{Start: 11, End: 12, NewText: nil}},
		{"break const keyword", "const x = 1\n", source.Edit{Start: 2, End: 2, NewText: []byte(" ")}},
		{"edit first of three decls", "const a = 1\nconst b = 2\nconst c = 3\n", source.Edit{Start: 6, End: 7, NewText: []byte("A")}},
		{"edit last of three decls", "const a = 1\nconst b = 2\nconst c = 3\n", source.Edit{Start: 30, End: 31, NewText: []byte("C")}},
		{"insert doc comment", "const x = 1\n", source.Edit{Start: 0, End: 0, NewText: []byte("/// doc\n")}},
		{"append at end", "const x = 1\n", source.Edit{Start: 12, End: 12, NewText: []byte("pub const y = 2\n")}},
		{"edit at very start", "const x = 1", source.Edit{Start: 0, End: 0, NewText: []byte("pub ")}},
		{"delete everything", "const x = 1\nconst y = 2\n", source.Edit{Start: 0, End: 24, NewText: nil}},
		{"introduce a syntax error", "const x = 1\n", source.Edit{Start: 8, End: 9, NewText: nil}}, // remove '='
		{"open block comment swallowing decls", "const x = 1\nconst y = 2\n", source.Edit{Start: 0, End: 0, NewText: []byte("/*")}},
		{"stray operator line", "const x = 1\n", source.Edit{Start: 0, End: 0, NewText: []byte("= =\n")}},
		{"edit an if condition", "pub fn f(n: int): int {\n  if n > 0 {\n    return 1\n  }\n  return 0\n}\n", source.Edit{Start: 33, End: 34, NewText: []byte("<")}}, // ">" -> "<"
		{"add an else branch to an if", "pub fn f(n: int): int {\n  if n > 0 {\n    return 1\n  }\n  return 0\n}\n", source.Edit{Start: 49, End: 49, NewText: []byte(" else {\n    return 2\n  }")}},
		{"turn an if into an else-if chain", "pub fn f(n: int): int {\n  if n > 0 {\n    return 1\n  } else {\n    return 0\n  }\n}\n", source.Edit{Start: 54, End: 54, NewText: []byte("if n < 0 ")}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := NewDocument([]byte(tc.initial))
			content := naiveSplice([]byte(tc.initial), tc.edit.Start, tc.edit.End, tc.edit.NewText)
			doc.Edit(tc.edit)
			assertMatchesFullParse(t, doc, content)
		})
	}
}

// TestErrorRunMirrorsDispatch pins parseError's stop predicate to
// nextChildren's dispatch. A token the dispatcher routes back to the error
// parser — extern (or pub extern) without fn — must not stop the error run:
// before the fix parseError returned a zero-width Error node for it, which the
// File-level loops re-encountered forever, allocating an unbounded tree. The
// well-formed declaration after the stray run also pins that the run still
// stops where a real declaration starts.
func TestErrorRunMirrorsDispatch(t *testing.T) {
	cases := []struct {
		name string
		src  string
	}{
		{"extern without fn", "extern !=!t0]\nconst y = 1\n"},
		{"pub extern without fn", "pub extern 1 + 2\nconst y = 1\n"},
		{"extern at EOF", "9 extern"},
		{"extern fn still a declaration", "* extern fn f(): int { return 1 }\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := NewDocument([]byte(tc.src))
			assertMatchesFullParse(t, doc, []byte(tc.src))
			// An edit at the start forces the incremental path through the
			// stray run as well.
			content := naiveSplice([]byte(tc.src), 0, 0, []byte("9"))
			doc.Edit(source.Edit{Start: 0, End: 0, NewText: []byte("9")})
			assertMatchesFullParse(t, doc, content)
		})
	}
}

// TestBlockClearsHeadRestriction pins the fix for a parser hang: a function
// literal in an if condition or switch scrutinee parses its body while the
// head's record-literal restriction (noRecordLit) is in force, and parseBlock
// must lift that restriction for the body's statements. Before the fix a "{"
// statement inside such a body parsed as a zero-width error expression that the
// statement loop re-encountered forever, allocating an unbounded tree. The
// inputs here are well-formed, so the assertion that there are no diagnostics
// also catches a regression that survives only via the loop's progress guard.
func TestBlockClearsHeadRestriction(t *testing.T) {
	cases := []struct {
		name string
		src  string
	}{
		{"record literal stmt in lambda body in if head", "fn f(): int { if fn() { {} } { return 1 }\nreturn 0 }\n"},
		{"field record literal in lambda body in if head", "fn f(): int { if fn() { {a: 1} } { return 1 }\nreturn 0 }\n"},
		{"record literal stmt in lambda body in switch head", "fn f(): int { switch fn() { {} } {}\nreturn 0 }\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := NewDocument([]byte(tc.src))
			assertMatchesFullParse(t, doc, []byte(tc.src))
			if diags := doc.Diagnostics(); len(diags) != 0 {
				t.Fatalf("unexpected diagnostics for %q: %v", tc.src, diags)
			}
		})
	}
}

// TestDocumentMalformedRecoveryBoundary covers a class of incremental divergence
// where a malformed declaration's recovery anchored a diagnostic exactly on its
// own right boundary — the start of the next File child. When an edit made the
// reparse realign at that boundary (reusing the following declaration verbatim),
// the diagnostic splice counted that boundary diagnostic twice, so the
// incrementally maintained diagnostics no longer matched a full parse.
//
// Each case is a malformed declaration whose recovery once anchored on its right
// edge — an unterminated record/collection/paren literal, a record-field or
// map-entry missing its ":", an else/ternary missing its branch, an impl block or
// control statement missing its "{", a function missing its body — followed by a
// reusable declaration. The edit is a no-op-sized splice at the document start, so
// the whole prefix is reparsed and realigns at the malformed declaration's right
// boundary, exercising the splice exactly where the boundary diagnostic sat. The
// oracle is a full parse of the same bytes.
func TestDocumentMalformedRecoveryBoundary(t *testing.T) {
	cases := []struct {
		name    string
		initial string
	}{
		{"unterminated record literal", "pub = {Z\nconst y = 1\n"},
		{"record field missing colon", "const c = {a b}\nconst y = 1\n"},
		{"impl block missing brace", "type T impl >\nconst y = 1\n"},
		{"unterminated collection", "const c = [1\nconst y = 1\n"},
		{"unterminated paren", "const c = (1\nconst y = 1\n"},
		{"map entry missing colon", "const c = {1:2, 3\nconst y = 1\n"},
		{"ternary missing colon", "const c = a ? b\nconst y = 1\n"},
		{"func decl missing body", "fn f()\nconst y = 1\n"},
		{"func decl missing param list", "fn f\nconst y = 1\n"},
		{"enum missing brace", "enum E >\nconst y = 1\n"},
		{"interface missing brace", "interface I >\nconst y = 1\n"},
		{"use list missing brace", "use {a from \"m\"\nconst y = 1\n"},
	}

	// The realigning edits: a true no-op at the start, a single-byte insert at the
	// start (shifts every offset, so a stale absolute-offset diagnostic shows), and
	// a delete-then-retype of the leading byte.
	edits := []struct {
		name string
		edit source.Edit
	}{
		{"noop at start", source.Edit{Start: 0, End: 0, NewText: nil}},
		{"insert at start", source.Edit{Start: 1, End: 1, NewText: []byte("9")}},
		{"delete leading byte", source.Edit{Start: 0, End: 1, NewText: nil}},
	}

	for _, tc := range cases {
		for _, e := range edits {
			t.Run(tc.name+"/"+e.name, func(t *testing.T) {
				doc := NewDocument([]byte(tc.initial))
				content := naiveSplice([]byte(tc.initial), e.edit.Start, e.edit.End, e.edit.NewText)
				doc.Edit(e.edit)
				assertMatchesFullParse(t, doc, content)
			})
		}
	}
}

func TestDocumentSequentialEdits(t *testing.T) {
	// Type a declaration one character at a time, then delete it from the front.
	doc := NewDocument(nil)
	var content []byte

	typed := "/// doc\npub const Answer: int64 = 42\n"
	for i := 0; i < len(typed); i++ {
		e := source.Edit{Start: len(content), End: len(content), NewText: []byte{typed[i]}}
		content = naiveSplice(content, e.Start, e.End, e.NewText)
		doc.Edit(e)
		assertMatchesFullParse(t, doc, content)
	}

	for len(content) > 0 {
		e := source.Edit{Start: 0, End: 1, NewText: nil}
		content = naiveSplice(content, e.Start, e.End, e.NewText)
		doc.Edit(e)
		assertMatchesFullParse(t, doc, content)
	}
}

func TestDocumentReusesUneditedDecls(t *testing.T) {
	// Editing the last declaration must leave the green node of the first
	// untouched (same pointer), proving the subtree was reused, not rebuilt.
	doc := NewDocument([]byte("const a = 1\nconst b = 2\n"))
	first := doc.Root().Children()[0]

	doc.Edit(source.Edit{Start: 22, End: 23, NewText: []byte("9")}) // b's value 2 -> 9
	assertMatchesFullParse(t, doc, []byte("const a = 1\nconst b = 9\n"))

	if doc.Root().Children()[0] != first {
		t.Fatal("first declaration was rebuilt; expected it to be reused")
	}
}

func TestDocumentFuzz(t *testing.T) {
	r := rand.New(rand.NewSource(0xB317))
	alphabet := []string{
		"a", "Z", "x", "0", "9", " ", "\n", "/", "*", ":", "=", "あ",
		"const ", "pub ", "// c\n", "/* b */", "int64",
		// Operators, booleans, and a few expression fragments so the reparse
		// oracle covers binary/unary expressions and the maximal-munch edits.
		"+", "-", "%", "!", "<", ">", "&&", "||", "==", "!=", "<=", ">=",
		"true", "false", "1 + 2", "a && b", "-x", "!true",
	}

	start := "const x = 0\n"
	doc := NewDocument([]byte(start))
	content := []byte(start)

	for range 2000 {
		s := r.Intn(len(content) + 1)
		e := s + r.Intn(len(content)-s+1)

		var repl []byte
		for n := r.Intn(6); n > 0; n-- {
			repl = append(repl, alphabet[r.Intn(len(alphabet))]...)
		}

		edit := source.Edit{Start: s, End: e, NewText: repl}
		content = naiveSplice(content, s, e, repl)
		doc.Edit(edit)
		assertMatchesFullParse(t, doc, content)
	}
}
