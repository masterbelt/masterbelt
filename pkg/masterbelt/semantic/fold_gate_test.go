// The fold totality gates: the language rule is that a constant either folds
// to a value or carries an error — a silently unfolded const is a compiler
// bug. Gate 1 runs the corpus in testdata/fold (one case per file, each with
// its expectation written in a fold-expect directive); gate 2 sweeps every
// shared example, so adding an example automatically extends the gate. The
// knownGaps list names the corpus cases that are open gaps today; each entry
// asserts the gap is still open, so fixing one forces its removal here — and
// an empty list arms the gates fully.
package semantic

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/eval"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/project"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

const foldCorpus = "testdata/fold"

// knownGaps names the corpus cases that are open silent-unfold gaps today —
// analysis green, value missing. Each entry is asserted to still be broken,
// so closing a gap fails the gate until its entry is removed; the milestones
// empty the list ((a)/(d) in M4, (b) in M5, (c)/extern in M3, depth in M6),
// and once it is empty the gates enforce the full rule with no exceptions.
var knownGaps = map[string]string{
	"gap-a-const-ref.belt":  "(a) a const-referenced function value applied directly",
	"gap-a-immediate.belt":  "(a) an immediately applied function literal",
	"gap-a-alias.belt":      "(a) a function value reached through a const alias",
	"gap-a-in-body.belt":    "(a) a function value applied inside an applied body",
	"gap-b-cross-assoc.belt": "(b) an assoc const initializer reading another type's member",
	"gap-c-user-builtin.belt": "(c) a user-code `type Foo = builtin` with no native behind it",
	"gap-d-overload.belt":   "(d) a fn overload split by a named type the kind rule cannot see",
	"gap-extern-pure.belt":  "a pure extern outside the builtin surface never folds",
	"gap-depth.belt":        "the depth budget refuses the fold with no diagnostic (M6)",
}

// foldExpect is one corpus case's expectation: it folds completely (and is
// diagnostic-free), or it is diagnosed with at least the listed codes.
type foldExpect struct {
	folds bool
	codes []string // the bare code names (the catalog's last segment)
}

// parseFoldExpect reads the case's `// fold-expect:` directive — the contract
// the file pins, written in the file so the case and its expectation travel
// together.
func parseFoldExpect(t *testing.T, name string, src []byte) foldExpect {
	t.Helper()
	for line := range strings.SplitSeq(string(src), "\n") {
		rest, ok := strings.CutPrefix(line, "// fold-expect:")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		switch {
		case len(fields) == 1 && fields[0] == "folds":
			return foldExpect{folds: true}
		case len(fields) > 1 && fields[0] == "diagnosed":
			return foldExpect{codes: fields[1:]}
		}
		t.Fatalf("%s: malformed fold-expect directive: %q", name, line)
	}
	t.Fatalf("%s: no fold-expect directive", name)
	return foldExpect{}
}

// unfoldedItems lists every declaration in the module that should carry a
// value and does not: a const with an initializer, an assert condition, an
// associated constant with an initializer, and an enum member. It is the
// totality probe both gates share.
func unfoldedItems(m *ir.Module) []string {
	var out []string
	for _, c := range m.Consts {
		if c != nil && c.Syntax != nil && c.Syntax.Value != nil && c.Eval == nil {
			out = append(out, "const "+c.Name)
		}
	}
	for _, a := range m.Asserts {
		if a.Eval == nil {
			out = append(out, "assert "+a.Cond)
		}
	}
	for _, def := range m.Types {
		for _, ac := range def.Consts {
			if ac.Syntax != nil && ac.Syntax.Value != nil && ac.Value == nil {
				out = append(out, "assoc const "+def.Name+"."+ac.Name)
			}
		}
		if def.Enum != nil {
			for _, em := range def.Enum.Members {
				if em.Value == nil {
					out = append(out, "enum member "+def.Name+"."+em.Name)
				}
			}
		}
	}
	return out
}

