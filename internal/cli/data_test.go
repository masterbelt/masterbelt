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
// the locator resolves under, plus the csv body, and returns the root.
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

func TestDataCellTypeMismatch(t *testing.T) {
	// "oops" is not an int; the row's other cells still type.
	root := skillProject(t, "id,name,power\noops,Heal,12\n")
	stdout, stderr, err := execData(t, root)
	if err == nil {
		t.Fatalf("data succeeded, want an error\nstdout: %s", stdout)
	}
	if !strings.Contains(stderr, "data/skills.csv:2,1") || !strings.Contains(stderr, "oops") {
		t.Errorf("stderr = %q, want it to point at the bad cell", stderr)
	}
	// The bad cell is a gap; the rest of the row is still printed.
	if !strings.Contains(stdout, `"Heal" | 12`) {
		t.Errorf("stdout = %q, want the typed cells of the row", stdout)
	}
}

func TestDataRefinementViolation(t *testing.T) {
	// power -5 has the right type but fails Level's where self > 0.
	root := skillProject(t, "id,name,power\n1,Drain,-5\n")
	_, stderr, err := execData(t, root)
	if err == nil {
		t.Fatal("data succeeded, want a refinement error")
	}
	// FieldPos points at the character the field starts at: -5 is at column 9
	// of "1,Drain,-5".
	for _, frag := range []string{"data/skills.csv:2,9", "-5", "Level"} {
		if !strings.Contains(stderr, frag) {
			t.Errorf("stderr = %q, want it to contain %q", stderr, frag)
		}
	}
}

func TestDataMissingColumn(t *testing.T) {
	root := skillProject(t, "id,name\n1,Heal\n")
	_, stderr, err := execData(t, root)
	if err == nil {
		t.Fatal("data succeeded, want a missing-column error")
	}
	if !strings.Contains(stderr, "power") {
		t.Errorf("stderr = %q, want it to name the missing field", stderr)
	}
}

func TestDataBasePathResolves(t *testing.T) {
	// The csv lives under data/ only because [source.csv] basePath says so;
	// without the base path the locator would not resolve.
	root := t.TempDir()
	belttest.WriteFile(t, root, "masterbelt.toml", "entry = \"skills.belt\"\n\n[source.csv]\nbasePath = \"assets/master\"\n")
	belttest.WriteFile(t, root, "skills.belt", ""+
		"master Skill {\n"+
		"  record { id: int, name: string }\n"+
		"  primary id\n"+
		"  source { csv \"skills.csv\" }\n"+
		"}\n")
	belttest.WriteFile(t, root, "assets/master/skills.csv", "id,name\n1,Heal\n")

	stdout, stderr, err := execData(t, root)
	if err != nil {
		t.Fatalf("data = %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(stdout, "Skill <- assets/master/skills.csv") {
		t.Errorf("stdout = %q, want the base-path-resolved display name", stdout)
	}
}

func TestDataUnknownFormat(t *testing.T) {
	root := t.TempDir()
	belttest.WriteFile(t, root, "masterbelt.toml", "entry = \"skills.belt\"\n")
	belttest.WriteFile(t, root, "skills.belt", ""+
		"master Skill {\n"+
		"  record { id: int }\n"+
		"  primary id\n"+
		"  source { xlsx \"skills.xlsx\" }\n"+
		"}\n")

	_, stderr, err := execData(t, root)
	if err == nil {
		t.Fatal("data succeeded, want an unknown-format error")
	}
	if !strings.Contains(stderr, "xlsx") {
		t.Errorf("stderr = %q, want it to name the unknown format", stderr)
	}
}

func TestDataBadOptions(t *testing.T) {
	cases := []struct {
		name string
		opts string
		want string
	}{
		{"unknown key", "{ sheet: \"Sheet1\" }", "sheet"},
		{"wrong type", "{ delimiter: 5 }", "delimiter"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			belttest.WriteFile(t, root, "masterbelt.toml", "entry = \"skills.belt\"\n")
			belttest.WriteFile(t, root, "skills.belt", ""+
				"master Skill {\n"+
				"  record { id: int }\n"+
				"  primary id\n"+
				"  source { csv \"skills.csv\" "+c.opts+" }\n"+
				"}\n")
			belttest.WriteFile(t, root, "skills.csv", "id\n1\n")

			_, stderr, err := execData(t, root)
			if err == nil {
				t.Fatal("data succeeded, want an option error")
			}
			if !strings.Contains(stderr, c.want) {
				t.Errorf("stderr = %q, want it to name %q", stderr, c.want)
			}
		})
	}
}
