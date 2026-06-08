package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/internal/belttest"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// execDump executes `masterbelt dump args...` through the root command and
// returns its combined output.
func execDump(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	RootCmd.SetOut(&out)
	RootCmd.SetErr(&out)
	RootCmd.SetArgs(append([]string{"dump"}, args...))
	t.Cleanup(func() {
		RootCmd.SetOut(nil)
		RootCmd.SetErr(nil)
		RootCmd.SetArgs([]string{})
		_ = RootCmd.PersistentFlags().Set("reporter", reporterText)
	})
	err := RootCmd.Execute()
	return out.String(), err
}

// TestDumpToken renders the lexer's token stream: kind, span, and lexeme.
func TestDumpToken(t *testing.T) {
	root := t.TempDir()
	belttest.WriteFile(t, root, "main.belt", "const A = 1\n")

	out, err := execDump(t, "token", filepath.Join(root, "main.belt"))
	if err != nil {
		t.Fatalf("dump token = %v\n%s", err, out)
	}
	for _, want := range []string{`Const@0+5 "const"`, `Ident@6+1 "A"`, `Int@10+1 "1"`} {
		if !strings.Contains(out, want) {
			t.Errorf("token dump missing %q:\n%s", want, out)
		}
	}
}

// TestDumpCST renders the lossless concrete syntax tree, trivia included.
func TestDumpCST(t *testing.T) {
	root := t.TempDir()
	belttest.WriteFile(t, root, "main.belt", "const A = 1\n")

	out, err := execDump(t, "cst", filepath.Join(root, "main.belt"))
	if err != nil {
		t.Fatalf("dump cst = %v\n%s", err, out)
	}
	for _, want := range []string{"File", "ConstDecl", `Const "const"`, `Whitespace " "`} {
		if !strings.Contains(out, want) {
			t.Errorf("cst dump missing %q:\n%s", want, out)
		}
	}
}

// TestDumpAST renders the abstract syntax tree, trivia dropped.
func TestDumpAST(t *testing.T) {
	root := t.TempDir()
	belttest.WriteFile(t, root, "main.belt", "const A = 1\n")

	out, err := execDump(t, "ast", filepath.Join(root, "main.belt"))
	if err != nil {
		t.Fatalf("dump ast = %v\n%s", err, out)
	}
	if !strings.HasPrefix(out, "File\n") || !strings.Contains(out, "ConstDecl") {
		t.Errorf("ast dump = %q, want the File/ConstDecl tree", out)
	}
	if strings.Contains(out, "Whitespace") {
		t.Errorf("ast dump carries trivia, want it dropped:\n%s", out)
	}
}

// TestDumpIRText pins the IR text output: the exact representation, parseable
// back into a module.
func TestDumpIRText(t *testing.T) {
	root := t.TempDir()
	belttest.WriteFile(t, root, "main.belt", "const A = 1\n")

	out, err := execDump(t, "ir", filepath.Join(root, "main.belt"))
	if err != nil {
		t.Fatalf("dump ir = %v\n%s", err, out)
	}
	var m ir.Module
	if err := m.UnmarshalText([]byte(out)); err != nil {
		t.Fatalf("output does not unmarshal: %v\n%s", err, out)
	}
	if len(m.Consts) != 1 || m.Consts[0].Name != "A" {
		t.Errorf("round-tripped module = %+v, want the one const A", m.Consts)
	}
}

// TestDumpIRJSON pins the stdlib dividend: the module embeds in a JSON envelope
// keyed by the stage, the exact text the text format emits.
func TestDumpIRJSON(t *testing.T) {
	root := t.TempDir()
	belttest.WriteFile(t, root, "main.belt", "const A = 1\n")

	out, err := execDump(t, "ir", filepath.Join(root, "main.belt"), "--reporter", "json")
	if err != nil {
		t.Fatalf("dump ir --reporter json = %v\n%s", err, out)
	}
	var doc struct {
		File string `json:"file"`
		IR   string `json:"ir"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if !strings.HasPrefix(doc.IR, "Module\n") {
		t.Errorf("embedded IR = %q, want the exact text form", doc.IR)
	}
	var m ir.Module
	if err := m.UnmarshalText([]byte(doc.IR)); err != nil {
		t.Fatalf("embedded IR does not unmarshal: %v", err)
	}
}

// TestDumpIRRejectsBrokenFile pins the ir gate: a file with errors gets its
// diagnostics and a non-zero exit, never a silently partial graph.
func TestDumpIRRejectsBrokenFile(t *testing.T) {
	root := t.TempDir()
	belttest.WriteFile(t, root, "main.belt", "const A = bogus\n")

	out, err := execDump(t, "ir", filepath.Join(root, "main.belt"))
	if err == nil {
		t.Fatalf("dump ir on a broken file succeeded:\n%s", out)
	}
	if strings.HasPrefix(out, "Module\n") {
		t.Errorf("a broken file printed IR:\n%s", out)
	}
	if !strings.Contains(out, "undefined") && !strings.Contains(out, "error") {
		t.Errorf("output carries no diagnostic: %q", out)
	}
}

// TestDumpEarlyStagesIgnoreSemanticErrors: a semantic error (an undefined name)
// is not a lex or parse error, so token, cst, and ast still render the file —
// only ir withholds its graph.
func TestDumpEarlyStagesIgnoreSemanticErrors(t *testing.T) {
	root := t.TempDir()
	belttest.WriteFile(t, root, "main.belt", "const A = bogus\n")
	path := filepath.Join(root, "main.belt")

	for _, stage := range []string{"token", "cst", "ast"} {
		if out, err := execDump(t, stage, path); err != nil {
			t.Errorf("dump %s on a semantically-broken file = %v, want it to render anyway\n%s", stage, err, out)
		}
	}
}

// TestDumpUnknownStage rejects a stage that is not a compilation stage.
func TestDumpUnknownStage(t *testing.T) {
	root := t.TempDir()
	belttest.WriteFile(t, root, "main.belt", "const A = 1\n")

	if out, err := execDump(t, "bogus", filepath.Join(root, "main.belt")); err == nil || !strings.Contains(err.Error(), "unknown stage") {
		t.Errorf("dump bogus = %v, want an unknown-stage error\n%s", err, out)
	}
}
