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

// TestReparseBacksOffLookaheadChain pins reparseStart's anchor rule: a
// File-child boundary can hinge on a lookahead across pub/extern/fn and the
// effect keywords (an error run stops at fn only when a declaring name
// follows), so an edit to the token after such a run must reparse from before
// the run. Before the fix the incremental parse kept the preceding error node's
// stale right edge and a divergence from the full parse resulted.
func TestReparseBacksOffLookaheadChain(t *testing.T) {
	cases := []struct {
		name    string
		initial string
		edit    source.Edit
	}{
		// "fn i" is a declaration; the edit turns the name into "type", so fn
		// must fold back into the preceding error run.
		{"fn loses its name", "+ 2fn i\n", source.Edit{Start: 6, End: 7, NewText: []byte("type ")}},
		// The same, across an effect list (fn io async i).
		{"fn loses its name across effects", "+ 2fn io async i\n", source.Edit{Start: 15, End: 16, NewText: []byte("type ")}},
		// "extern fn" is a declaration; the edit removes the fn, so extern must
		// fold back into the preceding error run.
		{"extern loses its fn", "+ 2extern fn f(): int { return 1 }\n", source.Edit{Start: 10, End: 12, NewText: []byte("zz")}},
		// "pub extern fn" likewise, with the chain crossing two tokens.
		{"pub extern loses its fn", "+ 2pub extern fn f(): int { return 1 }\n", source.Edit{Start: 14, End: 16, NewText: []byte("zz")}},
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
		// The arrow body of a lambda in a head context is the same fresh,
		// brace-delimited context as the block body: a typed record literal there
		// must parse as a record literal, not leak the head's noRecordLit
		// restriction. (A bare "-> {" is a separate, pre-existing arrow-block-body
		// recovery and is unaffected either way.)
		{"typed record literal in arrow lambda body in if head", "fn f(): int { if fn() -> P{a: 1} { return 1 }\nreturn 0 }\n"},
		{"typed record literal in arrow lambda body in switch head", "fn f(): int { switch fn() -> P{a: 1} {}\nreturn 0 }\n"},
		// A record literal further into the arrow body (after a binary operator),
		// where the leak also surfaces because noRecordLit spans the whole head.
		{"non-leading record literal in arrow lambda body in if head", "fn f(): int { if fn() -> n + P{a: 1} { return 1 }\nreturn 0 }\n"},
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
		// An unterminated record literal must recover at every File-level
		// declaration starter, not only const/type/use — the stop list once
		// omitted enum/interface/fn/extern and swallowed the whole following
		// declaration token-by-token.
		{"unterminated record literal before enum", "const c = {a: 1\nenum E { A }\n"},
		{"unterminated record literal before interface", "const c = {a: 1\ninterface I { f(): int }\n"},
		{"unterminated record literal before fn decl", "const c = {a: 1\nfn g(): int { return 1 }\n"},
		{"unterminated record literal before extern fn", "const c = {a: 1\nextern fn g(): int\n"},
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

// TestUnterminatedRecordLitStopsAtDeclaration pins the unterminated-record-
// literal recovery boundary to the File-level declaration-starter set
// (beginsDeclaration, via atUnterminatedConstructStop). The stop list once
// omitted enum/interface/fn/extern, so an unterminated record literal followed
// by such a declaration absorbed the whole declaration token-by-token — one
// unexpected_token per token — instead of recovering at the boundary. After the
// fix the recovery stops at the declaration keyword: that declaration parses as
// its own File child and a single boundary diagnostic is emitted. The
// conditional starters must stay conditional — a bare `fn` literal or an
// expression-level `extern` is not a declaration and must not stop the loop.
func TestUnterminatedRecordLitStopsAtDeclaration(t *testing.T) {
	cases := []struct {
		name     string
		src      string
		declKind cst.Kind // the kind the recovered-to declaration must parse as
	}{
		{"before enum", "const c = {a: 1\nenum E { A }\n", cst.EnumDecl},
		{"before interface", "const c = {a: 1\ninterface I { f(): int }\n", cst.InterfaceDecl},
		{"before fn decl", "const c = {a: 1\nfn g(): int { return 1 }\n", cst.FuncDecl},
		{"before extern fn", "const c = {a: 1\nextern fn g(): int\n", cst.FuncDecl},
		{"before const", "const c = {a: 1\nconst y = 1\n", cst.ConstDecl},
		{"before type", "const c = {a: 1\ntype T = int\n", cst.TypeDecl},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, diags := Parse([]byte(tc.src))
			if len(diags) != 1 {
				t.Fatalf("diagnostics = %d, want 1 (the missing-brace boundary): %v", len(diags), diags)
			}
			// The trailing declaration must have recovered into its own File child,
			// not been swallowed by the unterminated record literal.
			var found bool
			for _, child := range root.Children() {
				if n, ok := child.(*cst.Node); ok && n.Kind() == tc.declKind {
					found = true
				}
			}
			if !found {
				t.Fatalf("declaration %v was swallowed, not recovered as a File child\n%s",
					tc.declKind, cst.Sprint(source.NewFile("", []byte(tc.src)), root))
			}
		})
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

// TestWellFormedProgramsParseClean is a correctness oracle independent of
// incrementality. The fuzz oracle (assertMatchesFullParse) only checks
// incremental == full parse, so a misparse both paths agree on is invisible to
// it — the arrow-body noRecordLit leak was exactly such a bug, structurally
// wrong yet identical across both paths. This table pins a curated set of
// well-formed, deliberately tricky programs to zero diagnostics, so a structural
// misparse that both parse paths share is still caught. Each case parses from
// scratch (the full path) and incrementally after a no-op edit (the reparse
// path); both must yield zero diagnostics and agree with the full parse.
func TestWellFormedProgramsParseClean(t *testing.T) {
	cases := []struct {
		name string
		src  string
	}{
		// Head-context lambdas in both body forms: the class the arrow-body
		// noRecordLit leak lived in. A typed record literal in the body must parse
		// as a record literal, not be read as the if/switch control block.
		{"block-body lambda record in if head", "fn f(): int { if fn() { return P{a: 1} } { return 1 }\nreturn 0 }\n"},
		{"arrow-body lambda record in if head", "fn f(): int { if fn() -> P{a: 1} { return 1 }\nreturn 0 }\n"},
		{"arrow-body lambda record in switch head", "fn f(): int { switch fn() -> P{a: 1} {}\nreturn 0 }\n"},
		{"arrow-body lambda record after operator in if head", "fn f(): int { if n + fn() -> P{a: 1} { return 1 }\nreturn 0 }\n"},
		// Literal classes whose multi-token scans (datetime, duration, string) the
		// fuzz alphabet only sampled coarsely.
		{"datetime literal", "const t = D2009-03-31T23:59:59.000Z\n"},
		{"duration run", "const d = 3w4d5h6m7s8ms\n"},
		{"string with escapes", "const s = \"a\\tb\\r\\n\\0\\\"q\\\"\"\n"},
		{"datetime/duration arithmetic", "const x = D1970-01-01T00:00:00.000Z + 7d\n"},
		// await over a postfix chain, with a record literal as the awaited operand's
		// argument (the bracketed context must re-enable record literals).
		{"await postfix chain", "fn f(): int { let x = await g(P{a: 1})\nreturn 0 }\n"},
		// A record literal in a bracketed context inside a head expression: the
		// bracketed helper must re-enable the record reading there.
		{"record literal in call arg in if head", "fn f(): int { if g(P{a: 1}) { return 1 }\nreturn 0 }\n"},
		// match arms: a type pattern with a binding, the wildcard, a null arm, an
		// inline-statement and a block body, and an index-read scrutinee — the
		// class the type-pattern parse and the match body's separator rule cover.
		{"match with binding and wildcard arms", "fn f(v: T): int {\n  match v {\n    Coin c -> return c.n\n    _      -> return 0\n  }\n}\n"},
		{"match with null arm and block body", "fn f(v: T): int {\n  match v {\n    Coin c -> return 1\n    null   -> {\n      return 0\n    }\n  }\n}\n"},
		{"match over an index read", "fn f(xs: list<int>): int {\n  match xs[0] {\n    int v   -> return v\n    error e -> return 0\n  }\n}\n"},
		// A record-literal scrutinee written explicitly must parse as the
		// scrutinee, with the noRecordLit restriction not leaking past the parens.
		{"match scrutinee in parens with record", "fn f(): int { match (P{a: 1}) {\n    P p -> return 1\n  }\n  return 0\n}\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, diags := Parse([]byte(tc.src)); len(diags) != 0 {
				t.Fatalf("full parse of well-formed %q produced diagnostics: %v", tc.src, diags)
			}
			// The reparse path must agree (zero diagnostics, identical tree).
			doc := NewDocument([]byte(tc.src))
			content := naiveSplice([]byte(tc.src), 0, 0, []byte("\n"))
			doc.Edit(source.Edit{Start: 0, End: 0, NewText: []byte("\n")})
			assertMatchesFullParse(t, doc, content)
			if diags := doc.Diagnostics(); len(diags) != 0 {
				t.Fatalf("reparse of well-formed %q produced diagnostics: %v", tc.src, diags)
			}
		})
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
		// Declaration and statement keywords with their brackets, so the walk
		// reaches the malformed-recovery paths whose diagnostics once anchored on
		// a declaration's right boundary: enum/interface/impl/control blocks
		// missing "{", a function missing its body, an unterminated record /
		// collection / paren / map literal, and the if/else and ternary branches.
		"{", "}", "(", ")", "[", "]", "?", "->", ".", ",",
		"fn ", "fn", "type ", "enum ", "interface ", "impl ", "use ", "from ",
		"assert ", "extern ", "if ", "else ", "switch ", "match ", "let ", "return ",
		"where ", "builtin", "self", "null", "io ", "Point {", "{ a: 1 }",
		// match-arm fragments: a type pattern with a binding, the wildcard, and
		// the arrow, so the random walk reaches the type-pattern parse and the
		// match body's unterminated-construct recovery.
		"Coin c", "_", "int v", "error e",
		// Literal and keyword classes the E-series and earlier work added that the
		// walk above never reaches: a datetime literal and a duration run (so the
		// multi-token leftward-fusion scanDatetime/scanNumber paths are stressed), a
		// string with an escape, await, and an `fn() -> ` arrow-body fragment placed
		// next to the head contexts (if/switch) so the lambda noRecordLit
		// interaction is exercised by the random walk too.
		"D2009-03-31T23:59:59.000Z", "3w4d", "5s", "\"s\\n\"", "await ", "fn() -> ",
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
