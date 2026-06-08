package cli

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/belt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/belt/semantic"
	"github.com/masterbelt/masterbelt/pkg/source/formatter"
)

var updateFixtures = flag.Bool("update", false, "regenerate pretty.belt fixtures from their first dirty input")

// TestFormatFixtures drives the dirty/pretty corpus that grows with each
// formatting rule. A case directory holds one pretty.belt (the canonical
// spelling) and one or more dirty*.belt inputs of the SAME meaning but messy
// layout, plus an optional .editorconfig the real resolution path picks up. For
// every case it asserts:
//
//   - pretty.belt is a fixed point: Format(pretty) == pretty.
//   - every dirty formats to pretty: Format(dirty) == pretty — so several dirty
//     inputs collapse to one spelling, the "one canonical spelling" property.
//   - formatting preserves meaning: the resolved IR of each dirty equals that of
//     pretty, for analyzable (complete, diagnostic-free) cases — the hardest
//     invariant, and the one that catches a layout change that altered meaning.
//
// pretty.belt is the documentation of what a rule does: its diff against a dirty
// input shows the rewrite at a glance. Refresh the pretty files from their first
// dirty input with:
//
//	go test ./internal/cli/ -run TestFormatFixtures -update
//
// pretty is the assertion, so a regenerated file must be reviewed before commit.
func TestFormatFixtures(t *testing.T) {
	root := filepath.Join("testdata", "format")
	cases := fixtureCases(t, root)
	if len(cases) == 0 {
		t.Fatalf("no fixture cases found under %s", root)
	}
	for _, dir := range cases {
		name, err := filepath.Rel(root, dir)
		if err != nil {
			t.Fatal(err)
		}
		t.Run(filepath.ToSlash(name), func(t *testing.T) {
			runFixtureCase(t, dir)
		})
	}
}

func runFixtureCase(t *testing.T, dir string) {
	prettyPath := filepath.Join(dir, "pretty.belt")
	dirty, err := filepath.Glob(filepath.Join(dir, "dirty*.belt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(dirty) == 0 {
		t.Fatalf("%s: no dirty*.belt inputs", dir)
	}

	if *updateFixtures {
		layout := formatter.Resolve(prettyPath, formatter.DefaultLayout)
		src, err := os.ReadFile(dirty[0])
		if err != nil {
			t.Fatal(err)
		}
		out, _ := formatSource(src, layout)
		if err := os.WriteFile(prettyPath, []byte(out), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	pretty, err := os.ReadFile(prettyPath)
	if err != nil {
		t.Fatal(err)
	}
	prettyLayout := formatter.Resolve(prettyPath, formatter.DefaultLayout)

	if got, _ := formatSource(pretty, prettyLayout); got != string(pretty) {
		t.Errorf("pretty.belt is not a fixed point:\n%s",
			unifiedDiff("pretty.belt", "Format(pretty.belt)", string(pretty), got))
	}

	prettyIR := analyzableIR(pretty)

	for _, dp := range dirty {
		src, err := os.ReadFile(dp)
		if err != nil {
			t.Fatal(err)
		}
		// Same directory, so the same .editorconfig resolves — the layout is the
		// case's, picked up through the real resolution path.
		layout := formatter.Resolve(dp, formatter.DefaultLayout)
		if got, _ := formatSource(src, layout); got != string(pretty) {
			t.Errorf("Format(%s) != pretty.belt:\n%s", filepath.Base(dp),
				unifiedDiff(filepath.Base(dp), "pretty.belt", got, string(pretty)))
		}
		if prettyIR != "" {
			if ir := analyzableIR(src); ir != prettyIR {
				t.Errorf("Format(%s) changed meaning: IR differs from pretty.belt", filepath.Base(dp))
			}
		}
	}
}

// fixtureCases returns every directory under root that holds a pretty.belt.
func fixtureCases(t *testing.T, root string) []string {
	var dirs []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && d.Name() == "pretty.belt" {
			dirs = append(dirs, filepath.Dir(path))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return dirs
}

// analyzableIR returns the exact IR text of src, or "" when src has any
// lexer/parser/semantic error (then it is not a complete program and the
// meaning-preservation check does not apply to it). A fixed file id is used so
// the IR of a dirty input and its pretty form — which differ only in layout —
// compare byte for byte, anchors included.
func analyzableIR(src []byte) string {
	doc := abstract.NewDocument(src)
	prog := semantic.NewProgram()
	id := semantic.FileID("fixture")
	prog.SetFile(id, doc, nil)
	prog.Refresh()
	if anyError(doc.Concrete().LexDiagnostics()) || anyError(doc.Diagnostics()) || anyError(prog.Diagnostics(id)) {
		return ""
	}
	text, err := prog.Module(id).MarshalText()
	if err != nil {
		return ""
	}
	return string(text)
}

// TestFmtCorpusIsCanonical pins the executable spec: every .belt file shipped in
// the repository — the example corpus and the prelude — is already in its
// canonical form, so growing a formatting rule that would move them fails here.
func TestFmtCorpusIsCanonical(t *testing.T) {
	corpora := []string{
		filepath.Join("..", "..", "pkg", "belt", "testdata", "examples"),
		filepath.Join("..", "..", "pkg", "belt", "builtin", "belt"),
	}
	for _, dir := range corpora {
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("corpus directory %s is missing: %v", dir, err)
		}
	}
	out, _, err := execFmt(t, "", append([]string{"--check"}, corpora...)...)
	if err != nil {
		t.Errorf("the in-repo .belt corpus is not canonically formatted; run `masterbelt fmt -w` on:\n%s", out)
	}
}
