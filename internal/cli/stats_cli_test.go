package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/internal/belttest"
)

// execIRStats runs `masterbelt ir --stats <file>` capturing stdout+stderr.
func TestStatsFlagEmitsReuseProfile(t *testing.T) {
	root := t.TempDir()
	belttest.WriteFile(t, root, "main.belt", "const A = 1\nconst B = A\n")
	path := filepath.Join(root, "main.belt")

	var out bytes.Buffer
	RootCmd.SetOut(&out)
	RootCmd.SetErr(&out)
	RootCmd.SetArgs([]string{"ir", "--stats", path})
	t.Cleanup(func() {
		RootCmd.SetOut(nil)
		RootCmd.SetErr(nil)
		RootCmd.SetArgs([]string{})
		_ = IRCmd.Flags().Set("format", "text")
		_ = RootCmd.PersistentFlags().Set("stats", "")
		runStats = nil
	})
	if err := RootCmd.Execute(); err != nil {
		t.Fatalf("ir --stats: %v\n%s", err, out.String())
	}
	// The stats JSON went to stderr (merged into out); the IR went to stdout
	// (also out). Find the JSON object.
	s := out.String()
	i := strings.Index(s, "{")
	if i < 0 {
		t.Fatalf("no stats JSON in output:\n%s", s)
	}
	var doc struct {
		Queries struct {
			Computed map[string]int `json:"computed"`
		} `json:"queries"`
		Files int `json:"files"`
		Decls int `json:"decls"`
	}
	// The IR text precedes the JSON; decode from the first brace forward, but the
	// IR text form has no braces, so the first brace opens the stats object.
	dec := json.NewDecoder(strings.NewReader(s[i:]))
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("stats is not JSON: %v\n%s", err, s[i:])
	}
	if doc.Files != 1 || doc.Decls != 2 {
		t.Errorf("stats files=%d decls=%d, want 1 and 2", doc.Files, doc.Decls)
	}
	if len(doc.Queries.Computed) == 0 {
		t.Error("stats reported no computed queries for a fresh analysis")
	}
}
