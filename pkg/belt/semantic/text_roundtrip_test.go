// This file holds the IR half of the text-representation gates, and the
// headline one — the detached fold: a module rebuilt from its text form has
// lost every AST backpointer by construction, so when the IR interpreter folds
// the detached graph to the very values the live pipeline published, "the AST
// carries no semantics" is a fact CI re-proves on every run, not a doctrine.
// Linked canonicity (the linked re-marshal is byte-identical) and golden
// survival (committed .ir snapshots round-trip) ride the same corpus; the fuzz
// gate keeps the unmarshaler panic-free on arbitrary input.
package semantic

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/belt/eval"
	"github.com/masterbelt/masterbelt/pkg/belt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/project"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// textResolver links a detached module against the world outside it: the
// prelude's type definitions and — for a project file — the live sibling
// modules' declarations. This is the explicit-Link half of the closure story:
// the text carries one module, and the resolver is the closure.
func textResolver(siblings []*ir.Module) ir.Resolver {
	return ir.Resolver{
		Const:    func(name string) *ir.Const { return siblingConst(siblings, name) },
		TypeDef:  func(name string) *ir.TypeDef { return siblingTypeDef(siblings, name) },
		Function: func(ref string) *ir.Function { return siblingFunction(siblings, ref) },
	}
}

// siblingConst finds a published constant by name across the sibling modules.
func siblingConst(siblings []*ir.Module, name string) *ir.Const {
	for _, m := range siblings {
		for _, c := range m.Consts {
			if c != nil && c.Name == name {
				return c
			}
		}
	}
	return nil
}

// siblingTypeDef finds a type definition by name in the prelude, then across the
// sibling modules.
func siblingTypeDef(siblings []*ir.Module, name string) *ir.TypeDef {
	if def := universe().prelude[name]; def != nil {
		return def
	}
	for _, m := range siblings {
		for _, def := range m.Types {
			if def != nil && def.Name == name {
				return def
			}
		}
	}
	return nil
}

// siblingFunction finds a function by its reference across the sibling modules.
func siblingFunction(siblings []*ir.Module, ref string) *ir.Function {
	for _, m := range siblings {
		for _, fn := range m.Funcs {
			if fn != nil && ir.FunctionRef(fn) == ref {
				return fn
			}
		}
	}
	return nil
}

// constantText renders a folded constant for cross-graph comparison: two
// graphs never share pointers (an enum constant holds its own graph's
// definition), so the exact text — which renders references by name — is the
// equality that survives the trip.
func constantText(t *testing.T, c *ir.Constant) string {
	t.Helper()
	if c == nil {
		return "<unevaluated>"
	}
	text, err := c.MarshalText()
	if err != nil {
		t.Fatalf("Constant.MarshalText: %v", err)
	}
	return string(text)
}

// roundTripModule drives one module through the whole gate: marshal,
// unmarshal, link, re-marshal byte-identically, then fold every
// constant of the detached graph and compare with the published Eval.
func roundTripModule(t *testing.T, label string, live *ir.Module, siblings []*ir.Module) {
	t.Helper()
	first, err := live.MarshalText()
	if err != nil {
		t.Fatalf("%s: MarshalText: %v", label, err)
	}
	var back ir.Module
	if err := back.UnmarshalText(first); err != nil {
		t.Fatalf("%s: UnmarshalText: %v", label, err)
	}
	if err := back.Link(textResolver(siblings)); err != nil {
		t.Fatalf("%s: Link: %v", label, err)
	}
	second, err := back.MarshalText()
	if err != nil {
		t.Fatalf("%s: re-MarshalText: %v", label, err)
	}
	if !bytes.Equal(first, second) {
		t.Errorf("%s: linked re-marshal is not byte-identical", label)
	}

	// The detached fold. The graph env reads types off the detached
	// module (plus the prelude and the siblings); the folder reads only the
	// graph. Nothing here can touch an AST — the backpointers do not exist.
	// The oracle is the same interpreter folding the live module's graph:
	// identical inputs modulo detachment, so any disagreement is semantics
	// the backpointers were carrying. (The live graph fold itself agrees with
	// the published Eval — TestGraphFoldParity pins that separately.)
	detachedEnv := newModuleGraphEnv(append([]*ir.Module{&back}, siblings...)...)
	liveEnv := newModuleGraphEnv(append([]*ir.Module{live}, siblings...)...)
	if len(back.Consts) != len(live.Consts) {
		t.Fatalf("%s: %d consts after the trip, want %d", label, len(back.Consts), len(live.Consts))
	}
	for i, c := range back.Consts {
		if c == nil || c.Value == nil {
			continue
		}
		got := eval.GraphExpecting(c.Value, c.Type, detachedEnv)
		want := eval.GraphExpecting(live.Consts[i].Value, live.Consts[i].Type, liveEnv)
		if constantText(t, got) != constantText(t, want) {
			t.Errorf("%s: const %s: detached fold = %s, live fold = %s", label, c.Name, got, want)
		}
	}
}

// TestIRTextRoundTrip runs the round-trip and detached-fold gates over every
// shared example, single files and projects alike.
func TestIRTextRoundTrip(t *testing.T) {
	entries, err := os.ReadDir(sharedExamples)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		switch {
		case entry.IsDir():
			t.Run(name, func(t *testing.T) { roundTripProjectExample(t, name) })
		case strings.HasSuffix(name, ".belt"):
			t.Run(name, func(t *testing.T) { roundTripFileExample(t, name) })
		}
	}
}

