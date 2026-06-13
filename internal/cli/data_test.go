package cli

import (
	"bytes"
	"encoding/json"
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
	// A refinement violation is reported (on stdout, where check emits) and fails.
	root := skillProject(t, "id,name,power\n1,Drain,-5\n")
	stdout, _, err := execData(t, root)
	if err == nil {
		t.Fatal("data succeeded, want an error")
	}
	if !strings.Contains(stdout, "data/skills.csv:2,9") {
		t.Errorf("stdout = %q, want it to point at the bad cell", stdout)
	}
}

func TestDataExitsNonzeroOnRowValidation(t *testing.T) {
	// A master's per-row validate each check fails for a row, and the data command
	// surfaces it (naming the failing row) and exits nonzero.
	root := t.TempDir()
	belttest.WriteFile(t, root, "masterbelt.toml", "entry = \"skills.belt\"\n\n[source.csv]\nbasePath = \"data\"\n")
	belttest.WriteFile(t, root, "skills.belt", ""+
		"master Skill {\n"+
		"  record { id: int, cost: int, power: int }\n"+
		"  primary id\n"+
		"  source { csv \"skills.csv\" }\n"+
		"  validate {\n"+
		"    each {\n"+
		"      assert self.power >= self.cost\n"+
		"    }\n"+
		"  }\n"+
		"}\n")
	belttest.WriteFile(t, root, "data/skills.csv", "id,cost,power\n1,10,30\n2,50,20\n")

	stdout, _, err := execData(t, root)
	if err == nil {
		t.Fatalf("data succeeded on a failing row, want an error\nstdout: %s", stdout)
	}
	if !strings.Contains(stdout, "data/skills.csv:3") {
		t.Errorf("stdout = %q, want it to name the failing row", stdout)
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

	stdout, _, err := execData(t, root)
	if err == nil {
		t.Fatalf("data succeeded on a broken project, want an error\nstdout: %s", stdout)
	}
	if !strings.Contains(stdout, "skills.belt") {
		t.Errorf("stdout = %q, want the project's own error surfaced", stdout)
	}
	if strings.Contains(stdout, "Skill <-") {
		t.Errorf("stdout = %q, want no table read for a broken project", stdout)
	}
}

func TestDataGatesOnWholeProject(t *testing.T) {
	// A clean file declaring a master must not be read when another file in the
	// project is broken — the master's resolved types could depend on it.
	root := t.TempDir()
	belttest.WriteFile(t, root, "masterbelt.toml", "entry = \"main.belt\"\n\n[source.csv]\nbasePath = \"data\"\n")
	belttest.WriteFile(t, root, "types.belt", "pub const Helper = 1\nconst Broken: int = \"x\"\n")
	belttest.WriteFile(t, root, "main.belt", ""+
		"use types from \"types.belt\"\n\n"+
		"const useIt = types.Helper\n\n"+
		"master Skill {\n"+
		"  record { id: int }\n"+
		"  primary id\n"+
		"  source { csv \"skills.csv\" }\n"+
		"}\n")
	belttest.WriteFile(t, root, "data/skills.csv", "id\n1\n")

	stdout, _, err := execData(t, root)
	if err == nil {
		t.Fatal("data succeeded with a broken file in the project, want an error")
	}
	if strings.Contains(stdout, "Skill <-") {
		t.Errorf("stdout = %q, want no table read while any project file is broken", stdout)
	}
}

func TestDataNormalizesBackslashBasePath(t *testing.T) {
	// A portable backslash base path the manifest accepts must resolve to real
	// directories, not a literal "data\csv" on Unix.
	root := t.TempDir()
	belttest.WriteFile(t, root, "masterbelt.toml", "entry = \"skills.belt\"\n\n[source.csv]\nbasePath = \"data\\\\csv\"\n")
	belttest.WriteFile(t, root, "skills.belt", ""+
		"master Skill {\n  record { id: int, name: string }\n  primary id\n  source { csv \"skills.csv\" }\n}\n")
	belttest.WriteFile(t, root, "data/csv/skills.csv", "id,name\n1,Heal\n")

	stdout, _, err := execData(t, root)
	if err != nil {
		t.Fatalf("data = %v\nstdout: %s", err, stdout)
	}
	if !strings.Contains(stdout, "Skill <- data/csv/skills.csv") {
		t.Errorf("stdout = %q, want the normalized base path resolved", stdout)
	}
}

func TestDataJSONReporterIsCleanDocument(t *testing.T) {
	// Under --reporter=json a data error must leave a single valid JSON document
	// on stdout — no text table, and the final error log lands on a stream the
	// test does not capture here.
	root := skillProject(t, "id,name,power\n1,Drain,-5\n")
	stdout, _, err := execData(t, "--reporter", "json", root)
	if err == nil {
		t.Fatal("data succeeded, want an error")
	}
	if strings.Contains(stdout, "Skill <-") {
		t.Errorf("stdout = %q, want no text table in JSON mode", stdout)
	}
	if !json.Valid([]byte(stdout)) {
		t.Errorf("stdout is not a valid JSON document:\n%s", stdout)
	}
}

func TestDataCyclicAliasDoesNotCrash(t *testing.T) {
	// A cyclic alias must not send the unwrap into a stack overflow.
	root := t.TempDir()
	belttest.WriteFile(t, root, "masterbelt.toml", "entry = \"m.belt\"\n")
	belttest.WriteFile(t, root, "m.belt", ""+
		"type A = B\n"+
		"type B = A\n\n"+
		"master M {\n"+
		"  record { id: A }\n"+
		"  primary id\n"+
		"  source { csv \"m.csv\" }\n"+
		"}\n")
	belttest.WriteFile(t, root, "m.csv", "id\n1\n")

	// Reaching the assertions at all means the unwrap terminated rather than
	// overflowing the stack; the command reports the problem and fails.
	stdout, _, err := execData(t, root)
	if err == nil {
		t.Fatal("data succeeded on a cyclic alias, want an error")
	}
	if stdout == "" {
		t.Error("stdout empty, want the problem reported")
	}
}