// TestFoldTotalityCorpus is gate 1: every corpus case meets its directive —
// a folds case is diagnostic-free with every value present, a diagnosed case
// carries its codes — except the known gaps, each of which is asserted to
// still be broken (so a fix forces the list to shrink).
func TestFoldTotalityCorpus(t *testing.T) {
	entries, err := os.ReadDir(foldCorpus)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]bool{}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".belt") {
			continue
		}
		files[name] = true
		t.Run(name, func(t *testing.T) {
			src, err := os.ReadFile(filepath.Join(foldCorpus, name))
			if err != nil {
				t.Fatal(err)
			}
			exp := parseFoldExpect(t, name, src)
			doc := abstract.NewDocument(src)
			for _, d := range doc.Concrete().LexDiagnostics() {
				t.Fatalf("lex diagnostic: %s @%d: %s", d.Code, d.Offset, d.Message)
			}
			for _, d := range doc.Diagnostics() {
				t.Fatalf("parse diagnostic: %s @%d: %s", d.Code, d.Offset, d.Message)
			}
			module, diags := Analyze(doc)

			if reason, known := knownGaps[name]; known {
				// A known gap is silent today: green analysis, missing value.
				// Both halves are asserted so the entry cannot outlive the fix.
				if len(diags) != 0 {
					t.Errorf("known gap (%s) now has diagnostics %v — update its corpus expectation and remove it from knownGaps", reason, codes(diags))
				}
				if missing := unfoldedItems(module); len(missing) == 0 {
					t.Errorf("known gap (%s) now folds — remove it from knownGaps to arm the gate", reason)
				}
				return
			}

			if exp.folds {
				for _, d := range diags {
					t.Errorf("unexpected diagnostic: %s @%d: %s", d.Code, d.Offset, d.Message)
				}
				for _, item := range unfoldedItems(module) {
					t.Errorf("%s did not fold", item)
				}
				return
			}
			for _, code := range exp.codes {
				found := false
				for _, d := range diags {
					if strings.HasSuffix(string(d.Code), "."+code) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected diagnostic %s, got %v", code, codes(diags))
				}
			}
		})
	}
	// Every known-gap entry must name a corpus file, so a renamed or deleted
	// case cannot leave a stale exemption behind.
	for name := range knownGaps {
		if !files[name] {
			t.Errorf("knownGaps entry %q names no corpus file", name)
		}
	}
}

// TestFoldTotalityExamples is gate 2: every shared example — they are pinned
// diagnostic-free by TestExamplesAnalyzeClean — carries a value for every
// const, assert, associated constant, and enum member. An example added to
// testdata/examples is gated automatically.
func TestFoldTotalityExamples(t *testing.T) {
	entries, err := os.ReadDir(sharedExamples)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("no examples found")
	}
	for _, entry := range entries {
		name := entry.Name()
		switch {
		case entry.IsDir():
			t.Run(name, func(t *testing.T) {
				proj, pdiags := project.Open(filepath.Join(sharedExamples, name))
				if pdiags.Len() > 0 {
					t.Fatalf("project diagnostics: %v", pdiags.Items())
				}
				docs := map[FileID]*abstract.Document{}
				uses := map[FileID]map[*ast.UseDecl]FileID{}
				for _, f := range proj.Files() {
					docs[FileID(f.ID)] = f.AST
					uses[FileID(f.ID)] = UsesOf(f.Uses)
				}
				modules, _ := AnalyzeProgram(docs, uses)
				for id, module := range modules {
					for _, item := range unfoldedItems(module) {
						t.Errorf("%s: %s did not fold", id, item)
					}
				}
			})
		case strings.HasSuffix(name, ".belt"):
			t.Run(name, func(t *testing.T) {
				src, err := os.ReadFile(filepath.Join(sharedExamples, name))
				if err != nil {
					t.Fatal(err)
				}
				module, _ := Analyze(abstract.NewDocument(src))
				for _, item := range unfoldedItems(module) {
					t.Errorf("%s did not fold", item)
				}
			})
		}
	}
}

// TestFoldFailureClassifier pins the two failure reasons: a fold refused by
// the application depth budget classifies as depth (the user's to fix), and a
// missing fold rule classifies as evaluator gap (the compiler's bug).
func TestFoldFailureClassifier(t *testing.T) {
	classify := func(src, name string) string {
		t.Helper()
		doc := abstract.NewDocument([]byte(src))
		file := doc.File()
		files := map[FileID]*ast.File{soleFileID: file}
		q := newDirectQueries(files, nil, universe())
		for _, decl := range file.Decls {
			if decl.Name == name {
				if q.valueOf(decl) != nil {
					t.Fatalf("const %s folded; the classifier only runs on failures", name)
				}
				return foldFailure(soleFileID, decl, q)
			}
		}
		t.Fatalf("const %s not found", name)
		return ""
	}

	depth := "pub fn deep(n: nint): nint {\n  if n == 0 {\n    return 0\n  }\n  return deep(n - 1)\n}\nconst A = deep(300)\n"
	if got := classify(depth, "A"); got != eval.FailureDepth {
		t.Errorf("deep recursion classified %q, want %q", got, eval.FailureDepth)
	}

	// A const-referenced function value applied directly is gap (a) — an
	// evaluator gap while it is open (M4 closes it; this case then needs an
	// artificial gap instead).
	gap := "const F = fn(x: nint): nint -> x + 1\nconst A = F(2)\n"
	if got := classify(gap, "A"); got != eval.FailureGap {
		t.Errorf("function-value call classified %q, want %q", got, eval.FailureGap)
	}
}
