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

// execIR executes `masterbelt ir args...` through the root command and
// returns its combined output.
func execIR(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	RootCmd.SetOut(&out)
	RootCmd.SetErr(&out)
	RootCmd.SetArgs(append([]string{"ir"}, args...))
	t.Cleanup(func() {
		RootCmd.SetOut(nil)
		RootCmd.SetErr(nil)
		RootCmd.SetArgs([]string{})
		_ = IRCmd.Flags().Set("format", "text")
	})
	err := RootCmd.Execute()
	return out.String(), err
}

// TestIRText pins the text output: the exact representation, parseable back
// into a module.
func TestIRText(t *testing.T) {
	root := t.TempDir()
	belttest.WriteFile(t, root, "main.belt", "const A = 1\n")
	path := filepath.Join(root, "main.belt")

	out, err := execIR(t, path)
	if err != nil {
		t.Fatalf("ir = %v\n%s", err, out)
	}
	var m ir.Module
	if err := m.UnmarshalText([]byte(out)); err != nil {
		t.Fatalf("output does not unmarshal: %v\n%s", err, out)
	}
	if len(m.Consts) != 1 || m.Consts[0].Name != "A" {
		t.Errorf("round-tripped module = %+v, want the one const A", m.Consts)
	}
}

// TestIRJSON pins the stdlib dividend (F-4 motivation 6): the module embeds
// in a JSON document through encoding.TextMarshaler with no custom code, and
// the embedded text is the same exact representation the text format emits.
func TestIRJSON(t *testing.T) {
	root := t.TempDir()
	belttest.WriteFile(t, root, "main.belt", "const A = 1\n")
	path := filepath.Join(root, "main.belt")

	out, err := execIR(t, path, "--format", "json")
	if err != nil {
		t.Fatalf("ir --format json = %v\n%s", err, out)
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

// TestIRRejectsBrokenFile pins the diagnostics gate: a file with errors gets
// its diagnostics and a non-zero exit, never a silently partial graph.
func TestIRRejectsBrokenFile(t *testing.T) {
	root := t.TempDir()
	belttest.WriteFile(t, root, "main.belt", "const A = bogus\n")
	path := filepath.Join(root, "main.belt")

	out, err := execIR(t, path)
	if err == nil {
		t.Fatalf("ir on a broken file succeeded:\n%s", out)
	}
	if strings.HasPrefix(out, "Module\n") {
		t.Errorf("a broken file printed IR:\n%s", out)
	}
	if !strings.Contains(out, "undefined") && !strings.Contains(out, "error") {
		t.Errorf("output carries no diagnostic: %q", out)
	}
}
