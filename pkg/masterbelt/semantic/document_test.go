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

func naiveSplice(src []byte, start, end int, repl []byte) []byte {
	out := make([]byte, 0, len(src)-(end-start)+len(repl))
	out = append(out, src[:start]...)
	out = append(out, repl...)
	out = append(out, src[end:]...)
	return out
}

// assertMatchesReference is the oracle: the incrementally analyzed module and
// diagnostics must equal a full Analyze of the current source.
func assertMatchesReference(t *testing.T, doc *Document, content []byte) {
	t.Helper()
	refModule, refDiags := Analyze(abstract.NewDocument(content))

	if got, want := ir.Dump(doc.Module()), ir.Dump(refModule); got != want {
		t.Fatalf("IR mismatch (content %q)\n--- got ---\n%s--- want ---\n%s", content, got, want)
	}

	got, want := doc.Diagnostics(), refDiags
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
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := NewDocument([]byte(tc.initial))
			content := naiveSplice([]byte(tc.initial), tc.edit.Start, tc.edit.End, tc.edit.NewText)
			doc.Edit(tc.edit)
			assertMatchesReference(t, doc, content)
		})
	}
}

// TestEarlyCutoff checks the engine's defining property: editing a constant's
// value without changing its type does not ripple through to a constant two
// references away — the change is cut off once a type comes out unchanged.
func TestEarlyCutoff(t *testing.T) {
	doc := NewDocument([]byte("const A: int64 = 1\nconst B = A\nconst C = B\n"))
	cDecl := doc.AST().File().Decls[2] // unchanged by the edit below

	// Change A's value 1 -> 2; A stays int64, so B's type is unchanged.
	doc.Edit(source.Edit{Start: 17, End: 18, NewText: []byte("2")})
	assertMatchesReference(t, doc, []byte("const A: int64 = 2\nconst B = A\nconst C = B\n"))

	if doc.db.computed[typeOfKey(cDecl)] {
		t.Error("typeOf(C) was recomputed; early cutoff should have stopped the change at B")
	}
}

func TestDocumentFuzz(t *testing.T) {
	r := rand.New(rand.NewSource(0x5E3A))
	alphabet := []string{
		"const ", "pub ", "A", "B", "C", "Name", " = ", " : ", "int64", "int32",
		"nope", "0", "1", "42", " ", "\n", "=", ":",
		// Operators, booleans, and the bool type so the oracle checks that
		// incremental typing and evaluation of expressions match a full
		// analysis (division by zero and type errors included).
		"+", "-", "*", "/", "%", "&&", "||", "!", "<", "==", "bool",
		"true", "false", " + ", " && ", "A + B", "!true",
	}

	start := "const A = 1\nconst B = A\n"
	doc := NewDocument([]byte(start))
	content := []byte(start)

	for range 2000 {
		s := r.Intn(len(content) + 1)
		e := s + r.Intn(len(content)-s+1)
		var repl []byte
		for n := r.Intn(5); n > 0; n-- {
			repl = append(repl, alphabet[r.Intn(len(alphabet))]...)
		}

		edit := source.Edit{Start: s, End: e, NewText: repl}
		content = naiveSplice(content, s, e, repl)
		doc.Edit(edit)
		assertMatchesReference(t, doc, content)
	}
}

// TestFuncLitTypes checks the editor-facing query over all three paths the
// assembler distinguishes: a call-typed literal (un-annotated const), an
// annotation-typed literal, and one typed by a method's declared result.
func TestFuncLitTypes(t *testing.T) {
	src := "const Doubled = [1, 2].map(fn(x) { return x * 2 })\n" +
		"const Twice: fn(x: int): int = fn(x) { return x * 2 }\n" +
		"pub type T = int8 impl {\n  pub f(): fn(x: bool): bool {\n    return fn(b) { return b }\n  }\n}\n"
	doc := NewDocument([]byte(src))

	var got []string
	for _, ft := range doc.FuncLitTypes() {
		got = append(got, ft.String())
	}
	sort.Strings(got)
	want := "fn(bool): bool|fn(int): int|fn(int): int"
	if strings.Join(got, "|") != want {
		t.Fatalf("FuncLitTypes = %v, want %s", got, want)
	}
}

// TestEarlyCutoffLambdaBody checks that editing a lambda body re-checks only
// what depends on it: the edit changes F's value but not its type
// (list<int>), so the change must not propagate past F's dependents.
func TestEarlyCutoffLambdaBody(t *testing.T) {
	src := "const F = [1, 2].map(fn(x) { return x * 2 })\nconst G = F\nconst H = G\n"
	doc := NewDocument([]byte(src))
	hDecl := doc.AST().File().Decls[2]

	// x * 2 -> x * 9: the body's fold changes, the solved types do not.
	i := strings.Index(src, "x * 2") + len("x * ")
	doc.Edit(source.Edit{Start: i, End: i + 1, NewText: []byte("9")})
	assertMatchesReference(t, doc, []byte(strings.Replace(src, "x * 2", "x * 9", 1)))

	if doc.db.computed[typeOfKey(hDecl)] {
		t.Error("typeOf(H) was recomputed; the lambda edit left F's type unchanged")
	}
}

// TestEarlyCutoffValue is the value-side mirror of TestEarlyCutoff: changing a
// constant's annotation (not its value) must not re-evaluate a constant two
// references away.
func TestEarlyCutoffValue(t *testing.T) {
	doc := NewDocument([]byte("const A: int64 = 1\nconst B = A\nconst C = B\n"))
	cDecl := doc.AST().File().Decls[2]

	// Change A's annotation int64 -> int32; A's value (1) is unchanged.
	doc.Edit(source.Edit{Start: 9, End: 14, NewText: []byte("int32")})
	assertMatchesReference(t, doc, []byte("const A: int32 = 1\nconst B = A\nconst C = B\n"))

	if doc.db.computed[valueKey(cDecl)] {
		t.Error("valueOf(C) was recomputed; an annotation change should not affect values")
	}
}
