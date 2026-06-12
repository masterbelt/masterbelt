package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/internal/belttest"
)

// execData executes `masterbelt data args...` through the root command and
// returns its stdout (the typed tables) and stderr (the diagnostics) separately.
func execData(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errBuf bytes.Buffer
	RootCmd.SetOut(&out)
	RootCmd.SetErr(&errBuf)
	RootCmd.SetArgs(append([]string{"data"}, args...))
	t.Cleanup(func() {
		RootCmd.SetOut(nil)
		RootCmd.SetErr(nil)
		RootCmd.SetArgs([]string{})
		_ = RootCmd.PersistentFlags().Set("reporter", reporterText)
		_ = DataCmd.Flags().Set("locale", "en")
		_ = DataCmd.Flags().Set("profile", "")
	})
	err = RootCmd.Execute()
	return out.String(), errBuf.String(), err
}

// skillProject writes a master with a refined field and a [source.csv] base path
// the locator resolves under, plus the csv body, and returns the root. The
// detailed read/coerce/refine behaviour is covered at the loader; these tests
// cover the command's own wiring — rendering, routing, and exit code.
func skillProject(t *testing.T, csvBody string) string {
	t.Helper()
	root := t.TempDir()
	belttest.WriteFile(t, root, "masterbelt.toml", "entry = \"skills.belt\"\n\n[source.csv]\nbasePath = \"data\"\n")
	belttest.WriteFile(t, root, "skills.belt", ""+
		"type Level = int where self > 0\n\n"+
		"master Skill {\n"+
		"  record { id: int, name: string, power: Level }\n"+
		"  primary id\n"+
		"  source {\n"+
		"    csv \"skills.csv\"\n"+
		"  }\n"+
		"}\n")
	belttest.WriteFile(t, root, "data/skills.csv", csvBody)
	return root
}

func TestDataTypedRows(t *testing.T) {
	root := skillProject(t, "id,name,power\n1,Fireball,30\n2,Heal,12\n")
	stdout, stderr, err := execData(t, root)
	if err != nil {
		t.Fatalf("data = %v\nstderr: %s", err, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want none", stderr)
	}
	want := "Skill <- data/skills.csv\nid, name, power\n1 | \"Fireball\" | 30\n2 | \"Heal\" | 12\n\n"
	if stdout != want {
		t.Errorf("stdout =\n%q\nwant\n%q", stdout, want)
	}
}

func TestDataExitsNonzeroOnDataError(t *testing.T) {
	// A refinement violation routes to stderr and fails the command.
	root := skillProject(t, "id,name,power\n1,Drain,-5\n")
	_, stderr, err := execData(t, root)
	if err == nil {
		t.Fatal("data succeeded, want an error")
	}
	if !strings.Contains(stderr, "data/skills.csv:2,9") {
		t.Errorf("stderr = %q, want it to point at the bad cell", stderr)
	}
}

func TestDataReportsProjectErrors(t *testing.T) {
	// A data run over a project that does not type-check must not report success:
	// an unrelated semantic error fails the command, just as check would.
	root := t.TempDir()
	belttest.WriteFile(t, root, "masterbelt.toml", "entry = \"skills.belt\"\n\n[source.csv]\nbasePath = \"data\"\n")
	belttest.WriteFile(t, root, "skills.belt", ""+
		"const Broken: int = \"x\"\n\n"+
		"master Skill {\n"+
		"  record { id: int, name: string }\n"+
		"  primary id\n"+
		"  source { csv \"skills.csv\" }\n"+
		"}\n")
	belttest.WriteFile(t, root, "data/skills.csv", "id,name\n1,Heal\n")

	_, stderr, err := execData(t, root)
	if err == nil {
		t.Fatalf("data succeeded on a broken project, want an error\nstderr: %s", stderr)
	}
	if !strings.Contains(stderr, "skills.belt") {
		t.Errorf("stderr = %q, want the project's own error surfaced", stderr)
	}
}
