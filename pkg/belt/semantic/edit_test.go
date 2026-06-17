package semantic

import (
	"math/rand"
	"sort"
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/belt/parser/abstract"
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

	if got, want := dumpIR(t, e.prog.Module(soleFileID)), dumpIR(t, refModule); got != want {
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
		{"value change keeps type", "const A: long = 1\nconst B = A\n", source.Edit{Start: 16, End: 17, NewText: []byte("2")}},
		{"append a declaration", "const A = 1\n", source.Edit{Start: 11, End: 11, NewText: []byte("const B = A\n")}},
		{"introduce undefined name", "const A = B\n", source.Edit{Start: 10, End: 11, NewText: []byte("C")}},
		{"resolve an undefined name", "const A = B\nconst C = 1\n", source.Edit{Start: 10, End: 11, NewText: []byte("C")}},
		{"introduce a cycle", "const A = 1\nconst B = A\n", source.Edit{Start: 9, End: 10, NewText: []byte("B")}},
		{"change annotation", "const A: long = 1\n", source.Edit{Start: 9, End: 13, NewText: []byte("int")}},
		{"unknown type", "const A: long = 1\n", source.Edit{Start: 9, End: 13, NewText: []byte("nope")}},
		{"add pub", "const A = 1\n", source.Edit{Start: 0, End: 0, NewText: []byte("pub ")}},
		{"duplicate name", "const A = 1\nconst B = 2\n", source.Edit{Start: 18, End: 19, NewText: []byte("A")}},
		{"delete a declaration", "const A = 1\nconst B = A\n", source.Edit{Start: 0, End: 12, NewText: nil}},
		{"break an assertion", "const A = 1\nassert A > 0\n", source.Edit{Start: 21, End: 22, NewText: []byte("<")}},
		{"fix an assertion via its const", "const A = 1\nassert A < 0\n", source.Edit{Start: 10, End: 11, NewText: []byte("-1")}},
		{"insert an assertion", "const A = 1\n", source.Edit{Start: 12, End: 12, NewText: []byte("assert A == 1\n")}},
		{"delete an assertion", "const A = 1\nassert A < 0\n", source.Edit{Start: 12, End: 25, NewText: nil}},
		// Flip a getter to an ordinary method by deleting its `get ` modifier: the
		// read `t.size` was a getter read and is now a method value, so the
		// signature key must change (it carries the kind) for the use site to
		// re-resolve rather than reuse the stale getter typing. The incremental ==
		// full oracle catches an over-eager early cutoff here.
		{
			"getter becomes a method",
			"pub type T = { n: nint } impl {\n  pub get size(): nint {\n    return self.n\n  }\n}\nconst V: T = T{ n: 1 }\nconst S = V.size\n",
			source.Edit{Start: 38, End: 42, NewText: nil}, // delete "get "
		},
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
	e := newEditable([]byte("const A: long = 1\nconst B = A\nconst C = B\n"))
	cDecl := e.doc.File().Decls[2] // unchanged by the edit below

	// Change A's value 1 -> 2; A stays long, so B's type is unchanged.
	e.edit(source.Edit{Start: 16, End: 17, NewText: []byte("2")})
	assertMatchesReference(t, e, []byte("const A: long = 2\nconst B = A\nconst C = B\n"))

	if e.prog.db.computed[typeOfKey(cDecl)] {
		t.Error("typeOf(C) was recomputed; early cutoff should have stopped the change at B")
	}
}

func TestEditFuzz(t *testing.T) {
	r := rand.New(rand.NewSource(0x5E3A))
	alphabet := []string{
		"const ", "pub ", "A", "B", "C", "Name", " = ", " : ", "long", "int",
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
		"const Twice: fn(x: nint): nint = fn(x) { return x * 2 }\n" +
		"pub type T = sbyte impl {\n  pub f(): fn(x: bool): bool {\n    return fn(b) { return b }\n  }\n}\n" +
		"pub fn g(): nint {\n  return [1, 2].map(fn(y) { return y + 1 }).count()\n}\n"
	e := newEditable([]byte(src))

	got := make([]string, 0, len(e.prog.FuncLitTypes(soleFileID)))
	for _, ft := range e.prog.FuncLitTypes(soleFileID) {
		got = append(got, ft.String())
	}
	sort.Strings(got)
	// The fn-body lambda fn(y) { return y + 1 } settles to fn(int): int.
	want := "fn(bool): bool|fn(nint): nint|fn(nint): nint|fn(nint): nint"
	if strings.Join(got, "|") != want {
		t.Fatalf("FuncLitTypes = %v, want %s", got, want)
	}
}

// TestExprTypesRelationChain pins that the checker writes back a relation type for a
// master query, so the editor reads it rather than re-deriving the scope rules: a
// master in a body reads as its relation and a where chain carries relation<Cards>,
// the types a chained .count or .sum resolves through. The bare master and the where
// call both settle to relation<Cards>, so the stream carries it at least twice.
func TestExprTypesRelationChain(t *testing.T) {
	src := "master Cards {\n  record { id: int, cost: int }\n  primary id\n}\n" +
		"fn probe(): nint {\n  return Cards.where(fn(c) -> c.cost > 0).count()\n}\n"
	e := newEditable([]byte(src))
	n := 0
	for _, ty := range e.prog.ExprTypes(soleFileID) {
		if ty != nil && ty.String() == "relation<Cards>" {
			n++
		}
	}
	if n < 2 {
		t.Fatalf("ExprTypes should carry relation<Cards> for the bare master and the where chain; got %d such entries", n)
	}
}

// TestInterfaceMethodTypeParamsResolved pins that a generic interface method keeps
// its explicit type parameters and their bounds on the resolved ir.Method — the
// interface twin of a concrete method — so an IR dump and text round-trip carry the
// declared list rather than serializing it away.
func TestInterfaceMethodTypeParamsResolved(t *testing.T) {
	src := "pub interface ordered {\n  pub lt(other: self): bool\n}\n" +
		"pub interface seq<V> {\n  pick<A: ordered>(init: A): A\n}\n"
	e := newEditable([]byte(src))
	var pick *ir.Method
	for _, def := range e.prog.Module(soleFileID).Types {
		if def.Name != "seq" {
			continue
		}
		for _, m := range def.Methods {
			if m.Name == "pick" {
				pick = m
			}
		}
	}
	if pick == nil {
		t.Fatal("did not resolve the interface method pick")
	}
	if len(pick.TypeParams) != 1 || pick.TypeParams[0].Name != "A" {
		t.Fatalf("pick.TypeParams = %#v, want one parameter A", pick.TypeParams)
	}
	if pick.TypeParams[0].Bound == nil {
		t.Fatalf("pick.TypeParams[0].Bound is nil, want the ordered interface bound recorded")
	}
}

// TestGenericBoundReportedOnce pins that an invalid type-parameter bound is reported
// exactly once: recording the resolved type parameters reuses the bounds the signature
// already settled rather than settling them a second time, so a generic function — and
// a generic method or interface member, which share the recording path — does not emit
// the same unknown_type twice.
func TestGenericBoundReportedOnce(t *testing.T) {
	for _, src := range []string{
		"pub fn g<T: Nope>(x: T): T {\n  return x\n}\n",
		"pub type Box<V> = { v: V } impl {\n  pub fn g<T: Nope>(x: T): T {\n    return x\n  }\n}\n",
		"pub interface seq<V> {\n  pick<T: Nope>(x: T): T\n}\n",
	} {
		e := newEditable([]byte(src))
		n := 0
		for _, d := range e.prog.Diagnostics(soleFileID) {
			if strings.Contains(d.Message, "Nope") {
				n++
			}
		}
		if n != 1 {
			t.Errorf("an unknown bound should be reported once, got %d for:\n%s", n, src)
		}
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
	src := "const A: long = 1\nconst B = A\nassert B > 0\n"
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
	e := newEditable([]byte("const A: long = 1\nconst B = A\nconst C = B\n"))
	cDecl := e.doc.File().Decls[2]

	// Change A's annotation long -> int; A's value (1) is unchanged.
	e.edit(source.Edit{Start: 9, End: 13, NewText: []byte("int")})
	assertMatchesReference(t, e, []byte("const A: int = 1\nconst B = A\nconst C = B\n"))

	if e.prog.db.computed[valueKey(cDecl)] {
		t.Error("valueOf(C) was recomputed; an annotation change should not affect values")
	}
}

// TestEarlyCutoffNonScalarValue guards the value-side early cutoff for the
// constant kinds beyond int/bool/string: datetime, duration, record, and error.
// Each case declares a base const V, an intermediate X that folds V into the
// kind under test, and two pure consumers (Y = X, Z = Y). The edit rewrites V's
// initializer to a different but equal-valued, equal-length expression, so V is
// re-evaluated to a byte-identical value and X re-folds to a structurally equal
// constant — the cutoff must therefore stop at X and not re-evaluate Z.
//
// Before the cutoff equality covered these kinds (its switch defaulted to "not
// equal"), every recompute of X compared unequal to its prior value and the
// change rippled to Y and Z, defeating early cutoff for the whole dependency
// cone. These cases turn that silent over-invalidation into a hard failure: a
// new ConstKind left out of ir.ConstantsEqual now panics there rather than
// regressing the cutoff unnoticed. (Enum is intentionally omitted: an enum
// const carries the enum's *TypeDef pointer, which the type-defs table rebuilds
// on any edit, so its invalidation is governed by type identity — a separate,
// correct mechanism — not by this value equality.)
//
// The edits are length-preserving so the downstream consts' decl nodes and byte
// offsets stay put; only V's value query is forced to recompute.
func TestEarlyCutoffNonScalarValue(t *testing.T) {
	cases := []struct {
		name     string
		src      string
		old, new string // an equal-valued, equal-length rewrite of V's initializer
	}{
		{
			// X = epoch + V, a datetime. V is a duration; 60s and 1m are the same
			// span, so X folds to the same UTC instant. The trailing space keeps
			// the line width so nothing downstream shifts.
			name: "datetime",
			src:  "const V = 60s\nconst X = D1970-01-01T00:00:00.000Z + V\nconst Y = X\nconst Z = Y\n",
			old:  "= 60s\n",
			new:  "= 1m \n",
		},
		{
			// X = V * 2, a duration. 60s and 1m are the same span; the trailing
			// space keeps the line width so nothing downstream shifts.
			name: "duration",
			src:  "const V = 60s\nconst X = V * 2\nconst Y = X\nconst Z = Y\n",
			old:  "= 60s\n",
			new:  "= 1m \n",
		},
		{
			// X = { a: V }, a record. 2 + 0 and 0 + 2 both fold to the integer 2,
			// so X's field a folds to the same value.
			name: "record",
			src:  "const V = 2 + 0\nconst X = { a: V }\nconst Y = X\nconst Z = Y\n",
			old:  "2 + 0",
			new:  "0 + 2",
		},
		{
			// X = error(V), an error. "ab" + "" and "" + "ab" both fold to "ab",
			// so X's error message folds to the same string.
			name: "error",
			src:  "const V = \"ab\" + \"\"\nconst X = error(V)\nconst Y = X\nconst Z = Y\n",
			old:  "\"ab\" + \"\"",
			new:  "\"\" + \"ab\"",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.old) != len(tc.new) {
				t.Fatalf("setup: edit is not length-preserving (%d vs %d)", len(tc.old), len(tc.new))
			}
			e := newEditable([]byte(tc.src))
			decls := e.doc.File().Decls
			zDecl := decls[len(decls)-1] // the far consumer, const Z = Y

			i := strings.Index(tc.src, tc.old)
			if i < 0 {
				t.Fatalf("setup: %q not found in src", tc.old)
			}
			e.edit(source.Edit{Start: i, End: i + len(tc.old), NewText: []byte(tc.new)})
			assertMatchesReference(t, e, []byte(strings.Replace(tc.src, tc.old, tc.new, 1)))

			if e.prog.db.computed[valueKey(zDecl)] {
				t.Errorf("valueOf(Z) was recomputed; X's %s value was unchanged, so the cutoff should have stopped at X", tc.name)
			}
		})
	}
}