// roundTripProjectExample analyzes a multi-file example project and runs the
// round-trip gate over each module against its siblings.
func roundTripProjectExample(t *testing.T, name string) {
	t.Helper()
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
	for id, m := range modules {
		var siblings []*ir.Module
		for sid, sm := range modules {
			if sid != id {
				siblings = append(siblings, sm)
			}
		}
		roundTripModule(t, string(id), m, siblings)
	}
}

// roundTripFileExample analyzes a single-file example and runs the
// round-trip gate over its module.
func roundTripFileExample(t *testing.T, name string) {
	t.Helper()
	src, err := os.ReadFile(filepath.Join(sharedExamples, name))
	if err != nil {
		t.Fatal(err)
	}
	module, _ := Analyze(abstract.NewDocument(src))
	roundTripModule(t, name, module, nil)
}

// TestIRSnapshotsUnmarshal pins golden survival: every committed .ir snapshot
// unmarshals and re-marshals to its own bytes. A project snapshot concatenates its
// per-file modules under "# id" headers; each section is its own module text.
func TestIRSnapshotsUnmarshal(t *testing.T) {
	matches, err := irSnapshotFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		t.Fatal("no .ir snapshots found")
	}
	for _, path := range matches {
		name, err := filepath.Rel(snapshotDir, path)
		if err != nil {
			t.Fatal(err)
		}
		t.Run(filepath.ToSlash(name), func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for label, section := range irSnapshotSections(data) {
				var m ir.Module
				if err := m.UnmarshalText(section); err != nil {
					t.Errorf("%s: snapshot does not unmarshal: %v", label, err)
					continue
				}
				// Canonicity of the committed bytes: the unlinked re-marshal
				// must reproduce the section exactly (placeholders render the
				// very names they were decoded from).
				again, err := m.MarshalText()
				if err != nil {
					t.Errorf("%s: re-MarshalText: %v", label, err)
					continue
				}
				if !bytes.Equal(again, section) {
					t.Errorf("%s: snapshot is not canonical: unmarshal+marshal diverges", label)
				}
			}
		})
	}
}

// irSnapshotFiles lists every committed .ir snapshot.
func irSnapshotFiles() ([]string, error) {
	return filepath.Glob(filepath.Join(snapshotDir, "*.ir"))
}

// irSnapshotSections splits a snapshot into its module texts: a single-file
// snapshot is one unlabeled section, a project snapshot one section per
// "# id" header.
func irSnapshotSections(data []byte) map[string][]byte {
	if !bytes.HasPrefix(data, []byte("# ")) {
		return map[string][]byte{"": data}
	}
	out := map[string][]byte{}
	rest := data
	for len(rest) > 0 {
		nl := bytes.IndexByte(rest, '\n')
		header := string(rest[2:nl])
		rest = rest[nl+1:]
		end := bytes.Index(rest, []byte("\n# "))
		if end < 0 {
			out[header] = rest
			break
		}
		out[header] = rest[:end+1]
		rest = rest[end+1:]
	}
	return out
}

// FuzzIRUnmarshal is the fuzz gate: the unmarshaler accepts or rejects any
// input without panicking, and whatever it accepts marshals without error to
// something the unmarshaler accepts again. (Byte canonicity is the linked
// round trip's property — TestIRTextRoundTrip pins it; an unlinked module's
// method placeholders do not carry enough to re-render their references.)
func FuzzIRUnmarshal(f *testing.F) {
	matches, err := irSnapshotFiles()
	if err != nil {
		f.Fatal(err)
	}
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			f.Fatal(err)
		}
		for _, section := range irSnapshotSections(data) {
			f.Add(section)
		}
	}
	f.Add([]byte("Module\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		var m ir.Module
		if err := m.UnmarshalText(data); err != nil {
			return // rejected: fine, as long as it never panics
		}
		text, err := m.MarshalText()
		if err != nil {
			t.Fatalf("accepted input fails to marshal: %v", err)
		}
		var again ir.Module
		if err := again.UnmarshalText(text); err != nil {
			t.Fatalf("marshal output fails to unmarshal: %v", err)
		}
	})
}

// TestMethodOwnersStamped pins the AttachMethods convention the marshaler
// depends on: every method of every definition — the prelude's, the
// registry's, and the examples' — carries Owner == its containing def. A
// method appended around TypeDef.AttachMethods would marshal as an
// unresolvable "?." reference, and that failure would surface far from its
// cause (at Link time of a serialized module); this pin moves it to the
// attach site's test run.
func TestMethodOwnersStamped(t *testing.T) {
	checkDef := func(label string, def *ir.TypeDef) {
		for _, m := range def.Methods {
			if m.Owner != def {
				t.Errorf("%s: method %s.%s has Owner %v — attach methods through TypeDef.AttachMethods", label, def.Name, m.Name, m.Owner)
			}
		}
	}
	for name, def := range universe().prelude {
		checkDef("prelude "+name, def)
	}
	entries, err := os.ReadDir(sharedExamples)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".belt") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(sharedExamples, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		module, _ := Analyze(abstract.NewDocument(src))
		for _, def := range module.Types {
			checkDef(entry.Name(), def)
		}
	}
}
