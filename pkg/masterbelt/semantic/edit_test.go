package semantic

import (
	"math/rand"
	"sort"
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/source"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// editable drives a one-file Program the way an editor does: edits go to the
// syntax document, then the file is re-pushed and the program refreshed. It
// is the test stand-in for the LSP's standalone-file flow.
type editable struct {
	prog *Program
	doc  *abstract.Document
}

func newEditable(src []byte) *editable {
	e := &editable{prog: NewProgram(), doc: abstract.NewDocument(src)}
	e.prog.SetFile(soleFileID, e.doc, nil)
	e.prog.Refresh()
	return e
}

func (e *editable) edit(ed source.Edit) {
	e.doc.Edit(ed)
	e.prog.SetFile(soleFileID, e.doc, nil)
	e.prog.Refresh()
}

func naiveSplice(src []byte, start, end int, repl []byte) []byte {
	out := make([]byte, 0, len(src)-(end-start)+len(repl))
	out = append(out, src[:start]...)
	out = append(out, repl...)
	out = append(out, src[end:]...)
	return out
}

// assertMatchesReference is the oracle: the incrementally analyzed module and
// diagnostics must equal a full Analyze of the current source.
func assertMatchesReference(t *testing.T, e *editable, content []byte) {
	t.Helper()
	refModule, refDiags := Analyze(abstract.NewDocument(content))

	if got, want := ir.Dump(e.prog.Module(soleFileID)), ir.Dump(refModule); got != want {
		t.Fatalf("IR mismatch (content %q)\n--- got ---\n%s--- want ---\n%s", content, got, want)
	}

	got, want := e.prog.Diagnostics(soleFileID), refDiags
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

func TestScriptedEdits(t *testing.T) {
	cases := []struct {
		name    string
		initial string
		edit    source.Edit
	}{
		{"value change keeps type", "const A: int64 = 1\nconst B = A\n", source.Edit{Start: 17, End: 18, NewText: []byte("2")}},
		{"append a declaration", "const A = 1\n", source.Edit{Start: 11, End: 11, NewText: []byte("const B = A\n")}},
		{"introduce undefined name", "const A = B\n", source.Edit{Start: 10, End: 11, NewText: []byte("C")}},
		{"resolve an undefined name", "const A = B\nconst C = 1\n", source.Edit{Start: 10, End: 11, NewText: []byte("C")}},
		{"introduce a cycle", "const A = 1\nconst B = A\n", source.Edit{Start: 9, End: 10, NewText: []byte("B")}},
		{"change annotation", "const A: int64 = 1\n", source.Edit{Start: 9, End: 14, NewText: []byte("int32")}},
		{"unknown type", "const A: int64 = 1\n", source.Edit{Start: 9, End: 14, NewText: []byte("nope")}},
		{"add pub", "const A = 1\n", source.Edit{Start: 0, End: 0, NewText: []byte("pub ")}},
		{"duplicate name", "const A = 1\nconst B = 2\n", source.Edit{Start: 18, End: 19, NewText: []byte("A")}},
		{"delete a declaration", "const A = 1\nconst B = A\n", source.Edit{Start: 0, End: 12, NewText: nil}},
		{"break an assertion", "const A = 1\nassert A > 0\n", source.Edit{Start: 21, End: 22, NewText: []byte("<")}},
		{"fix an assertion via its const", "const A = 1\nassert A < 0\n", source.Edit{Start: 10, End: 11, NewText: []byte("-1")}},
		{"insert an assertion", "const A = 1\n", source.Edit{Start: 12, End: 12, NewText: []byte("assert A == 1\n")}},
		{"delete an assertion", "const A = 1\nassert A < 0\n", source.Edit{Start: 12, End: 25, NewText: nil}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEditable([]byte(tc.initial))
			content := naiveSplice([]byte(tc.initial), tc.edit.Start, tc.edit.End, tc.edit.NewText)
			e.edit(tc.edit)
			assertMatchesReference(t, e, content)
		})
	}
}

// TestEarlyCutoff checks the engine's defining property: editing a constant's
// value without changing its type does not ripple through to a constant two
// references away — the change is cut off once a type comes out unchanged.
func TestEarlyCutoff(t *testing.T) {
	e := newEditable([]byte("const A: int64 = 1\nconst B = A\nconst C = B\n"))
	cDecl := e.doc.File().Decls[2] // unchanged by the edit below

	// Change A's value 1 -> 2; A stays int64, so B's type is unchanged.
	e.edit(source.Edit{Start: 17, End: 18, NewText: []byte("2")})
	assertMatchesReference(t, e, []byte("const A: int64 = 2\nconst B = A\nconst C = B\n"))

	if e.prog.db.computed[typeOfKey(cDecl)] {
		t.Error("typeOf(C) was recomputed; early cutoff should have stopped the change at B")
	}
}

func TestEditFuzz(t *testing.T) {
	r := rand.New(rand.NewSource(0x5E3A))
	alphabet := []string{
		"const ", "pub ", "A", "B", "C", "Name", " = ", " : ", "int64", "int32",
		"nope", "0", "1", "42", " ", "\n", "=", ":",
		// Operators, booleans, and the bool type so the oracle checks that
		// incremental typing and evaluation of expressions match a full
		// analysis (division by zero and type errors included).
		"+", "-", "*", "/", "%", "&&", "||", "!", "<", "==", "bool",
		"true", "false", " + ", " && ", "A + B", "!true",
		// Assertions and groupings, so the oracle covers the assert loop's
		// diagnostics (failed/not bool/not constant) incrementally.
		"assert ", "assert A > 0\n", "(", ")",
	}

	start := "const A = 1\nconst B = A\nassert A > 0\n"
	e := newEditable([]byte(start))
	content := []byte(start)

	for range 2000 {
		s := r.Intn(len(content) + 1)
		en := s + r.Intn(len(content)-s+1)
		var repl []byte
		for n := r.Intn(5); n > 0; n-- {
			repl = append(repl, alphabet[r.Intn(len(alphabet))]...)
		}

		edit := source.Edit{Start: s, End: en, NewText: repl}
		content = naiveSplice(content, s, en, repl)
		e.edit(edit)
		assertMatchesReference(t, e, content)
	}
}

// TestFuncLitTypes checks the editor-facing query over every path the
// assembler distinguishes: a call-typed literal (un-annotated const), an
// annotation-typed literal, one typed by a method's declared result, and one
// nested in a top-level function body — so the editor surfaces a lambda's
// signature wherever it sits, not only in a const, annotation, or method body.
func TestFuncLitTypes(t *testing.T) {
	src := "const Doubled = [1, 2].map(fn(x) { return x * 2 })\n" +
		"const Twice: fn(x: int): int = fn(x) { return x * 2 }\n" +
		"pub type T = int8 impl {\n  pub f(): fn(x: bool): bool {\n    return fn(b) { return b }\n  }\n}\n" +
		"pub fn g(): int {\n  return [1, 2].map(fn(y) { return y + 1 }).count()\n}\n"
	e := newEditable([]byte(src))

	var got []string
	for _, ft := range e.prog.FuncLitTypes(soleFileID) {
		got = append(got, ft.String())
	}
	sort.Strings(got)
	// The fn-body lambda fn(y) { return y + 1 } settles to fn(int): int.
	want := "fn(bool): bool|fn(int): int|fn(int): int|fn(int): int"
	if strings.Join(got, "|") != want {
		t.Fatalf("FuncLitTypes = %v, want %s", got, want)
	}
}

// TestEarlyCutoffLambdaBody checks that editing a lambda body re-checks only
// what depends on it: the edit changes F's value but not its type
// (list<int>), so the change must not propagate past F's dependents.
func TestEarlyCutoffLambdaBody(t *testing.T) {
	src := "const F = [1, 2].map(fn(x) { return x * 2 })\nconst G = F\nconst H = G\n"
	e := newEditable([]byte(src))
	hDecl := e.doc.File().Decls[2]

	// x * 2 -> x * 9: the body's fold changes, the solved types do not.
	i := strings.Index(src, "x * 2") + len("x * ")
	e.edit(source.Edit{Start: i, End: i + 1, NewText: []byte("9")})
	assertMatchesReference(t, e, []byte(strings.Replace(src, "x * 2", "x * 9", 1)))

	if e.prog.db.computed[typeOfKey(hDecl)] {
		t.Error("typeOf(H) was recomputed; the lambda edit left F's type unchanged")
	}
}

// TestEarlyCutoffAssert checks an assertion is a pure consumer: editing it
// re-checks the assertion (its diagnostic flips) but recomputes neither the
// type nor the value of the constants it reads.
func TestEarlyCutoffAssert(t *testing.T) {
	src := "const A: int64 = 1\nconst B = A\nassert B > 0\n"
	e := newEditable([]byte(src))
	bDecl := e.doc.File().Decls[1]

	// B > 0 -> B > 9: the assertion now fails; B itself is untouched.
	i := strings.Index(src, "> 0") + 2
	e.edit(source.Edit{Start: i, End: i + 1, NewText: []byte("9")})
	assertMatchesReference(t, e, []byte(strings.Replace(src, "> 0", "> 9", 1)))

	if e.prog.db.computed[typeOfKey(bDecl)] || e.prog.db.computed[valueKey(bDecl)] {
		t.Error("editing the assertion recomputed B; the assert is a pure consumer")
	}
}

// TestEarlyCutoffValue is the value-side mirror of TestEarlyCutoff: changing a
// constant's annotation (not its value) must not re-evaluate a constant two
// references away.
func TestEarlyCutoffValue(t *testing.T) {
	e := newEditable([]byte("const A: int64 = 1\nconst B = A\nconst C = B\n"))
	cDecl := e.doc.File().Decls[2]

	// Change A's annotation int64 -> int32; A's value (1) is unchanged.
	e.edit(source.Edit{Start: 9, End: 14, NewText: []byte("int32")})
	assertMatchesReference(t, e, []byte("const A: int32 = 1\nconst B = A\nconst C = B\n"))

	if e.prog.db.computed[valueKey(cDecl)] {
		t.Error("valueOf(C) was recomputed; an annotation change should not affect values")
	}
}
